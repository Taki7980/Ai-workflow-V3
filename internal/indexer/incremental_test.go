package indexer

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/Taki7980/ai-workflow-v3/internal/storage"
	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

func incrementalRepo() workspace.Repository {
	return workspace.Repository{RepositoryID: workspace.RepositoryID(".", ""), RelativePath: ".", Included: true}
}

func writeSource(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hasSymbol(idx Index, name, path string) bool {
	for _, s := range idx.Symbols {
		if s.Name == name && s.Path == path {
			return true
		}
	}
	return false
}

func TestIncrementalNoChangeReparsesZeroFiles(t *testing.T) {
	root := t.TempDir()
	repo := incrementalRepo()
	writeSource(t, root, "a.go", "package fixture\nfunc Alpha() {}\n")
	writeSource(t, root, "b.py", "def beta():\n    return 1\n")
	if _, _, err := BuildWithMode(context.Background(), root, repo, BuildFull); err != nil {
		t.Fatal(err)
	}
	_, stats, err := BuildWithMode(context.Background(), root, repo, BuildIncremental)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Hashed != 2 || stats.Reparsed != 0 || stats.Reused != 2 || stats.Added != 0 || stats.Changed != 0 || stats.Removed != 0 {
		t.Fatalf("unexpected stats: %#v", stats)
	}
}

func TestIncrementalOneChangedFileReparsesExactlyOne(t *testing.T) {
	root := t.TempDir()
	repo := incrementalRepo()
	writeSource(t, root, "a.go", "package fixture\nfunc Alpha() {}\n")
	writeSource(t, root, "b.go", "package fixture\nfunc Beta() {}\n")
	if _, _, err := BuildWithMode(context.Background(), root, repo, BuildFull); err != nil {
		t.Fatal(err)
	}
	writeSource(t, root, "b.go", "package fixture\nfunc Gamma() {}\n")
	idx, stats, err := BuildWithMode(context.Background(), root, repo, BuildIncremental)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Reparsed != 1 || stats.Changed != 1 || stats.Reused != 1 {
		t.Fatalf("unexpected stats: %#v", stats)
	}
	if !hasSymbol(idx, "Gamma", "b.go") || hasSymbol(idx, "Beta", "b.go") {
		t.Fatalf("unexpected symbols: %#v", idx.Symbols)
	}
}

func TestIncrementalSameSizeSameMTimeContentChangeIsDetected(t *testing.T) {
	root := t.TempDir()
	repo := incrementalRepo()
	path := filepath.Join(root, "a.go")
	writeSource(t, root, "a.go", "package fixture\nfunc Alpha() {}\n")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	mtime := info.ModTime()
	if _, _, err := BuildWithMode(context.Background(), root, repo, BuildFull); err != nil {
		t.Fatal(err)
	}
	writeSource(t, root, "a.go", "package fixture\nfunc Bravo() {}\n")
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	idx, stats, err := BuildWithMode(context.Background(), root, repo, BuildIncremental)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Reparsed != 1 || stats.Changed != 1 {
		t.Fatalf("unexpected stats: %#v", stats)
	}
	if !hasSymbol(idx, "Bravo", "a.go") || hasSymbol(idx, "Alpha", "a.go") {
		t.Fatalf("unexpected symbols: %#v", idx.Symbols)
	}
}

func TestIncrementalMetadataOnlyChangeReusesSymbols(t *testing.T) {
	root := t.TempDir()
	repo := incrementalRepo()
	path := filepath.Join(root, "a.go")
	writeSource(t, root, "a.go", "package fixture\nfunc Alpha() {}\n")
	if _, _, err := BuildWithMode(context.Background(), root, repo, BuildFull); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
	_, stats, err := BuildWithMode(context.Background(), root, repo, BuildIncremental)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Reparsed != 0 || stats.Reused != 1 || stats.Changed != 0 {
		t.Fatalf("unexpected stats: %#v", stats)
	}
}

func TestIncrementalAddsNewFile(t *testing.T) {
	root := t.TempDir()
	repo := incrementalRepo()
	writeSource(t, root, "a.go", "package fixture\nfunc Alpha() {}\n")
	if _, _, err := BuildWithMode(context.Background(), root, repo, BuildFull); err != nil {
		t.Fatal(err)
	}
	writeSource(t, root, "b.go", "package fixture\nfunc Beta() {}\n")
	idx, stats, err := BuildWithMode(context.Background(), root, repo, BuildIncremental)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Added != 1 || stats.Reparsed != 1 || stats.Reused != 1 {
		t.Fatalf("unexpected stats: %#v", stats)
	}
	if !hasSymbol(idx, "Beta", "b.go") {
		t.Fatalf("missing new symbol: %#v", idx.Symbols)
	}
}

func TestIncrementalRemovesDeletedFileAndSymbols(t *testing.T) {
	root := t.TempDir()
	repo := incrementalRepo()
	writeSource(t, root, "a.go", "package fixture\nfunc Alpha() {}\n")
	writeSource(t, root, "b.go", "package fixture\nfunc Beta() {}\n")
	if _, _, err := BuildWithMode(context.Background(), root, repo, BuildFull); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "b.go")); err != nil {
		t.Fatal(err)
	}
	idx, stats, err := BuildWithMode(context.Background(), root, repo, BuildIncremental)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Removed != 1 || stats.Reparsed != 0 || stats.Reused != 1 {
		t.Fatalf("unexpected stats: %#v", stats)
	}
	if _, ok := idx.Files["b.go"]; ok || hasSymbol(idx, "Beta", "b.go") {
		t.Fatalf("deleted file still indexed: %#v", idx)
	}
}

