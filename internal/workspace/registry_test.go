package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDiscoverNestedRepos(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	nested := filepath.Join(root, "frontend")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{root, nested} {
		cmd := exec.Command("git", "init", "-q", p)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, b)
		}
	}
	repos, err := Discover(context.Background(), root, 8, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 {
		t.Fatalf("want 2 repos, got %#v", repos)
	}
	if repos[0].RelativePath != "." || repos[1].RelativePath != "frontend" {
		t.Fatalf("unexpected repos: %#v", repos)
	}
}

func TestRepositoryIDMatchesV2NullRemoteEncoding(t *testing.T) {
	got := RepositoryID(".", "")
	const want = "be4df980feef21cb8e2f9f7cfdf9e2fc298c9f92d48fba92de1f8cc7bb18725d"
	if got != want {
		t.Fatalf("repository id mismatch: got %s want %s", got, want)
	}
}

func TestRemoteIdentityMatchesV2(t *testing.T) {
	cases := map[string]string{
		"git@github.com:Taki7980/ai-workflow-control-plane-v2.git":     "github.com/Taki7980/ai-workflow-control-plane-v2",
		"https://github.com/Taki7980/ai-workflow-control-plane-v2.git": "github.com/Taki7980/ai-workflow-control-plane-v2",
	}
	for input, want := range cases {
		if got := RemoteIdentity(input); got != want {
			t.Fatalf("%q => %q want %q", input, got, want)
		}
	}
}
