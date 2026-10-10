package telemetry

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Taki7980/ai-workflow-v3/internal/config"
	"github.com/Taki7980/ai-workflow-v3/internal/model"
)

func TestEnabledModes(t *testing.T) {
	cfg := config.Default()
	if Enabled(cfg, model.LaneAnswer, false) || !Enabled(cfg, model.LaneSmall, false) || !Enabled(cfg, model.LaneAnswer, true) {
		t.Fatal("mutations mode")
	}
	cfg.Context.Telemetry.Mode = "off"
	if Enabled(cfg, model.LaneFull, false) {
		t.Fatal("off mode")
	}
	cfg.Context.Telemetry.Mode = "all"
	if !Enabled(cfg, model.LaneAnswer, false) {
		t.Fatal("all mode")
	}
}

func TestWriteNeverStoresTaskAndRedacts(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.Context.Telemetry.RedactPatterns = []string{`sk-[a-z0-9]+`}
	t.Setenv(HMACKeyEnv, "k")
	path, err := Write(context.Background(), root, cfg, Trace{Task: "secret task text", Intent: "exact", Fallbacks: []string{"used sk-abc123"}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	s := string(b)
	if strings.Contains(s, "secret task text") || strings.Contains(s, "sk-abc123") || !strings.Contains(s, "[REDACTED]") || !strings.Contains(s, "task_fingerprint") {
		t.Fatal(s)
	}
}

func TestPruneKeepsMaxFiles(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.Context.Telemetry.MaxTraceFiles = 2
	for i := 0; i < 4; i++ {
		if _, err := Write(context.Background(), root, cfg, Trace{Intent: "exact"}); err != nil {
			t.Fatal(err)
		}
	}
	if s := Summarize(root, 200); s["runs"] != 2 {
		t.Fatalf("runs=%v", s["runs"])
	}
}

func TestRecommendAndSummarize(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	for i := 0; i < 5; i++ {
		_, _ = Write(context.Background(), root, cfg, Trace{Intent: "exact", Fallbacks: []string{"x"}, BudgetChars: 100, UsedChars: 50, StageLatencyMS: map[string]float64{"index": float64(i)}})
	}
	if r := Recommend(root, 200, 20); len(r["recommendations"].([]map[string]any)) != 0 {
		t.Fatal("must not recommend below minimum runs")
	}
	r := Recommend(root, 200, 5)
	if recs := r["recommendations"].([]map[string]any); len(recs) != 1 || recs[0]["signal"] != "high_fallback_rate" {
		t.Fatalf("%v", r)
	}
	s := Summarize(root, 200)
	if s["fallback_rate"] != 1.0 || s["mean_budget_utilization"] != 0.5 {
		t.Fatalf("%v", s)
	}
	lat := s["latency_ms"].(map[string]any)["index"].(map[string]float64)
	if lat["p50"] != 2 || lat["mean"] != 2 {
		t.Fatalf("latency=%v", lat)
	}
}

func TestOTLPTrustRules(t *testing.T) {
	cases := map[string]bool{
		"https://otel.example.com/v1/logs":  true,
		"http://otel.example.com/v1/logs":   false,
		"https://user:pw@otel.example.com/": false,
		"https://127.0.0.1/":                false,
		"https://otel.other.com/":           false,
	}
	t.Setenv(OTLPAllowedHostsEnv, "otel.example.com,127.0.0.1")
	for endpoint, want := range cases {
		t.Setenv(OTLPEndpointEnv, endpoint)
		if _, ok := trustedOTLP(); ok != want {
			t.Errorf("%s trusted=%v want %v", endpoint, ok, want)
		}
	}
}
