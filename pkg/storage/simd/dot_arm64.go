//go:build arm64

package simd

// dot is the scalar fallback for ARM64 (no NEON-based implementation yet).
func dot(a, b []float32) float32 {
	var sum float32
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
}
