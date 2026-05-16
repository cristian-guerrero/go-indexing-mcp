//go:build !windows

package llama

import (
	"os"
	"syscall"
)

// isProcessAlive checks whether a Unix process with the given PID exists.
// Sends signal 0 which tests whether the process exists without actually signaling it.
func isProcessAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
