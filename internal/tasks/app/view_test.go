package app

import (
	"context"
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

func TestViewTaskReturnsTheTask(t *testing.T) {
	view := &fakeViewer{}
	got, err := ViewTask(context.Background(), view, Access{UserID: userID, Allow: "task:view"}, preparingTask)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != preparingTask || got.Status != domain.Preparing || view.calls != 1 {
		t.Fatalf("task = %#v calls = %d", got, view.calls)
	}
}

func TestViewTaskNeedsAccess(t *testing.T) {
	view := &fakeViewer{}
	if _, err := ViewTask(context.Background(), view, Access{UserID: userID}, preparingTask); !errors.Is(err, ErrAccess) {
		t.Fatalf("error = %v", err)
	}
	if view.calls != 0 {
		t.Fatalf("calls = %d", view.calls)
	}
}

func TestViewTaskRejectsABadID(t *testing.T) {
	view := &fakeViewer{}
	if _, err := ViewTask(context.Background(), view, Access{UserID: userID, Allow: "task:view"}, "bad"); !errors.Is(err, ErrTask) {
		t.Fatalf("error = %v", err)
	}
}
