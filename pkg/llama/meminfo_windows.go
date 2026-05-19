//go:build windows

package llama

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modpsapi                = windows.NewLazySystemDLL("psapi.dll")
	procGetProcessMemoryInfo = modpsapi.NewProc("GetProcessMemoryInfo")
)

type _PROCESS_MEMORY_COUNTERS struct {
	CB                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uint64
	WorkingSetSize             uint64
	QuotaPeakPagedPoolUsage    uint64
	QuotaPagedPoolUsage        uint64
	QuotaPeakNonPagedPoolUsage uint64
	QuotaNonPagedPoolUsage     uint64
	PagefileUsage              uint64
	PeakPagefileUsage          uint64
}

func init() {
	getProcessMemoryMB = getProcessMemoryMBWindows
}

func getProcessMemoryMBWindows(pid int) (uint64, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ, false, uint32(pid))
	if err != nil {
		return 0, fmt.Errorf("OpenProcess(%d): %w", pid, err)
	}
	defer windows.CloseHandle(handle)

	var pmc _PROCESS_MEMORY_COUNTERS
	pmc.CB = uint32(unsafe.Sizeof(pmc))
	ret, _, err := procGetProcessMemoryInfo.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&pmc)),
		uintptr(pmc.CB),
	)
	if ret == 0 {
		return 0, fmt.Errorf("GetProcessMemoryInfo(%d): %w", pid, err)
	}
	return pmc.WorkingSetSize / (1024 * 1024), nil
}
