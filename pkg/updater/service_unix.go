//go:build !windows

package updater

// applyUpdateWindows is a no-op on non-Windows platforms.
func (s *Service) applyUpdateWindows(exe, newBinary, tmpDir string) error {
	return nil
}
