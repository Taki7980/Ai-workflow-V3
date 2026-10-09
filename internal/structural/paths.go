// Package structural adapts code-review-graph (CRG) and SCIP indexes into
// validated, freshness-gated brief evidence.
package structural

import (
	"path"
	"regexp"
	"strings"

	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

const (
	graphRelative = "ai-workspace/code-review-graph"
	scipRelative  = "ai-workspace/scip"
)

var unsafeKeyChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// RepoKey maps a repository relative path to its state directory name,
// matching V2 _repo_key.
func RepoKey(rel string) string {
	v := strings.ReplaceAll(strings.TrimSpace(rel), `\`, "/")
	if v == "" || v == "." {
		return "root"
	}
	parts := strings.Split(v, "/")
	for i, raw := range parts {
		p := strings.Trim(unsafeKeyChars.ReplaceAllString(raw, "-"), ".-_")
		if p == "" {
			p = "repo"
		}
		parts[i] = p
	}
	return strings.Join(parts, "__")
}

// normRel returns the slash form of a repository relative path, "." for the root.
func normRel(rel string) string {
	v := strings.ReplaceAll(strings.TrimSpace(rel), `\`, "/")
	if v == "" {
		return "."
	}
	return path.Clean(v)
}

func stateDir(ws, kind, rel string) (string, error) {
	_, abs, err := workspace.Within(ws, kind+"/"+RepoKey(rel))
	return abs, err
}

// GraphDir is the central CRG data directory for one repository.
func GraphDir(ws, rel string) (string, error) { return stateDir(ws, graphRelative, rel) }

// ScipDir is the central SCIP index directory for one repository.
func ScipDir(ws, rel string) (string, error) { return stateDir(ws, scipRelative, rel) }

// repoRoot resolves a repository relative path inside the workspace.
func repoRoot(ws, rel string) (string, error) {
	_, abs, err := workspace.Within(ws, normRel(rel))
	return abs, err
}
