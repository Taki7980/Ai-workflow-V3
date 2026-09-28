package doctor

import (
	"context"
	"os"
	"os/exec"

	"github.com/Taki7980/ai-workflow-v3/internal/config"
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
	ok := true
	for _, c := range checks {
		if !c.OK {
			ok = false
		}
	}
	return Report{OK: ok, Checks: checks}
}
func detail(err error, success string) string {
	if err != nil {
		return err.Error()
	}
	return success
}
func repoDetail(err error, n int) string {
	if err != nil {
		return err.Error()
	}
	if n == 0 {
		return "no Git repositories discovered"
	}
	return itoa(n) + " repository/repositories discovered"
}
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	b := []byte{}
	for n > 0 {
		b = append(b, byte('0'+n%10))
		n /= 10
	}
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return string(b)
}
