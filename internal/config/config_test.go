package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultDocumentPreservesV2MigrationKeys(t *testing.T) {
	doc := DefaultDocument()
	ctx := doc["context"].(map[string]any)
	for _, key := range []string{"semantic", "external_retrievers", "selector", "learning", "deployment", "production", "targeted_search"} {
		if _, ok := ctx[key]; !ok {
			t.Fatalf("missing V2 compatibility key context.%s", key)
		}
	}
}

func TestLoadToleratesAdditiveV2Fields(t *testing.T) {
	root := t.TempDir()
	p := Path(root)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	doc := DefaultDocument()
	doc["future_additive_field"] = map[string]any{"enabled": true}
	b, _ := json.Marshal(doc)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err != nil {
		t.Fatalf("additive field should remain migration-compatible: %v", err)
	}
}

func TestDefaultSelectorMatchesV2Contract(t *testing.T) {
	cfg := Default()
	got := cfg.Context.Selector
	if !got.Enabled {
		t.Fatal("selector must default enabled")
	}
	if got.TightBudgetFraction != 0.3 {
		t.Fatalf("tight_budget_fraction=%v want=0.3", got.TightBudgetFraction)
	}
	if !got.MandatoryStructuralEvidence {
		t.Fatal("mandatory_structural_evidence must default true")
	}
	if got.MaxSelectorCandidates != 200 {
		t.Fatalf("max_selector_candidates=%d want=200", got.MaxSelectorCandidates)
	}

	doc := DefaultDocument()
	ctx := doc["context"].(map[string]any)
	raw, ok := ctx["selector"].(map[string]any)
	if !ok {
		t.Fatalf("context.selector has unexpected type %T", ctx["selector"])
	}
	if raw["enabled"] != true ||
		raw["tight_budget_fraction"] != 0.3 ||
		raw["mandatory_structural_evidence"] != true ||
		raw["max_selector_candidates"] != float64(200) {
		t.Fatalf("selector document=%#v", raw)
	}
}

func TestSelectorValidationRejectsInvalidTightBudgetFraction(t *testing.T) {
	for _, value := range []float64{0, -0.1, 1.1} {
		cfg := Default()
		cfg.Context.Selector.TightBudgetFraction = value
		if err := cfg.Validate(); err == nil {
			t.Fatalf("fraction=%v expected validation error", value)
		}
	}
}

func TestSelectorValidationRejectsInvalidCandidateCap(t *testing.T) {
	cfg := Default()
	cfg.Context.Selector.MaxSelectorCandidates = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected max_selector_candidates validation error")
	}
}
