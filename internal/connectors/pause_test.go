package connectors

import (
	"errors"
	"testing"
)

const (
	pauseUser = "6f1c2a3b-4d5e-4f60-8a7b-9c0d1e2f3a4b"
	pauseRun  = "7a2b3c4d-5e6f-4a71-9b8c-0d1e2f3a4b5c"
	pauseID   = "8b3c4d5e-6f7a-4b82-8c9d-1e2f3a4b5c6d"
)

// refused checks that a hold failed with ErrPause and a zero Pause.
func refused(t *testing.T, p Pause, err error) {
	t.Helper()
	if !errors.Is(err, ErrPause) {
		t.Fatalf("err = %v, want ErrPause", err)
	}
	if err.Error() != "connectors: pause" {
		t.Fatalf("err text = %q", err.Error())
	}
	if p != (Pause{}) {
		t.Fatalf("pause = %+v, want zero", p)
	}
}

func TestHold(t *testing.T) {
	for _, reason := range []string{PauseExpired, PauseAsk} {
		t.Run(reason, func(t *testing.T) {
			p, err := Hold(" "+pauseID+" ", pauseUser, pauseRun, " listUsers ", reason)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			want := Pause{ID: pauseID, User: pauseUser, Run: pauseRun, Tool: "listUsers", Reason: reason}
			if p != want {
				t.Fatalf("pause = %+v, want %+v", p, want)
			}
		})
	}
}

func TestHoldNilID(t *testing.T) {
	p, err := Hold("00000000-0000-0000-0000-000000000000", pauseUser, pauseRun, "listUsers", PauseAsk)
	refused(t, p, err)
}

func TestHoldSameUserAndRun(t *testing.T) {
	p, err := Hold(pauseID, pauseUser, pauseUser, "listUsers", PauseAsk)
	refused(t, p, err)
}

func TestHoldCredentialTool(t *testing.T) {
	p, err := Hold(pauseID, pauseUser, pauseRun, "call SK-live", PauseAsk)
	refused(t, p, err)
}

func TestHoldBlankReason(t *testing.T) {
	p, err := Hold(pauseID, pauseUser, pauseRun, "listUsers", "  ")
	refused(t, p, err)
}

func TestResume(t *testing.T) {
	p, err := Hold(pauseID, pauseUser, pauseRun, "listUsers", PauseExpired)
	if err != nil {
		t.Fatalf("hold err = %v", err)
	}
	if err := Resume(p, " "+pauseUser+" ", pauseRun, pauseID); err != nil {
		t.Fatalf("resume err = %v", err)
	}
}

func TestResumeDifferentID(t *testing.T) {
	p, err := Hold(pauseID, pauseUser, pauseRun, "listUsers", PauseExpired)
	if err != nil {
		t.Fatalf("hold err = %v", err)
	}
	err = Resume(p, pauseUser, pauseRun, "9c4d5e6f-7a8b-4c93-9d0e-2f3a4b5c6d7e")
	if !errors.Is(err, ErrPause) {
		t.Fatalf("err = %v, want ErrPause", err)
	}
}
