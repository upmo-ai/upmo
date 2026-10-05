package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/tasks"
	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

// ListOpenGoals returns non-terminal task goals for one person.
func (s Store) ListOpenGoals(ctx context.Context, userID uuid.UUID) ([]app.OpenGoal, error) {
	access := app.Access{UserID: userID.String(), Allow: "task:view"}
	tasks, err := s.List(ctx, access)
	if err != nil {
		return nil, err
	}
	out := make([]app.OpenGoal, 0, len(tasks))
	for _, task := range tasks {
		if task.Status == domain.Done || task.Status == domain.Failed {
			continue
		}
		out = append(out, app.OpenGoal{ID: task.ID, Goal: task.Goal})
	}
	return out, nil
}

func enqueueScout(ctx context.Context, tx pgx.Tx, s Store, userID, trigger, sourceID string) error {
	if s.Enqueuer == nil || userID == "" || sourceID == "" {
		return nil
	}
	_, err := s.Enqueuer.InsertTx(ctx, tx, tasks.ScoutJobArgs{
		UserID:   userID,
		Trigger:  trigger,
		SourceID: sourceID,
	}, nil)
	if err != nil {
		return fmt.Errorf("tasks: scout enqueue: %w", err)
	}
	return nil
}
