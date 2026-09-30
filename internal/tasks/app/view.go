package app

import (
	"context"

	"github.com/google/uuid"
)

// ViewTask returns one task the person can see under row security.
func ViewTask(ctx context.Context, view Viewer, access Access, taskID string) (Task, error) {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return Task{}, ErrAccess
	}
	if _, err := uuid.Parse(taskID); err != nil {
		return Task{}, ErrTask
	}
	if view == nil {
		return Task{}, ErrGone
	}
	return view.View(ctx, access, taskID)
}
