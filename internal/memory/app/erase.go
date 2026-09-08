package app

import (
	"context"

	"github.com/google/uuid"
)

// EraseStore deletes a person's memory as that person.
type EraseStore interface {
	Erase(ctx context.Context, userID, taskID string) error
}

// Erase deletes a person's memory. An empty taskID is the whole person;
// otherwise only that task's facts and extract rows go.
func Erase(ctx context.Context, store EraseStore, userID, taskID string) error {
	if _, err := uuid.Parse(userID); err != nil {
		return err
	}
	if taskID != "" {
		if _, err := uuid.Parse(taskID); err != nil {
			return err
		}
	}
	return store.Erase(ctx, userID, taskID)
}
