package indexer

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func commitHistory(t *testing.T, dir string, files ...string) {
	t.Helper()
	for _, f := range files {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		old, _ := os.ReadFile(p)
		if err := os.WriteFile(p, append(old, "x\n"...), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "c"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
}

func TestFileHistoryWeightsRecentAndFrequentChanges(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	commitHistory(t, root, "old.go")
	commitHistory(t, root, "hot.go")
	commitHistory(t, root, "hot.go", "unindexed.md")
	sweep := []string{}
	for i := range historyMaxFiles + 1 {
		sweep = append(sweep, fmt.Sprintf("gen/f%d.go", i))
	}
	commitHistory(t, root, sweep...)
	commitHistory(t, root, "new.go")

	keep := map[string]FileState{"old.go": {}, "hot.go": {}, "new.go": {}, "gen/f0.go": {}}
	h := fileHistory(context.Background(), root, keep)
	if !(h["hot.go"] > h["new.go"] && h["new.go"] > h["old.go"] && h["old.go"] > 0) {
		t.Fatalf("want hot.go > new.go > old.go > 0, got %v", h)
	}
	if _, ok := h["unindexed.md"]; ok {
		t.Fatalf("files outside keep must be dropped: %v", h)
	}
	if _, ok := h["gen/f0.go"]; ok {
		t.Fatalf("sweep commits must not count: %v", h)
	}
	if got := fileHistory(context.Background(), t.TempDir(), keep); got != nil {
		t.Fatalf("non-Git directory: want nil, got %v", got)
	}
}

func TestSearchHistoryPriorBreaksTextTies(t *testing.T) {
	idx := Index{Symbols: []Symbol{
		{Name: "parseConfig", Kind: "function", Path: "a/config.go", Line: 1},
		{Name: "parseConfig", Kind: "function", Path: "b/config.go", Line: 1},
	}}
	if hits := Search("parse config", map[string]Index{"r": idx}, 2); hits[0].Path != "a/config.go" {
		t.Fatalf("without history the tie keeps index order, got %v", hits)
	}
	idx.History = map[string]float64{"b/config.go": 2}
	hits := Search("parse config", map[string]Index{"r": idx}, 2)
	if hits[0].Path != "b/config.go" || hits[0].Score <= hits[1].Score {
		t.Fatalf("changed file should rank first, got %v", hits)
	}
}
