package domain

import (
	"errors"
	"testing"
	"time"
)

func TestResourceShapeIsFixed(t *testing.T) {
	want := Shape{Scratch: "scratch", CPU: 1, Burst: 2, PIDs: 256, Exec: 15 * time.Minute}
	got := ResourceShape()
	if got != want {
		t.Fatalf("ResourceShape() = %+v, want %+v", got, want)
	}
	if again := ResourceShape(); again != got {
		t.Fatalf("ResourceShape() = %+v, then %+v", got, again)
	}
}

func TestAllowExecAllowsUpToCap(t *testing.T) {
	for _, d := range []time.Duration{0, time.Minute, ExecCap} {
		if err := AllowExec(d); err != nil {
			t.Fatalf("AllowExec(%v) error = %v, want nil", d, err)
		}
	}
}

func TestAllowExecRefusesOutsideCap(t *testing.T) {
	for _, d := range []time.Duration{-time.Nanosecond, ExecCap + time.Nanosecond} {
		if err := AllowExec(d); !errors.Is(err, ErrShape) {
			t.Fatalf("AllowExec(%v) error = %v, want %v", d, err, ErrShape)
		}
	}
}

func TestExecCapIsFifteenMinutes(t *testing.T) {
	if ExecCap != 15*time.Minute {
		t.Fatalf("ExecCap = %v, want %v", ExecCap, 15*time.Minute)
	}
}
