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
