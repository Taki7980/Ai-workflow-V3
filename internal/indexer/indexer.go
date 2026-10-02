package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Taki7980/ai-workflow-v3/internal/storage"
	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

// IndexVersion is the on-disk index schema version. Freshness metadata
// (built_at_ns, parsers, head_sha) is additive, so version 1 indexes written
// by earlier V3 builds still load; they are simply rebuilt in full once.
const IndexVersion = 1

// MaxParseBytes caps how much of a single file is handed to symbol parsers.
// Larger files (generated code, minified bundles, vendored blobs) are still
// hashed and tracked for freshness, but are not parsed for symbols.
const MaxParseBytes = 4 << 20

// RacyWindow is the safety margin used for "racily clean" detection. A file
// whose recorded mtime falls within this window of the moment its index was
// built may have been modified without its mtime changing (coarse filesystem
// timestamp granularity), so the stat fast-path is not trusted for it and its
// content is re-hashed instead. Two seconds covers FAT's 2s granularity.
// See Git's Documentation/technical/racy-git.txt.
const RacyWindow = 2 * time.Second

var sourceExts = map[string]bool{".py": true, ".rs": true, ".js": true, ".jsx": true, ".ts": true, ".tsx": true, ".go": true, ".java": true, ".cs": true, ".cpp": true, ".cc": true, ".cxx": true, ".c": true, ".h": true, ".hpp": true, ".rb": true, ".php": true, ".swift": true, ".kt": true, ".scala": true, ".sql": true, ".vue": true, ".svelte": true}
var excludes = map[string]bool{".git": true, "node_modules": true, "venv": true, ".venv": true, "dist": true, "build": true, "bin": true, "obj": true, "__pycache__": true, "ai-workspace": true, ".ai": true, ".agents": true}
var repoKeyRE = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

type FileState struct {
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	MTimeNS int64  `json:"mtime_ns"`
}
type Symbol struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Line    int    `json:"line"`
	EndLine int    `json:"end_line,omitempty"`
	SHA256  string `json:"sha256"`
}
type Index struct {
	Version    int    `json:"version"`
	Repository string `json:"repository"`
	BuiltAt    string `json:"built_at"`
	// BuiltAtNS is the scan start time used for racily-clean detection.
	BuiltAtNS int64 `json:"built_at_ns,omitempty"`
	// Parsers fingerprints the symbol parser set that produced Symbols. Cached
	// symbols are only reused when it matches the running binary's parsers.
	Parsers string `json:"parsers,omitempty"`
	// HeadSHA records the repository HEAD at build time as provenance.
	HeadSHA string               `json:"head_sha,omitempty"`
	Files   map[string]FileState `json:"files"`
	Symbols []Symbol             `json:"symbols"`
}

// BuildOptions controls index construction.
type BuildOptions struct {
	// Full disables reuse of the previous index and re-reads every file.
	Full bool
}

// BuildStats describes the work an index build performed.
type BuildStats struct {
	Files    int  `json:"files"`
	Symbols  int  `json:"symbols"`
	Reused   int  `json:"reused"`
	Rehashed int  `json:"rehashed"`
	Parsed   int  `json:"parsed"`
	Removed  int  `json:"removed"`
	Skipped  int  `json:"skipped"`
	Full     bool `json:"full"`
	Written  bool `json:"written"`
}

// repoKey derives a filesystem-safe key for a repository's relative path,
// used to namespace its on-disk index directory.
func repoKey(rel string) string {
	if rel == "." || rel == "" {
		return "root"
	}
	s := repoKeyRE.ReplaceAllString(filepath.ToSlash(rel), "-")
	return strings.Trim(s, ".-_")
}

// Path returns the on-disk location of the index file for repo within the
// workspace rooted at controlRoot.
func Path(controlRoot string, repo workspace.Repository) string {
	return filepath.Join(controlRoot, "ai-workspace", "indexes", repoKey(repo.RelativePath), "index.json")
}

// RepoRoot returns the absolute working-tree root of repo inside controlRoot.
func RepoRoot(controlRoot string, repo workspace.Repository) string {
	if repo.RelativePath == "." || repo.RelativePath == "" {
		return controlRoot
	}
	return filepath.Join(controlRoot, filepath.FromSlash(repo.RelativePath))
}

// Build incrementally (re)builds the index for repo and returns it. It is
// equivalent to BuildWithOptions with default options.
func Build(ctx context.Context, controlRoot string, repo workspace.Repository) (Index, error) {
	idx, _, err := BuildWithOptions(ctx, controlRoot, repo, BuildOptions{})
	return idx, err
}

