package indexer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

const IndexVersion = 2

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
	Version           int                  `json:"version"`
	Repository        string               `json:"repository"`
	RepositoryID      string               `json:"repository_id"`
	ExtractorRevision string               `json:"extractor_revision"`
	BuiltAt           string               `json:"built_at"`
	Files             map[string]FileState `json:"files"`
	Symbols           []Symbol             `json:"symbols"`
}

type WorkspaceBuildResult struct {
	Index Index      `json:"index"`
	Stats BuildStats `json:"stats"`
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

// Build indexes repo using the default automatic freshness mode.
func Build(ctx context.Context, controlRoot string, repo workspace.Repository) (Index, error) {
	idx, _, err := BuildWithMode(ctx, controlRoot, repo, BuildAuto)
	return idx, err
}

// BuildWorkspaceWithMode builds every included repository using mode and
// returns both the resulting index and deterministic build statistics.
func BuildWorkspaceWithMode(ctx context.Context, root string, reg workspace.Registry, mode BuildMode) (map[string]WorkspaceBuildResult, error) {
	out := map[string]WorkspaceBuildResult{}
	for _, repo := range reg.Repositories {
		if !repo.Included {
			continue
		}
		idx, stats, err := BuildWithMode(ctx, root, repo, mode)
		if err != nil {
			return nil, fmt.Errorf("index %s: %w", repo.RelativePath, err)
		}
		out[repo.RelativePath] = WorkspaceBuildResult{Index: idx, Stats: stats}
	}
	return out, nil
}

// BuildWorkspace preserves the pre-Stage-5 API while delegating freshness
// decisions to automatic mode.
func BuildWorkspace(ctx context.Context, root string, reg workspace.Registry) (map[string]Index, error) {
	built, err := BuildWorkspaceWithMode(ctx, root, reg, BuildAuto)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Index, len(built))
	for rel, result := range built {
		out[rel] = result.Index
	}
	return out, nil
}

// isNestedGitRoot reports whether path contains a .git entry, indicating it
// is the root of a nested Git repository that should not be indexed.
func isNestedGitRoot(path string) bool {
	st, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil && (st.IsDir() || st.Mode().IsRegular())
}
