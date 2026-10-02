package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/Taki7980/ai-workflow-v3/internal/config"
	"github.com/Taki7980/ai-workflow-v3/internal/indexer"
	"github.com/Taki7980/ai-workflow-v3/internal/storage"
	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

func writeContextWorkspace(t *testing.T, cfg config.Config, idx indexer.Index) string {
	t.Helper()
	root := t.TempDir()
	repo := workspace.Repository{
		RepositoryID: workspace.RepositoryID(".", ""),
		Name:         "fixture",
		RelativePath: ".",
		Included:     true,
		Reason:       "test",
	}
	if err := workspace.Save(root, []workspace.Repository{repo}); err != nil {
		t.Fatal(err)
	}
	if err := storage.WriteJSON(config.Path(root), cfg); err != nil {
		t.Fatal(err)
	}
	if err := storage.WriteJSON(indexer.Path(root, repo), idx); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestContextCommandRetainsHitArraySchema(t *testing.T) {
	cfg := config.Default()
	idx := indexer.Index{
		Version:    1,
		Repository: ".",
		Files: map[string]indexer.FileState{
			"service.go": {Size: 40},
		},
		Symbols: []indexer.Symbol{
			{Name: "ProcessPayment", Kind: "function", Path: "service.go", Line: 10},
		},
	}
	root := writeContextWorkspace(t, cfg, idx)
	var out, errOut bytes.Buffer
	code := Run([]string{"--root", root, "context", "ProcessPayment"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut.String())
	}
	var hits []indexer.Hit
	if err := json.Unmarshal(out.Bytes(), &hits); err != nil {
		t.Fatalf("context output must remain []indexer.Hit JSON: %v\n%s", err, out.String())
	}
	if len(hits) != 1 || hits[0].Path != "service.go" {
		t.Fatalf("hits=%#v", hits)
	}
}

func TestContextCommandRespectsAnswerLaneTokenBudget(t *testing.T) {
	cfg := config.Default()
	cfg.Budgets.Answer.EstimatedTokens = 5
	idx := indexer.Index{
		Version:    1,
		Repository: ".",
		Files: map[string]indexer.FileState{
			"a.go": {Size: 20},
			"b.go": {Size: 20},
		},
		Symbols: []indexer.Symbol{
			{Name: "PaymentAlpha", Kind: "function", Path: "a.go", Line: 1},
			{Name: "PaymentBeta", Kind: "function", Path: "b.go", Line: 1},
		},
	}
	root := writeContextWorkspace(t, cfg, idx)
	var out, errOut bytes.Buffer
	code := Run([]string{"--root", root, "context", "Payment"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut.String())
	}
	var hits []indexer.Hit
	if err := json.Unmarshal(out.Bytes(), &hits); err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("selected hits=%d want=1 under 5-token budget: %#v", len(hits), hits)
	}
}

func TestEstimateFileTokensZeroByteMinimumOne(t *testing.T) {
	if got := estimateFileTokens(indexer.FileState{Size: 0}); got != 1 {
		t.Fatalf("zero-byte estimate=%d want=1", got)
	}
}

func TestSelectContextHitsReturnsSelectorError(t *testing.T) {
	cfg := config.Default()
	cfg.Context.Selector.MaxSelectorCandidates = 0
	indexes := map[string]indexer.Index{
		".": {
			Version:    1,
			Repository: ".",
			Files:      map[string]indexer.FileState{"a.go": {Size: 4}},
			Symbols:    []indexer.Symbol{{Name: "Payment", Kind: "function", Path: "a.go", Line: 1}},
		},
	}
	if _, err := selectContextHits("Payment", indexes, cfg); err == nil {
		t.Fatal("expected selector error")
	}
}
