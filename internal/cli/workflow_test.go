package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Taki7980/ai-workflow-v3/internal/doctor"
	"github.com/Taki7980/ai-workflow-v3/internal/indexer"
	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func mustRun(t *testing.T, args ...string) string {
	t.Helper()
	code, out, errOut := run(t, args...)
	if code != 0 {
		t.Fatalf("%v: code=%d stderr=%s", args, code, errOut)
	}
	return out
}

// newWorkspace creates a non-Git control root containing two Git repos.
func newWorkspace(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	for name, src := range map[string]string{
		"backend":     "package backend\nfunc ProcessPayment() {}\n",
		"admin-panel": "export function renderDashboard() {}\n",
	} {
		dir := filepath.Join(root, name)
		if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
		file := "main.go"
		if name == "admin-panel" {
			file = "app.js"
		}
		writeOld(t, filepath.Join(dir, file), src)
	}
	return root
}

func writeOld(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

func registry(t *testing.T, root string) map[string]workspace.Repository {
	t.Helper()
	reg, err := workspace.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]workspace.Repository{}
	for _, r := range reg.Repositories {
		out[r.RelativePath] = r
	}
	return out
}

func TestHelpExitsZero(t *testing.T) {
	for _, arg := range []string{"help", "--help", "-h"} {
		code, out, _ := run(t, arg)
		if code != 0 || !strings.Contains(out, "repos include") {
			t.Fatalf("%s: code=%d out=%s", arg, code, out)
		}
	}
}

func TestSetupPreservesExclusionsAndRefreshDoesNotAutoInclude(t *testing.T) {
	root := newWorkspace(t)
	mustRun(t, "--root", root, "setup", "--no-index")
	reg := registry(t, root)
	if !reg["backend"].Included || reg["backend"].Reason != workspace.ReasonAutoDiscovered {
		t.Fatalf("backend=%+v", reg["backend"])
	}

	mustRun(t, "--root", root, "repos", "exclude", "admin-panel")
	mustRun(t, "--root", root, "setup", "--no-index")
	if r := registry(t, root)["admin-panel"]; r.Included || r.Reason != workspace.ReasonManualExclude {
		t.Fatalf("setup discarded exclusion: %+v", r)
	}

	if out, err := exec.Command("git", "init", "-q", filepath.Join(root, "worker")).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	mustRun(t, "--root", root, "repos", "refresh")
	reg = registry(t, root)
	if reg["worker"].Included {
		t.Fatal("refresh auto-included a new repository")
	}
	if reg["admin-panel"].Included || !reg["backend"].Included {
		t.Fatal("refresh changed existing decisions")
	}
	mustRun(t, "--root", root, "repos", "include", "worker")
	if !registry(t, root)["worker"].Included {
		t.Fatal("include did not apply")
	}
	if code, _, _ := run(t, "--root", root, "repos", "include", "does-not-exist"); code != 1 {
		t.Fatalf("unknown selector code=%d", code)
	}
	if code, _, _ := run(t, "--root", root, "repos", "include"); code != 2 {
		t.Fatalf("missing selector code=%d", code)
	}
}

func TestIndexIsIncrementalAndContextFlagsStaleHits(t *testing.T) {
	root := newWorkspace(t)
	mustRun(t, "--root", root, "setup")

	var stats map[string]indexer.BuildStats
	if err := json.Unmarshal([]byte(mustRun(t, "--root", root, "index")), &stats); err != nil {
		t.Fatal(err)
	}
	if s := stats["backend"]; s.Reused != 1 || s.Parsed != 0 || s.Written {
		t.Fatalf("second index was not incremental: %+v", s)
	}
	if err := json.Unmarshal([]byte(mustRun(t, "--root", root, "index", "--full")), &stats); err != nil {
		t.Fatal(err)
	}
	if s := stats["backend"]; !s.Full || s.Parsed != 1 {
		t.Fatalf("--full stats=%+v", s)
	}

	_, out, errOut := run(t, "--root", root, "context", "ProcessPayment")
	if strings.Contains(out, `"stale"`) || errOut != "" {
		t.Fatalf("fresh hit flagged stale: %s %s", out, errOut)
	}

	writeOld(t, filepath.Join(root, "backend", "main.go"), "package backend\nfunc ProcessPayment(amount int) {}\n")
	code, out, errOut := run(t, "--root", root, "context", "ProcessPayment")
	if code != 0 || !strings.Contains(out, `"stale": true`) || !strings.Contains(errOut, "changed since indexing") {
		t.Fatalf("stale hit not flagged: code=%d out=%s err=%s", code, out, errOut)
	}
	// Flags may follow the query.
	code, out, errOut = run(t, "--root", root, "context", "ProcessPayment", "--refresh")
	if code != 0 || strings.Contains(out, `"stale"`) || errOut != "" {
		t.Fatalf("--refresh still stale: code=%d out=%s err=%s", code, out, errOut)
	}
}

func TestContextLaneFlag(t *testing.T) {
	root := newWorkspace(t)
	mustRun(t, "--root", root, "setup")
	for _, lane := range []string{"answer", "small", "full"} {
		if code, _, errOut := run(t, "--root", root, "context", "--lane", lane, "ProcessPayment"); code != 0 {
			t.Fatalf("lane %s: %s", lane, errOut)
		}
	}
	if code, _, _ := run(t, "--root", root, "context", "--lane", "huge", "ProcessPayment"); code != 2 {
		t.Fatalf("bad lane code=%d", code)
	}
}

func TestContextWarnsOnMissingIndex(t *testing.T) {
	root := newWorkspace(t)
	mustRun(t, "--root", root, "setup", "--no-index")
	code, out, errOut := run(t, "--root", root, "context", "ProcessPayment")
	if code != 0 || strings.TrimSpace(out) != "[]" || !strings.Contains(errOut, "no usable index") {
		t.Fatalf("code=%d out=%s err=%s", code, out, errOut)
	}
}

func TestDoctorReportsIndexFreshness(t *testing.T) {
	root := newWorkspace(t)
	mustRun(t, "--root", root, "setup")
	code, out, _ := run(t, "--root", root, "doctor", "--strict")
	if code != 0 {
		t.Fatalf("fresh workspace failed strict doctor: %s", out)
	}
	writeOld(t, filepath.Join(root, "backend", "extra.go"), "package backend\n")
	code, out, _ = run(t, "--root", root, "doctor", "--strict")
	var rep doctor.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatal(err)
	}
	if code != 1 || rep.OK {
		t.Fatalf("stale index passed strict doctor: %s", out)
	}
	found := false
	for _, c := range rep.Checks {
		if c.Name == "index:backend" && !c.OK && strings.Contains(c.Detail, "1 added") {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing stale check: %s", out)
	}
	if code, _, _ := run(t, "--root", root, "doctor"); code != 0 {
		t.Fatal("non-strict doctor must tolerate stale indexes")
	}
}
