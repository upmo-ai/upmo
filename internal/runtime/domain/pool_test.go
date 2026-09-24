package domain

import (
	"encoding/hex"
	"errors"
	"slices"
	"testing"
)

func TestOpenPoolIsEmptyWithOneRefill(t *testing.T) {
	got := OpenPool()
	if len(got.Idle) != 0 {
		t.Fatalf("OpenPool().Idle = %v, want empty", got.Idle)
	}
	if got.Refill != 1 {
		t.Fatalf("OpenPool().Refill = %d, want 1", got.Refill)
	}
}

func TestSupplyFillsToCap(t *testing.T) {
	pool, err := Supply(OpenPool(), "base-1")
	if err != nil {
		t.Fatalf("Supply() error = %v", err)
	}
	if !slices.Equal(pool.Idle, []string{"base-1"}) || pool.Refill != 0 {
		t.Fatalf("Supply() = %+v, want [base-1] with Refill 0", pool)
	}

	pool, err = Supply(pool, "base-2")
	if err != nil {
		t.Fatalf("Supply() error = %v", err)
	}
	want := []string{"base-1", "base-2"}
	if !slices.Equal(pool.Idle, want) {
		t.Fatalf("Supply().Idle = %v, want %v", pool.Idle, want)
	}

	got, err := Supply(pool, "base-3")
	if !errors.Is(err, ErrPool) {
		t.Fatalf("Supply() error = %v, want %v", err, ErrPool)
	}
	if !slices.Equal(got.Idle, want) {
		t.Fatalf("Supply().Idle = %v, want %v", got.Idle, want)
	}
}

func TestClaimTakesOldestAndKeepsRefill(t *testing.T) {
	pool := Pool{Idle: []string{"base-1", "base-2"}, Refill: 0}

	got, id, err := Claim(pool)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if id != "base-1" {
		t.Fatalf("Claim() id = %q, want %q", id, "base-1")
	}
	if !slices.Equal(got.Idle, []string{"base-2"}) {
		t.Fatalf("Claim().Idle = %v, want [base-2]", got.Idle)
	}
	if got.Refill != 0 {
		t.Fatalf("Claim().Refill = %d, want 0", got.Refill)
	}
}

func TestClaimLastOwesRefill(t *testing.T) {
	pool := Pool{Idle: []string{"base-1"}, Refill: 0}

	got, id, err := Claim(pool)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if id != "base-1" {
		t.Fatalf("Claim() id = %q, want %q", id, "base-1")
	}
	if len(got.Idle) != 0 {
		t.Fatalf("Claim().Idle = %v, want empty", got.Idle)
	}
	if got.Refill != 1 {
		t.Fatalf("Claim().Refill = %d, want 1", got.Refill)
	}
}

func TestClaimEmptyKeepsRefill(t *testing.T) {
	pool := Pool{Idle: []string{}, Refill: 1}

	got, id, err := Claim(pool)
	if !errors.Is(err, ErrPool) {
		t.Fatalf("Claim() error = %v, want %v", err, ErrPool)
	}
	if id != "" {
		t.Fatalf("Claim() id = %q, want empty", id)
	}
	if len(got.Idle) != 0 {
		t.Fatalf("Claim().Idle = %v, want empty", got.Idle)
	}
	if got.Refill != 1 {
		t.Fatalf("Claim().Refill = %d, want 1", got.Refill)
	}
}

func TestSupplyRefusesBlankAndDuplicate(t *testing.T) {
	pool := Pool{Idle: []string{"base-1"}, Refill: 0}

	for _, id := range []string{"  ", "base-1"} {
		got, err := Supply(pool, id)
		if !errors.Is(err, ErrPool) {
			t.Fatalf("Supply(%q) error = %v, want %v", id, err, ErrPool)
		}
		if !slices.Equal(got.Idle, pool.Idle) || got.Refill != pool.Refill {
			t.Fatalf("Supply(%q) = %+v, want %+v", id, got, pool)
		}
	}
}

func TestClaimCopiesIdle(t *testing.T) {
	backing := []string{"base-1", "base-2", "spare"}
	pool := Pool{Idle: backing[:2], Refill: 0}

	got, _, err := Claim(pool)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	grown := append(got.Idle, "extra")
	if !slices.Equal(grown, []string{"base-2", "extra"}) {
		t.Fatalf("grown Idle = %v, want [base-2 extra]", grown)
	}

	if !slices.Equal(pool.Idle, []string{"base-1", "base-2"}) {
		t.Fatalf("caller Idle = %v, want [base-1 base-2]", pool.Idle)
	}
	if backing[2] != "spare" {
		t.Fatalf("caller backing = %v, want spare kept", backing)
	}
}

func TestLockKey(t *testing.T) {
	if _, err := LockKey(nil); !errors.Is(err, ErrCache) {
		t.Fatalf("LockKey(nil) error = %v, want %v", err, ErrCache)
	}

	first, err := LockKey([]byte("ab"))
	if err != nil {
		t.Fatalf("LockKey() error = %v", err)
	}
	second, err := LockKey([]byte("ab"))
	if err != nil {
		t.Fatalf("LockKey() error = %v", err)
	}
	if first != second {
		t.Fatalf("LockKey() = %q and %q, want equal", first, second)
	}
	if len(first) != 64 {
		t.Fatalf("len(LockKey()) = %d, want 64", len(first))
	}
	if _, err := hex.DecodeString(first); err != nil {
		t.Fatalf("LockKey() = %q, want hex: %v", first, err)
	}

	other, err := LockKey([]byte("ac"))
	if err != nil {
		t.Fatalf("LockKey() error = %v", err)
	}
	if other == first {
		t.Fatalf("LockKey() = %q for different bytes, want different", other)
	}
}
