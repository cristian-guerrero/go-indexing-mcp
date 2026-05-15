// Package simd provides a SIMD-accelerated dot product for float64 vectors.
// On amd64 with AVX2+FMA, it uses an assembly implementation (~18x faster for 768-dim vectors).
// On arm64 and other platforms, it falls back to a scalar loop.
package simd

// Dot computes the dot product of two float64 vectors.
// Returns 0 if the vectors have different lengths.
func Dot(a, b []float64) float64 {
	if len(a) != len(b) {
		return 0
	}
	return dot(a, b)
}
