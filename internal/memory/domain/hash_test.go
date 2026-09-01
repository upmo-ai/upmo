package domain

import "testing"

func TestClaimHashNormalization(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
	}{
		{
			name: "nfkc and lowercase",
			a:    "ＦＵＬＬＷＩＤＴＨ preference",
			b:    "fullwidth preference",
		},
		{
			name: "collapse whitespace",
			a:    "weekly\t\n  summary",
			b:    "weekly summary",
		},
		{
			name: "terminal punctuation",
			a:    "compact notifications.!?",
			b:    "compact notifications",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, want := ClaimHash(tt.a), ClaimHash(tt.b); got != want {
				t.Errorf("ClaimHash(%q) differs from ClaimHash(%q)", tt.a, tt.b)
			}
		})
	}
}

func TestClaimHashPreservesOtherPunctuation(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
	}{
		{
			name: "internal punctuation",
			a:    "weekly-summary",
			b:    "weekly summary",
		},
		{
			name: "terminal comma",
			a:    "weekly summary,",
			b:    "weekly summary",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if ClaimHash(tt.a) == ClaimHash(tt.b) {
				t.Errorf("ClaimHash(%q) equals ClaimHash(%q)", tt.a, tt.b)
			}
		})
	}
}
