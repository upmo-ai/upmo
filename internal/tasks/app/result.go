package app

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/platform/trustfence"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

// Recorder stores one child result and claims one worktree.
type Recorder interface {
	SaveResult(ctx context.Context, access Access, taskID string, result domain.Result) error
	Claim(ctx context.Context, access Access, taskID, worktree string) error
}

// SaveResult neutralises the text and stores the fixed result once.
func SaveResult(ctx context.Context, book Recorder, access Access, taskID, summary, detail string) error {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return ErrAccess
	}
	if _, err := uuid.Parse(taskID); err != nil {
		return ErrTask
	}
	result, err := domain.AcceptResult(
		strings.TrimSpace(trustfence.NeutralizeSlot(summary)),
		strings.TrimSpace(trustfence.NeutralizeSlot(detail)),
	)
	if err != nil {
		return err
	}
	return book.SaveResult(ctx, access, taskID, result)
}

// Claim gives one open task the worktree. A second open task cannot hold it.
func Claim(ctx context.Context, book Recorder, access Access, taskID, worktree string) error {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return ErrAccess
	}
	if _, err := uuid.Parse(taskID); err != nil {
		return ErrTask
	}
	name, err := domain.Worktree(worktree)
	if err != nil {
		return err
	}
	return book.Claim(ctx, access, taskID, name)
}
