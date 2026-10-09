package brief

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Taki7980/ai-workflow-v3/internal/model"
	"github.com/Taki7980/ai-workflow-v3/internal/structural"
	"github.com/Taki7980/ai-workflow-v3/internal/structural/structuraltest"
	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

const fooGo = "package pkg\n\nfunc Foo() {}\n\nfunc Bar() { Foo() }\n"

func crgLog(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "crg.log")
	t.Setenv("CRG_FAKE_LOG", p)
	return p
}

func crgCalls(t *testing.T, p string) []string {
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

func freshGraph(t *testing.T, root, rel string) {
	t.Helper()
	dir, err := structural.GraphDir(root, rel)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "graph.db"), []byte("graph"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := structural.WriteGraphManifest(context.Background(), root, rel, "2.3.8", "build"); err != nil {
		t.Fatal(err)
	}
}

func hasNote(p Packet, sub string) bool {
	for _, f := range p.Retrieval.Fallbacks {
		if strings.Contains(f, sub) {
			return true
		}
	}
	return false
}

func TestBriefStructuralCRG(t *testing.T) {
	structuraltest.UseFakeCRG(t)
	root := newWorkspace(t, map[string]string{"pkg/a.go": fooGo})
	freshGraph(t, root, ".")
	p, err := Build(context.Background(), root, "who calls Foo", Options{})
	if err != nil {
		t.Fatal(err)
	}
	r := p.Retrieval
	if !r.Sufficiency.StructuralComplete || !slices.Contains(r.ProvidersAttempted, "structural-expansion") {
		t.Fatalf("structural incomplete: %+v", r)
	}
	if _, ok := firstSource(p, "code_review_graph"); !ok {
		t.Fatalf("no code_review_graph item selected: %+v", p.Context)
	}
	if _, ok := r.ProvidersSkipped["structural"]; ok || hasNote(p, "source fallback used") {
		t.Fatalf("fallback reported: %+v %v", r.ProvidersSkipped, r.Fallbacks)
	}
}

func TestBriefStructuralStaleGraph(t *testing.T) {
	structuraltest.UseFakeCRG(t)
	root := newWorkspace(t, map[string]string{"pkg/a.go": fooGo})
	p, err := Build(context.Background(), root, "who calls Foo", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !hasNote(p, "code-review-graph stale for .: graph.db is missing; run ai-workflow graph sync") || p.Retrieval.Sufficiency.StructuralComplete {
		t.Fatalf("got %v", p.Retrieval.Fallbacks)
	}
	if p.Retrieval.ProvidersSkipped["structural-expansion"] != "graph.db is missing" {
		t.Fatalf("skipped %v", p.Retrieval.ProvidersSkipped)
	}
}

func TestBriefNonStructuralSkipsCRG(t *testing.T) {
	structuraltest.UseFakeCRG(t)
	log := crgLog(t)
	root := newWorkspace(t, map[string]string{"pkg/a.go": fooGo, "README.md": "teh readme\n"})
	freshGraph(t, root, ".")
	if _, err := Build(context.Background(), root, "fix typo in README", Options{}); err != nil {
		t.Fatal(err)
	}
	if calls := crgCalls(t, log); calls != nil {
		t.Fatalf("CRG ran for a non-structural task: %v", calls)
	}
}

func TestBriefStructuralNestedRepoAnchor(t *testing.T) {
	structuraltest.UseFakeCRG(t)
	log := crgLog(t)
	root := newWorkspace(t, map[string]string{"README.md": "control\n", "svc/pkg/a.go": fooGo})
	gitInit(t, filepath.Join(root, "svc"))
	reindex(t, root)
	freshGraph(t, root, "svc")
	if _, err := Build(context.Background(), root, "who calls Foo", Options{}); err != nil {
		t.Fatal(err)
	}
	calls := crgCalls(t, log)
	if len(calls) == 0 {
		t.Fatal("CRG never ran")
	}
	for _, c := range calls {
		if !strings.HasSuffix(strings.SplitN(c, "\t", 2)[0], "/svc") {
			t.Fatalf("CRG ran outside the anchor repo: %v", calls)
		}
	}
}

func TestBriefStructuralDeadline(t *testing.T) {
	structuraltest.UseFakeCRG(t)
	t.Setenv("CRG_FAKE_MODE", "hang")
	old := structuralDeadline
	structuralDeadline = 500 * time.Millisecond
	t.Cleanup(func() { structuralDeadline = old })
	root := newWorkspace(t, map[string]string{"pkg/a.go": fooGo})
	freshGraph(t, root, ".")
	start := time.Now()
	p, err := Build(context.Background(), root, "who calls Foo", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 10*time.Second || p.Retrieval.ProviderErrors["structural-expansion"] == "" {
		t.Fatalf("took %v, errors %v", time.Since(start), p.Retrieval.ProviderErrors)
	}
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
}

func TestStructuralAnchorPrefersSymbolNamedInTask(t *testing.T) {
	repo := workspace.Repository{RelativePath: ".", RepositoryID: "r"}
	item := func(symbol string, score float64) model.ContextItem {
		return model.ContextItem{Source: "lightweight_index", Score: score,
			Text:     `{"symbol":"` + symbol + `","kind":"func","file":"x.go","line":1,"repository":"."}` + "\nbody",
			Metadata: map[string]any{"repository": "."}}
	}
	_, symbol, _, ok := structuralAnchor("who calls Build", []model.ContextItem{item("crgCalls", 9), item("Build", 3)}, []workspace.Repository{repo})
	if !ok || symbol != "Build" {
		t.Fatalf("anchor %q", symbol)
	}
	_, symbol, _, _ = structuralAnchor("what breaks here", []model.ContextItem{item("crgCalls", 9), item("Build", 3)}, []workspace.Repository{repo})
	if symbol != "crgCalls" {
		t.Fatalf("fallback anchor %q", symbol)
	}
}
