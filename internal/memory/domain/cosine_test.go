package domain

import (
	"math"
	"testing"
)

func TestCosine(t *testing.T) {
	valid := testVector(3, 4)

	tests := []struct {
		name    string
		a       []float32
		b       []float32
		want    float64
		wantErr bool
	}{
		{name: "identical", a: valid, b: valid, want: 1},
		{name: "zero left", a: make([]float32, Dim), b: valid, wantErr: true},
		{name: "zero right", a: valid, b: make([]float32, Dim), wantErr: true},
		{name: "wrong left dimension", a: make([]float32, Dim-1), b: valid, wantErr: true},
		{name: "wrong right dimension", a: valid, b: make([]float32, Dim+1), wantErr: true},
		{name: "NaN left", a: testVector(float32(math.NaN())), b: valid, wantErr: true},
		{name: "NaN right", a: valid, b: testVector(float32(math.NaN())), wantErr: true},
		{name: "infinity left", a: testVector(float32(math.Inf(1))), b: valid, wantErr: true},
		{name: "infinity right", a: valid, b: testVector(float32(math.Inf(-1))), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Cosine(tt.a, tt.b)
			if tt.wantErr {
				if err == nil {
					t.Fatal("Cosine() error = nil, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Cosine() error = %v", err)
			}
			if math.Abs(got-tt.want) > 1e-12 {
				t.Fatalf("Cosine() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValid(t *testing.T) {
	if err := Valid(testVector(1)); err != nil {
		t.Fatal(err)
	}
	if err := Valid(make([]float32, Dim)); err == nil {
		t.Fatal("zero vector")
	}
	if err := Valid([]float32{1}); err == nil {
		t.Fatal("short vector")
	}
}

func TestDecideNearDup(t *testing.T) {
	tests := []struct {
		name       string
		forgotten  []Hit
		active     []Hit
		wantAction string
		wantReason string
	}{
		{
			name:       "forgotten threshold precedes closer active",
			forgotten:  []Hit{{Cosine: 0.95}},
			active:     []Hit{{Cosine: 0.99}},
			wantAction: "drop",
			wantReason: "forgotten_near",
		},
		{
			name:       "active threshold",
			active:     []Hit{{Cosine: 0.95}},
			wantAction: "reinforce",
			wantReason: "active_near",
		},
		{
			name:       "below threshold",
			forgotten:  []Hit{{Cosine: 0.949}},
			active:     []Hit{{Cosine: 0.949}},
			wantAction: "insert",
			wantReason: "new",
		},
		{
			name:       "empty lists",
			wantAction: "insert",
			wantReason: "new",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action, reason := DecideNearDup(tt.forgotten, tt.active)
			if action != tt.wantAction || reason != tt.wantReason {
				t.Fatalf(
					"DecideNearDup() = (%q, %q), want (%q, %q)",
					action,
					reason,
					tt.wantAction,
					tt.wantReason,
				)
			}
		})
	}
}

func testVector(values ...float32) []float32 {
	vector := make([]float32, Dim)
	copy(vector, values)
	return vector
}
