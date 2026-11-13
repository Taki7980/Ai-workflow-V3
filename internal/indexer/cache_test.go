package indexer

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Taki7980/ai-workflow-v3/internal/storage"
)

func TestReadIndexFileReloadsOnlyAfterRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	if err := storage.WriteJSON(path, Index{Repository: "a"}); err != nil {
		t.Fatal(err)
	}
	first, err := readIndexFile(path)
	if err != nil || first.Repository != "a" {
		t.Fatalf("first read: %v %+v", err, first)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Same size and mtime, as on a coarse clock: only file identity differs.
	if err := storage.WriteJSON(path, Index{Repository: "b"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, st.ModTime(), st.ModTime()); err != nil {
		t.Fatal(err)
	}
	second, err := readIndexFile(path)
	if err != nil || second.Repository != "b" {
		t.Fatalf("rewritten file must be re-read: %v %+v", err, second)
	}
	fileCache.Lock()
	fileCache.m[path] = cachedFile{info: fileCache.m[path].info, idx: Index{Repository: "cached"}}
	fileCache.Unlock()
	if third, _ := readIndexFile(path); third.Repository != "cached" {
		t.Fatalf("unchanged file must come from cache, got %+v", third)
	}
}

func TestSearchCacheReusesAndRebuilds(t *testing.T) {
	idx := Index{Symbols: []Symbol{{Name: "parseConfig", Kind: "function", Path: "config.go", Line: 1}}}
	Search("parse config", map[string]Index{"r": idx}, 5)
	built := lastSearch.idx
	Search("config", map[string]Index{"r": idx}, 5)
	if lastSearch.idx != built {
		t.Fatal("same symbol slice must reuse the built index")
	}
	changed := Index{Symbols: slices.Clone(idx.Symbols)}
	changed.Symbols[0].Name = "renderPage"
	hits := Search("render page", map[string]Index{"r": changed}, 5)
	if lastSearch.idx == built || len(hits) != 1 || hits[0].Symbol != "renderPage" {
		t.Fatalf("new symbol slice must rebuild, got %v", hits)
	}
}
