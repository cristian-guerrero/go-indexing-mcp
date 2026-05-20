//go:build !windows && !linux

package config

// hasGPU returns false on platforms that lack a GPU detection implementation.
func hasGPU() bool {
	return false
}
