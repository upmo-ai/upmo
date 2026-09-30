package domain

import (
	"errors"
	"testing"
)

func TestAdmit(t *testing.T) {
	cases := []struct {
		name     string
		enabled  bool
		canaries []Canary
		want     bool
		wantErr  error
	}{
		{
			name:     "disabled",
			enabled:  false,
			canaries: []Canary{{Name: "a", Baseline: 100, Lean: 10}},
			want:     false,
		},
		{
			name:     "disabled ignores malformed canaries",
			enabled:  false,
			canaries: []Canary{{Name: "", Baseline: 0, Lean: -1}},
			want:     false,
		},
		{
			name:     "nil list",
			enabled:  true,
			canaries: nil,
			want:     false,
		},
		{
			name:     "empty list",
			enabled:  true,
			canaries: []Canary{},
			want:     false,
		},
		{
			name:     "blank name",
			enabled:  true,
			canaries: []Canary{{Name: "   ", Baseline: 100, Lean: 50}},
			wantErr:  ErrLean,
		},
		{
			name:    "duplicate names after trim",
			enabled: true,
			canaries: []Canary{
				{Name: "plan", Baseline: 100, Lean: 50},
				{Name: " plan ", Baseline: 100, Lean: 50},
			},
			wantErr: ErrLean,
		},
		{
			name:     "baseline zero",
			enabled:  true,
			canaries: []Canary{{Name: "a", Baseline: 0, Lean: 0}},
			wantErr:  ErrLean,
		},
		{
			name:     "baseline negative",
			enabled:  true,
			canaries: []Canary{{Name: "a", Baseline: -5, Lean: 0}},
			wantErr:  ErrLean,
		},
		{
			name:     "lean negative",
			enabled:  true,
			canaries: []Canary{{Name: "a", Baseline: 100, Lean: -1}},
			wantErr:  ErrLean,
		},
		{
			name:     "lean above baseline",
			enabled:  true,
			canaries: []Canary{{Name: "a", Baseline: 100, Lean: 101}},
			wantErr:  ErrLean,
		},
		{
			name:     "saving exactly at floor",
			enabled:  true,
			canaries: []Canary{{Name: "a", Baseline: 100, Lean: 85}},
			want:     true,
		},
		{
			name:     "saving just below floor",
			enabled:  true,
			canaries: []Canary{{Name: "a", Baseline: 100, Lean: 86}},
			want:     false,
		},
		{
			name:    "one canary below floor rejects the set",
			enabled: true,
			canaries: []Canary{
				{Name: "a", Baseline: 100, Lean: 50},
				{Name: "b", Baseline: 100, Lean: 90},
			},
			want: false,
		},
		{
			name:    "malformed canary after a weak one still errors",
			enabled: true,
			canaries: []Canary{
				{Name: "a", Baseline: 100, Lean: 99},
				{Name: "b", Baseline: 0, Lean: 0},
			},
			wantErr: ErrLean,
		},
		{
			name:    "every canary at or above floor",
			enabled: true,
			canaries: []Canary{
				{Name: "a", Baseline: 100, Lean: 85},
				{Name: "b", Baseline: 200, Lean: 20},
			},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Admit(tc.enabled, tc.canaries)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err: got %v want %v", err, tc.wantErr)
				}
				if got {
					t.Fatalf("got true alongside an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}
