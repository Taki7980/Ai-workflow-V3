package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/Taki7980/ai-workflow-v3/internal/storage"
)

const (
	excludeBegin = "# >>> ai-workflow local state >>>"
	excludeEnd   = "# <<< ai-workflow local state <<<"
)

// LocalExcludePatterns are machine-local paths hidden through Git's common
// info/exclude so tracked .gitignore files are never edited (V2 parity).
var LocalExcludePatterns = []string{
	"/ai-workspace/config/repositories.json",
	"/ai-workspace/generated/",
	"/ai-workspace/indexes/",
	"/ai-workspace/code-review-graph/",
	"/ai-workspace/memory/",
	"/.code-review-graph/",
	"/.ai/",
}

// ExcludeReport mirrors V2 install_local_excludes output.
type ExcludeReport struct {
	Applicable bool     `json:"applicable"`
	Installed  bool     `json:"installed"`
	Changed    bool     `json:"changed"`
	Path       *string  `json:"path"`
	Patterns   []string `json:"patterns"`
	Reason     *string  `json:"reason"`
	Error      string   `json:"error,omitempty"`
}

func managedBlock() string {
	return excludeBegin + "\n" + strings.Join(LocalExcludePatterns, "\n") + "\n" + excludeEnd + "\n"
}

// InstallLocalExcludes idempotently writes the managed block into the Git
// common-dir info/exclude of root. Non-Git roots are reported, not errors.
func InstallLocalExcludes(ctx context.Context, root string) ExcludeReport {
	r := ExcludeReport{Patterns: LocalExcludePatterns}
	reason := func(s string) ExcludeReport { r.Reason = &s; return r }
	raw, err := gitText(ctx, root, "rev-parse", "--git-common-dir")
	if err != nil || raw == "" {
		return reason("not_git_repository")
	}
	if !filepath.IsAbs(raw) {
		raw = filepath.Join(root, raw)
	}
	if st, err := os.Stat(raw); err != nil || !st.IsDir() {
		return reason("not_git_repository")
	}
	path := filepath.Join(raw, "info", "exclude")
	r.Applicable, r.Path = true, &path
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		r.Error = err.Error()
		return reason("exclude_unreadable")
	}
	existing := string(b)
	begins, ends := strings.Count(existing, excludeBegin), strings.Count(existing, excludeEnd)
	if begins != ends || begins > 1 {
		return reason("malformed_managed_block")
	}
	updated := existing
	if begins == 0 {
		if updated != "" && !strings.HasSuffix(updated, "\n") {
			updated += "\n"
		}
		if updated != "" && !strings.HasSuffix(updated, "\n\n") {
			updated += "\n"
		}
		updated += managedBlock()
	} else {
		start := strings.Index(existing, excludeBegin)
		end := start + strings.Index(existing[start:], excludeEnd) + len(excludeEnd)
		if end < len(existing) && existing[end] == '\n' {
			end++
		}
		updated = existing[:start] + managedBlock() + existing[end:]
	}
	if updated != existing {
		if err := storage.WriteFileAtomic(path, []byte(updated)); err != nil {
			r.Error = err.Error()
			return reason("exclude_write_failed")
		}
		r.Changed = true
	}
	r.Installed = true
	return r
}
