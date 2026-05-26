package updater

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// applyUpdateWindows performs the binary replacement on Windows.
// Windows allows renaming a running executable, so the strategy is:
//  1. Rename current exe → exe.old
//  2. Copy new binary to exe path
//  3. Remove the downloaded binary (not the version file — ApplyUpdate's
//     version guard will clean stale files on the next startup)
//  4. Write .updated-marker
//
// If removing .old fails (because the process still holds a handle), the file
// is hidden instead.
func (s *Service) applyUpdateWindows(exe, newBinary, tmpDir string) error {
	oldBinary := exe + ".old"
	os.Remove(oldBinary)

	if err := os.Rename(exe, oldBinary); err != nil {
		return err
	}

	if err := copyFile(newBinary, exe); err != nil {
		os.Rename(oldBinary, exe)
		return err
	}

	// Remove the binary file; the version marker stays for the guard check
	// on next startup. Don't RemoveAll — that may fail on Windows when files
	// are transiently locked.
	os.Remove(newBinary)

	// Try to remove .old; if it fails (in-use), hide it
	if err := os.Remove(oldBinary); err != nil {
		_ = hideFile(oldBinary)
	}

	// Write updated marker
	markerPath := filepath.Join(s.dataDir, ".updated-marker")
	os.WriteFile(markerPath, []byte(s.pending), 0644)

	return nil
}

// hideFile sets the FILE_ATTRIBUTE_HIDDEN flag on the given file using
// SetFileAttributesW from the Windows API.
func hideFile(path string) error {
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	// FILE_ATTRIBUTE_HIDDEN = 0x2
	return windows.SetFileAttributes(ptr, windows.FILE_ATTRIBUTE_HIDDEN)
}
