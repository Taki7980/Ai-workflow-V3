package structural

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Taki7980/ai-workflow-v3/internal/structural/structuraltest"
)

// readyGraph returns a git workspace with a fresh (fake) graph and the fake
// code-review-graph on PATH.
func readyGraph(t *testing.T) string {
	t.Helper()
	structuraltest.UseFakeCRG(t)
	ws := gitRepo(t)
	writeFile(t, graphDB(t, ws), "graph")
	if _, err := WriteGraphManifest(context.Background(), ws, ".", "2.3.8", "build"); err != nil {
		t.Fatal(err)
	}
	return ws
}

func fakeLog(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "calls.log")
	t.Setenv("CRG_FAKE_LOG", p)
	return p
}

func logLines(t *testing.T, p string) []string {
	t.Helper()
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func keys(t *testing.T, v any) map[string]bool {
	t.Helper()
	b, _ := json.Marshal(v)
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}

func sameKeys(t *testing.T, got map[string]bool, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("keys %v, want %v", got, want)
	}
	for _, k := range want {
		if !got[k] {
			t.Fatalf("keys %v missing %q", got, k)
		}
	}
}

func TestCompactCRGImpact(t *testing.T) {
	p := map[string]any{"status": "ok", "summary": "s", "target": "", "impacted_files": []any{"a", "b", "c"},
		"impacted_nodes": []any{map[string]any{"n": 1}}, "edges": []any{}, "total_impacted": 3.0, "truncated": false}
	c := CompactCRG(p, "impact", 2)
	sameKeys(t, keys(t, c), "status", "pattern", "summary", "truncated", "total_impacted", "impacted_files", "impacted_nodes", "edges")
	b, _ := json.Marshal(c)
	if !strings.HasPrefix(string(b), `{"status":"ok","pattern":"impact","summary":"s","truncated":false,"total_impacted":3,"impacted_files":["a","b"]`) {
		t.Fatalf("order/truncation: %s", b)
	}
}

func TestCompactCRGArchitecture(t *testing.T) {
	p := map[string]any{"status": "ok", "summary": "s", "communities": []any{1.0, 2.0, 3.0}, "hub_nodes": "not a list"}
	sameKeys(t, keys(t, CompactCRG(p, "architecture", 2)), "status", "pattern", "summary", "communities")
}

func TestCompactCRGQuery(t *testing.T) {
	p := map[string]any{"status": "ok", "results": []any{1.0, 2.0, 3.0}, "confidence": "c"}
	c := CompactCRG(p, "callers_of", 5)
	sameKeys(t, keys(t, c), "status", "pattern", "confidence", "result_count", "results", "edges")
	if ResultCount(p, "callers_of") != 3 {
		t.Fatal("result_count falls back to len(results)")
	}
}

func TestVerifiedEmptyCRG(t *testing.T) {
	for conf, want := range map[string]bool{
		"Real absence; graph current":       true,
		"real absence, current, unverified": false,
		"":                                  false,
		"current but maybe missing":         false,
	} {
		if got := VerifiedEmptyCRG(map[string]any{"confidence": conf}); got != want {
			t.Errorf("%q: got %v", conf, got)
		}
	}
}

func TestCRGItemMetadata(t *testing.T) {
	empty := CRGItem(map[string]any{"status": "ok", "results": []any{}, "confidence": "real absence (graph current)"}, "callers_of", 9, 6, "Foo")
	if empty.Source != "code_review_graph" || empty.Metadata["structural_valid"] != true || empty.Metadata["empty_verified"] != true ||
		empty.Metadata["result_count"] != 0 || empty.Metadata["anchor"] != "Foo" || empty.Metadata["truncated"] != false {
		t.Fatalf("got %+v", empty)
	}
	none := CRGItem(map[string]any{"status": "ok", "results": []any{}}, "callers_of", 9, 6, "")
	if none.Metadata["structural_valid"] != false {
		t.Fatalf("got %+v", none)
	}
	if _, ok := none.Metadata["anchor"]; ok {
		t.Fatal("empty anchor must be omitted")
	}
}

func TestRewritePathsWindowsSpaces(t *testing.T) {
	root := filepath.Join(t.TempDir(), "with space")
	writeFile(t, filepath.Join(root, "pkg", "a.go"), "package pkg\n")
	slash := filepath.ToSlash(root)
	p := map[string]any{
		"target":  slash + "/pkg/a.go::Foo",
		"summary": "callers_of('" + slash + "/pkg/a.go::Foo')",
		"results": []any{
			map[string]any{"name": "Bar", "file_path": slash + "/pkg/a.go", "qualified_name": slash + "/pkg/a.go::Bar"},
			map[string]any{"name": "Ext", "file_path": "/elsewhere/x.go", "qualified_name": "/elsewhere/x.go::Ext"},
		},
		"impacted_files": []any{slash + "/pkg/a.go", "/elsewhere/x.go"},
	}
	rewritePaths(root, p)
	rows := p["results"].([]any)
	if len(rows) != 1 {
		t.Fatalf("outside row kept: %v", rows)
	}
	row := rows[0].(map[string]any)
	if row["file_path"] != "pkg/a.go" || row["qualified_name"] != "pkg/a.go::Bar" {
		t.Fatalf("row %v", row)
	}
	if p["target"] != "pkg/a.go::Foo" || p["summary"] != "callers_of('pkg/a.go::Foo')" {
		t.Fatalf("target/summary %v / %v", p["target"], p["summary"])
	}
	if files := p["impacted_files"].([]any); len(files) != 1 || files[0] != "pkg/a.go" {
		t.Fatalf("files %v", files)
	}
}

