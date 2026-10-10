package structural

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Taki7980/ai-workflow-v3/internal/model"
	"github.com/Taki7980/ai-workflow-v3/internal/procx"
	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

// Query describes one structural lookup.
type Query struct {
	Text     string
	Symbol   string
	Changed  []string // repository-relative slash paths
	Limit    int
	Patterns []string
	MaxCalls int    // CRG subprocess calls allowed; 0 allows none
	File     string // repository-relative file of Symbol, used to disambiguate
}

var crgTimeout = 8 * time.Second

const crgMaxOutput = 8 << 20

// Field is one key of an Ordered JSON object.
type Field struct {
	Key   string
	Value any
}

// Ordered encodes as a JSON object keeping insertion order, matching V2's
// dict ordering in item text.
type Ordered []Field

func (o Ordered) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, f := range o {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, _ := json.Marshal(f.Key)
		buf.Write(k)
		buf.WriteByte(':')
		v, err := encode(f.Value)
		if err != nil {
			return nil, err
		}
		buf.Write(v)
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// encode is compact JSON without HTML escaping.
func encode(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case bool:
		if n {
			return 1, true
		}
		return 0, true
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(n))
		return i, err == nil
	}
	return 0, false
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return x != ""
	case bool:
		return x
	case float64:
		return x != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

func head(l []any, n int) []any {
	if l == nil {
		return []any{}
	}
	return l[:min(len(l), max(0, n))]
}

// ResultCount ports V2 _crg_result_count.
func ResultCount(payload map[string]any, pattern string) int {
	var raw any
	switch pattern {
	case "impact":
		raw = payload["total_impacted"]
		if raw == nil {
			raw = len(list(payload["impacted_nodes"]))
		}
	case "architecture":
		raw = 0
		if truthy(payload["summary"]) {
			raw = 1
		}
	default:
		raw = payload["result_count"]
		if raw == nil {
			raw = len(list(payload["results"]))
		}
	}
	n, _ := toInt(raw)
	return max(0, n)
}

// VerifiedEmptyCRG ports V2 _verified_empty_crg.
func VerifiedEmptyCRG(payload map[string]any) bool {
	c, _ := payload["confidence"].(string)
	c = strings.ToLower(c)
	return strings.Contains(c, "real absence") && strings.Contains(c, "current") && !strings.Contains(c, "unverified")
}

func present(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

// CompactCRG ports V2 _compact_crg_payload.
func CompactCRG(payload map[string]any, pattern string, limit int) Ordered {
	o := Ordered{{"status", "ok"}, {"pattern", pattern}}
	for _, k := range []string{"target", "summary", "confidence", "truncated"} {
		if v := payload[k]; present(v) {
			o = append(o, Field{k, v})
		}
	}
	switch pattern {
	case "impact":
		o = append(o,
			Field{"total_impacted", ResultCount(payload, pattern)},
			Field{"impacted_files", head(list(payload["impacted_files"]), limit)},
			Field{"impacted_nodes", head(list(payload["impacted_nodes"]), limit)},
			Field{"edges", head(list(payload["edges"]), limit)})
	case "architecture":
		for _, k := range []string{"communities", "entry_points", "hub_nodes", "bridge_nodes"} {
			if l, ok := payload[k].([]any); ok {
				o = append(o, Field{k, head(l, limit)})
			}
		}
	default:
		o = append(o,
			Field{"result_count", ResultCount(payload, pattern)},
			Field{"results", head(list(payload["results"]), limit)},
			Field{"edges", head(list(payload["edges"]), limit)})
	}
	return o
}

// CRGItem ports V2 _crg_item.
func CRGItem(payload map[string]any, pattern string, score float64, limit int, anchor string) model.ContextItem {
	count := ResultCount(payload, pattern)
	emptyVerified := count == 0 && VerifiedEmptyCRG(payload)
	truncated, _ := payload["truncated"].(bool)
	meta := map[string]any{
		"pattern":          pattern,
		"structural_valid": count > 0 || emptyVerified,
		"result_count":     count,
		"empty_verified":   emptyVerified,
		"truncated":        truncated,
	}
	if anchor != "" {
		meta["anchor"] = anchor
	}
	text, _ := encode(CompactCRG(payload, pattern, limit))
	return model.ContextItem{Source: "code_review_graph", Text: string(text), Score: score, Metadata: meta}
}

// relPath rewrites an absolute path (optionally "path::name") to a
// repository-relative slash path. ok is false when it lies outside root.
// Values that are not absolute paths are returned unchanged. alias, when
// set, is the spelling of root that matched by file identity (junction,
// subst drive, short name) rather than by path.
func relPath(root, s string) (rel string, ok bool, alias string) {
	p, suffix := s, ""
	if i := strings.LastIndex(s, "::"); i >= 0 {
		p, suffix = s[:i], s[i:]
	}
	native := filepath.FromSlash(p)
	if filepath.IsAbs(native) {
		if r, _, err := workspace.Within(root, native); err == nil {
			return r + suffix, true, ""
		}
		if r, dir, found := sameFileRel(root, native); found {
			return r + suffix, true, dir
		}
		return "", false, ""
	}
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return "", false, "" // rooted but not absolute (Windows): never inside root
	}
	return s, true, ""
}

