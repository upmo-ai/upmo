package domain

import (
	"errors"
	"testing"
	"time"
)

func TestNewHold(t *testing.T) {
	if _, err := NewHold("  "); !errors.Is(err, ErrHold) {
		t.Fatalf("NewHold(blank) error = %v, want %v", err, ErrHold)
	}

	got, err := NewHold(" box ")
	if err != nil {
		t.Fatalf("NewHold() error = %v", err)
	}
	if got != (Hold{Container: "box"}) {
		t.Fatalf("NewHold() = %+v, want container box and not paused", got)
	}
}

func TestPauseClearsContainerWithoutMutatingCaller(t *testing.T) {
	h := Hold{Container: "box"}

	got, id := Pause(h)
	if got != (Hold{Paused: true}) || id != "box" {
		t.Fatalf("Pause() = %+v, %q, want paused with empty container and id box", got, id)
	}
	if h.Container != "box" {
		t.Fatalf("caller Container = %q, want %q", h.Container, "box")
	}
}

func TestRelease(t *testing.T) {
	held := Hold{Container: "box"}

	tests := []struct {
		name   string
		hold   Hold
		idle   time.Duration
		want   Hold
		wantID string
	}{
		{"one minute keeps", held, time.Minute, held, ""},
		{"at IdleTTL releases", held, IdleTTL, Hold{}, "box"},
		{"just under IdleTTL keeps", held, IdleTTL - time.Nanosecond, held, ""},
		{"paused releases at zero", Hold{Container: "box", Paused: true}, 0, Hold{Paused: true}, "box"},
		{"empty container", Hold{}, IdleTTL, Hold{}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, id, err := Release(tt.hold, tt.idle)
			if err != nil {
				t.Fatalf("Release() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("Release() hold = %+v, want %+v", got, tt.want)
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
	h := Hold{Container: "box"}

	got, id, err := Release(h, -time.Nanosecond)
	if !errors.Is(err, ErrHold) {
		t.Fatalf("Release() error = %v, want %v", err, ErrHold)
	}
	if got != h || id != "" {
		t.Fatalf("Release() = %+v, %q, want %+v and empty id", got, id, h)
	}
}

func TestResumeRefusesUnpaused(t *testing.T) {
	if _, _, _, err := Resume(Hold{Container: "box"}); !errors.Is(err, ErrHold) {
		t.Fatalf("Resume() error = %v, want %v", err, ErrHold)
	}
}

func TestResumeAfterIdleRelease(t *testing.T) {
	got, rec, id, err := Resume(Hold{})
	if err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if got != (Hold{}) || id != "" || rec != Begin() {
		t.Fatalf("Resume() = %+v, %+v, %q", got, rec, id)
	}
}

func TestResume(t *testing.T) {
	tests := []struct {
		name   string
		hold   Hold
		wantID string
	}{
		{"still holds box", Hold{Container: "box", Paused: true}, "box"},
		{"already released", Hold{Paused: true}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, rec, id, err := Resume(tt.hold)
			if err != nil {
				t.Fatalf("Resume() error = %v", err)
			}
			if got != (Hold{}) {
				t.Fatalf("Resume() hold = %+v, want unpaused and empty", got)
			}
			if rec != Begin() {
				t.Fatalf("Resume() record = %+v, want %+v", rec, Begin())
			}
			if id != tt.wantID {
				t.Fatalf("Resume() id = %q, want %q", id, tt.wantID)
			}
			if !tt.hold.Paused {
				t.Fatalf("caller Paused = false, want true")
			}
		})
	}
}
