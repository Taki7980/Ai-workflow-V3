package verify

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestHelperSleep(t *testing.T) {
	if os.Getenv("VERIFY_HELPER") != "1" {
		t.Skip("helper process only")
	}
	time.Sleep(30 * time.Second)
}

func TestCompressShortUnchanged(t *testing.T) {
	if got := Compress("a\nb", 80, 12000); got != "a\nb\n" {
		t.Fatalf("got %q", got)
	}
	if got := Compress("", 80, 12000); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestCompressLines(t *testing.T) {
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = fmt.Sprintf("l%d", i)
	}
	got := strings.Split(strings.TrimSuffix(Compress(strings.Join(lines, "\n"), 80, 12000), "\n"), "\n")
	if len(got) != 81 || got[59] != "l59" || got[60] != "... [20 LINES OMITTED] ..." || got[61] != "l80" || got[80] != "l99" {
		t.Fatalf("got %d lines: %q", len(got), got)
	}
}

func TestCompressChars(t *testing.T) {
	got := Compress(strings.Repeat("x", 20000), 80, 12000)
	want := strings.Repeat("x", 11960) + "\n... [CHARACTER CAP REACHED]\n"
	if got != want {
		t.Fatalf("len %d want %d", len(got), len(want))
	}
}

func TestSplitCommandPOSIX(t *testing.T) {
	got, err := splitCommand(`go test "./a b" 'c d' e\ f`, true)
	if err != nil || !slices.Equal(got, []string{"go", "test", "./a b", "c d", "e f"}) {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := splitCommand(`"unterminated`, true); err == nil {
		t.Fatal("unterminated quote must error")
	}
}

func TestSplitCommandWindowsKeepsBackslash(t *testing.T) {
	got, err := splitCommand(`C:\go\bin\go.exe test "C:\a b"`, false)
	if err != nil || !slices.Equal(got, []string{`C:\go\bin\go.exe`, "test", `C:\a b`}) {
		t.Fatalf("got %q %v", got, err)
	}
}

func TestRunChecksPassFail(t *testing.T) {
	r := RunChecks(context.Background(), t.TempDir(), []string{"go version", "go definitely-not-a-command"})
	if r.OK || len(r.Checks) != 2 || r.Checks[0].ReturnCode != 0 || r.Checks[1].ReturnCode == 0 {
		t.Fatalf("got %+v", r)
	}
	if !strings.Contains(r.Checks[0].Output, "go version") {
		t.Fatalf("output not captured: %q", r.Checks[0].Output)
	}
}

func TestRunChecksParseError(t *testing.T) {
	r := RunChecks(context.Background(), t.TempDir(), []string{`"bad`})
	if r.Checks[0].ReturnCode != 2 || !strings.HasPrefix(r.Checks[0].Output, "parse error:") {
		t.Fatalf("got %+v", r.Checks[0])
	}
}

func TestRunChecksTimeout(t *testing.T) {
	old := checkTimeout
	checkTimeout = 300 * time.Millisecond
	t.Cleanup(func() { checkTimeout = old })
	t.Setenv("VERIFY_HELPER", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	r := RunChecks(context.Background(), t.TempDir(), []string{fmt.Sprintf("'%s' -test.run=^TestHelperSleep$", exe)})
	if r.Checks[0].ReturnCode != 124 {
		t.Fatalf("got %+v", r.Checks[0])
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("timed-out check was not killed promptly")
	}
}

func TestVerifyRequiresChecksAndHandoff(t *testing.T) {
	root := t.TempDir()
	if r := Verify(context.Background(), root, nil, 30); r.OK {
		t.Fatal("no checks must not be ok")
	}
	r := Verify(context.Background(), root, []string{"go version"}, 30)
	if r.OK || !slices.Contains(r.HandoffErrors, "missing ai-workspace/handoff/HANDOFF.md") {
		t.Fatalf("got %+v", r)
	}
}
