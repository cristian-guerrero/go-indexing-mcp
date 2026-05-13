//go:build !windows && !linux

package llama

import "os/exec"

func setChildDeath(cmd *exec.Cmd) {}

func (m *Manager) setupJob()                   {}
func (m *Manager) cleanupJob()                 {}
func (m *Manager) assignChildToJob(*exec.Cmd) error { return nil }
