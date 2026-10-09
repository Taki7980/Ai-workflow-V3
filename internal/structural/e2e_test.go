package structural_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Taki7980/ai-workflow-v3/internal/cli"
)

// TestE2EGraphSyncBrief runs the real code-review-graph: sync, then a
// structural brief must reach structural_complete without leaking paths.
func TestE2EGraphSyncBrief(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	if _, err := exec.LookPath("code-review-graph"); err != nil {
		t.Skip("code-review-graph not installed")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	src := "package pkg\n\n// Foo is called by Bar.\nfunc Foo() int { return 1 }\n\nfunc Bar() int { return Foo() + 1 }\n"
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pkg", "a.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run := func(args ...string) (int, string) {
		var out, errOut bytes.Buffer
		code := cli.RunWithStdin(append([]string{"--root", root}, args...), strings.NewReader(""), &out, &errOut)
		return code, out.String() + errOut.String()
	}
	if code, out := run("setup"); code != 0 {
		t.Fatalf("setup %d %s", code, out)
	}
	if code, out := run("graph", "sync"); code != 0 {
		t.Fatalf("graph sync %d %s", code, out)
	}
	code, out := run("brief", "who calls Foo", "--format", "json")
	if code != 0 {
		t.Fatalf("brief %d %s", code, out)
	}
	var p struct {
		Retrieval struct {
			Sufficiency struct {
				StructuralComplete bool `json:"structural_complete"`
			} `json:"sufficiency"`
			Fallbacks []string `json:"fallbacks"`
		} `json:"retrieval"`
		Context []struct {
			Source string `json:"source"`
			Text   string `json:"text"`
		} `json:"context"`
	}
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	found := false
	for _, it := range p.Context {
		if it.Source == "code_review_graph" && strings.Contains(it.Text, "Bar") {
			found = true
			if strings.Contains(strings.ToLower(it.Text), strings.ToLower(filepath.ToSlash(root))) {
				t.Fatalf("absolute path leaked: %s", it.Text)
			}
		}
	}
	if !p.Retrieval.Sufficiency.StructuralComplete || !found {
		t.Fatalf("structural_complete=%v found=%v fallbacks=%v context=%+v", p.Retrieval.Sufficiency.StructuralComplete, found, p.Retrieval.Fallbacks, p.Context)
	}
}
