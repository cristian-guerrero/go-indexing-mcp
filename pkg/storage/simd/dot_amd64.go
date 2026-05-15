//go:build amd64

package simd

import "golang.org/x/sys/cpu"

// useAVX2 checks at runtime whether the CPU supports AVX2 and FMA instructions.
// Only then does it dispatch to the assembly implementation.
var useAVX2 = cpu.X86.HasAVX2 && cpu.X86.HasFMA

// dotAVX2 is implemented in dot_amd64.s using AVX2 + FMA with 4x unrolling.
//go:noescape
func dotAVX2(a, b []float64) float64

// dot dispatches to the AVX2 assembly if the CPU supports it and vectors are ≥8 elements,
// otherwise falls back to the scalar loop.
func dot(a, b []float64) float64 {
	if useAVX2 && len(a) >= 8 {
		return dotAVX2(a, b)
	}
	var sum float64
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
}
