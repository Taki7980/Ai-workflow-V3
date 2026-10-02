package indexer

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Taki7980/ai-workflow-v3/internal/storage"
	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

var rootRepo = workspace.Repository{RelativePath: ".", Included: true}

// writeAged writes content to root/rel with an mtime safely outside the racy
// window, so the stat fast-path is allowed to trust it.
func writeAged(t *testing.T, root, rel, content string, age time.Duration) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

func build(t *testing.T, root string, opts BuildOptions) (Index, BuildStats) {
	t.Helper()
	idx, stats, err := BuildWithOptions(context.Background(), root, rootRepo, opts)
	if err != nil {
		t.Fatal(err)
	}
	return idx, stats
}

func symbolNames(idx Index) string {
	names := []string{}
	for _, s := range idx.Symbols {
		names = append(names, s.Path+":"+s.Name)
	}
	return strings.Join(names, ",")
}

func TestIncrementalBuildReusesUnchangedFilesWithoutWriting(t *testing.T) {
	root := t.TempDir()
	writeAged(t, root, "a.go", "package a\nfunc Alpha() {}\n", time.Hour)
	writeAged(t, root, "b.py", "def beta():\n    pass\n", time.Hour)

	first, stats := build(t, root, BuildOptions{})
	if !stats.Full || stats.Parsed != 2 || !stats.Written {
		t.Fatalf("first build stats=%+v", stats)
	}
	if first.BuiltAtNS == 0 || first.Parsers == "" {
		t.Fatalf("freshness metadata missing: %+v", first)
	}
	before, err := os.Stat(Path(root, rootRepo))
	if err != nil {
		t.Fatal(err)
	}

	second, stats := build(t, root, BuildOptions{})
	if stats.Full || stats.Reused != 2 || stats.Parsed != 0 || stats.Rehashed != 0 || stats.Written {
		t.Fatalf("no-op rebuild stats=%+v", stats)
	}
	if symbolNames(second) != symbolNames(first) {
		t.Fatalf("symbols changed on no-op rebuild: %s vs %s", symbolNames(second), symbolNames(first))
	}
	after, err := os.Stat(Path(root, rootRepo))
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("no-op rebuild rewrote the index file")
	}
}

func TestIncrementalBuildReparsesOnlyModifiedFiles(t *testing.T) {
	root := t.TempDir()
	writeAged(t, root, "a.go", "package a\nfunc Alpha() {}\n", time.Hour)
	writeAged(t, root, "b.go", "package b\nfunc Beta() {}\n", time.Hour)
	build(t, root, BuildOptions{})

	writeAged(t, root, "b.go", "package b\nfunc Gamma() {}\n", 30*time.Minute)
	idx, stats := build(t, root, BuildOptions{})
	if stats.Reused != 1 || stats.Parsed != 1 || !stats.Written {
		t.Fatalf("stats=%+v", stats)
	}
	if got := symbolNames(idx); got != "a.go:Alpha,b.go:Gamma" {
		t.Fatalf("symbols=%s", got)
	}
}

func TestIncrementalBuildDetectsAddedAndRemovedFiles(t *testing.T) {
	root := t.TempDir()
	writeAged(t, root, "a.go", "package a\nfunc Alpha() {}\n", time.Hour)
	writeAged(t, root, "gone.go", "package a\nfunc Gone() {}\n", time.Hour)
	build(t, root, BuildOptions{})

	if err := os.Remove(filepath.Join(root, "gone.go")); err != nil {
		t.Fatal(err)
	}
	writeAged(t, root, "pkg/new.go", "package pkg\nfunc Fresh() {}\n", time.Hour)
	idx, stats := build(t, root, BuildOptions{})
	if stats.Removed != 1 || stats.Parsed != 1 || stats.Reused != 1 {
		t.Fatalf("stats=%+v", stats)
	}
	if _, ok := idx.Files["gone.go"]; ok {
		t.Fatal("removed file still indexed")
	}
	if got := symbolNames(idx); got != "a.go:Alpha,pkg/new.go:Fresh" {
		t.Fatalf("symbols=%s", got)
	}
}

func TestTouchedFileKeepsCachedSymbolsWithoutReparse(t *testing.T) {
	root := t.TempDir()
	writeAged(t, root, "a.go", "package a\nfunc Alpha() {}\n", time.Hour)
	build(t, root, BuildOptions{})

	when := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(filepath.Join(root, "a.go"), when, when); err != nil {
		t.Fatal(err)
	}
	idx, stats := build(t, root, BuildOptions{})
	if stats.Rehashed != 1 || stats.Parsed != 0 || !stats.Written {
		t.Fatalf("touch-only stats=%+v", stats)
	}
	if idx.Files["a.go"].MTimeNS != when.UnixNano() {
		t.Fatal("touched file mtime not refreshed in index")
	}
	if got := symbolNames(idx); got != "a.go:Alpha" {
		t.Fatalf("symbols=%s", got)
	}
}

