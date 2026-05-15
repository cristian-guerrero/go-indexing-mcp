//go:build !windows && !linux

package llama

import "os/exec"

// setChildDeath is a no-op on non-Windows/non-Linux platforms (macOS, BSD).
func setChildDeath(cmd *exec.Cmd) {}

// setupJob is a no-op on Unix; process death is handled via Pdeathsig on Linux.
func (m *Manager) setupJob()                   {}

// cleanupJob is a no-op on Unix.
func (m *Manager) cleanupJob()                 {}

// assignChildToJob is a no-op on Unix, returns nil.
func (m *Manager) assignChildToJob(*exec.Cmd) error { return nil }
