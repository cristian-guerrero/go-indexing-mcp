//go:build !amd64 && !arm64

package simd

// dot is the generic scalar fallback for platforms without SIMD dot product.
func dot(a, b []float64) float64 {
	var sum float64
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
}
