package indexer

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

func TestParentRepoDoesNotIndexNestedRepoFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("git init root: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc RootOnly() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", nested).CombinedOutput(); err != nil {
		t.Fatalf("git init nested: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(nested, "nested.go"), []byte("package nested\nfunc NestedOnly() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, err := Build(context.Background(), root, workspace.Repository{RelativePath: ".", Included: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := idx.Files["nested/nested.go"]; ok {
		t.Fatal("nested repository file was duplicated into parent index")
	}
	if _, ok := idx.Files["main.go"]; !ok {
		t.Fatal("root source file missing")
	}
}
