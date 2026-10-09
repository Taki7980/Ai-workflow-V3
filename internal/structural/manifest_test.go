package structural

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// gitRepo creates a workspace that is itself a git repository with one
// committed Go file. ai-workspace/ is deliberately not git-ignored: control
// state written there must not make the repository look changed.
func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "pkg", "a.go"), "package pkg\n\nfunc Foo() {}\n\nfunc Bar() { Foo() }\n")
	git(t, dir, "init", "-q")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func TestRepoKey(t *testing.T) {
	for in, want := range map[string]string{
		"": "root", ".": "root", "services/api": "services__api",
		"a b/c!d": "a-b__c-d", "...": "repo", `x\y`: "x__y",
	} {
		if got := RepoKey(in); got != want {
			t.Errorf("RepoKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func graphDB(t *testing.T, ws string) string {
	t.Helper()
	dir, err := GraphDir(ws, ".")
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "graph.db")
}

func editManifest(t *testing.T, path, key string, value any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	m[key] = value
	b, _ = json.Marshal(m)
	writeFile(t, path, string(b))
}

func TestGraphStatusReasons(t *testing.T) {
	ctx := context.Background()
	ws := gitRepo(t)
	want := func(reason string) {
		t.Helper()
		if st := GraphStatus(ctx, ws, "."); st.Reason != reason || st.Ready != (reason == "graph provenance matches repository state") {
			t.Fatalf("got %+v, want %q", st, reason)
		}
	}
	want("graph.db is missing")
	db := graphDB(t, ws)
	writeFile(t, db, "graph")
	want("manifest.json is missing")
	if _, err := WriteGraphManifest(ctx, ws, ".", "2.3.8", "build"); err != nil {
		t.Fatal(err)
	}
	want("graph provenance matches repository state")
	manifest := filepath.Join(filepath.Dir(db), "manifest.json")
	orig, _ := os.ReadFile(manifest)

	writeFile(t, db, "tampered")
	want("graph hash mismatch")
	writeFile(t, db, "graph")

	editManifest(t, manifest, "manifest_schema", 2)
	want("unsupported manifest schema")
	writeFile(t, manifest, string(orig))

	editManifest(t, manifest, "repository_relative_path", "x")
	want("repository path identity mismatch")
	writeFile(t, manifest, "{not json")
	want("manifest.json is unreadable")
}

func TestGraphStatusStaleAfterEdit(t *testing.T) {
	ctx := context.Background()
	ws := gitRepo(t)
	writeFile(t, graphDB(t, ws), "graph")
	if _, err := WriteGraphManifest(ctx, ws, ".", "2.3.8", "build"); err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(ws, "pkg", "a.go")
	orig, _ := os.ReadFile(a)
	writeFile(t, a, string(orig)+"\nfunc Baz() {}\n")
	if st := GraphStatus(ctx, ws, "."); st.Ready || st.Reason != "repository fingerprint mismatch" {
		t.Fatalf("tracked edit: %+v", st)
	}
	writeFile(t, a, string(orig))
	if st := GraphStatus(ctx, ws, "."); !st.Ready {
		t.Fatalf("restored: %+v", st)
	}
	writeFile(t, filepath.Join(ws, "new.go"), "package x\n")
	if st := GraphStatus(ctx, ws, "."); st.Ready || st.Reason != "repository fingerprint mismatch" {
		t.Fatalf("untracked: %+v", st)
	}
}

func TestGraphStatusHeadChange(t *testing.T) {
	ctx := context.Background()
	ws := gitRepo(t)
	writeFile(t, graphDB(t, ws), "graph")
	if _, err := WriteGraphManifest(ctx, ws, ".", "2.3.8", "build"); err != nil {
		t.Fatal(err)
	}
	git(t, ws, "commit", "-q", "--allow-empty", "-m", "next")
	if st := GraphStatus(ctx, ws, "."); st.Ready {
		t.Fatalf("head change must be stale: %+v", st)
	}
}

func TestScipStatusReasons(t *testing.T) {
	ctx := context.Background()
	ws := gitRepo(t)
	dir, err := ScipDir(ws, ".")
	if err != nil {
		t.Fatal(err)
	}
	want := func(reason string) {
		t.Helper()
		if st := ScipStatus(ctx, ws, "."); st.Reason != reason || st.Ready != (reason == "SCIP provenance matches repository state") {
			t.Fatalf("got %+v, want %q", st, reason)
		}
	}
	want("index.scip is missing")
	writeFile(t, filepath.Join(dir, "index.scip"), "bin")
	want("manifest.json is missing")
	writeFile(t, filepath.Join(dir, "index.json"), `{"documents":[]}`)
	if _, err := WriteScipManifest(ctx, ws, ".", "go", "scip-go"); err != nil {
		t.Fatal(err)
	}
	want("SCIP provenance matches repository state")
	os.Remove(filepath.Join(dir, "index.json"))
	want("index.json is missing")
	writeFile(t, filepath.Join(dir, "index.json"), `{"documents":[1]}`)
	want("SCIP JSON hash mismatch")
}
