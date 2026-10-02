package indexer

import (
	"testing"

	"github.com/Taki7980/ai-workflow-v3/internal/storage"
	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

func testRepository() workspace.Repository {
	return workspace.Repository{RepositoryID: "repo-current", RelativePath: ".", Included: true}
}

func writeIndexFixture(t *testing.T, root string, repo workspace.Repository, idx any) {
	t.Helper()
	if err := storage.WriteJSON(Path(root, repo), idx); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRejectsV1Index(t *testing.T) {
	root := t.TempDir()
	repo := testRepository()
	writeIndexFixture(t, root, repo, map[string]any{
		"version":    1,
		"repository": ".",
		"built_at":   "2026-10-02T00:00:00Z",
		"files":      map[string]any{},
		"symbols":    []any{},
	})
	if _, err := Load(root, repo); err == nil {
		t.Fatal("expected v1 index to be rejected")
	}
}

func TestLoadRejectsWrongRepositoryID(t *testing.T) {
	root := t.TempDir()
	repo := testRepository()
	writeIndexFixture(t, root, repo, map[string]any{
		"version":            IndexVersion,
		"repository":         repo.RelativePath,
		"repository_id":      "wrong-repository",
		"extractor_revision": currentExtractorRevision(),
		"built_at":           "2026-10-02T00:00:00Z",
		"files":              map[string]any{},
		"symbols":            []any{},
	})
	if _, err := Load(root, repo); err == nil {
		t.Fatal("expected repository identity mismatch to be rejected")
	}
}

func TestLoadRejectsWrongExtractorRevision(t *testing.T) {
	root := t.TempDir()
	repo := testRepository()
	writeIndexFixture(t, root, repo, map[string]any{
		"version":            IndexVersion,
		"repository":         repo.RelativePath,
		"repository_id":      repo.RepositoryID,
		"extractor_revision": "symbols-v0:stale",
		"built_at":           "2026-10-02T00:00:00Z",
		"files":              map[string]any{},
		"symbols":            []any{},
	})
	if _, err := Load(root, repo); err == nil {
		t.Fatal("expected extractor revision mismatch to be rejected")
	}
}

func TestLoadAcceptsCurrentV2Index(t *testing.T) {
	root := t.TempDir()
	repo := testRepository()
	writeIndexFixture(t, root, repo, map[string]any{
		"version":            IndexVersion,
		"repository":         repo.RelativePath,
		"repository_id":      repo.RepositoryID,
		"extractor_revision": currentExtractorRevision(),
		"built_at":           "2026-10-02T00:00:00Z",
		"files":              map[string]any{},
		"symbols":            []any{},
	})
	idx, err := Load(root, repo)
	if err != nil {
		t.Fatal(err)
	}
	if idx.Version != IndexVersion || idx.RepositoryID != repo.RepositoryID || idx.ExtractorRevision != currentExtractorRevision() {
		t.Fatalf("unexpected loaded index: %#v", idx)
	}
}
