//go:build !windows

package procx

import (
	"os/exec"
	"syscall"
)

func envKey(k string) string { return k }

// setTreeAttrs starts the child as the leader of a new process group so the
// whole tree can be signalled together.
func setTreeAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killTree SIGKILLs the child's process group, falling back to the child.
func killTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err == nil {
		return nil
	}
	return cmd.Process.Kill()
}
