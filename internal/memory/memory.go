// Package memory stores verified, reusable knowledge as JSONL records whose
// file-backed evidence is rejected as stale once the source content changes.
package memory

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Taki7980/ai-workflow-v3/internal/retrieval"
	"github.com/Taki7980/ai-workflow-v3/internal/storage"
	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

// RelativePath is the workspace-relative memory store location.
const RelativePath = "ai-workspace/memory/memory.jsonl"

var types = map[string]bool{"decision": true, "incident": true, "verified-fix": true, "architecture": true, "pattern": true, "optimization": true, "constraint": true}

// Record is the persisted V2-compatible memory record.
type Record struct {
	ID           string            `json:"id"`
	Type         string            `json:"type"`
	CreatedAt    string            `json:"created_at"`
	VerifiedAt   string            `json:"verified_at"`
	Keywords     []string          `json:"keywords"`
	Summary      string            `json:"summary"`
	Evidence     string            `json:"evidence"`
	Files        []string          `json:"files"`
	SourceHashes map[string]string `json:"source_hashes"`
	Confidence   float64           `json:"confidence"`
}

// Entry is a Record annotated with read-time staleness and search score.
type Entry struct {
	Record
	Stale bool    `json:"stale"`
	Score float64 `json:"score,omitempty"`
}

// ponytail: Add is append-only (cross-process safe); Prune/Import rewrite the file under a
// process-local lock only, so a concurrent Add can be lost during them. Add a file lock if that matters.
var mu sync.Mutex

// Path returns the absolute memory store path for root.
func Path(root string) string { return filepath.Join(root, filepath.FromSlash(RelativePath)) }

// Add validates and appends a memory record, hashing referenced files.
func Add(root, typ, keywords, summary, evidence string, files []string, confidence float64) (Record, error) {
	if !types[typ] {
		return Record{}, fmt.Errorf("unsupported memory type: %s", typ)
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000000+00:00")
	rec := Record{
		ID: newID(), Type: typ, CreatedAt: now, VerifiedAt: now,
		Keywords: dedupe(retrieval.Tokenize(keywords)), Summary: strings.TrimSpace(summary),
		Evidence: strings.TrimSpace(evidence), Files: []string{}, SourceHashes: map[string]string{},
		Confidence: min(1, max(0, confidence)),
	}
	for _, raw := range files {
		rel, abs, err := workspace.Within(root, raw)
		if err != nil {
			return Record{}, fmt.Errorf("memory file path must stay within workspace: %s", raw)
		}
		rec.Files = append(rec.Files, rel)
		if info, err := os.Stat(abs); err == nil && info.Mode().IsRegular() {
			if sum, err := fileSHA(abs); err == nil {
				rec.SourceHashes[rel] = sum
			}
		}
	}
	return rec, appendRecord(root, rec)
}

// appendRecord writes one JSONL line with a single O_APPEND write, so
// concurrent writers never lose records and corrupt lines never block adds.
func appendRecord(root string, rec Record) error {
	line, err := encode([]Record{rec})
	if err != nil {
		return err
	}
	mu.Lock()
	defer mu.Unlock()
	p := Path(root)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, info.Size()-1); err == nil && last[0] != '\n' {
			line = append([]byte("\n"), line...)
		}
	}
	if _, err := f.Write(line); err != nil {
		return err
	}
	return f.Sync()
}

// Search ranks records by BM25 x confidence; stale records sort last.
// Corrupt lines are skipped; use SearchLenient to learn how many.
func Search(root, query string, limit int, minConfidence float64, excludeStale bool) ([]Entry, error) {
	out, _, err := SearchLenient(root, query, limit, minConfidence, excludeStale)
	return out, err
}

// SearchLenient is Search that also reports the number of skipped corrupt lines.
func SearchLenient(root, query string, limit int, minConfidence float64, excludeStale bool) ([]Entry, int, error) {
	recs, skipped, err := loadLenient(root)
	if err != nil {
		return nil, 0, err
	}
	texts, entries := []string{}, []Entry{}
	for _, r := range recs {
		if r.Confidence < minConfidence {
			continue
		}
		stale := isStale(root, r)
		if excludeStale && stale {
			continue
		}
		texts = append(texts, strings.Join(r.Keywords, " ")+" "+r.Summary+" "+r.Evidence)
		entries = append(entries, Entry{Record: r, Stale: stale})
	}
	out := []Entry{}
	for _, s := range retrieval.NewBM25(texts, entries).Rank(query) {
		e := s.Value
		e.Score = s.Score * e.Confidence
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Stale != out[j].Stale {
			return !out[i].Stale
		}
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Confidence > out[j].Confidence
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, skipped, nil
}

// List returns all records, fresh first, then by descending confidence.
func List(root string) ([]Entry, error) {
	recs, err := load(root)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(recs))
	for _, r := range recs {
		out = append(out, Entry{Record: r, Stale: isStale(root, r)})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Stale != out[j].Stale {
			return !out[i].Stale
		}
		return out[i].Confidence > out[j].Confidence
	})
	return out, nil
}

