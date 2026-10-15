//go:build !windows

package provider

import (
	"flag"
	"fmt"
	"io"
	"os/exec"
	"syscall"
)

// ExecWithLimits implements the hidden `__provider-exec` command: it applies
// rlimits to itself and then replaces itself with the provider (no shell).
func ExecWithLimits(args []string, errOut io.Writer) int {
	fs := flag.NewFlagSet(ExecWrapperCommand, flag.ContinueOnError)
	fs.SetOutput(errOut)
	cpu := fs.Uint64("cpu-seconds", 0, "")
	mem := fs.Uint64("memory-mb", 0, "")
	fsize := fs.Uint64("file-size-mb", 0, "")
	files := fs.Uint64("open-files", 0, "")
	if err := fs.Parse(args); err != nil || fs.NArg() == 0 {
		fmt.Fprintln(errOut, "usage: __provider-exec [limits] -- command [args...]")
		return 2
	}
	for _, l := range []struct {
		res int
		v   uint64
	}{{syscall.RLIMIT_CPU, *cpu}, {syscall.RLIMIT_AS, *mem << 20}, {syscall.RLIMIT_FSIZE, *fsize << 20}, {syscall.RLIMIT_NOFILE, *files}} {
		if l.v == 0 {
			continue
		}
		if err := syscall.Setrlimit(l.res, &syscall.Rlimit{Cur: l.v, Max: l.v}); err != nil {
			fmt.Fprintln(errOut, "setrlimit:", err)
			return 126
		}
	}
	argv := fs.Args()
	path, err := exec.LookPath(argv[0])
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 127
	}
	err = syscall.Exec(path, argv, syscall.Environ())
	fmt.Fprintln(errOut, "exec:", err)
	return 126
}