func TestIncrementalRenameIsRemovePlusAdd(t *testing.T) {
	root := t.TempDir()
	repo := incrementalRepo()
	writeSource(t, root, "old.go", "package fixture\nfunc Alpha() {}\n")
	if _, _, err := BuildWithMode(context.Background(), root, repo, BuildFull); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "old.go"), filepath.Join(root, "new.go")); err != nil {
		t.Fatal(err)
	}
	idx, stats, err := BuildWithMode(context.Background(), root, repo, BuildIncremental)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Removed != 1 || stats.Added != 1 || stats.Reparsed != 1 {
		t.Fatalf("unexpected stats: %#v", stats)
	}
	if hasSymbol(idx, "Alpha", "old.go") || !hasSymbol(idx, "Alpha", "new.go") {
		t.Fatalf("rename not reflected: %#v", idx.Symbols)
	}
}

func TestIncrementalZeroSymbolFileIsReusable(t *testing.T) {
	root := t.TempDir()
	repo := incrementalRepo()
	writeSource(t, root, "empty.go", "package fixture\n")
	if _, _, err := BuildWithMode(context.Background(), root, repo, BuildFull); err != nil {
		t.Fatal(err)
	}
	_, stats, err := BuildWithMode(context.Background(), root, repo, BuildIncremental)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Reparsed != 0 || stats.Reused != 1 {
		t.Fatalf("unexpected stats: %#v", stats)
	}
}

