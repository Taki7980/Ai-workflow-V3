package providers

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Taki7980/ai-workflow-v3/internal/config"
	"github.com/Taki7980/ai-workflow-v3/internal/model"
	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

func stubEnv(t *testing.T, home string, found map[string]bool) {
	t.Helper()
	oldLook, oldHome := lookPath, homeDir
	lookPath = func(name string) (string, error) {
		if found[name] {
			return "/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
	homeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { lookPath, homeDir = oldLook, oldHome })
}

func mkdir(t *testing.T, parts ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(parts...), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestDetectSuperpowersEnvOverride(t *testing.T) {
	root := t.TempDir()
	stubEnv(t, t.TempDir(), nil)
	mkdir(t, root, ".agents", "skills", "superpowers")
	t.Setenv("AI_WORKFLOW_SUPERPOWERS", "yes")
	if !Detect(root, config.Default(), workspace.Registry{}).Superpowers {
		t.Fatal("env yes must force superpowers on")
	}
	t.Setenv("AI_WORKFLOW_SUPERPOWERS", "0")
	if Detect(root, config.Default(), workspace.Registry{}).Superpowers {
		t.Fatal("env 0 must force superpowers off even when installed")
	}
}

func TestDetectSuperpowersPluginGlob(t *testing.T) {
	home := t.TempDir()
	stubEnv(t, home, nil)
	t.Setenv("AI_WORKFLOW_SUPERPOWERS", "")
	os.Unsetenv("AI_WORKFLOW_SUPERPOWERS")
	if Detect(t.TempDir(), config.Default(), workspace.Registry{}).Superpowers {
		t.Fatal("empty home must not detect superpowers")
	}
	mkdir(t, home, ".claude", "plugins", "cache", "x", "superpowers", "1")
	if !Detect(t.TempDir(), config.Default(), workspace.Registry{}).Superpowers {
		t.Fatal("plugin dir must detect superpowers")
	}
}

func TestDetectCRGRequiresBinaryAndGraph(t *testing.T) {
	root := t.TempDir()
	stubEnv(t, t.TempDir(), map[string]bool{"code-review-graph": true})
	reg := workspace.Registry{Repositories: []workspace.Repository{{RelativePath: "svc", Included: true}}}
	cfg := config.Default()
	if Detect(root, cfg, reg).CodeReviewGraph {
		t.Fatal("missing graph.db must not detect CRG")
	}
	mkdir(t, root, "ai-workspace", "code-review-graph", "svc")
	if err := os.WriteFile(filepath.Join(root, "ai-workspace", "code-review-graph", "svc", "graph.db"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !Detect(root, cfg, reg).CodeReviewGraph {
		t.Fatal("binary + graph.db must detect CRG")
	}
	cfg.Context.CRG.Mode = "off"
	if Detect(root, cfg, reg).CodeReviewGraph {
		t.Fatal("mode off must disable CRG")
	}
	stubEnv(t, t.TempDir(), nil)
	cfg.Context.CRG.Mode = "on"
	if !Detect(t.TempDir(), cfg, workspace.Registry{}).CodeReviewGraph {
		t.Fatal("mode on must force CRG")
	}
}

func TestExecutionProvider(t *testing.T) {
	cfg := config.Default()
	on := Status{Superpowers: true}
	if got := ExecutionProvider(model.LaneFull, cfg, on); got != "superpowers" {
		t.Fatalf("full+detected = %q", got)
	}
	if got := ExecutionProvider(model.LaneSmall, cfg, on); got != "native" {
		t.Fatalf("small+detected = %q", got)
	}
	cfg.Execution.PreferSuperpowersForFull = false
	if got := ExecutionProvider(model.LaneFull, cfg, on); got != "native" {
		t.Fatalf("full+not preferred = %q", got)
	}
}

func TestModelTier(t *testing.T) {
	cfg := config.Default()
	cases := []struct {
		lane model.Lane
		risk model.Risk
		want string
	}{
		{model.LaneAnswer, model.RiskHigh, "fast"},
		{model.LaneSmall, model.RiskLow, "fast"},
		{model.LaneFull, model.RiskHigh, "capable"},
		{model.LaneFull, model.RiskMedium, "standard"},
	}
	for _, c := range cases {
		if got := ModelTier(model.RouteDecision{Lane: c.lane, Risk: c.risk}, cfg); got != c.want {
			t.Fatalf("%s/%s = %q want %q", c.lane, c.risk, got, c.want)
		}
	}
}