// sameFileRel walks p's parents looking for a directory that is root by file
// identity, so paths spelled through a different alias of root still resolve.
func sameFileRel(root, p string) (rel, dir string, ok bool) {
	rootInfo, err := os.Stat(root)
	if err != nil {
		return "", "", false
	}
	for d := filepath.Dir(p); ; d = filepath.Dir(d) {
		if info, err := os.Stat(d); err == nil && os.SameFile(info, rootInfo) {
			r, err := filepath.Rel(d, p)
			if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
				return "", "", false
			}
			return filepath.ToSlash(r), d, true
		}
		if filepath.Dir(d) == d {
			return "", "", false
		}
	}
}

var (
	rowPathKeys = []string{"file_path", "relative_path", "path", "qualified_name", "source", "target", "name"}
	// absolutePath spots an absolute path left in free text after rewriting.
	absolutePath = regexp.MustCompile(`(^|[\s'"(=,\[])(/[^\s/'"()]|[A-Za-z]:[\\/]|\\\\)`)
)

// rewritePaths makes every path in a CRG payload repository-relative and
// drops rows or file entries outside the repository, so absolute machine
// paths never reach a brief. Free text that still holds an absolute path
// after rewriting is dropped, and result counts exclude dropped rows.
func rewritePaths(root string, payload map[string]any) {
	prefixes := map[string]bool{strings.TrimSuffix(filepath.ToSlash(root), "/"): true}
	rewrite := func(s string) (string, bool) {
		rel, ok, alias := relPath(root, s)
		if alias != "" {
			prefixes[strings.TrimSuffix(filepath.ToSlash(alias), "/")] = true
		}
		return rel, ok
	}
	dropped := map[string]int{}
	for _, k := range []string{"results", "impacted_nodes", "changed_nodes", "edges"} {
		rows, ok := payload[k].([]any)
		if !ok {
			continue
		}
		kept := []any{}
	rows:
		for _, r := range rows {
			row, ok := r.(map[string]any)
			if !ok {
				continue
			}
			for _, pk := range rowPathKeys {
				if s, ok := row[pk].(string); ok {
					rel, inside := rewrite(s)
					if !inside {
						dropped[k]++
						continue rows
					}
					row[pk] = rel
				}
			}
			kept = append(kept, row)
		}
		payload[k] = kept
	}
	for _, k := range []string{"impacted_files", "changed_files"} {
		files, ok := payload[k].([]any)
		if !ok {
			continue
		}
		kept := []any{}
		for _, f := range files {
			if s, ok := f.(string); ok {
				if rel, inside := rewrite(s); inside {
					kept = append(kept, rel)
				}
			}
		}
		payload[k] = kept
	}
	for count, list := range map[string]string{"result_count": "results", "total_impacted": "impacted_nodes"} {
		if n, ok := toInt(payload[count]); ok && dropped[list] > 0 {
			payload[count] = max(0, n-dropped[list])
		}
	}
	if s, ok := payload["target"].(string); ok {
		if rel, inside := rewrite(s); inside {
			payload["target"] = rel
		} else {
			delete(payload, "target")
		}
	}
	for _, k := range []string{"target", "summary", "confidence"} {
		s, ok := payload[k].(string)
		if !ok {
			continue
		}
		for prefix := range prefixes {
			s = regexp.MustCompile(`(?i)`+regexp.QuoteMeta(prefix+"/")).ReplaceAllString(s, "")
		}
		if absolutePath.MatchString(s) {
			delete(payload, k)
			continue
		}
		payload[k] = s
	}
}

type crgRun struct {
	ctx             context.Context
	exe, root, data string
	calls, maxCalls int
}

// call runs one CRG subcommand and returns its payload only for exit 0 and a
// JSON object with status "ok".
func (r *crgRun) call(args ...string) map[string]any {
	p := r.raw(args...)
	if p == nil || p["status"] != "ok" {
		return nil
	}
	return p
}

// raw returns any JSON object CRG prints on exit 0, whatever its status.
func (r *crgRun) raw(args ...string) map[string]any {
	if r.calls >= r.maxCalls {
		return nil
	}
	r.calls++
	// V2 passes the full environment to CRG (a user-installed Python tool).
	out, err := procx.Output(r.ctx, procx.Cmd{Argv: append([]string{r.exe}, append(args, "--repo", r.root)...), Dir: r.root,
		Env: append(procx.InheritEnv(), "CRG_DATA_DIR="+r.data, "CRG_REPO_ROOT="+r.root), Timeout: crgTimeout, MaxStdout: crgMaxOutput})
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return nil
	}
	payload := map[string]any{}
	if json.Unmarshal(out, &payload) != nil {
		return nil
	}
	return payload
}