func TestIncrementalCorruptSymbolSHAForcesReparse(t *testing.T) {
	root := t.TempDir()
	repo := incrementalRepo()
	writeSource(t, root, "a.go", "package fixture\nfunc Alpha() {}\n")
	if _, _, err := BuildWithMode(context.Background(), root, repo, BuildFull); err != nil {
		t.Fatal(err)
	}
	old, err := loadRaw(root, repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(old.Symbols) == 0 {
		t.Fatal("expected symbol fixture")
	}
	old.Symbols[0].SHA256 = "corrupt"
	if err := storage.WriteJSON(Path(root, repo), old); err != nil {
		t.Fatal(err)
	}
	_, stats, err := BuildWithMode(context.Background(), root, repo, BuildIncremental)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Reparsed != 1 || stats.Reused != 0 || stats.Changed != 0 {
		t.Fatalf("unexpected stats: %#v", stats)
	}
}

func TestFullAndIncrementalIndexesAreEquivalent(t *testing.T) {
	root := t.TempDir()
	repo := incrementalRepo()
	writeSource(t, root, "a.go", "package fixture\nfunc Alpha() {}\n")
	writeSource(t, root, "b.py", "def beta():\n    return 1\n")
	full, _, err := BuildWithMode(context.Background(), root, repo, BuildFull)
	if err != nil {
		t.Fatal(err)
	}
	inc, _, err := BuildWithMode(context.Background(), root, repo, BuildIncremental)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(full.Files, inc.Files) || !reflect.DeepEqual(full.Symbols, inc.Symbols) {
		t.Fatalf("full/incremental differ:\nfull=%#v\ninc=%#v", full, inc)
	}
}

func TestAutoV1IndexFallsBackToFull(t *testing.T) {
	root := t.TempDir()
	repo := incrementalRepo()
	writeSource(t, root, "a.go", "package fixture\nfunc Alpha() {}\n")
	if err := storage.WriteJSON(Path(root, repo), map[string]any{"version": 1, "repository": ".", "files": map[string]any{}, "symbols": []any{}}); err != nil {
		t.Fatal(err)
	}
	idx, stats, err := BuildWithMode(context.Background(), root, repo, BuildAuto)
	if err != nil {
		t.Fatal(err)
	}
	if stats.EffectiveMode != string(BuildFull) || stats.FullReason == "" || idx.Version != IndexVersion {
		t.Fatalf("unexpected fallback: idx=%#v stats=%#v", idx, stats)
	}
}

func TestIncrementalMalformedIndexFallsBackToFull(t *testing.T) {
	root := t.TempDir()
	repo := incrementalRepo()
	writeSource(t, root, "a.go", "package fixture\nfunc Alpha() {}\n")
	if err := os.MkdirAll(filepath.Dir(Path(root, repo)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(root, repo), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stats, err := BuildWithMode(context.Background(), root, repo, BuildIncremental)
	if err != nil {
		t.Fatal(err)
	}
	if stats.EffectiveMode != string(BuildFull) || stats.FullReason == "" {
		t.Fatalf("unexpected fallback stats: %#v", stats)
	}
}

func TestIncrementalWrongRepositoryIDFallsBackToFull(t *testing.T) {
	root := t.TempDir()
	repo := incrementalRepo()
	writeSource(t, root, "a.go", "package fixture\nfunc Alpha() {}\n")
	old := Index{Version: IndexVersion, Repository: repo.RelativePath, RepositoryID: "other", ExtractorRevision: currentExtractorRevision(), Files: map[string]FileState{}}
	if err := storage.WriteJSON(Path(root, repo), old); err != nil {
		t.Fatal(err)
	}
	_, stats, err := BuildWithMode(context.Background(), root, repo, BuildIncremental)
	if err != nil {
		t.Fatal(err)
	}
	if stats.EffectiveMode != string(BuildFull) || stats.FullReason == "" {
		t.Fatalf("unexpected fallback stats: %#v", stats)
	}
}

func TestIncrementalChangedExtractorRevisionFallsBackToFull(t *testing.T) {
	root := t.TempDir()
	repo := incrementalRepo()
	writeSource(t, root, "a.go", "package fixture\nfunc Alpha() {}\n")
	old := Index{Version: IndexVersion, Repository: repo.RelativePath, RepositoryID: repo.RepositoryID, ExtractorRevision: "stale", Files: map[string]FileState{}}
	if err := storage.WriteJSON(Path(root, repo), old); err != nil {
		t.Fatal(err)
	}
	_, stats, err := BuildWithMode(context.Background(), root, repo, BuildIncremental)
	if err != nil {
		t.Fatal(err)
	}
	if stats.EffectiveMode != string(BuildFull) || stats.FullReason == "" {
		t.Fatalf("unexpected fallback stats: %#v", stats)
	}
}

func TestCancelledIncrementalBuildPreservesPreviousIndex(t *testing.T) {
	root := t.TempDir()
	repo := incrementalRepo()
	writeSource(t, root, "a.go", "package fixture\nfunc Alpha() {}\n")
	if _, _, err := BuildWithMode(context.Background(), root, repo, BuildFull); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(Path(root, repo))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := BuildWithMode(ctx, root, repo, BuildIncremental); err == nil {
		t.Fatal("expected cancellation error")
	}
	after, err := os.ReadFile(Path(root, repo))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("cancelled build changed persisted index")
	}
}

func TestIncrementalReadFailurePreservesPreviousIndex(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission-bit read failure is not portable to Windows")
	}
	root := t.TempDir()
	repo := incrementalRepo()
	writeSource(t, root, "a.go", "package fixture\nfunc Alpha() {}\n")
	if _, _, err := BuildWithMode(context.Background(), root, repo, BuildFull); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(Path(root, repo))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "a.go")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0o644)
	if _, _, err := BuildWithMode(context.Background(), root, repo, BuildIncremental); err == nil {
		t.Skip("runner privileges still permit reading mode-000 file")
	}
	after, err := os.ReadFile(Path(root, repo))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed build changed persisted index")
	}
}
