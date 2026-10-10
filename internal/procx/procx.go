// Package procx runs external programs safely: argv only (never a shell),
// an explicit environment, a wall-clock timeout, bounded stdout, a bounded
// stderr tail, and termination of the whole process tree on timeout, output
// overflow or cancellation.
package procx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	DefaultTimeout   = 30 * time.Second
	DefaultMaxStdout = 8 << 20
	DefaultMaxStderr = 64 << 10
	waitDelay        = 2 * time.Second
)

// Cmd describes one invocation.
type Cmd struct {
	Argv []string
	Dir  string
	// Env is the complete child environment. Nil means an empty environment;
	// use InheritEnv or SafeEnv explicitly.
	Env       []string
	Stdin     []byte
	Timeout   time.Duration
	MaxStdout int64
	MaxStderr int64
	// MergeStderr sends stderr into the bounded stdout buffer (CombinedOutput).
	MergeStderr bool
}

// Result reports how the process ended. Launch failures are returned as errors.
type Result struct {
	Stdout          []byte
	Stderr          []byte // tail, at most MaxStderr bytes
	ExitCode        int
	TimedOut        bool
	StdoutExceeded  bool
	StderrTruncated bool
	Duration        time.Duration
}

// OK reports a clean, complete, zero-exit run.
func (r Result) OK() bool { return r.ExitCode == 0 && !r.TimedOut && !r.StdoutExceeded }

// SafeEnvKeys are the ambient variables most tools need to start.
var SafeEnvKeys = []string{
	"PATH", "PATHEXT", "SYSTEMROOT", "SYSTEMDRIVE", "WINDIR", "COMSPEC",
	"HOME", "USERPROFILE", "TMP", "TEMP", "TMPDIR", "LANG", "LC_ALL", "LC_CTYPE",
	"PYTHONUTF8", "PYTHONIOENCODING",
}

// SafeEnv returns SafeEnvKeys plus extra names that are set in this process.
func SafeEnv(extra ...string) []string {
	keep := map[string]bool{}
	for _, k := range append(append([]string{}, SafeEnvKeys...), extra...) {
		if k = strings.TrimSpace(k); k != "" {
			keep[envKey(k)] = true
		}
	}
	out := []string{}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if keep[envKey(k)] {
			out = append(out, kv)
		}
	}
	return out
}

// InheritEnv returns the full parent environment, for trusted user tools
// (git, the user's own verification commands) that need it.
func InheritEnv() []string { return os.Environ() }

// stdoutBuffer keeps the first n bytes and signals overflow once.
type stdoutBuffer struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	n        int64
	exceeded bool
	onFull   func()
}

func (b *stdoutBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.exceeded {
		return len(p), nil
	}
	remain := b.n - int64(b.buf.Len())
	if int64(len(p)) > remain {
		b.buf.Write(p[:max(0, remain)])
		b.exceeded = true
		b.onFull()
		return len(p), nil
	}
	return b.buf.Write(p)
}

// tailBuffer keeps the last n bytes.
type tailBuffer struct {
	mu        sync.Mutex
	buf       []byte
	n         int
	truncated bool
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.n; over > 0 {
		t.buf = append([]byte(nil), t.buf[over:]...)
		t.truncated = true
	}
	return len(p), nil
}

// Run executes c. The returned error is non-nil only when the program could
// not be started; exit status, timeout and overflow are reported in Result.
func Run(parent context.Context, c Cmd) (Result, error) {
	if len(c.Argv) == 0 || strings.TrimSpace(c.Argv[0]) == "" {
		return Result{}, errors.New("command is empty")
	}
	if c.Timeout <= 0 {
		c.Timeout = DefaultTimeout
	}
	if c.MaxStdout <= 0 {
		c.MaxStdout = DefaultMaxStdout
	}
	if c.MaxStderr <= 0 {
		c.MaxStderr = DefaultMaxStderr
	}
	ctx, cancel := context.WithTimeout(parent, c.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...)
	cmd.Dir = c.Dir
	cmd.Env = c.Env
	if cmd.Env == nil {
		cmd.Env = []string{}
	}
	if c.Stdin != nil {
		cmd.Stdin = bytes.NewReader(c.Stdin)
	}
	setTreeAttrs(cmd)
	cmd.Cancel = func() error { return killTree(cmd) }
	cmd.WaitDelay = waitDelay

	stdout := &stdoutBuffer{n: c.MaxStdout, onFull: cancel}
	stderr := &tailBuffer{n: int(c.MaxStderr)}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if c.MergeStderr {
		cmd.Stderr = stdout
	}

	start := time.Now()
	if err := cmd.Start(); err != nil {
		return Result{}, err
	}
	waitErr := cmd.Wait()
	r := Result{Duration: time.Since(start), ExitCode: -1}
	if cmd.ProcessState != nil {
		r.ExitCode = cmd.ProcessState.ExitCode()
	}
	stdout.mu.Lock()
	r.Stdout, r.StdoutExceeded = stdout.buf.Bytes(), stdout.exceeded
	stdout.mu.Unlock()
	stderr.mu.Lock()
	r.Stderr, r.StderrTruncated = stderr.buf, stderr.truncated
	stderr.mu.Unlock()
	r.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded) && !r.StdoutExceeded
	if r.ExitCode == 0 && waitErr != nil && !r.TimedOut && !r.StdoutExceeded {
		r.ExitCode = -1 // I/O failure after a clean exit is still a failure
	}
	return r, nil
}

// Output runs c and returns stdout only for a clean zero-exit run.
func Output(ctx context.Context, c Cmd) ([]byte, error) {
	r, err := Run(ctx, c)
	switch {
	case err != nil:
		return nil, err
	case r.TimedOut:
		return nil, fmt.Errorf("%s timed out after %s", filepath.Base(c.Argv[0]), c.Timeout)
	case r.StdoutExceeded:
		return nil, fmt.Errorf("%s output exceeded %d bytes", filepath.Base(c.Argv[0]), c.MaxStdout)
	case r.ExitCode != 0:
		msg := strings.TrimSpace(string(r.Stderr))
		if msg == "" {
			msg = fmt.Sprintf("exit status %d", r.ExitCode)
		}
		return nil, errors.New(msg)
	}
	return r.Stdout, nil
}
