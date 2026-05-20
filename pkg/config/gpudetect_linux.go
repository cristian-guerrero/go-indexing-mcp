package config

import (
	"os"
	"strings"
)

// hasGPU returns true when at least one DRM card device is found in sysfs.
// Reads /sys/class/drm and looks for entries starting with "card" that are
// not display output interfaces (cardN-DP-*, cardN-HDMI-*).
// Covers NVIDIA, AMD, Intel integrated and dedicated GPUs.
func hasGPU() bool {
	entries, err := os.ReadDir("/sys/class/drm")
	if err != nil {
		return false
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "card") && !strings.Contains(name, "-") {
			return true
		}
	}
	return false
}
