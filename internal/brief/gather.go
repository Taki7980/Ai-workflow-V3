package brief

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

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
	"github.com/Taki7980/ai-workflow-v3/internal/procx"
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

// minHitScoreRatio drops index hits scoring under this share of the best hit
// (adaptive-k cutoff): weak partial-name matches crowd out better evidence.
// V2 sample tasks are flat on recall over 0.4-0.7; 0.8 breaks abstention.
const minHitScoreRatio = 0.6

// Descriptive test names (test_carry_run_identity_in_traces) out-match the
// code a task edits, so test hits are demoted unless the task is about tests.
// 0.8 lifts held-out V2-commit file recall .51->.55; 0.7 breaks abstention.
const testHitPenalty = 0.8

var (
	testWordRE = regexp.MustCompile(`(?i)\b(tests?|testing|spec|specs|coverage)\b`)
	testPathRE = regexp.MustCompile(`(^|/)(tests?|__tests__|spec)/|(^|/)test_[^/]*$|_test\.[a-z]+$|\.(test|spec)\.[a-z]+$`)
)

// demoteTests scales test-file hit scores by testHitPenalty and re-sorts,
// unless the query is about tests.
func demoteTests(query string, hits []indexer.Hit) {
	if testWordRE.MatchString(query) {
		return
	}
	for i := range hits {
		if testPathRE.MatchString(hits[i].Path) {
			hits[i].Score *= testHitPenalty
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
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
	hits := indexer.Search(query, indexes, cfg.Context.Selector.MaxSelectorCandidates)
	demoteTests(query, hits)
	for _, hit := range hits {
		if hit.Score < minHitScoreRatio*hits[0].Score {
			break // hits are score-sorted; the rest are weaker still
		}
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

var namedFileRE = regexp.MustCompile(`[\w./-]*\w\.[A-Za-z0-9]+`)

// gatherNamed adds the head of each non-code file the task names ("typo in
// README.md"): the symbol index never covers such files, yet the task points
// straight at them. Read live, so always fresh; ranked with the best hit.
func gatherNamed(g *gathered, root, query string, repos []workspace.Repository, indexes map[string]indexer.Index, snippet int) {
	top := 1.0
	for _, it := range g.items {
		top = max(top, it.Score)
	}
	for _, rel := range namedFileRE.FindAllString(query, 3) {
		if !filepath.IsLocal(filepath.FromSlash(rel)) {
			continue
		}
		for _, r := range repos {
			if _, indexed := indexes[r.RelativePath].Files[rel]; indexed {
				break
			}
			p := filepath.Join(root, filepath.FromSlash(r.RelativePath), filepath.FromSlash(rel))
			info, err := os.Stat(p)
			if err != nil || !info.Mode().IsRegular() || info.Size() > maxScanBytes {
				continue
			}
			b, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
			window := strings.Join(lines[:min(len(lines), 2*snippet+1)], "\n")
			g.items = append(g.items, model.ContextItem{
				Source: "lightweight_index", Text: compactJSON(indexHeader{rel, "file", rel, 1, r.RelativePath}) + "\n" + window, Score: top,
				Metadata: map[string]any{"repository": r.RelativePath, "repository_id": r.RepositoryID, "path": rel, "line": 1, "kind": "file"},
			})
			break
		}
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

// codeShaped matches words that look like code identifiers rather than prose:
// a digit, an underscore, or an upper-case letter after the first character.
var codeShaped = regexp.MustCompile(`^.+[A-Z0-9_]|_`)

// searchTerm picks the longest code-shaped identifier in query, else the
// longest word (first wins on ties). "fix BM25 ranking" greps BM25, not ranking.
func searchTerm(query string) string {
	best, bestCode := "", false
	for _, w := range identRE.FindAllString(query, -1) {
		code := codeShaped.MatchString(w)
		if code && !bestCode || code == bestCode && len(w) > len(best) {
			best, bestCode = w, code
		}
	}
	if len(best) < 3 {
		return ""
	}
	return best
}

// gatherTargeted adds targeted_source line matches via ripgrep, or a bounded
// scan of indexed files when ripgrep is unavailable or fails. Matches inside
// an already gathered index window of snippet lines are dropped.
func gatherTargeted(ctx context.Context, g *gathered, root, query string, repos []workspace.Repository, indexes map[string]indexer.Index, limit, snippet int) {
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
		// Over-fetch so code matches can outrank the path-sorted docs and
		// READMEs that otherwise fill every slot.
		if lines, err := ripgrep(ctx, rg, root, term, rels, limit*targetedOverfetch); err == nil {
			addTargeted(g, codeFirst(uncovered(lines, g.items, snippet), indexes, limit))
			return
		}
		g.note("ripgrep failed; file scan used")
	} else {
		g.note("ripgrep unavailable; file scan used")
	}
	addTargeted(g, codeFirst(uncovered(scan(root, term, rels, indexes, limit*targetedOverfetch), g.items, snippet), indexes, limit))
}

// uncovered drops "path:line: text" matches that sit inside a lightweight_index
// source window, which already shows that code (object-level dedupe, after
// VITAL-RAG, arXiv 2607.26937).
func uncovered(lines []string, items []model.ContextItem, snippet int) []string {
	windows := map[string][][2]int{}
	for _, it := range items {
		if it.Source != "lightweight_index" {
			continue
		}
		repo, _ := it.Metadata["repository"].(string)
		path, _ := it.Metadata["path"].(string)
		line, _ := it.Metadata["line"].(int)
		key := prefix(repo, path)
		windows[key] = append(windows[key], [2]int{line - snippet, line + snippet})
	}
	out := lines[:0:0]
	for _, l := range lines {
		parts := strings.SplitN(l, ":", 3)
		n, err := strconv.Atoi(parts[min(1, len(parts)-1)])
		covered := false
		if err == nil {
			for _, w := range windows[parts[0]] {
				covered = covered || w[0] <= n && n <= w[1]
			}
		}
		if !covered {
			out = append(out, l)
		}
	}
	return out
}

// targetedOverfetch is how many ripgrep matches per slot codeFirst ranks.
const targetedOverfetch = 4

// codeFirst stably moves matches in indexed source files ahead of other files
// (docs, config) and keeps the first limit.
func codeFirst(lines []string, indexes map[string]indexer.Index, limit int) []string {
	code := map[string]bool{}
	for repo, idx := range indexes {
		for rel := range idx.Files {
			code[prefix(repo, rel)] = true
		}
	}
	slices.SortStableFunc(lines, func(a, b string) int {
		ca, cb := code[strings.SplitN(a, ":", 2)[0]], code[strings.SplitN(b, ":", 2)[0]]
		switch {
		case ca == cb:
			return 0
		case ca:
			return -1
		}
		return 1
	})
	return lines[:min(limit, len(lines))]
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
		// SafeEnv drops RIPGREP_CONFIG_PATH, which could inject arbitrary rg flags.
		res, err := procx.Run(ctx, procx.Cmd{Argv: append([]string{rg}, args...), Dir: filepath.Join(root, filepath.FromSlash(repo)),
			Env: procx.SafeEnv(), Timeout: 6 * time.Second, MaxStdout: 4 << 20})
		if err != nil {
			return nil, err
		}
		if res.TimedOut || (res.ExitCode != 0 && res.ExitCode != 1) { // 1 = no match
			return nil, fmt.Errorf("ripgrep failed (exit %d)", res.ExitCode)
		}
		sc := bufio.NewScanner(bytes.NewReader(res.Stdout))
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
