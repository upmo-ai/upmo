package app

import (
	"context"

	"github.com/google/uuid"
)

// Lister returns the tasks a person can see, newest update first.
type Lister interface {
	List(ctx context.Context, access Access) ([]TaskSummary, error)
}

// ListTasks validates the caller and returns their tasks.
func ListTasks(ctx context.Context, book Lister, access Access) ([]TaskSummary, error) {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return nil, ErrAccess
	}
	return book.List(ctx, access)
}
