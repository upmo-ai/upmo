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
)

// Access is the person and the allow list for this transaction.
// The database still decides what those permissions can see.
type Access struct {
	UserID string
	Allow  string
}

// Task is the status after a transition.
type Task struct {
	ID     string
	Status domain.Status
	Goal   string
}

// Book applies one transition under a row lock and appends the event.
type Book interface {
	Transition(ctx context.Context, access Access, taskID string, input domain.Input) (Task, error)
}

// ApplyTransition is the only writer. Pass is refused here: Done needs core
// evidence, and this writer does not collect any.
func ApplyTransition(ctx context.Context, book Book, access Access, taskID string, input domain.Input) (Task, error) {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return Task{}, ErrAccess
	}
	if input.Kind == domain.Pass {
		return Task{}, domain.ErrEvidence
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
