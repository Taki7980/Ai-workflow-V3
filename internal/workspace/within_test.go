package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWithinRejectsSymlinkEscapeForMissingFile(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	for _, p := range []string{"link/new.txt", "link/a/b/new.txt", "link", "../x", filepath.Join(outside, "y")} {
		if _, _, err := Within(root, p); err == nil {
			t.Errorf("%s escaped the root", p)
		}
	}
	if rel, _, err := Within(root, "sub/new.txt"); err != nil || rel != "sub/new.txt" {
		t.Fatalf("rel=%q err=%v", rel, err)
	}
}
