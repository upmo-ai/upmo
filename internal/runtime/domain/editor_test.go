package domain

import (
	"errors"
	"testing"
	"time"
)

func TestMountRuleMountsScratch(t *testing.T) {
	cases := []struct {
		status string
		want   Mount
	}{
		{" Working ", Mount{Volume: ScratchName, Mode: ModeReadOnly}},
		{"Checking", Mount{Volume: ScratchName, Mode: ModeReadWrite}},
		{"Done", Mount{Volume: ScratchName, Mode: ModeReadWrite}},
		{"Failed", Mount{Volume: ScratchName, Mode: ModeReadWrite}},
	}
	for _, c := range cases {
		got, err := MountRule(c.status)
		if err != nil {
			t.Fatalf("MountRule(%q) error = %v", c.status, err)
		}
		if got != c.want {
			t.Fatalf("MountRule(%q) = %+v, want %+v", c.status, got, c.want)
		}
	}
}

func TestMountRuleRefusesOtherStatus(t *testing.T) {
	cases := []string{"Preparing", "Planning", "", "working", "done"}
	for _, status := range cases {
		got, err := MountRule(status)
		if !errors.Is(err, ErrEditor) {
			t.Fatalf("MountRule(%q) error = %v, want %v", status, err, ErrEditor)
		}
		if got != (Mount{}) {
			t.Fatalf("MountRule(%q) = %+v, want zero Mount", status, got)
		}
	}
}

func TestIdleStop(t *testing.T) {
	cases := []struct {
		idle    time.Duration
		want    bool
		wantErr error
	}{
		{-time.Nanosecond, false, ErrEditor},
		{0, false, nil},
		{EditorIdle - time.Nanosecond, false, nil},
		{EditorIdle, true, nil},
		{EditorIdle + time.Nanosecond, true, nil},
	}
	for _, c := range cases {
		got, err := IdleStop(c.idle)
		if !errors.Is(err, c.wantErr) {
			t.Fatalf("IdleStop(%v) error = %v, want %v", c.idle, err, c.wantErr)
		}
		if got != c.want {
			t.Fatalf("IdleStop(%v) = %v, want %v", c.idle, got, c.want)
		}
	}
}

func TestAdmitEditor(t *testing.T) {
	if err := AdmitEditor(0); err != nil {
		t.Fatalf("AdmitEditor(0) error = %v, want nil", err)
	}
	for _, open := range []int{1, 2, -1} {
		if err := AdmitEditor(open); !errors.Is(err, ErrEditor) {
			t.Fatalf("AdmitEditor(%d) error = %v, want %v", open, err, ErrEditor)
		}
	}
}

func TestEditorLimits(t *testing.T) {
	if EditorIdle != 15*time.Minute {
		t.Fatalf("EditorIdle = %v, want %v", EditorIdle, 15*time.Minute)
	}
	if EditorCap != 1 {
		t.Fatalf("EditorCap = %d, want 1", EditorCap)
	}
}
