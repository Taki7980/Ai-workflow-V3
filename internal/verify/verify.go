// Package verify runs focused verification checks without a shell and bounds
// their output.
package verify

import (
	"context"
	"errors"
	"os/exec"
	"time"

	"github.com/Taki7980/ai-workflow-v3/internal/handoff"
)

type CheckResult struct {
	Check      string `json:"check"`
	ReturnCode int    `json:"returncode"`
	Output     string `json:"output"`
}

type Result struct {
	OK            bool          `json:"ok"`
	Checks        []CheckResult `json:"checks"`
	HandoffErrors []string      `json:"handoff_errors"`
}

var checkTimeout = 120 * time.Second

// RunChecks executes each check as argv in root with a per-check timeout.
// Parse failures report 2 and timeouts report 124, as in V2.
func RunChecks(ctx context.Context, root string, checks []string) Result {
	res := Result{Checks: []CheckResult{}, HandoffErrors: []string{}}
	for _, raw := range checks {
		res.Checks = append(res.Checks, runOne(ctx, root, raw))
	}
	res.OK = len(res.Checks) > 0
	for _, c := range res.Checks {
		if c.ReturnCode != 0 {
			res.OK = false
		}
	}
	return res
}

func runOne(parent context.Context, root, raw string) CheckResult {
	argv, err := SplitCommand(raw)
	if err != nil {
		return CheckResult{raw, 2, "parse error: " + err.Error()}
	}
	if len(argv) == 0 {
		return CheckResult{raw, 2, "empty check command"}
	}
	ctx, cancel := context.WithTimeout(parent, checkTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = root
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return CheckResult{raw, 124, err.Error()}
	}
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		return CheckResult{raw, exitErr.ExitCode(), Compress(string(out), 60, 10000)}
	case err != nil:
		return CheckResult{raw, 124, err.Error()}
	}
	return CheckResult{raw, 0, Compress(string(out), 60, 10000)}
}

// Verify runs checks and validates the handoff; ok requires both.
func Verify(ctx context.Context, root string, checks []string, handoffMaxLines int) Result {
	res := Result{Checks: []CheckResult{}}
	if len(checks) > 0 {
		res = RunChecks(ctx, root, checks)
	}
	res.HandoffErrors = handoff.Validate(root, handoffMaxLines)
	res.OK = res.OK && len(res.HandoffErrors) == 0
	return res
}
