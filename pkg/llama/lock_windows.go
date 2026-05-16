package llama

import (
	"golang.org/x/sys/windows"
)

// isProcessAlive checks whether a Windows process with the given PID exists.
// Uses OpenProcess with PROCESS_QUERY_LIMITED_INFORMATION (Vista+).
func isProcessAlive(pid int) bool {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	windows.CloseHandle(handle)
	return true
}