// crgAnchor ports V2 _crg_anchor on a raw payload.
func crgAnchor(payload map[string]any) (anchor, path string) {
	for _, r := range list(payload["results"]) {
		row, ok := r.(map[string]any)
		if !ok {
			continue
		}
		a := firstString(row, "qualified_name", "name")
		if a != "" {
			return a, firstString(row, "relative_path", "file_path", "path")
		}
	}
	return "", ""
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, _ := m[k].(string); strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func has(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// CRGContext ports V2 crg_context for one repository: it resolves an anchor,
// runs the requested graph patterns and returns compact, path-rewritten items.
// Nothing runs unless the CLI is installed and the graph is fresh.
func CRGContext(ctx context.Context, ws, rel string, q Query) []model.ContextItem {
	exe, err := exec.LookPath("code-review-graph")
	if err != nil || !GraphStatus(ctx, ws, rel).Ready {
		return nil
	}
	root, err := repoRoot(ws, rel)
	if err != nil {
		return nil
	}
	data, err := GraphDir(ws, rel)
	if err != nil {
		return nil
	}
	limit := max(1, q.Limit)
	r := &crgRun{ctx: ctx, exe: exe, root: root, data: data, maxCalls: q.MaxCalls}

	requested := q.Patterns
	if len(requested) == 0 {
		switch {
		case len(q.Changed) > 0:
			requested = []string{"impact"}
		case q.Symbol != "":
			requested = []string{"callers_of"}
		default:
			requested = []string{"architecture"}
		}
	}
	anchor, anchorPath := q.Symbol, ""
	needsAnchor := (has(requested, "impact") && len(q.Changed) == 0) ||
		has(requested, "callers_of") || has(requested, "callees_of") || has(requested, "tests_for")
	if needsAnchor && anchor == "" {
		if p := r.call("search", q.Text, "--limit", strconv.Itoa(limit)); p != nil {
			anchor, anchorPath = crgAnchor(p)
		}
	}
	shown := func() string {
		if a, ok, _ := relPath(root, anchor); ok {
			return a
		}
		return ""
	}

	items := []model.ContextItem{}
	add := func(p map[string]any, pattern string, score float64, anchor string) {
		rewritePaths(root, p)
		items = append(items, CRGItem(p, pattern, score, limit, anchor))
	}
	if has(requested, "impact") {
		files := append([]string{}, q.Changed...)
		if len(files) == 0 && anchorPath == "" && anchor != "" {
			if p := r.call("search", anchor, "--limit", strconv.Itoa(limit)); p != nil {
				_, anchorPath = crgAnchor(p)
			}
		}
		if len(files) == 0 && anchorPath != "" {
			if f, ok, _ := relPath(root, anchorPath); ok {
				files = []string{f}
			}
		}
		if len(files) > 0 {
			args := append(append([]string{"impact", "--files"}, files...), "--max-results", strconv.Itoa(limit))
			if p := r.call(args...); p != nil {
				add(p, "impact", 9, shown())
			}
		}
	}
	for _, pattern := range requested {
		if pattern == "impact" || pattern == "architecture" || pattern == "references_to" || anchor == "" {
			continue
		}
		p := r.raw("query", pattern, anchor)
		if p != nil && p["status"] == "ambiguous" {
			if qn := disambiguate(root, p, q.File, anchor); qn != "" {
				anchor = qn
				p = r.raw("query", pattern, anchor)
			}
		}
		if p != nil && p["status"] == "ok" {
			add(p, pattern, 9, shown())
		}
		if len(items) >= limit {
			break
		}
	}
	if has(requested, "architecture") && len(items) < limit {
		if p := r.call("architecture"); p != nil {
			add(p, "architecture", 8, "")
		}
	}
	vq := q
	vq.Symbol = anchor
	return Validate(ctx, ws, rel, items[:min(len(items), limit)], vq)
}

// disambiguate picks the one ambiguous-anchor candidate named name and
// defined in file (repository-relative) and returns its raw qualified name.
// CRG's candidate list is fuzzy, so both must match exactly; without exactly
// one match it returns "" rather than guess.
func disambiguate(root string, payload map[string]any, file, name string) string {
	if file == "" {
		return ""
	}
	want := strings.ToLower(strings.ReplaceAll(file, `\`, "/"))
	found := ""
	for _, c := range list(payload["candidates"]) {
		row, ok := c.(map[string]any)
		if !ok {
			continue
		}
		qn := firstString(row, "qualified_name")
		rel, inside, _ := relPath(root, firstString(row, "file_path", "relative_path", "path"))
		if qn == "" || !inside || strings.ToLower(rel) != want || firstString(row, "name") != name {
			continue
		}
		if found != "" {
			return ""
		}
		found = qn
	}
	return found
}
