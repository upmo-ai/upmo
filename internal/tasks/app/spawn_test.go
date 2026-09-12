package app

import (
	"context"
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

func TestSpawnRejectsABadGoalBeforeTheBook(t *testing.T) {
	book := &fakeSpawner{}
	access := Access{UserID: userID, Allow: "task:create"}
	parent := "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"

	if _, err := Spawn(context.Background(), book, access, parent, "  "); !errors.Is(err, domain.ErrGoal) {
		t.Fatalf("empty goal error = %v, want %v", err, domain.ErrGoal)
	}
	if _, err := Spawn(context.Background(), book, Access{UserID: userID}, parent, "File them"); !errors.Is(err, ErrAccess) {
		t.Fatalf("empty allow error = %v, want %v", err, ErrAccess)
	}
	if _, err := Spawn(context.Background(), book, access, "bad-id", "File them"); !errors.Is(err, ErrTask) {
		t.Fatalf("bad parent error = %v, want %v", err, ErrTask)
	}
	if book.spawns != 0 {
		t.Fatalf("spawn calls = %d, want 0", book.spawns)
	}
}

func TestAwaitWaitsForEveryChild(t *testing.T) {
	parent := "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"
	access := Access{UserID: userID, Allow: "task:view"}
	book := &fakeSpawner{children: []Subtask{{
		ID:     "018f3b2c-7a10-7c4a-8e11-cccccccccccc",
		Status: domain.Preparing,
		Goal:   "File them",
	}}}
	if _, err := Await(context.Background(), book, access, parent); !errors.Is(err, domain.ErrWaiting) {
		t.Fatalf("open child error = %v, want %v", err, domain.ErrWaiting)
	}

	book.children[0].Status = domain.Failed
	got, err := Await(context.Background(), book, access, parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Status != domain.Failed {
		t.Fatalf("children = %+v", got)
	}

	book.children = nil
	got, err = Await(context.Background(), book, access, parent)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("empty children = %#v", got)
	}
}

func TestSendRejectsAnEmptyLineBeforeTheBook(t *testing.T) {
	book := &fakeSpawner{}
	access := Access{UserID: userID, Allow: "task:edit"}
	taskID := "018f3b2c-7a10-7c4a-8e11-cccccccccccc"
	if err := Send(context.Background(), book, access, taskID, "  "); !errors.Is(err, domain.ErrChat) {
		t.Fatalf("empty line error = %v, want %v", err, domain.ErrChat)
	}
	if book.sends != 0 {
		t.Fatalf("send calls = %d, want 0", book.sends)
	}
}

func TestCancelFailsThatTask(t *testing.T) {
	book := &fake{status: domain.Preparing}
	taskID := "018f3b2c-7a10-7c4a-8e11-cccccccccccc"
	got, err := Cancel(context.Background(), book, Access{UserID: userID, Allow: "task:edit"}, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.Failed {
		t.Fatalf("status = %s, want Failed", got.Status)
	}
	if _, err := Cancel(context.Background(), book, Access{UserID: "bad"}, taskID); !errors.Is(err, ErrAccess) {
		t.Fatalf("bad access error = %v, want %v", err, ErrAccess)
	}
}

type fakeSpawner struct {
	spawns   int
	sends    int
	children []Subtask
}

func (f *fakeSpawner) Spawn(context.Context, Access, string, string) (Task, error) {
	f.spawns++
	return Task{}, nil
}

func (f *fakeSpawner) Children(context.Context, Access, string) ([]Subtask, error) {
	return f.children, nil
}

func (f *fakeSpawner) Send(context.Context, Access, string, string) error {
	f.sends++
	return nil
}
