//go:build !windows

package filelock

import (
	"errors"
	"syscall"
)

// pidAlive probes a local PID with signal 0 (EPERM still means alive).
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
