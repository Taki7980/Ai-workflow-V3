package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"

	"github.com/Taki7980/ai-workflow-v3/internal/config"
	"github.com/Taki7980/ai-workflow-v3/internal/indexer"
	"github.com/Taki7980/ai-workflow-v3/internal/workspace"
)

type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}
type Report struct {
	OK     bool    `json:"ok"`
	Checks []Check `json:"checks"`
}

// Run performs a set of workspace health checks (git availability, config
// validity, repository discovery, and workspace root existence) and returns
// an aggregated report, followed by one "index:<repo>" freshness check per
// included repository. In strict mode, repository discovery must find at
// least one repository, and every included repository must have a fresh index.
func Run(ctx context.Context, root string, strict bool) Report {
	checks := []Check{}
	_, err := exec.LookPath("git")
	checks = append(checks, Check{Name: "git", OK: err == nil, Detail: detail(err, "available")})
	cfg, err := config.Load(root)
	checks = append(checks, Check{Name: "config", OK: err == nil, Detail: detail(err, "valid")})
	if err == nil {
		repos, derr := workspace.Discover(ctx, root, cfg.Workspace.Discovery.MaxDepth, cfg.Workspace.Discovery.AutoIncludeOnSetup)
		ok := derr == nil && len(repos) > 0
		if !strict && derr == nil {
			ok = true
		}
		checks = append(checks, Check{Name: "repository-discovery", OK: ok, Detail: repoDetail(derr, len(repos))})
	}
	_, statErr := os.Stat(root)
	checks = append(checks, Check{Name: "workspace-root", OK: statErr == nil, Detail: detail(statErr, "exists")})
	checks = append(checks, indexChecks(ctx, root, strict)...)
	ok := true
	for _, c := range checks {
		if !c.OK {
			ok = false
		}
	}
	return Report{OK: ok, Checks: checks}
}

// detail returns err's message if non-nil, otherwise the given success message.
func detail(err error, success string) string {
	if err != nil {
		return err.Error()
	}
	return success
}

// repoDetail describes the outcome of repository discovery: err's message if
// discovery failed, otherwise a human-readable count of repositories found.
func repoDetail(err error, n int) string {
	if err != nil {
		return err.Error()
	}
	if n == 0 {
		return "no Git repositories discovered"
	}
	return strconv.Itoa(n) + " repository/repositories discovered"
}

// indexChecks reports index freshness for each included repository in the
// registry. Missing or stale indexes fail only in strict mode. A workspace
// without a registry yet (before setup) contributes no checks.
func indexChecks(ctx context.Context, root string, strict bool) []Check {
	reg, err := workspace.Load(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return []Check{{Name: "registry", OK: false, Detail: err.Error()}}
	}
	repos := append([]workspace.Repository(nil), reg.Repositories...)
	sort.Slice(repos, func(i, j int) bool { return repos[i].RelativePath < repos[j].RelativePath })
	checks := []Check{}
	for _, repo := range repos {
		if !repo.Included {
			continue
		}
		check := Check{Name: "index:" + repo.RelativePath, OK: true, Detail: "fresh"}
		f, err := indexer.CheckFreshness(ctx, root, repo)
		switch {
		case err != nil:
			check.OK, check.Detail = false, err.Error()
		case !f.Indexed:
			check.OK, check.Detail = !strict, `not indexed; run "ai-workflow index"`
		case f.Stale:
			check.OK = !strict
			check.Detail = fmt.Sprintf(`stale: %d added, %d modified, %d removed; run "ai-workflow index"`, f.Added, f.Modified, f.Removed)
		}
		checks = append(checks, check)
	}
	return checks
}
