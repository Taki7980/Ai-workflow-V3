package brief

import (
	"slices"
	"testing"

	"github.com/Taki7980/ai-workflow-v3/internal/indexer"
	"github.com/Taki7980/ai-workflow-v3/internal/model"
)

func TestSearchTermPrefersCodeShapedIdentifiers(t *testing.T) {
	for query, want := range map[string]string{
		"fix BM25 ranking tie-break":     "BM25",
		"update retry_count handling":    "retry_count",
		"make SelectMMR respect budgets": "SelectMMR",
		"fix the checkout flow":          "checkout",
		"go to it":                       "",
	} {
		if got := searchTerm(query); got != want {
			t.Errorf("searchTerm(%q) = %q, want %q", query, got, want)
		}
	}
}

func TestCodeFirstRanksIndexedFilesAheadOfDocs(t *testing.T) {
	indexes := map[string]indexer.Index{".": {Files: map[string]indexer.FileState{"svc/a.go": {}, "svc/b.go": {}}}}
	lines := []string{"README.md:3: ranking", "docs/x.md:9: ranking", "svc/a.go:4: ranking", "svc/b.go:7: ranking"}
	got := codeFirst(lines, indexes, 3)
	want := []string{"svc/a.go:4: ranking", "svc/b.go:7: ranking", "README.md:3: ranking"}
	if !slices.Equal(got, want) {
		t.Fatalf("codeFirst = %q, want %q", got, want)
	}
}

func TestUncoveredDropsMatchesInsideIndexWindows(t *testing.T) {
	items := []model.ContextItem{{Source: "lightweight_index", Metadata: map[string]any{"repository": ".", "path": "svc/a.go", "line": 20}}}
	lines := []string{"svc/a.go:14: in", "svc/a.go:26: in", "svc/a.go:27: out", "svc/b.go:20: other file"}
	got := uncovered(lines, items, 6)
	want := []string{"svc/a.go:27: out", "svc/b.go:20: other file"}
	if !slices.Equal(got, want) {
		t.Fatalf("uncovered = %q, want %q", got, want)
	}
}

func TestTestPathAndWordPatterns(t *testing.T) {
	for path, want := range map[string]bool{
		"tests/test_pay.py": true, "pay/pay_test.go": true, "src/pay.spec.ts": true, "web/__tests__/pay.js": true,
		"test_pay.py": true, "pay/pay.go": false, "latest/pay.py": false, "attestation.go": false,
	} {
		if got := testPathRE.MatchString(path); got != want {
			t.Errorf("testPathRE(%q) = %v, want %v", path, got, want)
		}
	}
	for query, want := range map[string]bool{"which tests cover retry": true, "add test for retry": true, "fix the latest retry": false, "attest builds": false} {
		if got := testWordRE.MatchString(query); got != want {
			t.Errorf("testWordRE(%q) = %v, want %v", query, got, want)
		}
	}
}

func TestDemoteTestsRanksSourceFirstUnlessTaskIsAboutTests(t *testing.T) {
	mk := func() []indexer.Hit {
		return []indexer.Hit{{Path: "pay_test.go", Score: 1.1}, {Path: "pay.go", Score: 1.0}}
	}
	hits := mk()
	demoteTests("fix retry backoff", hits)
	if hits[0].Path != "pay.go" || hits[1].Score >= 1.1 {
		t.Fatalf("edit task: %+v", hits)
	}
	hits = mk()
	demoteTests("which tests cover retry backoff", hits)
	if hits[0].Path != "pay_test.go" || hits[0].Score != 1.1 {
		t.Fatalf("test task: %+v", hits)
	}
}
