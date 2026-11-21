package postgres

import (
	"testing"
	"time"
)

func TestUUIDv7LowerBound(t *testing.T) {
	pinned := time.Date(2026, time.November, 20, 12, 0, 0, 500_987_654, time.UTC)
	if got, want := uuidv7LowerBound(pinned).String(), "01a1f8d3-ebf4-7000-0000-000000000000"; got != want {
		t.Fatalf("uuidv7LowerBound(%v) = %s, want %s", pinned, got, want)
	}
	if got, want := uuidv7LowerBound(time.Unix(0, 0).UTC()).String(), "00000000-0000-7000-0000-000000000000"; got != want {
		t.Fatalf("uuidv7LowerBound(epoch) = %s, want %s", got, want)
	}
	if got, want := uuidv7LowerBound(time.Unix(-5, 0).UTC()).String(), "00000000-0000-7000-0000-000000000000"; got != want {
		t.Fatalf("uuidv7LowerBound(before epoch) = %s, want %s", got, want)
	}
}
