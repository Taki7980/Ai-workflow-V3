package brief

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Taki7980/ai-workflow-v3/internal/config"
	"github.com/Taki7980/ai-workflow-v3/internal/indexer"
	"github.com/Taki7980/ai-workflow-v3/internal/memory"
	"github.com/Taki7980/ai-workflow-v3/internal/model"
	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

var rgPath = func() (string, error) { return exec.LookPath("rg") }

const maxScanBytes = 500_000

// gathered collects candidate context items plus degradation notes.
type gathered struct {
	items     []model.ContextItem
	attempted []string
	fallbacks []string
	stale     int
}

func (g *gathered) note(format string, args ...any) {
	g.fallbacks = append(g.fallbacks, fmt.Sprintf(format, args...))
}

// compactJSON encodes v without HTML escaping or trailing newline.
func compactJSON(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return strings.TrimSuffix(buf.String(), "\n")
}

type indexHeader struct {
	Symbol     string `json:"symbol"`
	Kind       string `json:"kind"`
	File       string `json:"file"`
	Line       int    `json:"line"`
	Repository string `json:"repository"`
}

// gatherIndex adds lightweight_index items with fresh source windows. A file
// whose content hash no longer matches the index is never served.
func gatherIndex(g *gathered, root, query string, repos []workspace.Repository, indexes map[string]indexer.Index, cfg config.Config) {
	g.attempted = append(g.attempted, "lightweight_index")
	ids := map[string]string{}
	for _, r := range repos {
		ids[r.RelativePath] = r.RepositoryID
	}
	files := map[string][]string{}
	stale := map[string]int{}
	defer func() {
		for _, repo := range slices.Sorted(maps.Keys(stale)) {
			g.note("index stale: %s (%d files)", repo, stale[repo])
		}
	}()
	for _, hit := range indexer.Search(query, indexes, cfg.Context.Selector.MaxSelectorCandidates) {
		key := hit.Repository + "\x00" + hit.Path
		lines, seen := files[key]
		if !seen {
			lines = freshLines(root, hit.Repository, hit.Path, indexes[hit.Repository].Files[hit.Path].SHA256)
			files[key] = lines
			if lines == nil {
				stale[hit.Repository]++
				g.stale++
			}
		}
		if lines == nil {
			continue
		}
		lo, hi := max(1, hit.Line-cfg.Context.Snippet()), min(len(lines), hit.Line+cfg.Context.Snippet())
		window := ""
		if lo <= hi {
			window = strings.Join(lines[lo-1:hi], "\n")
		}
		header := compactJSON(indexHeader{hit.Symbol, hit.Kind, hit.Path, hit.Line, hit.Repository})
		g.items = append(g.items, model.ContextItem{
			Source: "lightweight_index", Text: header + "\n" + window, Score: hit.Score,
			Metadata: map[string]any{"repository": hit.Repository, "repository_id": ids[hit.Repository], "path": hit.Path, "line": hit.Line, "kind": hit.Kind},
		})
	}
}

// freshLines returns the file's lines when its SHA-256 matches want, else nil.
func freshLines(root, repo, rel, want string) []string {
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(repo), filepath.FromSlash(rel)))
	if err != nil || want == "" {
		return nil
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != want {
		return nil
	}
	return strings.Split(strings.ReplaceAll(strings.ReplaceAll(string(b), "\r\n", "\n"), "\r", "\n"), "\n")
}

var identRE = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// searchTerm picks the longest identifier in query (first wins on ties).
func searchTerm(query string) string {
	best := ""
	for _, w := range identRE.FindAllString(query, -1) {
		if len(w) > len(best) {
			best = w
		}
	}
	if len(best) < 3 {
		return ""
	}
	return best
}

// gatherTargeted adds targeted_source line matches via ripgrep, or a bounded
// scan of indexed files when ripgrep is unavailable or fails.
func gatherTargeted(ctx context.Context, g *gathered, root, query string, repos []workspace.Repository, indexes map[string]indexer.Index, limit int) {
	term := searchTerm(query)
	if term == "" || limit <= 0 {
		return
	}
	g.attempted = append(g.attempted, "targeted_source")
	rels := make([]string, 0, len(repos))
	for _, r := range repos {
		rels = append(rels, r.RelativePath)
	}
	if rg, err := rgPath(); err == nil {
		if lines, err := ripgrep(ctx, rg, root, term, rels, limit); err == nil {
			addTargeted(g, lines)
			return
		}
		g.note("ripgrep failed; file scan used")
	} else {
		g.note("ripgrep unavailable; file scan used")
	}
	addTargeted(g, scan(root, term, rels, indexes, limit))
}

