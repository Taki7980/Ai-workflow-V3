package indexer

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestSourcePathWalkerPropagatesDescendantTraversalError(t *testing.T) {
	root := t.TempDir()
	var paths []string
	walk := sourcePathWalker(root, &paths)
	want := errors.New("descendant traversal failed")

	err := walk(filepath.Join(root, "blocked"), nil, want)
	if !errors.Is(err, want) {
		t.Fatalf("walk error=%v want=%v", err, want)
	}
	if len(paths) != 0 {
		t.Fatalf("paths=%v want none after traversal error", paths)
	}
}
