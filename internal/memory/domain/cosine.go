package domain

import (
	"fmt"
	"math"
)

const Dim = 1024
const NearDup = 0.95

type Hit struct {
	ID     string
	Cosine float64
	Trust  string // user, admin, tool, web, document
}

func Cosine(a, b []float32) (float64, error) {
	if len(a) != Dim || len(b) != Dim {
		return 0, fmt.Errorf("cosine: both vectors must have dimension %d", Dim)
	}

	var dot, normA, normB float64
	for i := range a {
		av := float64(a[i])
		bv := float64(b[i])
		if math.IsNaN(av) || math.IsInf(av, 0) || math.IsNaN(bv) || math.IsInf(bv, 0) {
			return 0, fmt.Errorf("cosine: vectors must contain only finite values")
		}
		dot += av * bv
		normA += av * av
		normB += bv * bv
	}

	if normA == 0 || normB == 0 {
		return 0, fmt.Errorf("cosine: vectors must have non-zero L2 norms")
	}

	return dot / (math.Sqrt(normA) * math.Sqrt(normB)), nil
}

func DecideNearDup(forgotten, active []Hit) (action, reason string) {
	for _, hit := range forgotten {
		if hit.Cosine >= NearDup {
			return "drop", "forgotten_near"
		}
	}
	for _, hit := range active {
		if hit.Cosine >= NearDup {
			return "reinforce", "active_near"
		}
	}
	return "insert", "new"
}
