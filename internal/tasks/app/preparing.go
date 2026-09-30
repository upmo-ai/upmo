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
	View(ctx context.Context, access Access, taskID string) (Task, error)
}

// Progresses returns the in-process provisioning record for a task.
// *runtime.Seats implements it.
type Progresses interface {
	Progress(taskID string) (runtime.Record, bool)
}

// PreparingProgress returns the provisioning phase for a task the person can
// see. The record lives only in this brain process until Release.
func PreparingProgress(ctx context.Context, view Viewer, progress Progresses, access Access, taskID string) (runtime.Record, error) {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return runtime.Record{}, ErrAccess
	}
	if _, err := uuid.Parse(taskID); err != nil {
		return runtime.Record{}, ErrTask
	}
	if view == nil {
		return runtime.Record{}, ErrGone
	}
	if _, err := view.View(ctx, access, taskID); err != nil {
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
