package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func setupWorkspace(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	src := "package pay\n\nfunc RetryPayment() error { return nil }\n"
	if err := os.WriteFile(filepath.Join(root, "pay.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if code, _, stderr := run(t, root, nil, "setup"); code != 0 {
		t.Fatalf("setup: %d %s", code, stderr)
	}
	return root
}

func run(t *testing.T, root string, stdin *string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	in := strings.NewReader("")
	if stdin != nil {
		in = strings.NewReader(*stdin)
	}
	code := RunWithStdin(append([]string{"--root", root}, args...), in, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestCLIBriefPrompt(t *testing.T) {
	root := setupWorkspace(t)
	code, out, stderr := run(t, root, nil, "brief", "fix RetryPayment", "--format", "prompt")
	if code != 0 || !strings.HasPrefix(out, "[TASK] fix RetryPayment\n") || !strings.Contains(out, "[EVIDENCE_STATE] ") {
		t.Fatalf("code=%d out=%s stderr=%s", code, out, stderr)
	}
}

func TestCLIBriefJSONAndChangedFiles(t *testing.T) {
	root := setupWorkspace(t)
	code, out, stderr := run(t, root, nil, "brief", "--changed-file", "pay.go", "--changed-file", "x.go", "fix RetryPayment")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr)
	}
	var packet map[string]any
	if err := json.Unmarshal([]byte(out), &packet); err != nil {
		t.Fatal(err)
	}
	if got := packet["changed_files_detected"].([]any); len(got) != 2 {
		t.Fatalf("changed = %v", got)
	}
}

func TestCLIBriefBadFormat(t *testing.T) {
	root := setupWorkspace(t)
	if code, _, _ := run(t, root, nil, "brief", "x", "--format", "xml"); code != 2 {
		t.Fatalf("code=%d", code)
	}
	if code, _, _ := run(t, root, nil, "brief"); code != 2 {
		t.Fatalf("missing task code=%d", code)
	}
}

func TestCLIHandoffInvalidExit1(t *testing.T) {
	code, out, _ := run(t, t.TempDir(), nil, "handoff")
	if code != 1 || !strings.Contains(out, `"valid": false`) {
		t.Fatalf("code=%d out=%s", code, out)
	}
}

func TestCLIVerifyStrict(t *testing.T) {
	root := t.TempDir()
	code, out, _ := run(t, root, nil, "verify", "--check", "go version", "--strict")
	if code != 1 || !strings.Contains(out, `"ok": false`) || !strings.Contains(out, `"returncode": 0`) {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if code, _, _ := run(t, root, nil, "verify", "--check", "go version"); code != 0 {
		t.Fatalf("non-strict verify must exit 0, got %d", code)
	}
}

func TestCLICompressStdin(t *testing.T) {
	in := "a\nb\nc\nd\n"
	code, out, _ := run(t, t.TempDir(), &in, "compress", "--max-lines", "2")
	if code != 0 || out != "a\n... [2 LINES OMITTED] ...\nd\n" {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestCLIMemoryRoundTrip(t *testing.T) {
	root := t.TempDir()
	code, out, stderr := run(t, root, nil, "memory", "add", "--type", "decision", "--keywords", "retry backoff", "--summary", "use jitter")
	if code != 0 || !strings.Contains(out, `"type": "decision"`) {
		t.Fatalf("add code=%d out=%s err=%s", code, out, stderr)
	}
	if code, out, _ := run(t, root, nil, "memory", "search", "retry"); code != 0 || !strings.Contains(out, "use jitter") {
		t.Fatalf("search code=%d out=%s", code, out)
	}
	if code, out, _ := run(t, root, nil, "memory", "list"); code != 0 || !strings.Contains(out, `"stale": false`) {
		t.Fatalf("list code=%d out=%s", code, out)
	}
	export := filepath.Join(t.TempDir(), "m.jsonl")
	if code, _, _ := run(t, root, nil, "memory", "export", export); code != 0 {
		t.Fatalf("export code=%d", code)
	}
	if code, out, _ := run(t, t.TempDir(), nil, "memory", "import", export); code != 0 || !strings.Contains(out, `"imported": 1`) {
		t.Fatalf("import code=%d out=%s", code, out)
	}
	if code, _, _ := run(t, root, nil, "memory", "add", "--type", "guess", "--keywords", "k", "--summary", "s"); code != 1 {
		t.Fatalf("bad type code=%d", code)
	}
	if code, out, _ := run(t, root, nil, "memory", "prune"); code != 0 || !strings.Contains(out, `"kept": 1`) {
		t.Fatalf("prune code=%d out=%s", code, out)
	}
}
