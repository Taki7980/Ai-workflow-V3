package structural

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func scipPayload(t *testing.T) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "scip", "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestItemsFromScipPayload(t *testing.T) {
	root := filepath.Join("testdata", "scip", "repo")
	p := scipPayload(t)
	items := ItemsFromScipPayload(root, p, Query{Symbol: "Foo", Limit: 10})
	if len(items) != 2 {
		t.Fatalf("got %d: %+v", len(items), items)
	}
	def, ref := items[0], items[1]
	if def.Source != "scip" || def.Score != 10 || def.Text != "pkg/a.go:3: func Foo() {} [definition] Foo" {
		t.Fatalf("definition %+v", def)
	}
	if def.Metadata["pattern"] != "definition_of" || def.Metadata["structural_valid"] != true || def.Metadata["line"] != 3 ||
		def.Metadata["path"] != "pkg/a.go" || def.Metadata["symbol_name"] != "Foo" || def.Metadata["role"] != "definition" ||
		def.Metadata["language"] != "go" || def.Metadata["result_count"] != 1 {
		t.Fatalf("definition metadata %+v", def.Metadata)
	}
	if ref.Score != 9 || ref.Metadata["pattern"] != "references_to" || ref.Text != "pkg/a.go:5: func Bar() { Foo() } [reference] Foo" {
		t.Fatalf("reference %+v", ref)
	}

	refs := ItemsFromScipPayload(root, p, Query{Symbol: "Foo", Limit: 10, Patterns: []string{"references_to"}})
	if len(refs) != 1 || refs[0].Metadata["role"] != "reference" {
		t.Fatalf("references_to: %+v", refs)
	}
	changed := ItemsFromScipPayload(root, p, Query{Symbol: "Foo", Limit: 10, Changed: []string{"pkg/a.go"}})
	if changed[1].Score != 9.25 {
		t.Fatalf("changed boost: %v", changed[1].Score)
	}
	if one := ItemsFromScipPayload(root, p, Query{Symbol: "Foo", Limit: 1}); len(one) != 1 {
		t.Fatalf("limit: %d", len(one))
	}
	// Query anchor: distinctive identifier wins; Bar matches only Bar's definition.
	if bar := ItemsFromScipPayload(root, p, Query{Text: "where is Bar", Limit: 10}); len(bar) != 1 || bar[0].Metadata["symbol_name"] != "Bar" {
		t.Fatalf("query anchor: %+v", bar)
	}
}

func TestQueryAnchor(t *testing.T) {
	for q, want := range map[string]string{
		"who calls build_brief now": "build_brief",
		"where is parseFoo used":    "parsefoo",
		"plain words only":          "only",
		"":                          "",
	} {
		if got := QueryAnchor(q); got != want {
			t.Errorf("QueryAnchor(%q) = %q, want %q", q, got, want)
		}
	}
}

func TestDetectIndexer(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module x\n")
	writeFile(t, filepath.Join(dir, "a.go"), "package x\n")
	ix, ok := DetectIndexer(dir, "")
	if !ok || ix.Language != "go" || ix.Executable != "scip-go" || !slices.Equal(ix.Args, []string{"./..."}) {
		t.Fatalf("go: %+v %v", ix, ok)
	}
	writeFile(t, filepath.Join(dir, "pyproject.toml"), "")
	writeFile(t, filepath.Join(dir, "b.py"), "")
	if _, ok := DetectIndexer(dir, ""); ok {
		t.Fatal("ambiguous languages must not pick an indexer")
	}
	py, ok := DetectIndexer(dir, "py")
	if !ok || py.Language != "python" || !slices.Equal(py.Args, []string{"index", ".", "--project-name", filepath.Base(dir)}) {
		t.Fatalf("python: %+v", py)
	}
	if _, ok := DetectIndexer(dir, "cobol"); ok {
		t.Fatal("unknown language")
	}

	vendored := t.TempDir()
	writeFile(t, filepath.Join(vendored, "requirements.txt"), "")
	writeFile(t, filepath.Join(vendored, "node_modules", "x.py"), "")
	if _, ok := DetectIndexer(vendored, ""); ok {
		t.Fatal("sources under skipped dirs must not count")
	}
}

func TestScipContextNotReady(t *testing.T) {
	ws := gitRepo(t)
	if items := ScipContext(context.Background(), ws, ".", Query{Symbol: "Foo", Limit: 5}); items != nil {
		t.Fatalf("got %+v", items)
	}
}

func TestScipContextReady(t *testing.T) {
	ctx := context.Background()
	ws := gitRepo(t)
	dir, _ := ScipDir(ws, ".")
	writeFile(t, filepath.Join(dir, "index.scip"), "bin")
	b, _ := os.ReadFile(filepath.Join("testdata", "scip", "index.json"))
	writeFile(t, filepath.Join(dir, "index.json"), string(b))
	if _, err := WriteScipManifest(ctx, ws, ".", "go", "scip-go"); err != nil {
		t.Fatal(err)
	}
	items := ScipContext(ctx, ws, ".", Query{Symbol: "Foo", Limit: 5})
	if len(items) != 2 || items[0].Text != "pkg/a.go:3: func Foo() {} [definition] Foo" {
		t.Fatalf("got %+v", items)
	}
}
