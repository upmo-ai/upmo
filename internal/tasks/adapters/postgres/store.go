package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

// Store is the task tables. One transition is one transaction.
type Store struct {
	Pool *pgxpool.Pool
}

// Transition locks the task, applies Step, and appends one event.
// Pass is refused so Done cannot be stored without evidence.
func (s Store) Transition(ctx context.Context, access app.Access, taskID string, input domain.Input) (app.Task, error) {
	if input.Kind == domain.Pass {
		return app.Task{}, domain.ErrEvidence
	}
	tx, err := s.begin(ctx, access)
	if err != nil {
		return app.Task{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var task app.Task
	if taskID == "" {
		task, err = insertOpen(ctx, tx, access.UserID, input)
	} else {
		task, err = stepLocked(ctx, tx, taskID, input)
	}
	if err != nil {
		return app.Task{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return app.Task{}, err
	}
	return task, nil
}

func (s Store) begin(ctx context.Context, access app.Access) (pgx.Tx, error) {
	if s.Pool == nil {
		return nil, fmt.Errorf("tasks: no database")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `
		SELECT set_config('upmo.user_id', $1, true),
		       set_config('upmo.allow', $2, true),
		       set_config('upmo.teams', '{}', true),
		       set_config('upmo.shared_view', '{}', true),
		       set_config('upmo.shared_edit', '{}', true)`,
		access.UserID, access.Allow)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func insertOpen(ctx context.Context, tx pgx.Tx, userID string, input domain.Input) (app.Task, error) {
	next, effect, err := domain.Step("", input)
	if err != nil {
		return app.Task{}, err
	}
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO tasks.tasks (owner_user_id, visibility, goal, status)
		VALUES ($1::uuid, 'private', $2, $3)
		RETURNING id::text`, userID, effect.Goal, string(next)).Scan(&id)
	if err != nil {
		return app.Task{}, fmt.Errorf("tasks: open: %w", err)
	}
	if err := insertTaskEvent(ctx, tx, id, effect); err != nil {
		return app.Task{}, err
	}
	return app.Task{ID: id, Status: next, Goal: effect.Goal}, nil
}

func stepLocked(ctx context.Context, tx pgx.Tx, taskID string, input domain.Input) (app.Task, error) {
	var status, goal string
	err := tx.QueryRow(ctx, `
		SELECT status, goal
		FROM tasks.tasks
		WHERE id = $1::uuid
		FOR UPDATE`, taskID).Scan(&status, &goal)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Task{}, app.ErrGone
	}
	if err != nil {
		return app.Task{}, fmt.Errorf("tasks: lock: %w", err)
	}
	next, effect, err := domain.Step(domain.Status(status), input)
	if err != nil {
		return app.Task{}, err
	}
	if effect.Kind == domain.Chat {
		if err := insertChatEvent(ctx, tx, taskID, effect.Body); err != nil {
			return app.Task{}, err
		}
		return app.Task{ID: taskID, Status: next, Goal: goal}, nil
	}
	if err := insertTaskEvent(ctx, tx, taskID, effect); err != nil {
		return app.Task{}, err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE tasks.tasks
		SET status = $2, updated_at = now()
		WHERE id = $1::uuid`, taskID, string(next))
	if err != nil {
		return app.Task{}, fmt.Errorf("tasks: status: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return app.Task{}, app.ErrGone
	}
	return app.Task{ID: taskID, Status: next, Goal: goal}, nil
}

func insertTaskEvent(ctx context.Context, tx pgx.Tx, taskID string, effect domain.Effect) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO tasks.task_events (task_id, seq, from_status, to_status, input)
		SELECT $1::uuid,
		       COALESCE((SELECT MAX(seq) FROM tasks.task_events WHERE task_id = $1::uuid), 0) + 1,
		       $2, $3, $4`,
		taskID, string(effect.From), string(effect.To), string(effect.Kind))
	if err != nil {
		return fmt.Errorf("tasks: event: %w", err)
	}
	return nil
}

func insertChatEvent(ctx context.Context, tx pgx.Tx, taskID, body string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO tasks.chat_events (task_id, seq, body)
		SELECT $1::uuid,
		       COALESCE((SELECT MAX(seq) FROM tasks.chat_events WHERE task_id = $1::uuid), 0) + 1,
		       $2`,
		taskID, body)
	if err != nil {
		return fmt.Errorf("tasks: chat: %w", err)
	}
	return nil
}
