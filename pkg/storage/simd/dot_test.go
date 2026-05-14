package simd

import (
	"math"
	"math/rand"
	"testing"
)

func TestDotProduct(t *testing.T) {
	tests := []struct {
		name string
		a    []float64
		b    []float64
		want float64
	}{
		{"empty", nil, nil, 0},
		{"identical unit", []float64{1, 0, 0}, []float64{1, 0, 0}, 1.0},
		{"orthogonal", []float64{1, 0}, []float64{0, 1}, 0.0},
		{"opposite", []float64{1, 0}, []float64{-1, 0}, -1.0},
		{"diff length", []float64{1, 0}, []float64{1, 0, 0}, 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Dot(tt.a, tt.b)
			if math.Abs(got-tt.want) > 1e-10 {
				t.Errorf("Dot(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestDotProductVersusScalar(t *testing.T) {
	sizes := []int{1, 2, 3, 4, 5, 8, 15, 16, 17, 32, 63, 64, 127, 128, 768, 1024}
	for _, n := range sizes {
		a := make([]float64, n)
		b := make([]float64, n)
		rng := rand.New(rand.NewSource(int64(n)))
		var want float64
		for i := range a {
			a[i] = rng.Float64()*2 - 1
			b[i] = rng.Float64()*2 - 1
			want += a[i] * b[i]
		}
		got := Dot(a, b)
		if math.Abs(got-want) > 1e-10 {
			t.Errorf("n=%d: Dot = %v, want %v (diff %v)", n, got, want, math.Abs(got-want))
		}
	}
}

func BenchmarkDotProductScalar(b *testing.B) {
	rng := rand.New(rand.NewSource(44))
	n := 768
	av := make([]float64, n)
	bv := make([]float64, n)
	for i := range av {
		av[i] = rng.Float64()*2 - 1
		bv[i] = rng.Float64()*2 - 1
	}

	b.ResetTimer()
	for b.Loop() {
		var sum float64
		for i := range av {
			sum += av[i] * bv[i]
		}
		_ = sum
	}
}

func BenchmarkDotProductSIMD(b *testing.B) {
	rng := rand.New(rand.NewSource(44))
	n := 768
	av := make([]float64, n)
	bv := make([]float64, n)
	for i := range av {
		av[i] = rng.Float64()*2 - 1
		bv[i] = rng.Float64()*2 - 1
	}

	b.ResetTimer()
	for b.Loop() {
		Dot(av, bv)
	}
}
