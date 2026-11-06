package workspace

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func commitFiles(t *testing.T, dir string, files ...string) {
	t.Helper()
	for _, f := range files {
		p := filepath.Join(dir, f)
		old, _ := os.ReadFile(p)
		write(t, p, string(old)+"x\n")
	}
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "c"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
}

func TestCoChangedSuggestsHistoricalPartners(t *testing.T) {
	root := t.TempDir()
	gitInit(t, root)
	commitFiles(t, root, "api.go", "api_test.go", "docs/api.md")
	commitFiles(t, root, "api.go", "api_test.go")
	commitFiles(t, root, "api.go", "util.go") // support 1: below the floor
	commitFiles(t, root, "api.go", "gone.go", "api_test.go")
	commitFiles(t, root, "api.go", "gone.go")
	if err := os.Remove(filepath.Join(root, "gone.go")); err != nil {
		t.Fatal(err)
	}
	sweep := []string{"api.go"} // a 31-file sweep must not count as coupling
	for i := range 30 {
		sweep = append(sweep, fmt.Sprintf("gen/f%d.go", i))
	}
	commitFiles(t, root, sweep...)
	commitFiles(t, root, sweep...)

	got := CoChanged(context.Background(), root, Registry{}, []string{"api.go", "docs/api.md", "ai-workspace/state/run.json"}, 5)
	want := []CoChange{{Path: "api_test.go", With: "api.go", Support: 3, Confidence: 0.6}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
}

func TestCoChangedPrefixesNestedRepositories(t *testing.T) {
	root := t.TempDir()
	svc := filepath.Join(root, "svc")
	if err := os.MkdirAll(svc, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInit(t, svc)
	commitFiles(t, svc, "a.go", "b.go")
	commitFiles(t, svc, "a.go", "b.go")
	reg := Registry{Repositories: []Repository{{RelativePath: "svc", Included: true}}}
	got := CoChanged(context.Background(), root, reg, []string{"svc/a.go"}, 5)
	if len(got) != 1 || got[0].Path != "svc/b.go" || got[0].With != "svc/a.go" || got[0].Confidence != 1 {
		t.Fatalf("got %+v", got)
	}
	sweep := []string{"svc/a.go"}
	for i := range 30 {
		sweep = append(sweep, fmt.Sprintf("svc/gen%d.go", i))
	}
	if got := CoChanged(context.Background(), root, reg, sweep, 5); len(got) != 0 {
		t.Fatalf("a sweep-sized change must yield nothing, got %+v", got)
	}
	if got := CoChanged(context.Background(), t.TempDir(), Registry{}, []string{"a.go"}, 5); len(got) != 0 {
		t.Fatalf("non-repository must yield nothing, got %+v", got)
	}
}
