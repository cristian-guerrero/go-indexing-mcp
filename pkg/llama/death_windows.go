//go:build windows

package llama

import "os/exec"

// setChildDeath is a no-op on Windows.
func setChildDeath(cmd *exec.Cmd) {}

// setupJob is a no-op on Windows — Job Objects caused crashes on repeated ForceRestart.
// The lock file mechanism handles process lifecycle (re-attach on restart).
func (m *Manager) setupJob() {}

// assignChildToJob is a no-op on Windows.
func (m *Manager) assignChildToJob(cmd *exec.Cmd) error { return nil }

// cleanupJob is a no-op on Windows.
func (m *Manager) cleanupJob() {}
