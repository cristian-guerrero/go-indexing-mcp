package config

import "github.com/yusufpapurcu/wmi"

type win32VideoController struct {
	Name string
}

// hasGPU returns true when at least one video controller is found via WMI.
// Covers NVIDIA, AMD, Intel, and any other GPU that exposes a WMI device node.
func hasGPU() bool {
	var gpus []win32VideoController
	err := wmi.Query("SELECT Name FROM Win32_VideoController", &gpus)
	if err != nil {
		return false
	}
	return len(gpus) > 0
}
