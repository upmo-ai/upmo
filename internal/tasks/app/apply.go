// Package app is the single writer for a task transition.
package app

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

var (
	// ErrAccess is a missing person or an empty allow list.
	ErrAccess = errors.New("tasks: access")
	// ErrTask is a task id that is not a UUID.
	ErrTask = errors.New("tasks: task")
	// ErrGone is a task this person cannot see.
	ErrGone = errors.New("tasks: no such task")
	// ErrRunnerOwned is a manual transition refused on a runner-owned task.
	ErrRunnerOwned = errors.New("tasks: runner owned")
	// ErrEnqueueHook means the runner turn store has no next-turn hook.
	ErrEnqueueHook = errors.New("tasks: enqueue hook required")
)

// Access is the person and the allow list for this transaction.
// The database still decides what those permissions can see.
// Teams, SharedView and SharedEdit are uuid array literals; empty is "{}".
type Access struct {
	UserID     string
	Allow      string
	Teams      string
	SharedView string
	SharedEdit string
}

// Task is the status after a transition. ChatID is the id of the chat line a
// Chat transition stored, and empty for any other transition.
type Task struct {
	ID     string
	Status domain.Status
	Goal   string
	ChatID string
}

// Book applies transitions and records checks under a row lock.
type Book interface {
	Transition(ctx context.Context, access Access, taskID string, input domain.Input) (Task, error)
	Record(ctx context.Context, access Access, taskID string, checks []domain.Check, inputs map[int]string) (bool, error)
}

// ApplyTransition validates the caller and delegates one transition.
func ApplyTransition(ctx context.Context, book Book, access Access, taskID string, input domain.Input) (Task, error) {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return Task{}, ErrAccess
	}
	if taskID == "" {
		if input.Kind != domain.Open {
			return Task{}, domain.ErrInput
		}
	} else {
		if _, err := uuid.Parse(taskID); err != nil {
			return Task{}, ErrTask
		}
		if input.Kind == domain.Open {
			return Task{}, domain.ErrInput
		}
	}
	return book.Transition(ctx, access, taskID, input)
}

// RecordChecks validates the caller and records one batch of checks.
func RecordChecks(ctx context.Context, book Book, access Access, taskID string, checks []domain.Check, inputs map[int]string) (bool, error) {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return false, ErrAccess
	}
	if _, err := uuid.Parse(taskID); err != nil {
		return false, ErrTask
	}
	if len(checks) == 0 {
		return false, domain.ErrEvidence
	}
	return book.Record(ctx, access, taskID, checks, inputs)
}