// BuildWithOptions walks repo's source files (skipping excluded directories
// and nested Git roots) and produces a symbol index. Unless opts.Full is set,
// it reuses the previous on-disk index: files whose size and mtime are
// unchanged (and not racily clean) are reused without being read; files whose
// stat changed but whose SHA-256 is unchanged keep their cached symbols; only
// new or modified files are parsed. The index is written to disk only when its
// content changed.
func BuildWithOptions(ctx context.Context, controlRoot string, repo workspace.Repository, opts BuildOptions) (Index, BuildStats, error) {
	repoRoot := RepoRoot(controlRoot, repo)
	scanStart := time.Now()
	rels, err := listSourceFiles(ctx, repoRoot)
	if err != nil {
		return Index{}, BuildStats{}, err
	}

	fingerprint := parserFingerprint()
	var prev *Index
	if !opts.Full {
		if p, err := Load(controlRoot, repo); err == nil && p.Version == IndexVersion && p.Files != nil {
			prev = &p
		}
	}
	prevSymbols := map[string][]Symbol{}
	symbolsReusable := prev != nil && prev.Parsers == fingerprint
	if symbolsReusable {
		for _, s := range prev.Symbols {
			prevSymbols[s.Path] = append(prevSymbols[s.Path], s)
		}
	}

	stats := BuildStats{Full: !symbolsReusable}
	idx := Index{
		Version:    IndexVersion,
		Repository: repo.RelativePath,
		BuiltAt:    scanStart.UTC().Format(time.RFC3339),
		BuiltAtNS:  scanStart.UnixNano(),
		Parsers:    fingerprint,
		HeadSHA:    workspace.HeadSHA(ctx, repoRoot),
		Files:      make(map[string]FileState, len(rels)),
	}

	// Classify each file against the previous index. Stat-clean files are
	// reused outright; everything else is queued for reading.
	work := make([]string, 0, len(rels))
	for _, rel := range rels {
		if symbolsReusable {
			if old, ok := prev.Files[rel]; ok {
				st, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(rel)))
				if err == nil && statClean(old, st, prev.BuiltAtNS) {
					idx.Files[rel] = old
					idx.Symbols = append(idx.Symbols, prevSymbols[rel]...)
					stats.Reused++
					continue
				}
			}
		}
		work = append(work, rel)
	}

	results, err := processFiles(ctx, repoRoot, work, func(rel string, state FileState, source []byte) ([]Symbol, bool) {
		if symbolsReusable {
			if old, ok := prev.Files[rel]; ok && old.SHA256 == state.SHA256 {
				return prevSymbols[rel], true
			}
		}
		if source == nil {
			return nil, false
		}
		syms, _ := parseSymbols(source, rel, state.SHA256)
		return syms, false
	})
	if err != nil {
		return Index{}, BuildStats{}, err
	}
	for _, r := range results {
		if r.skipped {
			stats.Skipped++
			continue
		}
		idx.Files[r.rel] = r.state
		idx.Symbols = append(idx.Symbols, r.symbols...)
		if r.reusedSymbols {
			stats.Rehashed++
		} else {
			stats.Parsed++
		}
	}
	if prev != nil {
		for rel := range prev.Files {
			if _, ok := idx.Files[rel]; !ok {
				stats.Removed++
			}
		}
	}
	sortSymbols(idx.Symbols)
	if idx.Symbols == nil {
		idx.Symbols = []Symbol{}
	}
	stats.Files = len(idx.Files)
	stats.Symbols = len(idx.Symbols)

	// Skip the write when nothing observable changed, so a no-op refresh does
	// not churn the index file. Racily-clean files that were re-hashed force a
	// write so the new scan timestamp lets the fast path trust them next time.
	if symbolsReusable && stats.Parsed == 0 && stats.Removed == 0 && stats.Rehashed == 0 && prev.HeadSHA == idx.HeadSHA {
		return *prev, stats, nil
	}
	if err := storage.WriteJSON(Path(controlRoot, repo), idx); err != nil {
		return Index{}, BuildStats{}, err
	}
	stats.Written = true
	return idx, stats, nil
}

// statClean reports whether a file's current stat matches its indexed state
// closely enough to skip reading it. Entries whose mtime is within RacyWindow
// of (or after) the previous scan start are "racily clean" and never trusted.
func statClean(old FileState, st os.FileInfo, builtAtNS int64) bool {
	if builtAtNS <= 0 || st.Size() != old.Size || st.ModTime().UnixNano() != old.MTimeNS {
		return false
	}
	return old.MTimeNS < builtAtNS-int64(RacyWindow)
}

type fileResult struct {
	rel           string
	state         FileState
	symbols       []Symbol
	reusedSymbols bool
	skipped       bool
}

// symbolFunc returns the symbols for a freshly hashed file and whether they
// were reused from cache. source is nil when the file exceeds MaxParseBytes.
type symbolFunc func(rel string, state FileState, source []byte) ([]Symbol, bool)

