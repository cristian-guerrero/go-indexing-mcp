//go:build !windows

package llama

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func init() {
	getProcessMemoryMB = getProcessMemoryMBUnix
}

func getProcessMemoryMBUnix(pid int) (uint64, error) {
	// Linux: read /proc/<pid>/status
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "VmRSS:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					val, _ := strconv.ParseUint(fields[1], 10, 64)
					if val > 0 {
						return val / 1024, nil
					}
				}
			}
		}
	}

	// Fallback: ps command (macOS, BSD)
	cmd := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(pid))
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("ps %d: %w", pid, err)
	}
	val, _ := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	if val == 0 {
		return 0, fmt.Errorf("empty RSS for pid %d", pid)
	}
	return val / 1024, nil
}
