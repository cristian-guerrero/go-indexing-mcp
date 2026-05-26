//go:build !windows

package updater

import (
	"os"
	"path/filepath"
)

// applyUpdateWindows is a no-op on non-Windows platforms.
func (s *Service) applyUpdateWindows(exe, newBinary, tmpDir string) error {
	return nil
}
