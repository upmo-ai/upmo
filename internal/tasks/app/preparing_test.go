package app

import (
	"context"
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/runtime"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

const preparingTask = "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"

type fakeViewer struct {
	err   error
	calls int
}

func (f *fakeViewer) View(_ context.Context, _ Access, _ string) (Task, error) {
	f.calls++
	if f.err != nil {
		return Task{}, f.err
	}
	return Task{ID: preparingTask, Status: domain.Preparing, Goal: "File the receipts"}, nil
}

type fakeProgress map[string]runtime.Record

func (f fakeProgress) Progress(taskID string) (runtime.Record, bool) {
	rec, ok := f[taskID]
	return rec, ok
}

func TestPreparingProgressReturnsTheRecord(t *testing.T) {
	view := &fakeViewer{}
	progress := fakeProgress{preparingTask: {Phase: runtime.PhaseToolchain, Events: 3}}
	got, err := PreparingProgress(context.Background(), view, progress, Access{UserID: userID, Allow: "task:view"}, preparingTask)
	if err != nil || got.Phase != runtime.PhaseToolchain || got.Events != 3 || view.calls != 1 {
		t.Fatalf("got = %+v err = %v calls = %d", got, err, view.calls)
	}
}

func TestPreparingProgressRefusesAccessAndMissing(t *testing.T) {
	view := &fakeViewer{}
	progress := fakeProgress{preparingTask: {Phase: runtime.PhaseClone, Events: 1}}
	access := Access{UserID: userID, Allow: "task:view"}
	if _, err := PreparingProgress(context.Background(), view, progress, Access{UserID: "bad", Allow: "task:view"}, preparingTask); !errors.Is(err, ErrAccess) || view.calls != 0 {
		t.Fatalf("bad user = %v calls %d", err, view.calls)
	}
	if _, err := PreparingProgress(context.Background(), view, progress, Access{UserID: userID}, preparingTask); !errors.Is(err, ErrAccess) {
		t.Fatalf("empty allow = %v", err)
	}
	if _, err := PreparingProgress(context.Background(), view, progress, access, "not-a-uuid"); !errors.Is(err, ErrTask) || view.calls != 0 {
		t.Fatalf("bad task = %v calls %d", err, view.calls)
	}
	view.err = ErrGone
	if _, err := PreparingProgress(context.Background(), view, progress, access, preparingTask); !errors.Is(err, ErrGone) {
		t.Fatalf("gone = %v", err)
	}
	view.err = nil
	if _, err := PreparingProgress(context.Background(), view, fakeProgress{}, access, preparingTask); !errors.Is(err, ErrPreparing) {
		t.Fatalf("no progress = %v", err)
	}
}
