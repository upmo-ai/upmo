package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/memory"
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
		if task.State == domain.Done || task.State == domain.Failed {
			continue
		}
		out = append(out, app.OpenGoal{ID: task.ID, Goal: task.Title})
	}
	return out, nil
}

func enqueueScout(ctx context.Context, tx pgx.Tx, s Store, userID, trigger, sourceID string) error {
	if s.Enqueuer == nil || userID == "" || sourceID == "" {
		return nil
	}
	_, err := s.Enqueuer.InsertTx(ctx, tx, app.ScoutJobArgs{
		UserID:   userID,
		Trigger:  trigger,
		SourceID: sourceID,
	}, nil)
	if err != nil {
		return fmt.Errorf("tasks: scout enqueue: %w", err)
	}
	return nil
}

func enqueueExtract(ctx context.Context, tx pgx.Tx, s Store, userID, taskID, goal string) error {
	if s.Enqueuer == nil || userID == "" || taskID == "" {
		return nil
	}
	_, err := s.Enqueuer.InsertTx(ctx, tx, memory.ExtractJobArgs{
		UserID:     userID,
		SourceKind: "task",
		ID:         taskID,
		Seq:        0,
		Text:       goal,
		AtUnix:     time.Now().UTC().Unix(),
	}, nil)
	if err != nil {
		return fmt.Errorf("tasks: extract enqueue: %w", err)
	}
	return nil
}