// TestRacilyCleanFileIsRehashed reproduces Git's "racy clean" hazard: a file
// is rewritten with the same size and the same mtime it had when indexed (as
// happens on coarse-granularity filesystems). The stat fast-path must not be
// trusted for files modified close to the index build time.
func TestRacilyCleanFileIsRehashed(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.go")
	if err := os.WriteFile(path, []byte("package a\nfunc Alpha() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, _ := build(t, root, BuildOptions{})
	recorded := first.Files["a.go"]

	if err := os.WriteFile(path, []byte("package a\nfunc Omega() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mtime := time.Unix(0, recorded.MTimeNS)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() != recorded.Size || st.ModTime().UnixNano() != recorded.MTimeNS {
		t.Fatal("test setup failed to reproduce identical stat")
	}

	idx, stats := build(t, root, BuildOptions{})
	if stats.Reused != 0 || stats.Parsed != 1 {
		t.Fatalf("racily clean file trusted by stat: stats=%+v", stats)
	}
	if got := symbolNames(idx); got != "a.go:Omega" {
		t.Fatalf("stale symbols served: %s", got)
	}
}

func TestFullBuildIgnoresPreviousIndex(t *testing.T) {
	root := t.TempDir()
	writeAged(t, root, "a.go", "package a\nfunc Alpha() {}\n", time.Hour)
	build(t, root, BuildOptions{})
	_, stats := build(t, root, BuildOptions{Full: true})
	if !stats.Full || stats.Reused != 0 || stats.Parsed != 1 || !stats.Written {
		t.Fatalf("stats=%+v", stats)
	}
}

func TestParserFingerprintChangeForcesReparse(t *testing.T) {
	root := t.TempDir()
	writeAged(t, root, "a.go", "package a\nfunc Alpha() {}\n", time.Hour)
	idx, _ := build(t, root, BuildOptions{})
	idx.Parsers = "v0:some-other-parser"
	idx.Symbols = []Symbol{{Name: "Bogus", Kind: "symbol", Path: "a.go", Line: 1}}
	if err := storage.WriteJSON(Path(root, rootRepo), idx); err != nil {
		t.Fatal(err)
	}
	rebuilt, stats := build(t, root, BuildOptions{})
	if !stats.Full || stats.Parsed != 1 {
		t.Fatalf("stats=%+v", stats)
	}
	if got := symbolNames(rebuilt); got != "a.go:Alpha" {
		t.Fatalf("symbols from foreign parser set reused: %s", got)
	}
}

func TestLegacyIndexWithoutFreshnessMetadataIsRebuilt(t *testing.T) {
	root := t.TempDir()
	writeAged(t, root, "a.go", "package a\nfunc Alpha() {}\n", time.Hour)
	legacy := Index{Version: 1, Repository: ".", Files: map[string]FileState{}, Symbols: []Symbol{}}
	if err := storage.WriteJSON(Path(root, rootRepo), legacy); err != nil {
		t.Fatal(err)
	}
	idx, stats := build(t, root, BuildOptions{})
	if !stats.Full || stats.Parsed != 1 || idx.Parsers == "" || idx.BuiltAtNS == 0 {
		t.Fatalf("stats=%+v idx=%+v", stats, idx)
	}
}

func TestOversizedFileIsTrackedButNotParsed(t *testing.T) {
	root := t.TempDir()
	big := "package big\nfunc Hidden() {}\n" + strings.Repeat("// padding\n", MaxParseBytes/10)
	writeAged(t, root, "big.go", big, time.Hour)
	idx, _ := build(t, root, BuildOptions{})
	state, ok := idx.Files["big.go"]
	if !ok || state.SHA256 == "" || state.Size != int64(len(big)) {
		t.Fatalf("oversized file not tracked: %+v", state)
	}
	if len(idx.Symbols) != 0 {
		t.Fatalf("oversized file was parsed: %s", symbolNames(idx))
	}
}

func TestProcessFilesHardErrorDoesNotLeakWorkers(t *testing.T) {
	root := t.TempDir()
	rels := []string{}
	for i := 0; i < 64; i++ {
		rel := "f" + strings.Repeat("x", i%5) + string(rune('a'+i%26)) + ".go"
		writeAged(t, root, rel, "package f\n", time.Hour)
		rels = append(rels, rel)
	}
	if err := os.Mkdir(filepath.Join(root, "dir.go"), 0o755); err != nil {
		t.Fatal(err)
	}
	rels = append([]string{"dir.go"}, rels...)

	baseline := runtime.NumGoroutine()
	_, err := processFiles(context.Background(), root, rels, func(string, FileState, []byte) ([]Symbol, bool) { return nil, false })
	if err == nil {
		t.Fatal("expected error reading a directory as a file")
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > baseline && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > baseline {
		t.Fatalf("worker goroutines leaked: baseline=%d now=%d", baseline, n)
	}
}

func TestProcessFilesSkipsFilesThatVanish(t *testing.T) {
	root := t.TempDir()
	writeAged(t, root, "a.go", "package a\n", time.Hour)
	results, err := processFiles(context.Background(), root, []string{"a.go", "missing.go"}, func(string, FileState, []byte) ([]Symbol, bool) { return nil, false })
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].skipped || !results[1].skipped {
		t.Fatalf("results=%+v", results)
	}
}

func TestBuildHonorsCancelledContext(t *testing.T) {
	root := t.TempDir()
	writeAged(t, root, "a.go", "package a\nfunc Alpha() {}\n", time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := BuildWithOptions(ctx, root, rootRepo, BuildOptions{}); err == nil {
		t.Fatal("expected cancellation error")
	}
}

func TestFileStale(t *testing.T) {
	root := t.TempDir()
	writeAged(t, root, "a.go", "package a\nfunc Alpha() {}\n", time.Hour)
	writeAged(t, root, "b.go", "package b\nfunc Beta() {}\n", time.Hour)
	idx, _ := build(t, root, BuildOptions{})

	if idx.FileStale(root, "a.go") {
		t.Fatal("unchanged file reported stale")
	}
	writeAged(t, root, "b.go", "package b\nfunc Beta2() {}\n", 5*time.Minute)
	if !idx.FileStale(root, "b.go") {
		t.Fatal("modified file not reported stale")
	}
	if err := os.Remove(filepath.Join(root, "a.go")); err != nil {
		t.Fatal(err)
	}
	if !idx.FileStale(root, "a.go") {
		t.Fatal("deleted file not reported stale")
	}
	if !idx.FileStale(root, "never-indexed.go") {
		t.Fatal("unknown file not reported stale")
	}
}

func TestFileStaleRehashesRacilyCleanEntries(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.go")
	if err := os.WriteFile(path, []byte("package a\nfunc Alpha() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, _ := build(t, root, BuildOptions{})
	if idx.FileStale(root, "a.go") {
		t.Fatal("racily clean but unchanged file reported stale")
	}
	recorded := idx.Files["a.go"]
	if err := os.WriteFile(path, []byte("package a\nfunc Omega() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mtime := time.Unix(0, recorded.MTimeNS)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	if !idx.FileStale(root, "a.go") {
		t.Fatal("same-stat content change not detected")
	}
}

func TestCheckFreshness(t *testing.T) {
	root := t.TempDir()
	f, err := CheckFreshness(context.Background(), root, rootRepo)
	if err != nil || f.Indexed || !f.Stale {
		t.Fatalf("missing index: f=%+v err=%v", f, err)
	}
	writeAged(t, root, "a.go", "package a\n", time.Hour)
	writeAged(t, root, "b.go", "package b\n", time.Hour)
	build(t, root, BuildOptions{})
	if f, _ := CheckFreshness(context.Background(), root, rootRepo); f.Stale || !f.Indexed {
		t.Fatalf("fresh index reported stale: %+v", f)
	}
	writeAged(t, root, "a.go", "package a // changed\n", time.Minute)
	writeAged(t, root, "c.go", "package c\n", time.Hour)
	if err := os.Remove(filepath.Join(root, "b.go")); err != nil {
		t.Fatal(err)
	}
	f, err = CheckFreshness(context.Background(), root, rootRepo)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Stale || f.Added != 1 || f.Modified != 1 || f.Removed != 1 {
		t.Fatalf("f=%+v", f)
	}
}

func TestSearchIsDeterministicAcrossRepositories(t *testing.T) {
	sym := func(path string) []Symbol {
		return []Symbol{{Name: "ProcessPayment", Kind: "function", Path: path, Line: 1}}
	}
	indexes := map[string]Index{}
	for _, repo := range []string{"zeta", "alpha", "mid", "beta", "omega", "gamma"} {
		indexes[repo] = Index{Symbols: sym("svc.go")}
	}
	want := ""
	for i := 0; i < 25; i++ {
		hits := Search("ProcessPayment", indexes, 10)
		got := []string{}
		for _, h := range hits {
			got = append(got, h.Repository)
		}
		joined := strings.Join(got, ",")
		if i == 0 {
			want = joined
			if want != "alpha,beta,gamma,mid,omega,zeta" {
				t.Fatalf("tie order=%s", want)
			}
		} else if joined != want {
			t.Fatalf("run %d order %s != %s", i, joined, want)
		}
	}
}