// processFiles hashes and parses rels with bounded concurrency. The first
// hard error cancels outstanding work and is returned; workers never leak.
// Files that vanish or are unreadable between walk and read are reported as
// skipped rather than failing the whole build.
func processFiles(parent context.Context, repoRoot string, rels []string, symbolsFor symbolFunc) ([]fileResult, error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	type result struct {
		fileResult
		err error
	}
	jobs := make(chan string)
	results := make(chan result)
	workers := runtime.GOMAXPROCS(0)
	if workers > 8 {
		workers = 8
	}
	if workers < 1 {
		workers = 1
	}
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for rel := range jobs {
				var r result
				state, source, err := readFile(filepath.Join(repoRoot, filepath.FromSlash(rel)))
				switch {
				case errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission):
					r = result{fileResult: fileResult{rel: rel, skipped: true}}
				case err != nil:
					r = result{err: fmt.Errorf("%s: %w", rel, err)}
				default:
					syms, reused := symbolsFor(rel, state, source)
					r = result{fileResult: fileResult{rel: rel, state: state, symbols: syms, reusedSymbols: reused}}
				}
				select {
				case results <- r:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, rel := range rels {
			select {
			case jobs <- rel:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	out := make([]fileResult, 0, len(rels))
	var firstErr error
	for r := range results {
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
				cancel()
			}
			continue
		}
		if firstErr == nil {
			out = append(out, r.fileResult)
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	if err := parent.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, nil
}

// readFile stats the file before reading it, so a write racing with the read
// leaves a stale mtime behind (forcing a re-hash next time) rather than a
// fresh mtime paired with old content. Files larger than MaxParseBytes are
// hashed by streaming and returned with nil source.
func readFile(path string) (FileState, []byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return FileState{}, nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return FileState{}, nil, err
	}
	h := sha256.New()
	var source []byte
	if st.Size() > MaxParseBytes {
		if _, err := io.Copy(h, f); err != nil {
			return FileState{}, nil, err
		}
	} else {
		source, err = io.ReadAll(f)
		if err != nil {
			return FileState{}, nil, err
		}
		h.Write(source)
		if len(source) > MaxParseBytes {
			source = nil
		}
	}
	state := FileState{SHA256: hex.EncodeToString(h.Sum(nil)), Size: st.Size(), MTimeNS: st.ModTime().UnixNano()}
	return state, source, nil
}

// listSourceFiles returns the sorted, slash-separated relative paths of the
// indexable source files under repoRoot, excluding ignored directories and
// nested Git repositories.
func listSourceFiles(ctx context.Context, repoRoot string) ([]string, error) {
	rels := []string{}
	err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if d.IsDir() {
			if path != repoRoot && excludes[d.Name()] {
				return filepath.SkipDir
			}
			if path != repoRoot && isNestedGitRoot(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if sourceExts[strings.ToLower(filepath.Ext(path))] {
			rel, relErr := filepath.Rel(repoRoot, path)
			if relErr != nil {
				return nil
			}
			rels = append(rels, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(rels)
	return rels, nil
}

// sortSymbols orders symbols by path, then line, then name for stable output.
func sortSymbols(symbols []Symbol) {
	sort.SliceStable(symbols, func(i, j int) bool {
		if symbols[i].Path != symbols[j].Path {
			return symbols[i].Path < symbols[j].Path
		}
		if symbols[i].Line != symbols[j].Line {
			return symbols[i].Line < symbols[j].Line
		}
		return symbols[i].Name < symbols[j].Name
	})
}

// parserFingerprint identifies the active symbol-parser set. Changing build
// tags (e.g. enabling the treesitter pilot) changes it, which invalidates
// cached symbols without invalidating file hashes.
func parserFingerprint() string {
	names := []string{goASTSymbolParser{}.Name()}
	for _, p := range treeSitterParsers() {
		names = append(names, p.Name())
	}
	names = append(names, regexSymbolParser{}.Name())
	return "v1:" + strings.Join(names, ",")
}

// BuildWorkspace builds an index for every included repository in reg,
// returning a map of relative repository path to its built index.
func BuildWorkspace(ctx context.Context, root string, reg workspace.Registry) (map[string]Index, error) {
	built, _, err := BuildWorkspaceWithOptions(ctx, root, reg, BuildOptions{})
	return built, err
}

// BuildWorkspaceWithOptions is BuildWorkspace with explicit options, also
// returning per-repository build statistics.
func BuildWorkspaceWithOptions(ctx context.Context, root string, reg workspace.Registry, opts BuildOptions) (map[string]Index, map[string]BuildStats, error) {
	out := map[string]Index{}
	stats := map[string]BuildStats{}
	for _, repo := range reg.Repositories {
		if !repo.Included {
			continue
		}
		idx, st, err := BuildWithOptions(ctx, root, repo, opts)
		if err != nil {
			return nil, nil, fmt.Errorf("index %s: %w", repo.RelativePath, err)
		}
		out[repo.RelativePath] = idx
		stats[repo.RelativePath] = st
	}
	return out, stats, nil
}

// isNestedGitRoot reports whether path contains a .git entry, indicating it
// is the root of a nested Git repository that should not be indexed.
func isNestedGitRoot(path string) bool {
	st, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil && (st.IsDir() || st.Mode().IsRegular())
}