func TestCRGContextCallers(t *testing.T) {
	ws := readyGraph(t)
	items := CRGContext(context.Background(), ws, ".", Query{Text: "who calls Foo", Patterns: []string{"callers_of"}, Limit: 6, MaxCalls: 6})
	if len(items) != 1 {
		t.Fatalf("got %d items: %+v", len(items), items)
	}
	it := items[0]
	if it.Source != "code_review_graph" || it.Metadata["pattern"] != "callers_of" || it.Metadata["anchor"] != "pkg/a.go::Foo" {
		t.Fatalf("got %+v", it)
	}
	if n, _ := it.Metadata["result_count"].(int); n != 1 {
		t.Fatalf("result_count %v", it.Metadata["result_count"])
	}
	slash := filepath.ToSlash(ws)
	if strings.Contains(it.Text, "{{ROOT}}") || strings.Contains(strings.ToLower(it.Text), strings.ToLower(slash)) || strings.Contains(it.Text, "/elsewhere") {
		t.Fatalf("absolute path leaked: %s", it.Text)
	}
}

func TestCRGContextImpactUsesChangedFiles(t *testing.T) {
	ws := readyGraph(t)
	log := fakeLog(t)
	items := CRGContext(context.Background(), ws, ".", Query{Text: "impact of change", Changed: []string{"pkg/a.go"}, Patterns: []string{"impact"}, Limit: 6, MaxCalls: 6})
	if len(items) != 1 || items[0].Metadata["pattern"] != "impact" || items[0].Score != 9 {
		t.Fatalf("got %+v", items)
	}
	lines := logLines(t, log)
	if len(lines) != 1 || !strings.Contains(lines[0], "impact --files pkg/a.go") {
		t.Fatalf("calls %v", lines)
	}
}

func TestCRGContextArchitectureDefault(t *testing.T) {
	ws := readyGraph(t)
	items := CRGContext(context.Background(), ws, ".", Query{Text: "overview", Limit: 6, MaxCalls: 6})
	if len(items) != 1 || items[0].Metadata["pattern"] != "architecture" || items[0].Score != 8 {
		t.Fatalf("got %+v", items)
	}
}

func TestCRGContextStaleGraphNoCall(t *testing.T) {
	structuraltest.UseFakeCRG(t)
	ws := gitRepo(t)
	log := fakeLog(t)
	if items := CRGContext(context.Background(), ws, ".", Query{Text: "who calls Foo", Patterns: []string{"callers_of"}, Limit: 6, MaxCalls: 6}); items != nil {
		t.Fatalf("stale graph used: %+v", items)
	}
	if lines := logLines(t, log); lines != nil {
		t.Fatalf("fake ran: %v", lines)
	}
}

func TestRunCRGRejects(t *testing.T) {
	ws := readyGraph(t)
	for _, mode := range []string{"fail", "garbage"} {
		t.Setenv("CRG_FAKE_MODE", mode)
		if items := CRGContext(context.Background(), ws, ".", Query{Text: "overview", Limit: 6, MaxCalls: 6}); len(items) != 0 {
			t.Fatalf("%s accepted: %+v", mode, items)
		}
	}
	t.Setenv("CRG_FAKE_MODE", "")
	if items := CRGContext(context.Background(), ws, ".", Query{Text: "who calls Nope", Symbol: "Nope", Patterns: []string{"tests_for"}, Limit: 6, MaxCalls: 6}); len(items) != 0 {
		t.Fatalf("not_found accepted: %+v", items)
	}
}

func TestRunCRGTimeout(t *testing.T) {
	ws := readyGraph(t)
	old := crgTimeout
	crgTimeout = 300 * time.Millisecond
	t.Cleanup(func() { crgTimeout = old })
	t.Setenv("CRG_FAKE_MODE", "hang")
	start := time.Now()
	if items := CRGContext(context.Background(), ws, ".", Query{Text: "overview", Limit: 6, MaxCalls: 6}); len(items) != 0 {
		t.Fatalf("got %+v", items)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("hung CRG not killed")
	}
}

func TestCRGMaxCalls(t *testing.T) {
	ws := readyGraph(t)
	log := fakeLog(t)
	if items := CRGContext(context.Background(), ws, ".", Query{Text: "who calls Foo", Patterns: []string{"callers_of"}, Limit: 6, MaxCalls: 1}); len(items) != 0 {
		t.Fatalf("got %+v", items)
	}
	if lines := logLines(t, log); len(lines) != 1 {
		t.Fatalf("calls %v", lines)
	}
}
