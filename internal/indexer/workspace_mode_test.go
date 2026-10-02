package indexer

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

func TestBuildWorkspaceWithModeReportsPerRepositoryStats(t *testing.T) {
	root := t.TempDir()
	repo := workspace.Repository{RepositoryID: workspace.RepositoryID(".", ""), RelativePath: ".", Included: true}
	writeSource(t, root, "main.go", "package fixture\nfunc Alpha() {}\n")
	result, err := BuildWorkspaceWithMode(context.Background(), root, workspace.Registry{Version: workspace.RegistryVersion, ReviewRequired: true, Repositories: []workspace.Repository{repo}}, BuildFull)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := result["."]
	if !ok {
		t.Fatalf("missing root result: %#v", result)
	}
	if got.Stats.Files != 1 || got.Stats.Reparsed != 1 || got.Index.RepositoryID != repo.RepositoryID {
		t.Fatalf("unexpected result: %#v", got)
	}
}

func TestParentIncrementalIndexIgnoresNestedRepoChanges(t *testing.T) {
	root := t.TempDir()
	repo := workspace.Repository{RepositoryID: workspace.RepositoryID(".", ""), RelativePath: ".", Included: true}
	writeSource(t, root, "main.go", "package fixture\nfunc RootOnly() {}\n")
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(filepath.Join(nested, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeSource(t, root, "nested/nested.go", "package nested\nfunc NestedOne() {}\n")
	if _, _, err := BuildWithMode(context.Background(), root, repo, BuildFull); err != nil {
		t.Fatal(err)
	}
	writeSource(t, root, "nested/nested.go", "package nested\nfunc NestedTwo() {}\n")
	idx, stats, err := BuildWithMode(context.Background(), root, repo, BuildIncremental)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Files != 1 || stats.Reparsed != 0 || stats.Reused != 1 || stats.Changed != 0 {
		t.Fatalf("nested change affected parent stats: %#v", stats)
	}
	if hasSymbol(idx, "NestedOne", "nested/nested.go") || hasSymbol(idx, "NestedTwo", "nested/nested.go") {
		t.Fatalf("nested repository leaked into parent index: %#v", idx.Symbols)
	}
}

func TestRepositoryIDReplacementForcesFullRebuild(t *testing.T) {
	root := t.TempDir()
	first := workspace.Repository{RepositoryID: "first-repository", RelativePath: ".", Included: true}
	writeSource(t, root, "main.go", "package fixture\nfunc Alpha() {}\n")
	if _, _, err := BuildWithMode(context.Background(), root, first, BuildFull); err != nil {
		t.Fatal(err)
	}
	replacement := workspace.Repository{RepositoryID: "replacement-repository", RelativePath: ".", Included: true}
	idx, stats, err := BuildWithMode(context.Background(), root, replacement, BuildIncremental)
	if err != nil {
		t.Fatal(err)
	}
	if stats.EffectiveMode != string(BuildFull) || stats.FullReason != "repository-id" {
		t.Fatalf("replacement reused stale index: %#v", stats)
	}
	if idx.RepositoryID != replacement.RepositoryID {
		t.Fatalf("repository id=%q want=%q", idx.RepositoryID, replacement.RepositoryID)
	}
}