func addTargeted(g *gathered, lines []string) {
	for _, l := range lines {
		g.items = append(g.items, model.ContextItem{Source: "targeted_source", Text: l, Score: 1})
	}
}

// prefix joins a repository-relative path onto its repository path.
func prefix(repo, rel string) string {
	if repo == "." || repo == "" {
		return rel
	}
	return repo + "/" + rel
}

// nestedUnder lists included repositories nested inside repo, relative to it.
func nestedUnder(repo string, all []string) []string {
	out := []string{}
	for _, other := range all {
		switch {
		case other == repo:
		case repo == "." || repo == "":
			out = append(out, other)
		case strings.HasPrefix(other, repo+"/"):
			out = append(out, strings.TrimPrefix(other, repo+"/"))
		}
	}
	return out
}

func ripgrep(ctx context.Context, rg, root, term string, repos []string, limit int) ([]string, error) {
	out := []string{}
	for _, repo := range repos {
		// --sort path: rg's parallel walk otherwise reorders matches run to run.
		args := []string{"-n", "--no-heading", "--color", "never", "--sort", "path", "-m", "2", "-F", "-i",
			"--glob", "!ai-workspace/**", "--glob", "!**/.git/**", "--glob", "!**/node_modules/**"}
		for _, n := range nestedUnder(repo, repos) {
			args = append(args, "--glob", "!"+n+"/**")
		}
		args = append(args, "--", term)
		cctx, cancel := context.WithTimeout(ctx, 6*time.Second)
		cmd := exec.CommandContext(cctx, rg, args...)
		cmd.Dir = filepath.Join(root, filepath.FromSlash(repo))
		raw, err := cmd.Output()
		cancel()
		var exitErr *exec.ExitError
		if err != nil && !(errors.As(err, &exitErr) && exitErr.ExitCode() == 1) { // 1 = no match
			return nil, err
		}
		sc := bufio.NewScanner(bytes.NewReader(raw))
		for sc.Scan() && len(out) < limit {
			parts := strings.SplitN(sc.Text(), ":", 3)
			if len(parts) != 3 {
				continue
			}
			out = append(out, fmt.Sprintf("%s:%s: %s", prefix(repo, filepath.ToSlash(parts[0])), parts[1], strings.TrimSpace(parts[2])))
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// scan matches term case-insensitively in indexed files (≤500 KB each).
// ponytail: indexed files only (V2 walked every file); docs/config are missed without ripgrep — walk the tree if that matters.
func scan(root, term string, repos []string, indexes map[string]indexer.Index, limit int) []string {
	needle := strings.ToLower(term)
	out := []string{}
	for _, repo := range repos {
		idx, ok := indexes[repo]
		if !ok {
			continue
		}
		paths := make([]string, 0, len(idx.Files))
		for p, st := range idx.Files {
			if st.Size <= maxScanBytes {
				paths = append(paths, p)
			}
		}
		sort.Strings(paths)
		for _, rel := range paths {
			b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(repo), filepath.FromSlash(rel)))
			if err != nil {
				continue
			}
			perFile := 0
			for i, line := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
				if strings.Contains(strings.ToLower(line), needle) {
					out = append(out, prefix(repo, rel)+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
					perFile++
					if len(out) >= limit {
						return out
					}
					if perFile >= 2 {
						break
					}
				}
			}
		}
	}
	return out
}

type memoryText struct {
	ID         string   `json:"id"`
	Type       string   `json:"type"`
	Summary    string   `json:"summary"`
	Evidence   string   `json:"evidence"`
	Files      []string `json:"files"`
	Confidence float64  `json:"confidence"`
}

// gatherMemory adds fresh durable_memory records above the confidence floor.
func gatherMemory(g *gathered, root, query string, cfg config.Config) {
	g.attempted = append(g.attempted, "durable_memory")
	hits, skipped, err := memory.SearchLenient(root, query, cfg.Memory.MaxResults, cfg.Memory.MinimumConfidence, true)
	if err != nil {
		g.note("memory unavailable: %v", err)
		return
	}
	if skipped > 0 {
		g.note("memory: skipped %d corrupt line(s)", skipped)
	}
	for _, h := range hits {
		g.items = append(g.items, model.ContextItem{
			Source: "durable_memory",
			Text:   compactJSON(memoryText{h.ID, h.Type, h.Summary, h.Evidence, h.Files, h.Confidence}),
			Score:  h.Score,
		})
	}
}
