//go:build linux

package llama

import (
	"os/exec"
	"syscall"
)

func setChildDeath(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Pdeathsig: syscall.SIGTERM,
	}
}

func (m *Manager) setupJob()                   {}
func (m *Manager) cleanupJob()                 {}
func (m *Manager) assignChildToJob(*exec.Cmd) error { return nil }
