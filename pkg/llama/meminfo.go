package llama

import "fmt"

// getProcessMemoryMB is set by platform-specific init() functions.
var getProcessMemoryMB func(pid int) (uint64, error)

// MemoryUsageMB returns the current working set (RSS) of the llama-server
// process in megabytes.
func (m *Manager) MemoryUsageMB() (uint64, error) {
	var pid int
	if m.StartedProcess() {
		pid = m.cmd.Process.Pid
	} else {
		pid = findProcessByPort(m.Port)
	}
	if pid == 0 {
		return 0, fmt.Errorf("no llama-server process found")
	}
	if getProcessMemoryMB == nil {
		return 0, fmt.Errorf("memory monitoring not implemented on this platform")
	}
	return getProcessMemoryMB(pid)
}