// Prune removes stale records and reports kept and pruned counts.
func Prune(root string) (kept, pruned int, err error) {
	mu.Lock()
	defer mu.Unlock()
	recs, err := load(root)
	if err != nil {
		return 0, 0, err
	}
	fresh := []Record{}
	for _, r := range recs {
		if !isStale(root, r) {
			fresh = append(fresh, r)
		}
	}
	if len(fresh) == len(recs) {
		return len(fresh), 0, nil
	}
	return len(fresh), len(recs) - len(fresh), save(root, fresh)
}

// Export writes all records as JSONL to dest and returns the count.
func Export(root, dest string) (int, error) {
	recs, err := load(root)
	if err != nil {
		return 0, err
	}
	b, err := encode(recs)
	if err != nil {
		return 0, err
	}
	return len(recs), storage.WriteFileAtomic(dest, b)
}

// Import merges JSONL records from src (e.g. V2 `memory export`), skipping
// existing IDs and counting invalid lines.
func Import(root, src string) (imported, skipped, invalid int, err error) {
	f, err := os.Open(src)
	if err != nil {
		return 0, 0, 0, err
	}
	defer f.Close()
	mu.Lock()
	defer mu.Unlock()
	recs, err := load(root)
	if err != nil {
		return 0, 0, 0, err
	}
	seen := map[string]bool{}
	for _, r := range recs {
		seen[r.ID] = true
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var r Record
		if json.Unmarshal(line, &r) != nil || r.ID == "" || !types[r.Type] || strings.TrimSpace(r.Summary) == "" {
			invalid++
			continue
		}
		if seen[r.ID] {
			skipped++
			continue
		}
		seen[r.ID] = true
		recs = append(recs, normalize(r))
		imported++
	}
	if err := sc.Err(); err != nil {
		return 0, 0, 0, err
	}
	if imported > 0 {
		err = save(root, recs)
	}
	return imported, skipped, invalid, err
}

// load reads every record, failing on the first corrupt line (List, Prune,
// Export and Import must not silently drop data).
func load(root string) ([]Record, error) {
	recs, _, err := read(root, false)
	return recs, err
}

// loadLenient skips corrupt lines and reports how many were skipped.
func loadLenient(root string) ([]Record, int, error) {
	return read(root, true)
}

func read(root string, lenient bool) ([]Record, int, error) {
	f, err := os.Open(Path(root))
	if errors.Is(err, os.ErrNotExist) {
		return []Record{}, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	return decode(f, lenient)
}

func decode(r io.Reader, lenient bool) ([]Record, int, error) {
	out, skipped := []Record{}, 0
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			if lenient {
				skipped++
				continue
			}
			return nil, 0, fmt.Errorf("%s line %d: %w", RelativePath, n, err)
		}
		out = append(out, normalize(rec))
	}
	return out, skipped, sc.Err()
}

func encode(recs []Record) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	for _, r := range recs {
		if err := enc.Encode(r); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

func save(root string, recs []Record) error {
	b, err := encode(recs)
	if err != nil {
		return err
	}
	return storage.WriteFileAtomic(Path(root), b)
}

func normalize(r Record) Record {
	if r.Keywords == nil {
		r.Keywords = []string{}
	}
	if r.Files == nil {
		r.Files = []string{}
	}
	if r.SourceHashes == nil {
		r.SourceHashes = map[string]string{}
	}
	return r
}

// isStale mirrors V2 memory._stale: any referenced file without a hash, outside
// the root, missing, unreadable, or changed makes the record stale.
func isStale(root string, r Record) bool {
	for _, rel := range r.Files {
		if _, ok := r.SourceHashes[rel]; !ok {
			return true
		}
	}
	for rel, want := range r.SourceHashes {
		_, abs, err := workspace.Within(root, rel)
		if err != nil {
			return true
		}
		got, err := fileSHA(abs)
		if err != nil || got != want {
			return true
		}
	}
	return false
}

func fileSHA(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "mem-" + hex.EncodeToString(b)
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
