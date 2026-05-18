// Package simd provides a SIMD-accelerated dot product for float32 vectors.
// On amd64 with AVX2+FMA, it uses an assembly implementation (~18x faster for 768-dim vectors).
// On arm64 and other platforms, it falls back to a scalar loop.
package simd

// Dot32 computes the dot product of two float32 vectors.
// Returns 0 if the vectors have different lengths.
func Dot32(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}
	return dot(a, b)
}

// Dot computes the dot product of two float64 vectors (converts to float32 internally).
// Kept for backward compatibility. Prefer Dot32 for new code.
func Dot(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	a32 := make([]float32, len(a))
	b32 := make([]float32, len(b))
	for i := range a {
		a32[i] = float32(a[i])
		b32[i] = float32(b[i])
	}
	return float64(Dot32(a32, b32))
}
