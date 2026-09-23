package domain

import (
	"errors"
	"testing"
)

func TestBeginIsCloneWithOneEvent(t *testing.T) {
	got := Begin()
	want := Record{Phase: PhaseClone, Events: 1}
	if got != want {
		t.Fatalf("Begin() = %+v, want %+v", got, want)
	}
}

func TestApplyDoneReachesReady(t *testing.T) {
	rec := Begin()
	for range 3 {
		var err error
		rec, err = Apply(rec, KindDone)
		if err != nil {
			t.Fatalf("Apply() error = %v", err)
		}
	}

	want := Record{Phase: PhaseReady, Events: 4}
	if rec != want {
		t.Fatalf("Apply() = %+v, want %+v", rec, want)
	}
}

func TestApplyFailFromClone(t *testing.T) {
	got, err := Apply(Begin(), KindFail)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	want := Record{Phase: PhaseFailed, Events: 2}
	if got != want {
		t.Fatalf("Apply() = %+v, want %+v", got, want)
	}
}

func TestApplyRefusesNoteOnReady(t *testing.T) {
	rec := Record{Phase: PhaseReady, Events: 4}

	got, err := Apply(rec, KindNote)
	if !errors.Is(err, ErrProvision) {
		t.Fatalf("Apply() error = %v, want %v", err, ErrProvision)
	}
	if got != rec {
		t.Fatalf("Apply() = %+v, want %+v", got, rec)
	}
}

func TestApplyCeiling(t *testing.T) {
	rec := Begin()
	for range 7 {
		var err error
		rec, err = Apply(rec, KindNote)
		if err != nil {
			t.Fatalf("Apply() error = %v", err)
		}
	}

	want := Record{Phase: PhaseClone, Events: EventCap}
	if rec != want {
		t.Fatalf("Apply() = %+v, want %+v", rec, want)
	}

	got, err := Apply(rec, KindNote)
	if !errors.Is(err, ErrCeiling) {
		t.Fatalf("Apply() error = %v, want %v", err, ErrCeiling)
	}
	if got != rec {
		t.Fatalf("Apply() = %+v, want %+v", got, rec)
	}
}

func TestApplyCeilingKeepsPhaseOnDone(t *testing.T) {
	rec := Begin()
	kinds := []Kind{KindDone, KindDone, KindNote, KindNote, KindNote, KindNote, KindNote}
	for _, kind := range kinds {
		var err error
		rec, err = Apply(rec, kind)
		if err != nil {
			t.Fatalf("Apply() error = %v", err)
		}
	}

	want := Record{Phase: PhaseProbe, Events: EventCap}
	if rec != want {
		t.Fatalf("Apply() = %+v, want %+v", rec, want)
	}

	got, err := Apply(rec, KindDone)
	if !errors.Is(err, ErrCeiling) {
		t.Fatalf("Apply() error = %v, want %v", err, ErrCeiling)
	}
	if got != want {
		t.Fatalf("Apply() = %+v, want %+v", got, want)
	}
}

func TestApplyRefusesUnknownKind(t *testing.T) {
	rec := Begin()

	got, err := Apply(rec, "start")
	if !errors.Is(err, ErrProvision) {
		t.Fatalf("Apply() error = %v, want %v", err, ErrProvision)
	}
	if got != rec {
		t.Fatalf("Apply() = %+v, want %+v", got, rec)
	}
}
