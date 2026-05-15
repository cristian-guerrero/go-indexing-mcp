//go:build linux

package llama

import (
	"os/exec"
	"syscall"
)

// setChildDeath configures Pdeathsig=SIGTERM on Linux, so the child (llama-server)
// is automatically terminated when the parent process exits.
func setChildDeath(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Pdeathsig: syscall.SIGTERM,
	}
}

// setupJob is a no-op on Linux; child death uses Pdeathsig instead.
func (m *Manager) setupJob()                   {}

// cleanupJob is a no-op on Linux.
func (m *Manager) cleanupJob()                 {}

// assignChildToJob is a no-op on Linux, returns nil.
func (m *Manager) assignChildToJob(*exec.Cmd) error { return nil }
