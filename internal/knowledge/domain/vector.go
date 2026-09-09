package domain

import (
	"fmt"
	"math"
)

const Dim = 1024

// VectorOK reports a vector that cannot be stored.
func VectorOK(v []float32) error {
	if len(v) != Dim {
		return fmt.Errorf("knowledge: vector must have dimension %d", Dim)
	}
	var norm float64
	for _, x := range v {
		f := float64(x)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return fmt.Errorf("knowledge: vector must be finite")
		}
		norm += f * f
	}
	if norm == 0 {
		return fmt.Errorf("knowledge: vector must have a non-zero norm")
	}
	return nil
}

// Cosine is the similarity of two stored vectors.
func Cosine(a, b []float32) (float64, error) {
	if err := VectorOK(a); err != nil {
		return 0, err
	}
	if err := VectorOK(b); err != nil {
		return 0, err
	}
	var dot, na, nb float64
	for i := range a {
		av := float64(a[i])
		bv := float64(b[i])
		dot += av * bv
		na += av * av
		nb += bv * bv
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb)), nil
}
