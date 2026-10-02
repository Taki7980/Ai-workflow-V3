package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// helperSpec runs this test binary as a fake provider in the given mode.
// Mode is passed as an argument because the runner strips the environment.
func helperSpec(mode string) Spec {
	return Spec{
		Name:    "helper",
		Command: []string{os.Args[0], "-test.run=^TestHelperProvider$", "--", mode},
		Timeout: 5 * time.Second,
	}
}

// TestHelperProvider is not a real test: it is the fake provider process.
func TestHelperProvider(t *testing.T) {
	mode := ""
	for i, a := range os.Args {
		if a == "--" && i+1 < len(os.Args) {
			mode = os.Args[i+1]
		}
	}
	if mode == "" {
		return
	}
	switch mode {
	case "items":
		fmt.Print(`{"items":[{"text":"hello","score":1.5,"path":"a.go","line":3}]}`)
	case "array":
		fmt.Print(`[{"text":"one"},{"text":"two"}]`)
	case "env":
		fmt.Printf(`[{"text":%q}]`, os.Getenv("AIWF_TEST_SECRET"))
	case "flood":
		fmt.Print(`[{"text":"` + strings.Repeat("x", 4096) + `"}]`)
	case "fail":
		fmt.Fprint(os.Stderr, "boom")
		os.Exit(3)
	case "garbage":
		fmt.Print("not json")
	case "sleep":
		time.Sleep(30 * time.Second)
	case "orphan":
		// Start a grandchild that inherits stdout and outlives us.
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProvider$", "--", "sleep")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			os.Exit(9)
		}
		fmt.Print(`[]`)
	}
	os.Exit(0)
}

func TestRunDecodesEnvelopeAndArray(t *testing.T) {
	res, err := Run(context.Background(), helperSpec("items"), Request{Query: "q", Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 || res.Items[0].Path != "a.go" || res.Items[0].Line != 3 {
		t.Fatalf("items=%+v", res.Items)
	}
	res, err = Run(context.Background(), helperSpec("array"), Request{Root: t.TempDir()})
	if err != nil || len(res.Items) != 2 {
		t.Fatalf("items=%+v err=%v", res.Items, err)
	}
}

func TestRunStripsEnvironmentUnlessAllowlisted(t *testing.T) {
	t.Setenv("AIWF_TEST_SECRET", "s3cret")
	res, err := Run(context.Background(), helperSpec("env"), Request{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Items[0].Text != "" {
		t.Fatal("non-allowlisted variable leaked to provider")
	}
	spec := helperSpec("env")
	spec.EnvAllowlist = []string{"AIWF_TEST_SECRET"}
	res, err = Run(context.Background(), spec, Request{Root: t.TempDir()})
	if err != nil || res.Items[0].Text != "s3cret" {
		t.Fatalf("allowlisted variable missing: %+v err=%v", res.Items, err)
	}
}

func TestRunReportsOutputOverflow(t *testing.T) {
	spec := helperSpec("flood")
	spec.MaxOutputBytes = 128
	_, err := Run(context.Background(), spec, Request{Root: t.TempDir()})
	if !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("err=%v want ErrOutputLimit", err)
	}
}

func TestRunFailures(t *testing.T) {
	_, err := Run(context.Background(), helperSpec("fail"), Request{Root: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("non-zero exit: err=%v", err)
	}
	if _, err := Run(context.Background(), helperSpec("garbage"), Request{Root: t.TempDir()}); err == nil {
		t.Fatal("expected decode error")
	}
	if _, err := Run(context.Background(), Spec{Name: "x"}, Request{}); err == nil {
		t.Fatal("expected validation error for blank command")
	}
}

func TestRunTimesOut(t *testing.T) {
	spec := helperSpec("sleep")
	spec.Timeout = 300 * time.Millisecond
	start := time.Now()
	_, err := Run(context.Background(), spec, Request{Root: t.TempDir()})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("timeout not enforced: %v", elapsed)
	}
}

// TestRunDoesNotWaitForOrphanedGrandchild guards the WaitDelay fix: without
// it, a grandchild holding stdout keeps Run blocked for its full lifetime.
func TestRunDoesNotWaitForOrphanedGrandchild(t *testing.T) {
	spec := helperSpec("orphan")
	start := time.Now()
	_, _ = Run(context.Background(), spec, Request{Root: t.TempDir()})
	if elapsed := time.Since(start); elapsed > DefaultWaitDelay+5*time.Second {
		t.Fatalf("Run blocked on orphaned grandchild for %v", elapsed)
	}
}
