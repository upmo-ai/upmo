package domain

import (
	"errors"
	"testing"
)

func TestCeilingsAllowAChildUnderARoot(t *testing.T) {
	if err := Ceilings(1, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := Ceilings(1, Fanout-1, ChildCap-1); err != nil {
		t.Fatal(err)
	}
}

func TestCeilingsRejectAGrandchild(t *testing.T) {
	if err := Ceilings(TreeLevels, 0, 0); !errors.Is(err, ErrDepth) {
		t.Fatalf("Ceilings() error = %v, want %v", err, ErrDepth)
	}
}

func TestCeilingsRejectASixthChildBeforeTheSharedCap(t *testing.T) {
	if err := Ceilings(1, Fanout, ChildCap-1); !errors.Is(err, ErrFanout) {
		t.Fatalf("Ceilings() error = %v, want %v", err, ErrFanout)
	}
}

func TestCeilingsRejectTheSharedCapWhileTheParentHasRoom(t *testing.T) {
	if err := Ceilings(1, 1, ChildCap); !errors.Is(err, ErrCeiling) {
		t.Fatalf("Ceilings() error = %v, want %v", err, ErrCeiling)
	}
}
