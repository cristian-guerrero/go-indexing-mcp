//go:build amd64

package simd

import "golang.org/x/sys/cpu"

var useAVX2 = cpu.X86.HasAVX2 && cpu.X86.HasFMA

func dotAVX2(a, b []float64) float64

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
