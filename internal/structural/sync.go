package structural

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Taki7980/ai-workflow-v3/internal/procx"
	"github.com/Taki7980/ai-workflow-v3/internal/storage"
)

// SyncEntry is the per-repository result of a graph or SCIP sync.
type SyncEntry struct {
	RelativePath string `json:"relative_path"`
	Action       string `json:"action,omitempty"`
	OK           bool   `json:"ok"`
	Error        string `json:"error,omitempty"`
	SHA256       string `json:"graph_sha256,omitempty"`
}

type SyncReport struct {
	Installed    bool        `json:"installed"`
	Attempted    int         `json:"attempted"`
	Ready        int         `json:"ready"`
	DataRoot     string      `json:"data_root"`
	Repositories []SyncEntry `json:"repositories"`
}

var scipLookPath = exec.LookPath

func clip(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 500 {
		s = s[:500]
	}
	return s
}

func runTool(ctx context.Context, timeout time.Duration, dir string, env []string, exe string, args ...string) ([]byte, error) {
	r, err := procx.Run(ctx, procx.Cmd{Argv: append([]string{exe}, args...), Dir: dir, Env: append(procx.InheritEnv(), env...), Timeout: timeout, MaxStdout: 32 << 20})
	switch {
	case err != nil:
		return nil, fmt.Errorf("%s", clip(err.Error()))
	case r.TimedOut:
		return nil, fmt.Errorf("timed out after %s", timeout)
	case r.StdoutExceeded:
		return nil, fmt.Errorf("output exceeded %d bytes", 32<<20)
	case r.ExitCode != 0:
		msg := string(r.Stderr)
		if strings.TrimSpace(msg) == "" {
			msg = string(r.Stdout)
		}
		if strings.TrimSpace(msg) == "" {
			msg = fmt.Sprintf("exit status %d", r.ExitCode)
		}
		return nil, fmt.Errorf("%s", clip(msg))
	}
	return r.Stdout, nil
}

func crgVersion(ctx context.Context, exe string) string {
	out, err := runTool(ctx, 5*time.Second, "", nil, exe, "--version")
	if fields := strings.Fields(string(out)); err == nil && len(fields) > 0 {
		return fields[len(fields)-1]
	}
	return "unknown"
}

// SyncGraphs builds missing graphs and incrementally updates existing ones,
// then records a freshness manifest for each repository that succeeds.
// ponytail: no SQLite validation; trusts the CRG exit code, a non-empty graph.db and the manifest SHA-256.
func SyncGraphs(ctx context.Context, ws string, rels []string, timeout time.Duration) SyncReport {
	r := SyncReport{DataRoot: filepath.Join(ws, filepath.FromSlash(graphRelative)), Repositories: []SyncEntry{}}
	exe, err := exec.LookPath("code-review-graph")
	if err != nil {
		return r
	}
	r.Installed = true
	version := crgVersion(ctx, exe)
	for _, rel := range rels {
		e := SyncEntry{RelativePath: normRel(rel)}
		r.Attempted++
		func() {
			root, err := repoRoot(ws, rel)
			if err != nil {
				e.Error = "unsafe repository path: " + err.Error()
				return
			}
			dir, err := GraphDir(ws, rel)
			if err != nil {
				e.Error = "unsafe graph state path: " + err.Error()
				return
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				e.Error = err.Error()
				return
			}
			db := filepath.Join(dir, "graph.db")
			e.Action = "build"
			if isFile(db) {
				e.Action = "update"
			}
			env := []string{"CRG_DATA_DIR=" + dir, "CRG_REPO_ROOT=" + root}
			if _, err := runTool(ctx, timeout, root, env, exe, e.Action, "--repo", root, "--quiet"); err != nil {
				e.Error = err.Error()
				return
			}
			if info, err := os.Stat(db); err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
				e.Error = "graph database was not created"
				return
			}
			m, err := WriteGraphManifest(ctx, ws, rel, version, e.Action)
			if err != nil {
				e.Error = err.Error()
				return
			}
			e.OK, e.SHA256 = true, m.GraphSHA256
		}()
		if e.OK {
			r.Ready++
		}
		r.Repositories = append(r.Repositories, e)
	}
	return r
}

func moveFile(src, dst string) error {
	if os.Rename(src, dst) == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	b, err := io.ReadAll(in)
	in.Close()
	if err != nil {
		return err
	}
	if err := storage.WriteFileAtomic(dst, b); err != nil {
		return err
	}
	return os.Remove(src)
}

// SyncScip runs the detected SCIP indexer, converts index.scip to JSON with
// `scip print --json`, stores both centrally and records a manifest.
func SyncScip(ctx context.Context, ws string, rels []string, language string, timeout time.Duration) SyncReport {
	r := SyncReport{Installed: true, DataRoot: filepath.Join(ws, filepath.FromSlash(scipRelative)), Repositories: []SyncEntry{}}
	for _, rel := range rels {
		e := SyncEntry{RelativePath: normRel(rel)}
		r.Attempted++
		func() {
			root, err := repoRoot(ws, rel)
			if err != nil {
				e.Error = "unsafe repository path: " + err.Error()
				return
			}
			dir, err := ScipDir(ws, rel)
			if err != nil {
				e.Error = "unsafe SCIP state path: " + err.Error()
				return
			}
			ix, ok := DetectIndexer(root, language)
			if !ok {
				e.Error = "no unambiguous SCIP indexer; pass --language"
				return
			}
			e.Action = ix.Language
			indexer, err := scipLookPath(ix.Executable)
			if err != nil {
				e.Error = ix.Executable + " not installed"
				return
			}
			scip, err := scipLookPath("scip")
			if err != nil {
				e.Error = "scip not installed"
				return
			}
			produced := filepath.Join(root, "index.scip")
			if _, err := os.Lstat(produced); err == nil {
				e.Error = "index.scip already exists in the repository root; move it first"
				return
			}
			defer os.Remove(produced) // also cleans up a partial index from a failed run
			if _, err := runTool(ctx, timeout, root, nil, indexer, ix.Args...); err != nil {
				e.Error = err.Error()
				return
			}
			out, err := runTool(ctx, timeout, root, nil, scip, "print", "--json", "index.scip")
			if err != nil {
				e.Error = err.Error()
				return
			}
			if len(out) > scipMaxJSON {
				e.Error = "index.json exceeds 64 MiB"
				return
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				e.Error = err.Error()
				return
			}
			if err := storage.WriteFileAtomic(filepath.Join(dir, "index.json"), out); err != nil {
				e.Error = err.Error()
				return
			}
			if err := moveFile(produced, filepath.Join(dir, "index.scip")); err != nil {
				e.Error = err.Error()
				return
			}
			if _, err := WriteScipManifest(ctx, ws, rel, ix.Language, ix.Executable); err != nil {
				e.Error = err.Error()
				return
			}
			e.OK = true
		}()
		if e.OK {
			r.Ready++
		}
		r.Repositories = append(r.Repositories, e)
	}
	return r
}
