package structural

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Taki7980/ai-workflow-v3/internal/structural/structuraltest"
)

func TestSyncGraphsBuildThenUpdate(t *testing.T) {
	structuraltest.UseFakeCRG(t)
	ws := gitRepo(t)
	ctx := context.Background()
	r := SyncGraphs(ctx, ws, []string{"."}, time.Minute)
	if !r.Installed || r.Attempted != 1 || r.Ready != 1 || r.Repositories[0].Action != "build" || !r.Repositories[0].OK || r.Repositories[0].SHA256 == "" {
		t.Fatalf("build: %+v", r)
	}
	if st := GraphStatus(ctx, ws, "."); !st.Ready {
		t.Fatalf("not ready after sync: %+v", st)
	}
	dir, _ := GraphDir(ws, ".")
	b, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if !strings.Contains(string(b), `"crg_version": "2.3.8"`) || !strings.Contains(string(b), `"generation_mode": "build"`) {
		t.Fatalf("manifest %s", b)
	}
	if r := SyncGraphs(ctx, ws, []string{"."}, time.Minute); r.Repositories[0].Action != "update" || !r.Repositories[0].OK {
		t.Fatalf("update: %+v", r)
	}
}

func TestSyncGraphsNotInstalled(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	r := SyncGraphs(context.Background(), t.TempDir(), []string{"."}, time.Minute)
	if r.Installed || r.Attempted != 0 || r.Repositories == nil {
		t.Fatalf("got %+v", r)
	}
}

func TestSyncGraphsEmptyDB(t *testing.T) {
	structuraltest.UseFakeCRG(t)
	t.Setenv("CRG_FAKE_MODE", "nodb")
	ws := gitRepo(t)
	r := SyncGraphs(context.Background(), ws, []string{"."}, time.Minute)
	if r.Ready != 0 || r.Repositories[0].OK || r.Repositories[0].Error != "graph database was not created" {
		t.Fatalf("got %+v", r)
	}
}

func TestSyncGraphsFailureMessage(t *testing.T) {
	structuraltest.UseFakeCRG(t)
	t.Setenv("CRG_FAKE_MODE", "fail")
	r := SyncGraphs(context.Background(), gitRepo(t), []string{"."}, time.Minute)
	if r.Repositories[0].OK || r.Repositories[0].Error == "" {
		t.Fatalf("got %+v", r)
	}
}

func noTools(t *testing.T, missing ...string) {
	t.Helper()
	old := scipLookPath
	scipLookPath = func(name string) (string, error) {
		for _, m := range missing {
			if m == name {
				return "", errors.New("not found")
			}
		}
		return name, nil
	}
	t.Cleanup(func() { scipLookPath = old })
}

func TestSyncScipMissingTool(t *testing.T) {
	ws := gitRepo(t)
	writeFile(t, filepath.Join(ws, "go.mod"), "module x\n")
	noTools(t, "scip-go")
	r := SyncScip(context.Background(), ws, []string{"."}, "", time.Minute)
	if r.Ready != 0 || r.Repositories[0].Error != "scip-go not installed" {
		t.Fatalf("got %+v", r)
	}
	noTools(t, "scip")
	if r := SyncScip(context.Background(), ws, []string{"."}, "", time.Minute); r.Repositories[0].Error != "scip not installed" {
		t.Fatalf("got %+v", r)
	}
}

func TestSyncScipAmbiguous(t *testing.T) {
	ws := gitRepo(t)
	noTools(t)
	r := SyncScip(context.Background(), ws, []string{"."}, "", time.Minute)
	if r.Repositories[0].Error != "no unambiguous SCIP indexer; pass --language" {
		t.Fatalf("got %+v", r)
	}
}
