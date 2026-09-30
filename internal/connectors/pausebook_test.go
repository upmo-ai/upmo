package connectors

import (
	"errors"
	"testing"
)

const pauseTask = "9c4d5e6f-7a8b-4c93-9d0e-2f3a4b5c6d7e"

// refusedHeld checks that a book call failed with ErrPause and a zero Held.
func refusedHeld(t *testing.T, h Held, err error) {
	t.Helper()
	if !errors.Is(err, ErrPause) || err.Error() != "connectors: pause" {
		t.Fatalf("err = %v, want ErrPause", err)
	}
	if h != (Held{}) {
		t.Fatalf("held = %+v, want zero", h)
	}
}

func TestPauseBookPutAndGet(t *testing.T) {
	var b PauseBook
	h, err := b.Put(pauseID, pauseUser, pauseRun, " "+pauseTask+" ", "listUsers", PauseAsk)
	if err != nil {
		t.Fatalf("put err = %v", err)
	}
	want := Held{
		Pause:  Pause{ID: pauseID, User: pauseUser, Run: pauseRun, Tool: "listUsers", Reason: PauseAsk},
		TaskID: pauseTask,
	}
	if h != want {
		t.Fatalf("held = %+v, want %+v", h, want)
	}
	got, err := b.Get(" " + pauseID + " ")
	if err != nil || got != want {
		t.Fatalf("get = %+v %v", got, err)
	}
}

func TestPauseBookPutRefusesDuplicateID(t *testing.T) {
	var b PauseBook
	if _, err := b.Put(pauseID, pauseUser, pauseRun, pauseTask, "listUsers", PauseAsk); err != nil {
		t.Fatalf("put err = %v", err)
	}
	for _, id := range []string{pauseID, "8B3C4D5E-6F7A-4B82-8C9D-1E2F3A4B5C6D"} {
		h, err := b.Put(id, pauseUser, pauseRun, pauseTask, "listUsers", PauseExpired)
		refusedHeld(t, h, err)
	}
	got, err := b.Get(pauseID)
	if err != nil || got.Reason != PauseAsk {
		t.Fatalf("first pause = %+v %v", got, err)
	}
}

func TestPauseBookPutRefusesBadTask(t *testing.T) {
	tests := map[string]string{
		"blank":    "  ",
		"not uuid": "task",
		"nil":      "00000000-0000-0000-0000-000000000000",
		"id":       pauseID,
		"user":     pauseUser,
		"run":      pauseRun,
		"upper id": "8B3C4D5E-6F7A-4B82-8C9D-1E2F3A4B5C6D",
	}
	for name, task := range tests {
		t.Run(name, func(t *testing.T) {
			var b PauseBook
			h, err := b.Put(pauseID, pauseUser, pauseRun, task, "listUsers", PauseAsk)
			refusedHeld(t, h, err)
			if _, err := b.Get(pauseID); !errors.Is(err, ErrPause) {
				t.Fatalf("stored after refusal: %v", err)
			}
		})
	}
}

func TestPauseBookPutRefusesBadHold(t *testing.T) {
	var b PauseBook
	h, err := b.Put(pauseID, pauseUser, pauseRun, pauseTask, "call SK-live", PauseAsk)
	refusedHeld(t, h, err)
	if _, err := b.Get(pauseID); !errors.Is(err, ErrPause) {
		t.Fatalf("stored after refusal: %v", err)
	}
}

func TestPauseBookGetMissing(t *testing.T) {
	var b PauseBook
	h, err := b.Get(pauseID)
	refusedHeld(t, h, err)
}

func TestPauseBookTakeResumesOnce(t *testing.T) {
	var b PauseBook
	put, err := b.Put(pauseID, pauseUser, pauseRun, pauseTask, "listUsers", PauseExpired)
	if err != nil {
		t.Fatalf("put err = %v", err)
	}
	h, err := b.Take(" "+pauseUser+" ", pauseRun, " "+pauseID+" ")
	if err != nil || h != put {
		t.Fatalf("take = %+v %v", h, err)
	}
	if _, err := b.Get(pauseID); !errors.Is(err, ErrPause) {
		t.Fatalf("still stored: %v", err)
	}
	h, err = b.Take(pauseUser, pauseRun, pauseID)
	refusedHeld(t, h, err)
}

func TestPauseBookTakeRefusesOtherCaller(t *testing.T) {
	const other = "0d5e6f7a-8b9c-4da4-8e1f-3a4b5c6d7e8f"
	tests := map[string][2]string{
		"other user": {other, pauseRun},
		"other run":  {pauseUser, other},
	}
	for name, who := range tests {
		t.Run(name, func(t *testing.T) {
			var b PauseBook
			if _, err := b.Put(pauseID, pauseUser, pauseRun, pauseTask, "listUsers", PauseAsk); err != nil {
				t.Fatalf("put err = %v", err)
			}
			h, err := b.Take(who[0], who[1], pauseID)
			refusedHeld(t, h, err)
			if _, err := b.Get(pauseID); err != nil {
				t.Fatalf("refused take removed the pause: %v", err)
			}
		})
	}
}

func TestPauseBookNil(t *testing.T) {
	var b *PauseBook
	h, err := b.Put(pauseID, pauseUser, pauseRun, pauseTask, "listUsers", PauseAsk)
	refusedHeld(t, h, err)
	h, err = b.Get(pauseID)
	refusedHeld(t, h, err)
	h, err = b.Take(pauseUser, pauseRun, pauseID)
	refusedHeld(t, h, err)
}
