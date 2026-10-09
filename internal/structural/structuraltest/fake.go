// Package structuraltest builds a fake code-review-graph binary for tests.
package structuraltest

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

var (
	once     sync.Once
	binDir   string
	buildErr error
	output   []byte
)

func srcDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "testdata")
}

// FakeCRG builds testdata/fakecrg once per process as code-review-graph,
// points it at the CRG fixtures, and returns the directory holding it.
// Callers prepend the directory to PATH.
func FakeCRG(t testing.TB) string {
	t.Helper()
	once.Do(func() {
		binDir, buildErr = os.MkdirTemp("", "fakecrg-")
		if buildErr != nil {
			return
		}
		name := "code-review-graph"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", filepath.Join(binDir, name), ".")
		cmd.Dir = filepath.Join(srcDir(), "fakecrg")
		output, buildErr = cmd.CombinedOutput()
	})
	if buildErr != nil {
		t.Fatalf("build fake code-review-graph: %v %s", buildErr, output)
	}
	t.Setenv("CRG_FAKE_FIXTURES", filepath.Join(srcDir(), "crg"))
	return binDir
}

// UseFakeCRG puts the fake first on PATH for the test.
func UseFakeCRG(t testing.TB) {
	t.Helper()
	t.Setenv("PATH", FakeCRG(t)+string(os.PathListSeparator)+os.Getenv("PATH"))
}
