package domain

import (
	"fmt"
	"math"
	"strings"
)

// EmbedDim is the only vector length the embed route returns. Facts store
// halfvec(1024); any other length is refused.
const EmbedDim = 1024

// MaxEmbedInputs is the most texts one embed call accepts.
const MaxEmbedInputs = 64

// CheckEmbedInputs rejects an empty batch, a blank text, or a batch over the cap.
func CheckEmbedInputs(inputs []string) error {
	if len(inputs) == 0 {
		return fmt.Errorf("gateway: embed input is empty")
	}
	if len(inputs) > MaxEmbedInputs {
		return fmt.Errorf("gateway: embed input exceeds %d", MaxEmbedInputs)
	}
	for _, text := range inputs {
		if strings.TrimSpace(text) == "" {
			return fmt.Errorf("gateway: embed input is empty")
		}
	}
	return nil
}

// CheckEmbeddings requires one finite, non-zero vector of EmbedDim per input.
func CheckEmbeddings(n int, vectors [][]float32) error {
	if len(vectors) != n {
		return fmt.Errorf("gateway: embed count %d, want %d", len(vectors), n)
	}
	for _, v := range vectors {
		if len(v) != EmbedDim {
			return fmt.Errorf("gateway: embed dimension %d, want %d", len(v), EmbedDim)
		}
		var norm float64
		for _, x := range v {
			f := float64(x)
			if math.IsNaN(f) || math.IsInf(f, 0) {
				return fmt.Errorf("gateway: embed values must be finite")
			}
			norm += f * f
		}
		if norm == 0 {
			return fmt.Errorf("gateway: embed vector has zero norm")
		}
	}
	return nil
}
