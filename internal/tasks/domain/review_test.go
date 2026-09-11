package domain

import (
	"strings"
	"testing"
)

func TestReview(t *testing.T) {
	got, err := Review("  +added  ", "  match exactly  ")
	if err != nil {
		t.Fatalf("Review() error = %v", err)
	}

	want := "criteria:\nmatch exactly\n\ndiff:\n+added\n"
	if got != want {
		t.Errorf("Review() = %q, want %q", got, want)
	}
}

func TestReviewOmitsUnpassedTranscript(t *testing.T) {
	got, err := Review("+added", "match exactly")
	if err != nil {
		t.Fatalf("Review() error = %v", err)
	}
	if strings.Contains(got, "transcript") {
		t.Errorf("Review() included text that was not passed in: %q", got)
	}
}

func TestReviewRejectsInvalidInput(t *testing.T) {
	long := strings.Repeat("é", 8001)
	tests := []struct {
		name     string
		diff     string
		criteria string
	}{
		{name: "empty diff", criteria: "required"},
		{name: "empty criteria", diff: "+added"},
		{name: "long diff", diff: long, criteria: "required"},
		{name: "long criteria", diff: "+added", criteria: long},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Review(test.diff, test.criteria); err != ErrReview {
				t.Errorf("Review() error = %v, want %v", err, ErrReview)
			}
		})
	}
}

func TestReviewAcceptsRuneLimit(t *testing.T) {
	atLimit := strings.Repeat("é", 8000)
	if _, err := Review(atLimit, atLimit); err != nil {
		t.Errorf("Review() error = %v", err)
	}
}
