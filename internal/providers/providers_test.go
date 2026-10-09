package providers

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Taki7980/ai-workflow-v3/internal/config"
	"github.com/Taki7980/ai-workflow-v3/internal/model"
	"github.com/Taki7980/ai-workflow-v3/internal/structural"
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

func gitRepo(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
}

func TestDetectCRGRequiresFreshGraph(t *testing.T) {
	root := t.TempDir()
	mkdir(t, root, "svc")
	gitRepo(t, filepath.Join(root, "svc"))
	stubEnv(t, t.TempDir(), map[string]bool{"code-review-graph": true})
	reg := workspace.Registry{Repositories: []workspace.Repository{{RelativePath: "svc", Included: true}}}
	cfg := config.Default()
	if Detect(root, cfg, reg).CodeReviewGraph {
		t.Fatal("missing graph.db must not detect CRG")
	}
	dir, err := structural.GraphDir(root, "svc")
	if err != nil {
		t.Fatal(err)
	}
	mkdir(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "graph.db"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if Detect(root, cfg, reg).CodeReviewGraph {
		t.Fatal("graph.db without a fresh manifest must not detect CRG")
	}
	if _, err := structural.WriteGraphManifest(context.Background(), root, "svc", "2.3.8", "build"); err != nil {
		t.Fatal(err)
	}
	if !Detect(root, cfg, reg).CodeReviewGraph {
		t.Fatal("binary + fresh graph must detect CRG")
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

func TestDetectSCIP(t *testing.T) {
	root := t.TempDir()
	gitRepo(t, root)
	reg := workspace.Registry{Repositories: []workspace.Repository{{RelativePath: ".", Included: true}}}
	cfg := config.Default()
	if cfg.Context.SCIP.Mode != "auto" || Detect(root, cfg, reg).SCIP {
		t.Fatal("no index must not detect SCIP")
	}
	dir, _ := structural.ScipDir(root, ".")
	mkdir(t, dir)
	for _, f := range []string{"index.scip", "index.json"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := structural.WriteScipManifest(context.Background(), root, ".", "go", "scip-go"); err != nil {
		t.Fatal(err)
	}
	if !Detect(root, cfg, reg).SCIP {
		t.Fatal("fresh SCIP index must detect")
	}
	cfg.Context.SCIP.Mode = "off"
	if Detect(root, cfg, reg).SCIP {
		t.Fatal("mode off must disable SCIP")
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
