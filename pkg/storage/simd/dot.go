package simd

func Dot(a, b []float64) float64 {
	if len(a) != len(b) {
		return 0
	}
	return dot(a, b)
}
