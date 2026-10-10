//go:build windows

package procx

import (
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Windows environment variable names are case-insensitive.
func envKey(k string) string { return strings.ToUpper(k) }

const createNewProcessGroup = 0x00000200

func setTreeAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

// killTree terminates the child and all descendants with taskkill /T /F
// (resolved from SystemRoot, never PATH), falling back to killing the child.
func killTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	root := systemRoot()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tk := exec.CommandContext(ctx, filepath.Join(root, "System32", "taskkill.exe"), "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
	tk.Env = []string{"SystemRoot=" + root}
	if err := tk.Run(); err == nil {
		return nil
	}
	return cmd.Process.Kill()
}

func systemRoot() string {
	for _, kv := range syscall.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.EqualFold(k, "SystemRoot") && v != "" {
			return v
		}
	}
	return `C:\Windows`
}
