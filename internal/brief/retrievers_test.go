package brief

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Taki7980/ai-workflow-v3/internal/config"
	"github.com/Taki7980/ai-workflow-v3/internal/model"
	"github.com/Taki7980/ai-workflow-v3/internal/provider"
	"github.com/Taki7980/ai-workflow-v3/internal/storage"
)

func TestHybridSimilarity(t *testing.T) {
	if s := hybridSimilarity("payment retry", "RetryPayment function pay.go"); s < .3 {
		t.Fatalf("camel-case split must match: %v", s)
	}
	if s := hybridSimilarity("payment retry", "ParseConfig function cfg.go"); s >= .08 {
		t.Fatalf("unrelated symbol scored %v", s)
	}
	if hybridSimilarity("x", "x") > 1 {
		t.Fatal("score must be capped at 1")
	}
}

func TestBuiltinSemanticEvidence(t *testing.T) {
	root := newWorkspace(t, map[string]string{"pay.go": payGo})
	p, err := Build(context.Background(), root, "explain how payment retry works", Options{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range p.Context {
		if it.Source == "semantic" && it.Metadata["provider"] == config.BuiltinSemanticProvider && strings.Contains(it.Text, "RetryPayment") {
			found = true
		}
	}
	if !found || p.Retrieval.ProvidersSkipped["semantic"] != "" {
		t.Fatalf("builtin semantic evidence missing: %+v", p.Context)
	}
}

func buildFakeProvider(t *testing.T) string {
	t.Helper()
	name := "fakeprovider"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	out := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = filepath.Join("testdata", "fakeprovider")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build fake provider: %v %s", err, b)
	}
	return out
}

func TestExternalRetrieverThroughTrustedRegistry(t *testing.T) {
	root := newWorkspace(t, map[string]string{"pay.go": payGo})
	exe := buildFakeProvider(t)
	b, _ := os.ReadFile(exe)
	s := sha256.Sum256(b)
	sum := hex.EncodeToString(s[:])
	reg := filepath.Join(t.TempDir(), "providers.json")
	doc, _ := json.Marshal(map[string]any{"providers": map[string]any{"fake": map[string]any{"command": []string{exe}, "sha256": sum}}})
	if err := os.WriteFile(reg, doc, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(provider.RegistryEnv, reg)

	cfg, _ := config.Load(root)
	cfg.Context.ExternalRetrievers = []json.RawMessage{
		json.RawMessage(`{"name":"docs","provider_id":"fake","intents":["all"]}`),
		json.RawMessage(`{"name":"rogue","command":["` + filepath.ToSlash(exe) + `"]}`),
	}
	if err := storage.WriteJSON(config.Path(root), cfg); err != nil {
		t.Fatal(err)
	}
	p, err := Build(context.Background(), root, "fix RetryPayment retry handling", Options{})
	if err != nil {
		t.Fatal(err)
	}
	var ext []string
	for _, it := range p.Context {
		if it.Source == "external:docs" {
			ext = append(ext, it.Text)
			if it.Evidence == nil || it.Evidence.TrustClass != "untrusted_external_provider" {
				t.Fatalf("external evidence must be tagged untrusted: %+v", it.Evidence)
			}
			if path, ok := it.Metadata["path"]; ok && path != "pay.go" {
				t.Fatalf("escaping path kept: %v", path)
			}
		}
	}
	if len(ext) == 0 || !strings.Contains(strings.Join(ext, "\n"), "external evidence for") {
		t.Fatalf("external retriever evidence missing: %+v / errors %v", p.Context, p.Retrieval.ProviderErrors)
	}
	if !strings.Contains(p.Retrieval.ProviderErrors["external:rogue"], "disabled") {
		t.Fatalf("inline repository command must be refused: %v", p.Retrieval.ProviderErrors)
	}
}

func TestCapBySource(t *testing.T) {
	mk := func(src string, chars int) model.ContextItem {
		return model.ContextItem{Source: src, Text: strings.Repeat("x", chars)}
	}
	items := []model.ContextItem{mk("lightweight_index", 400), mk("lightweight_index", 400), mk("lightweight_index", 400),
		mk("semantic", 4000), mk("targeted_source", 40), mk("targeted_source", 40), mk("targeted_source", 40)}
	// 1000-token budget: lightweight share .3 = 300 tokens (3 x 100 fits), max 2 items per source.
	got := capBySource(items, 1000, map[string]float64{"lightweight": .3, "source_fallback": .15}, 2, nil, false)
	counts := map[string]int{}
	for _, it := range got {
		counts[it.Source]++
	}
	if counts["lightweight_index"] != 2 || counts["targeted_source"] != 2 || counts["semantic"] != 1 {
		t.Fatalf("counts=%v", counts)
	}
	// The first item of a bucket is kept even when it alone exceeds the share.
	if got := capBySource([]model.ContextItem{mk("lightweight_index", 8000)}, 100, map[string]float64{"lightweight": .3}, 6, nil, false); len(got) != 1 {
		t.Fatal("first item must survive")
	}
}
