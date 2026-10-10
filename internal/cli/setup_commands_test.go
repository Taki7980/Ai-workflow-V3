package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
}

func TestSetupIsIdempotentAndInstallsLocalExcludes(t *testing.T) {
	root := setupWorkspace(t)
	code, out, stderr := run(t, root, nil, "setup", "--json", "--no-crg-sync")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	var r struct {
		Created   []string `json:"created"`
		Preserved []string `json:"preserved"`
		Registry  struct {
			Status   string `json:"status"`
			Accepted int    `json:"accepted"`
		} `json:"repository_registry"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Created) != 0 || r.Registry.Status != "refreshed" || r.Registry.Accepted != 1 {
		t.Fatalf("second setup must only preserve: %s", out)
	}
	ex, err := os.ReadFile(filepath.Join(root, ".git", "info", "exclude"))
	if err != nil || strings.Count(string(ex), "# >>> ai-workflow local state >>>") != 1 {
		t.Fatalf("exclude block missing or duplicated: %v\n%s", err, ex)
	}
	if _, err := os.Stat(filepath.Join(root, "ai-workspace", "agents", "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
}

func TestSetupMissingRootRequiresCreate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "new")
	if code, _, _ := run(t, root, nil, "setup", "--no-index", "--no-crg-sync"); code != 1 {
		t.Fatalf("missing root without --create must fail, got %d", code)
	}
	if code, _, stderr := run(t, root, nil, "setup", "--create", "--no-index", "--no-crg-sync"); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
}

func TestBootstrapRefusesExistingWorkspace(t *testing.T) {
	root := setupWorkspace(t)
	code, _, stderr := run(t, root, nil, "bootstrap", "--project-name", "X")
	if code != 1 || !strings.Contains(stderr, "refuses to overwrite") {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
}

func TestInitStampsProjectName(t *testing.T) {
	root := t.TempDir()
	if code, _, stderr := run(t, root, nil, "setup", "--no-index", "--no-crg-sync", "--no-discover-repos"); code != 0 {
		t.Fatal(stderr)
	}
	p := filepath.Join(root, "ai-workspace", "agents", "AGENTS.md")
	if err := os.WriteFile(p, []byte("Project: {{PROJECT_NAME}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := run(t, root, nil, "init", "--project-name", "Acme"); code != 0 {
		t.Fatal(stderr)
	}
	if b, _ := os.ReadFile(p); string(b) != "Project: Acme\n" {
		t.Fatalf("got %q", b)
	}
}

func TestReposExcludeSurvivesRefresh(t *testing.T) {
	root := setupWorkspace(t)
	gitInit(t, filepath.Join(root, "svc"))
	// New repos found by refresh wait for review; existing decisions are kept.
	code, out, stderr := run(t, root, nil, "repos", "refresh")
	if code != 0 || !strings.Contains(out, `"accepted": 1`) || !strings.Contains(out, `"discovered": 2`) {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
	}
	if code, out, _ := run(t, root, nil, "repos", "include", "svc"); code != 0 || !strings.Contains(out, `"action": "included"`) {
		t.Fatalf("include: %d %s", code, out)
	}
	if code, _, _ := run(t, root, nil, "repos", "exclude", "."); code != 0 {
		t.Fatal("exclude root failed")
	}
	_, out, _ = run(t, root, nil, "repos", "refresh")
	var s struct {
		Repositories []struct {
			RelativePath string `json:"relative_path"`
			Included     bool   `json:"included"`
			Reason       string `json:"reason"`
		} `json:"repositories"`
	}
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range s.Repositories {
		got[r.RelativePath] = r.Reason
		if (r.RelativePath == "svc") != r.Included {
			t.Fatalf("decision lost: %+v", s.Repositories)
		}
	}
	if got["."] != "manual-exclude" || got["svc"] != "manual-include" {
		t.Fatalf("reasons=%v", got)
	}
	if code, _, stderr := run(t, root, nil, "repos", "include", "nope"); code != 1 || !strings.Contains(stderr, "repository not found") {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
}

func TestContextCommandV2Shape(t *testing.T) {
	root := setupWorkspace(t)
	code, out, stderr := run(t, root, nil, "context", "fix RetryPayment retry handling", "--symbol", "RetryPayment")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"lane", "risk", "confidence", "budget", "retrieval", "items", "estimated_tokens"} {
		if _, ok := p[k]; !ok {
			t.Fatalf("missing %q in %s", k, out)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "ai-workspace", "generated", "last-brief.json")); err == nil {
		t.Fatal("context must not persist last-brief.json")
	}
}

func TestIndexV2Flags(t *testing.T) {
	root := setupWorkspace(t)
	if code, _, stderr := run(t, root, nil, "index", "--incremental", "--strict-hash"); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	if code, out, _ := run(t, root, nil, "handoff", "validate"); code == 2 {
		t.Fatalf("handoff validate must be accepted: %s", out)
	}
}
