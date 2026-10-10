package procx

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The test binary doubles as the helper program: PROCX_HELPER selects a role.
func TestMain(m *testing.M) {
	switch os.Getenv("PROCX_HELPER") {
	case "echo-env":
		fmt.Print(os.Getenv("PROCX_SECRET"), "|", os.Getenv("PATH") != "")
		os.Exit(0)
	case "flood":
		for {
			os.Stdout.Write([]byte(strings.Repeat("x", 4096)))
		}
	case "stderr":
		fmt.Fprint(os.Stderr, strings.Repeat("a", 100)+"TAIL")
		os.Exit(3)
	case "parent":
		// Spawn a grandchild that heartbeats, then hang.
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), "PROCX_HELPER=heartbeat")
		child.Stdout = os.Stdout
		if err := child.Start(); err != nil {
			os.Exit(9)
		}
		time.Sleep(time.Hour)
	case "sleep":
		time.Sleep(time.Hour)
	case "heartbeat":
		f := os.Getenv("PROCX_BEAT")
		for i := 0; ; i++ {
			_ = os.WriteFile(f, []byte(fmt.Sprint(i)), 0o644)
			time.Sleep(20 * time.Millisecond)
		}
	}
	os.Exit(m.Run())
}

func helper(role string, extra ...string) Cmd {
	return Cmd{Argv: []string{os.Args[0]}, Env: append(SafeEnv(), append([]string{"PROCX_HELPER=" + role}, extra...)...)}
}

func TestEnvironmentIsExplicit(t *testing.T) {
	t.Setenv("PROCX_SECRET", "leak")
	c := helper("echo-env")
	r, err := Run(context.Background(), c)
	if err != nil || !r.OK() {
		t.Fatalf("err=%v r=%+v", err, r)
	}
	if string(r.Stdout) != "|true" {
		t.Fatalf("secret leaked or PATH missing: %q", r.Stdout)
	}
	c.Env = append(c.Env, "PROCX_SECRET=allowed")
	r, _ = Run(context.Background(), c)
	if !strings.HasPrefix(string(r.Stdout), "allowed|") {
		t.Fatalf("explicit env not passed: %q", r.Stdout)
	}
}

func TestStdoutOverflowKills(t *testing.T) {
	c := helper("flood")
	c.MaxStdout = 10000
	c.Timeout = 20 * time.Second
	start := time.Now()
	r, err := Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if !r.StdoutExceeded || r.OK() || len(r.Stdout) != 10000 || r.TimedOut {
		t.Fatalf("r=%+v len=%d", r, len(r.Stdout))
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("overflow must stop the process promptly")
	}
}

func TestStderrTailAndExitCode(t *testing.T) {
	c := helper("stderr")
	c.MaxStderr = 10
	r, err := Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if r.ExitCode != 3 || string(r.Stderr) != "aaaaaaTAIL" || !r.StderrTruncated {
		t.Fatalf("r=%+v stderr=%q", r, r.Stderr)
	}
}

func TestCancelKillsWholeTree(t *testing.T) {
	beat := filepath.Join(t.TempDir(), "beat")
	c := helper("parent", "PROCX_BEAT="+beat)
	c.Timeout = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan Result, 1)
	go func() {
		r, err := Run(ctx, c)
		if err != nil {
			t.Error(err)
		}
		done <- r
	}()
	// Cancel only once the grandchild is provably running.
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if b, _ := os.ReadFile(beat); len(b) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("grandchild never started")
		}
	}
	start := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("cancel did not stop the process tree")
	}
	t.Logf("tree stopped %s after cancel", time.Since(start))
	time.Sleep(300 * time.Millisecond) // let any straggler write once more
	before, _ := os.ReadFile(beat)
	time.Sleep(500 * time.Millisecond)
	after, _ := os.ReadFile(beat)
	if string(before) != string(after) {
		t.Fatalf("grandchild still running after tree kill: %s -> %s", before, after)
	}
}

func TestTimeoutReported(t *testing.T) {
	c := helper("sleep")
	c.Timeout = 500 * time.Millisecond
	r, err := Run(context.Background(), c)
	if err != nil || !r.TimedOut || r.OK() {
		t.Fatalf("r=%+v err=%v", r, err)
	}
}

func TestEmptyCommandRejected(t *testing.T) {
	if _, err := Run(context.Background(), Cmd{}); err == nil {
		t.Fatal("expected error")
	}
	if _, err := Run(context.Background(), Cmd{Argv: []string{"definitely-not-a-real-binary-xyz"}}); err == nil {
		t.Fatal("missing binary must be a launch error")
	}
}
