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

// Transition locks the task, applies Step, and appends its events.
func (s Store) Transition(ctx context.Context, access app.Access, taskID string, input domain.Input) (app.Task, error) {
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

// Record locks the task, appends one check batch, and records whether it passed.
func (s Store) Record(ctx context.Context, access app.Access, taskID string, checks []domain.Check) (bool, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status string
	err = tx.QueryRow(ctx, `
		SELECT status
		FROM tasks.tasks
		WHERE id = $1::uuid
		FOR UPDATE`, taskID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, app.ErrGone
	}
	if err != nil {
		return false, fmt.Errorf("tasks: lock: %w", err)
	}
	if domain.Status(status) != domain.Checking {
		return false, domain.ErrInput
	}

	var batch int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(batch), 0) + 1
		FROM tasks.checks
		WHERE task_id = $1::uuid`, taskID).Scan(&batch); err != nil {
		return false, fmt.Errorf("tasks: check batch: %w", err)
	}
	for index, check := range checks {
		if _, err := tx.Exec(ctx, `
			INSERT INTO tasks.checks (task_id, batch, seq, name, code)
			VALUES ($1::uuid, $2, $3, $4, $5)`,
			taskID, batch, index+1, check.Name, check.Code); err != nil {
			return false, fmt.Errorf("tasks: check: %w", err)
		}
	}

	passes := domain.Passes(checks)
	tag, err := tx.Exec(ctx, `
		UPDATE tasks.tasks
		SET passes = $2, updated_at = now()
		WHERE id = $1::uuid`, taskID, passes)
	if err != nil {
		return false, fmt.Errorf("tasks: passes: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return false, app.ErrGone
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return passes, nil
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
	var passes bool
	err := tx.QueryRow(ctx, `
		SELECT status, goal, passes
		FROM tasks.tasks
		WHERE id = $1::uuid
		FOR UPDATE`, taskID).Scan(&status, &goal, &passes)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Task{}, app.ErrGone
	}
	if err != nil {
		return app.Task{}, fmt.Errorf("tasks: lock: %w", err)
	}
	if input.Kind == domain.Pass && !passes {
		return app.Task{}, domain.ErrEvidence
	}
	var turnBody string
	if input.Kind == domain.Advance && domain.Status(status) == domain.Working {
		var ended bool
		turnBody, ended = domain.EndTurn(input.Turn)
		if !ended {
			return app.Task{}, domain.ErrTurn
		}
		if turnBody != "" {
			_, effect, err := domain.Step(domain.Working, domain.Input{Kind: domain.Chat, Body: turnBody})
			if err != nil {
				return app.Task{}, err
			}
			turnBody = effect.Body
		}
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
	if turnBody != "" {
		if err := insertChatEvent(ctx, tx, taskID, turnBody); err != nil {
			return app.Task{}, err
		}
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
