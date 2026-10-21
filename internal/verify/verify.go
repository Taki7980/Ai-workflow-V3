// Package verify runs focused verification checks without a shell and bounds
// their output.
package verify

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/Taki7980/ai-workflow-v3/internal/handoff"
	"github.com/Taki7980/ai-workflow-v3/internal/procx"
)

// AllowedCheckRunners are the standard testing, linting, and build tools
// permitted for automated verification checks.
var AllowedCheckRunners = map[string]bool{
	"go": true, "go.exe": true,
	"npm": true, "npm.cmd": true, "npx": true, "npx.cmd": true,
	"pnpm": true, "pnpm.cmd": true, "yarn": true, "yarn.cmd": true,
	"cargo": true, "cargo.exe": true,
	"python": true, "python.exe": true, "python3": true, "python3.exe": true,
	"pytest": true, "pytest.exe": true,
	"make": true, "make.exe": true, "ninja": true, "ninja.exe": true,
	"ctest": true, "ctest.exe": true,
	"mvn": true, "mvn.cmd": true, "mvnw": true, "mvnw.cmd": true,
	"gradle": true, "gradle.bat": true, "gradlew": true, "gradlew.bat": true,
	"dotnet": true, "dotnet.exe": true,
	"ruff": true, "ruff.exe": true, "eslint": true, "eslint.cmd": true,
	"tsc": true, "tsc.cmd": true,
	"golangci-lint": true, "golangci-lint.exe": true,
	"deno": true, "deno.exe": true, "bun": true, "bun.exe": true,
}

// IsAllowedCheck reports whether a command begins with an allowlisted test/build tool.
func IsAllowedCheck(raw string) bool {
	argv, err := SplitCommand(raw)
	if err != nil || len(argv) == 0 {
		return false
	}
	base := strings.ToLower(filepath.Base(argv[0]))
	return AllowedCheckRunners[base]
}

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
	// User checks run in a sanitized environment to prevent ambient secret propagation.
	env := procx.SafeEnv("GOPATH", "GOROOT", "CARGO_HOME", "RUSTUP_HOME", "PNPM_HOME", "NODE_PATH", "NVM_DIR", "VERIFY_HELPER")
	r, err := procx.Run(parent, procx.Cmd{Argv: argv, Dir: root, Env: env, Timeout: checkTimeout, MaxStdout: 32 << 20, MergeStderr: true})
	switch {
	case err != nil:
		return CheckResult{raw, 124, err.Error()}
	case r.TimedOut:
		return CheckResult{raw, 124, fmt.Sprintf("timed out after %s", checkTimeout)}
	case r.StdoutExceeded:
		return CheckResult{raw, 125, "output exceeded 32 MiB; check stopped\n" + Compress(string(r.Stdout), 60, 10000)}
	}
	return CheckResult{raw, r.ExitCode, Compress(string(r.Stdout), 60, 10000)}
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
