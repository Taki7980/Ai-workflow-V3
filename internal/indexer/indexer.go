package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Taki7980/ai-workflow-v3/internal/storage"
	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

var sourceExts = map[string]bool{".py": true, ".rs": true, ".js": true, ".jsx": true, ".ts": true, ".tsx": true, ".go": true, ".java": true, ".cs": true, ".cpp": true, ".cc": true, ".cxx": true, ".c": true, ".h": true, ".hpp": true, ".rb": true, ".php": true, ".swift": true, ".kt": true, ".scala": true, ".sql": true, ".vue": true, ".svelte": true}
var excludes = map[string]bool{".git": true, "node_modules": true, "venv": true, ".venv": true, "dist": true, "build": true, "bin": true, "obj": true, "__pycache__": true, "ai-workspace": true, ".ai": true, ".agents": true}

type FileState struct {
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
	MTimeNS int64  `json:"mtime_ns"`
}
type Symbol struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Line    int    `json:"line"`
	EndLine int    `json:"end_line,omitempty"`
	SHA256  string `json:"sha256"`
}
type Index struct {
	Version    int                  `json:"version"`
	Repository string               `json:"repository"`
	BuiltAt    string               `json:"built_at"`
	Files      map[string]FileState `json:"files"`
	Symbols    []Symbol             `json:"symbols"`
}

// repoKey derives a filesystem-safe key for a repository's relative path,
// used to namespace its on-disk index directory.
func repoKey(rel string) string {
	if rel == "." || rel == "" {
		return "root"
	}
	s := regexp.MustCompile(`[^A-Za-z0-9._-]+`).ReplaceAllString(filepath.ToSlash(rel), "-")
	return strings.Trim(s, ".-_")
}

// Path returns the on-disk location of the index file for repo within the
// workspace rooted at controlRoot.
func Path(controlRoot string, repo workspace.Repository) string {
	return filepath.Join(controlRoot, "ai-workspace", "indexes", repoKey(repo.RelativePath), "index.json")
}

// Build walks repo's source files (skipping excluded directories and nested
// Git roots), hashes and parses each file concurrently to build a symbol
// index, writes the resulting index to disk, and returns it.
func Build(ctx context.Context, controlRoot string, repo workspace.Repository) (Index, error) {
	repoRoot := controlRoot
	if repo.RelativePath != "." {
		repoRoot = filepath.Join(controlRoot, filepath.FromSlash(repo.RelativePath))
	}
	paths := []string{}
	err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != repoRoot && excludes[d.Name()] {
				return filepath.SkipDir
			}
			if path != repoRoot && isNestedGitRoot(path) {
				return filepath.SkipDir
			}
			return nil
		}
		if sourceExts[strings.ToLower(filepath.Ext(path))] {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return Index{}, err
	}
	sort.Strings(paths)
	idx := Index{Version: 1, Repository: repo.RelativePath, BuiltAt: time.Now().UTC().Format(time.RFC3339), Files: map[string]FileState{}}
	type result struct {
		rel     string
		state   FileState
		symbols []Symbol
		err     error
	}
	jobs := make(chan string)
	results := make(chan result)
	workers := runtime.GOMAXPROCS(0)
	if workers > 8 {
		workers = 8
	}
	if workers < 1 {
		workers = 1
	}
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				select {
				case <-ctx.Done():
					results <- result{err: ctx.Err()}
					return
				default:
				}
				rel, _ := filepath.Rel(repoRoot, p)
				rel = filepath.ToSlash(rel)
				state, syms, e := indexFile(p, rel)
				results <- result{rel: rel, state: state, symbols: syms, err: e}
			}
		}()
	}
	go func() {
		for _, p := range paths {
			jobs <- p
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()
	for r := range results {
		if r.err != nil {
			return Index{}, r.err
		}
		idx.Files[r.rel] = r.state
		idx.Symbols = append(idx.Symbols, r.symbols...)
	}
	sort.Slice(idx.Symbols, func(i, j int) bool {
		if idx.Symbols[i].Path == idx.Symbols[j].Path {
			return idx.Symbols[i].Line < idx.Symbols[j].Line
		}
		return idx.Symbols[i].Path < idx.Symbols[j].Path
	})
	if err := storage.WriteJSON(Path(controlRoot, repo), idx); err != nil {
		return Index{}, err
	}
	return idx, nil
}

// indexFile reads the file at path, computes its SHA-256 digest and file
// state, and parses it for symbols, returning the state and any found symbols.
func indexFile(path, rel string) (FileState, []Symbol, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return FileState{}, nil, err
	}
	h := sha256.Sum256(b)
	digest := hex.EncodeToString(h[:])
	st, err := os.Stat(path)
	if err != nil {
		return FileState{}, nil, err
	}
	state := FileState{SHA256: digest, Size: st.Size(), MTimeNS: st.ModTime().UnixNano()}
	syms, _ := parseSymbols(b, rel, digest)
	return state, syms, nil
}

// BuildWorkspace builds an index for every included repository in reg,
// returning a map of relative repository path to its built index.
func BuildWorkspace(ctx context.Context, root string, reg workspace.Registry) (map[string]Index, error) {
	out := map[string]Index{}
	for _, repo := range reg.Repositories {
		if !repo.Included {
			continue
		}
		idx, err := Build(ctx, root, repo)
		if err != nil {
			return nil, fmt.Errorf("index %s: %w", repo.RelativePath, err)
		}
		out[repo.RelativePath] = idx
	}
	return out, nil
}

// isNestedGitRoot reports whether path contains a .git entry, indicating it
// is the root of a nested Git repository that should not be indexed.
func isNestedGitRoot(path string) bool {
	st, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil && (st.IsDir() || st.Mode().IsRegular())
}
