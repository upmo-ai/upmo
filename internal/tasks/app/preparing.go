package app

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/runtime"
)

// ErrPreparing is a task with no provisioning record in this process.
var ErrPreparing = errors.New("tasks: no preparing progress")

// Viewer returns one task the person can see under row security.
type Viewer interface {
	View(ctx context.Context, access Access, taskID string) (TaskView, error)
}

// Finder returns the stored id, goal and status of one task the person can see
// under row security, in one query. It is the access check for callers that do
// not need the events, subtasks or result that Viewer reads.
type Finder interface {
	Find(ctx context.Context, access Access, taskID string) (Task, error)
}

// Reader reads a task in full or only checks that the person can see it.
type Reader interface {
	Viewer
	Finder
}

// FindTask validates the caller and returns the stored head of one task.
func FindTask(ctx context.Context, find Finder, access Access, taskID string) (Task, error) {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return Task{}, ErrAccess
	}
	if _, err := uuid.Parse(taskID); err != nil {
		return Task{}, ErrTask
	}
	if find == nil {
		return Task{}, ErrGone
	}
	return find.Find(ctx, access, taskID)
}

// Progresses returns the in-process provisioning record for a task.
// *runtime.TaskSandboxes implements it.
type Progresses interface {
	Progress(taskID string) (runtime.Record, bool)
}

// PreparingProgress returns the provisioning phase for a task the person can
// see. The record lives only in this brain process until Release.
func PreparingProgress(ctx context.Context, find Finder, progress Progresses, access Access, taskID string) (runtime.Record, error) {
	if _, err := FindTask(ctx, find, access, taskID); err != nil {
		return runtime.Record{}, err
	}
	if progress == nil {
		return runtime.Record{}, ErrPreparing
	}
	rec, ok := progress.Progress(taskID)
	if !ok {
		return runtime.Record{}, ErrPreparing
	}
	return rec, nil
}
