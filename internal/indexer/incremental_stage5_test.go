package indexer

import (
	"context"
	"testing"
)

func TestIncrementalCounterContract(t *testing.T) {
	root := t.TempDir()
	repo := incrementalRepo()
	writeSource(t, root, "a.go", "package fixture\nfunc Alpha() {}\n")
	writeSource(t, root, "b.go", "package fixture\nfunc Beta() {}\n")
	writeSource(t, root, "c.py", "def gamma():\n    return 1\n")

	_, full, err := BuildWithMode(context.Background(), root, repo, BuildFull)
	if err != nil {
		t.Fatal(err)
	}
	if full.Files != 3 || full.Hashed != 3 || full.Reparsed != 3 {
		t.Fatalf("unexpected full stats: %#v", full)
	}

	_, unchanged, err := BuildWithMode(context.Background(), root, repo, BuildIncremental)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Hashed != 3 || unchanged.Reparsed != 0 || unchanged.Reused != 3 || unchanged.Added != 0 || unchanged.Changed != 0 || unchanged.Removed != 0 {
		t.Fatalf("unexpected no-change stats: %#v", unchanged)
	}

	writeSource(t, root, "b.go", "package fixture\nfunc Bravo() {}\n")
	_, changed, err := BuildWithMode(context.Background(), root, repo, BuildIncremental)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Hashed != 3 || changed.Reparsed != 1 || changed.Reused != 2 || changed.Changed != 1 || changed.Added != 0 || changed.Removed != 0 {
		t.Fatalf("unexpected one-change stats: %#v", changed)
	}
}
