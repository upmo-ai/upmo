package domain

import (
	"errors"
	"testing"
	"time"
)

func TestHold(t *testing.T) {
	if _, err := Hold("  "); !errors.Is(err, ErrSeat) {
		t.Fatalf("Hold(blank) error = %v, want %v", err, ErrSeat)
	}

	got, err := Hold(" box ")
	if err != nil {
		t.Fatalf("Hold() error = %v", err)
	}
	if got != (Seat{Container: "box"}) {
		t.Fatalf("Hold() = %+v, want container box and not paused", got)
	}
}

func TestPauseClearsContainerWithoutMutatingCaller(t *testing.T) {
	seat := Seat{Container: "box"}

	got, id := Pause(seat)
	if got != (Seat{Paused: true}) || id != "box" {
		t.Fatalf("Pause() = %+v, %q, want paused with empty container and id box", got, id)
	}
	if seat.Container != "box" {
		t.Fatalf("caller Container = %q, want %q", seat.Container, "box")
	}
}

func TestRelease(t *testing.T) {
	held := Seat{Container: "box"}

	tests := []struct {
		name   string
		seat   Seat
		idle   time.Duration
		want   Seat
		wantID string
	}{
		{"one minute keeps", held, time.Minute, held, ""},
		{"at IdleTTL releases", held, IdleTTL, Seat{}, "box"},
		{"just under IdleTTL keeps", held, IdleTTL - time.Nanosecond, held, ""},
		{"paused releases at zero", Seat{Container: "box", Paused: true}, 0, Seat{Paused: true}, "box"},
		{"empty container", Seat{}, IdleTTL, Seat{}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, id, err := Release(tt.seat, tt.idle)
			if err != nil {
				t.Fatalf("Release() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("Release() seat = %+v, want %+v", got, tt.want)
			}
			if id != tt.wantID {
				t.Fatalf("Release() id = %q, want %q", id, tt.wantID)
			}
		})
	}

	if held.Container != "box" {
		t.Fatalf("caller Container = %q, want %q", held.Container, "box")
	}
}

func TestReleaseRefusesNegativeIdle(t *testing.T) {
	seat := Seat{Container: "box"}

	got, id, err := Release(seat, -time.Nanosecond)
	if !errors.Is(err, ErrSeat) {
		t.Fatalf("Release() error = %v, want %v", err, ErrSeat)
	}
	if got != seat || id != "" {
		t.Fatalf("Release() = %+v, %q, want %+v and empty id", got, id, seat)
	}
}

func TestResumeRefusesUnpaused(t *testing.T) {
	if _, _, _, err := Resume(Seat{Container: "box"}); !errors.Is(err, ErrSeat) {
		t.Fatalf("Resume() error = %v, want %v", err, ErrSeat)
	}
}

func TestResumeAfterIdleRelease(t *testing.T) {
	got, rec, id, err := Resume(Seat{})
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if got != (Seat{}) || id != "" || rec != Begin() {
		t.Fatalf("Resume() = %+v, %+v, %q", got, rec, id)
	}
}

func TestResume(t *testing.T) {
	tests := []struct {
		name   string
		seat   Seat
		wantID string
	}{
		{"still holds box", Seat{Container: "box", Paused: true}, "box"},
		{"already released", Seat{Paused: true}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, rec, id, err := Resume(tt.seat)
			if err != nil {
				t.Fatalf("Resume() error = %v", err)
			}
			if got != (Seat{}) {
				t.Fatalf("Resume() seat = %+v, want unpaused and empty", got)
			}
			if rec != Begin() {
				t.Fatalf("Resume() record = %+v, want %+v", rec, Begin())
			}
			if id != tt.wantID {
				t.Fatalf("Resume() id = %q, want %q", id, tt.wantID)
			}
			if !tt.seat.Paused {
				t.Fatalf("caller Paused = false, want true")
			}
		})
	}
}
