package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/Taki7980/ai-workflow-v3/internal/procx"
)

// ExecWrapperCommand is the hidden CLI command that applies rlimits and execs.
const ExecWrapperCommand = "__provider-exec"

const (
	maxCPUSeconds = 86_400
	maxMemoryMB   = 1_048_576
	maxFileSizeMB = 1_048_576
	maxOpenFiles  = 1_000_000
)

// ResourceLimits are per-process rlimits applied by the exec wrapper.
type ResourceLimits struct {
	CPUSeconds int `json:"cpu_seconds,omitempty"`
	MemoryMB   int `json:"memory_mb,omitempty"`
	FileSizeMB int `json:"file_size_mb,omitempty"`
	OpenFiles  int `json:"open_files,omitempty"`
}

func (l ResourceLimits) any() bool {
	return l.CPUSeconds > 0 || l.MemoryMB > 0 || l.FileSizeMB > 0 || l.OpenFiles > 0
}

// SandboxPolicy is registry-owned: off | preferred | required, network host | deny.
type SandboxPolicy struct {
	Mode    string         `json:"mode"`
	Backend string         `json:"backend"`
	Network string         `json:"network"`
	Limits  ResourceLimits `json:"limits"`
}

// Validate normalizes defaults and rejects policies that cannot be honoured.
func (p *SandboxPolicy) Validate() error {
	if p.Mode == "" {
		p.Mode = "off"
	}
	if p.Backend == "" {
		p.Backend = "auto"
	}
	if p.Network == "" {
		p.Network = "host"
	}
	switch {
	case p.Mode != "off" && p.Mode != "preferred" && p.Mode != "required":
		return errors.New("sandbox mode must be off, preferred, or required")
	case p.Backend != "auto" && p.Backend != "bubblewrap":
		return errors.New("sandbox backend must be auto or bubblewrap")
	case p.Network != "host" && p.Network != "deny":
		return errors.New("sandbox network must be host or deny")
	}
	for _, c := range []struct {
		v, max int
		name   string
	}{{p.Limits.CPUSeconds, maxCPUSeconds, "cpu_seconds"}, {p.Limits.MemoryMB, maxMemoryMB, "memory_mb"},
		{p.Limits.FileSizeMB, maxFileSizeMB, "file_size_mb"}, {p.Limits.OpenFiles, maxOpenFiles, "open_files"}} {
		if c.v < 0 || c.v > c.max {
			return fmt.Errorf("sandbox %s must be between 1 and %d", c.name, c.max)
		}
	}
	if p.Mode == "off" && (p.Network != "host" || p.Limits.any()) {
		return errors.New("sandbox mode=off cannot enforce network or resource limits")
	}
	return nil
}

func (p SandboxPolicy) mustEnforce() bool {
	return p.Mode == "required" || p.Network == "deny" || p.Limits.any()
}

type sandboxPlan struct {
	argv           []string
	sandboxed      bool
	backend        string
	limitsEnforced bool
	fallbackReason string
}

var bwrapBase = []string{"--die-with-parent", "--new-session", "--unshare-pid", "--unshare-ipc", "--unshare-uts",
	"--cap-drop", "ALL", "--ro-bind", "/", "/", "--proc", "/proc", "--dev", "/dev"}

var probeCache sync.Map // network -> bool

// resolveBubblewrap returns a working bwrap path on Linux, verified by
// actually creating the namespaces the plan depends on.
var resolveBubblewrap = func(network string) string {
	if runtime.GOOS != "linux" {
		return ""
	}
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		return ""
	}
	if ok, cached := probeCache.Load(network); cached {
		if ok.(bool) {
			return bwrap
		}
		return ""
	}
	tr, err := exec.LookPath("true")
	ok := false
	if err == nil {
		argv := append([]string{bwrap}, bwrapBase...)
		if network == "deny" {
			argv = append(argv, "--unshare-net")
		}
		r, err := procx.Run(context.Background(), procx.Cmd{Argv: append(argv, tr), Env: procx.SafeEnv(), Timeout: 3 * time.Second})
		ok = err == nil && r.OK()
	}
	probeCache.Store(network, ok)
	if ok {
		return bwrap
	}
	return ""
}

// buildSandboxPlan returns the exact command to launch or fails closed when
// the policy requires enforcement that no backend can provide.
func buildSandboxPlan(p SandboxPolicy, argv []string, cwd string, writable []string) (sandboxPlan, error) {
	if p.Mode == "off" {
		return sandboxPlan{argv: argv}, nil
	}
	bwrap := resolveBubblewrap(p.Network)
	if bwrap == "" {
		if p.mustEnforce() {
			return sandboxPlan{}, errors.New("provider sandbox policy requires an available enforcing backend")
		}
		return sandboxPlan{argv: argv, fallbackReason: "backend_unavailable"}, nil
	}
	inner := argv
	if p.Limits.any() {
		self, err := os.Executable()
		if err != nil {
			return sandboxPlan{}, errors.New("cannot locate the resource-limit exec wrapper")
		}
		w := []string{self, ExecWrapperCommand}
		for _, l := range []struct {
			flag string
			v    int
		}{{"--cpu-seconds", p.Limits.CPUSeconds}, {"--memory-mb", p.Limits.MemoryMB}, {"--file-size-mb", p.Limits.FileSizeMB}, {"--open-files", p.Limits.OpenFiles}} {
			if l.v > 0 {
				w = append(w, l.flag, strconv.Itoa(l.v))
			}
		}
		inner = append(append(w, "--"), argv...)
	}
	out := append([]string{bwrap}, bwrapBase...)
	if p.Network == "deny" {
		out = append(out, "--unshare-net")
	}
	seen := map[string]bool{}
	for _, w := range writable {
		if abs, err := filepath.Abs(w); err == nil && !seen[abs] {
			if _, err := os.Stat(abs); err == nil {
				seen[abs] = true
				out = append(out, "--bind", abs, abs)
			}
		}
	}
	if abs, err := filepath.Abs(cwd); err == nil {
		out = append(out, "--chdir", abs)
	}
	return sandboxPlan{argv: append(out, inner...), sandboxed: true, backend: "bubblewrap", limitsEnforced: p.Limits.any()}, nil
}
