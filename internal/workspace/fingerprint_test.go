package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func TestCanonicalJSONMatchesPython(t *testing.T) {
	b, err := CanonicalJSON(map[string]any{"b": 1, "a": []any{true, nil, "ñ<&>"}})
	if err != nil || string(b) != `{"a":[true,null,"ñ<&>"],"b":1}` {
		t.Fatalf("got %s %v", b, err)
	}
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFingerprintStableAndSensitive(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	write(t, filepath.Join(root, "a.go"), "package a\n")
	ctx := context.Background()
	idx := map[string]any{".": map[string]any{"a.go": "h1"}}
	one := Fingerprint(ctx, root, idx, []string{"a.go"})
	two := Fingerprint(ctx, root, idx, []string{"a.go"})
	if one.Fingerprint != two.Fingerprint || len(one.Fingerprint) != 64 || one.Schema != 2 || one.GitHead == nil {
		t.Fatalf("unstable or malformed: %+v %+v", one, two)
	}
	write(t, filepath.Join(root, "a.go"), "package a // edit\n")
	if Fingerprint(ctx, root, idx, []string{"a.go"}).Fingerprint == one.Fingerprint {
		t.Fatal("content change must change fingerprint")
	}
	if Fingerprint(ctx, root, nil, []string{"a.go"}).IndexStateSHA256 != nil {
		t.Fatal("nil index files must yield null index digest")
	}
}

func TestChangedFileStates(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "p.txt"), "x")
	got := Fingerprint(context.Background(), root, nil, []string{"../x", "missing.txt", "p.txt"}).ChangedFiles
	states := map[string]string{}
	for _, c := range got {
		states[c.Path] = c.State
	}
	if states["../x"] != "rejected" || states["missing.txt"] != "missing" || states["p.txt"] != "present" {
		t.Fatalf("states = %v", states)
	}
	for _, c := range got {
		if c.Path == "p.txt" && (c.SHA256 == nil || *c.SHA256 != "2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db02258717921a4881") {
			t.Fatalf("bad sha %+v", c)
		}
	}
}

func TestChangedFilesModifiedTracked(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	write(t, filepath.Join(root, "tracked.go"), "package a\n")
	for _, args := range [][]string{{"add", "tracked.go"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "x"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	write(t, filepath.Join(root, "tracked.go"), "package a // edit\n")
	if got := ChangedFiles(context.Background(), root, Registry{}); !slices.Equal(got, []string{"tracked.go"}) {
		t.Fatalf("changed = %q", got)
	}
}

func TestChangedFilesNested(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	svc := filepath.Join(root, "svc")
	if err := os.MkdirAll(svc, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInit(t, svc)
	write(t, filepath.Join(svc, "a.go"), "package a\n")
	write(t, filepath.Join(root, "README.md"), "hi\n")
	reg := Registry{Repositories: []Repository{{RelativePath: ".", Included: true}, {RelativePath: "svc", Included: true}}}
	got := ChangedFiles(context.Background(), root, reg)
	if !slices.Contains(got, "svc/a.go") || !slices.Contains(got, "README.md") || slices.Contains(got, "svc/") {
		t.Fatalf("changed = %q", got)
	}
}
