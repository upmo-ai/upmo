package domain

import (
	"errors"
	"testing"
)

func TestBudgetFitsReservesInsideTheParent(t *testing.T) {
	if err := BudgetFits(50, 20, 30); err != nil {
		t.Fatal(err)
	}
	if err := BudgetFits(50, 20, 31); !errors.Is(err, ErrBudget) {
		t.Fatalf("over reserve error = %v, want %v", err, ErrBudget)
	}
	if err := BudgetFits(50, 0, 0); !errors.Is(err, ErrBudget) {
		t.Fatalf("zero amount error = %v, want %v", err, ErrBudget)
	}
}

func TestChargePausesOnTheCeilingAndRefusesPastIt(t *testing.T) {
	spent, paused, err := Charge(10, 0, 10, false)
	if err != nil || spent != 10 || !paused {
		t.Fatalf("exact = %d paused %v err %v", spent, paused, err)
	}
	spent, paused, err = Charge(10, 0, 11, false)
	if err != nil || spent != 11 || !paused {
		t.Fatalf("over = %d paused %v err %v", spent, paused, err)
	}
	spent, paused, err = Charge(10, spent, 1, true)
	if !errors.Is(err, ErrExhausted) || spent != 11 || !paused {
		t.Fatalf("paused = %d paused %v err %v", spent, paused, err)
	}
	spent, paused, err = Charge(0, 0, 0, false)
	if err != nil || spent != 0 || !paused {
		t.Fatalf("zero budget = %d paused %v err %v", spent, paused, err)
	}
}

func TestIntersectDropsNamesTheParentLacks(t *testing.T) {
	got := Intersect("task:view, task:edit", "task:edit, task:delete, task:edit")
	if got != "task:edit" {
		t.Fatalf("intersect = %q", got)
	}
	if Intersect("task:view", "") != "" {
		t.Fatal("empty request was not empty")
	}
}
