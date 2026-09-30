package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

// View returns one task this person can see under row security.
func (s Store) View(ctx context.Context, access app.Access, taskID string) (app.Task, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return app.Task{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var task app.Task
	var status string
	err = tx.QueryRow(ctx, `
		SELECT id::text, status, goal
		FROM tasks.tasks
		WHERE id = $1::uuid`, taskID).Scan(&task.ID, &status, &task.Goal)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Task{}, app.ErrGone
	}
	if err != nil {
		return app.Task{}, fmt.Errorf("tasks: view: %w", err)
	}
	task.Status = domain.Status(status)
	if err := tx.Commit(ctx); err != nil {
		return app.Task{}, err
	}
	return task, nil
}

// List returns the tasks this person can see under row security.
func (s Store) List(ctx context.Context, access app.Access) ([]app.Task, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT id::text, status, goal
		FROM tasks.tasks
		ORDER BY updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("tasks: list: %w", err)
	}
	defer rows.Close()

	var out []app.Task
	for rows.Next() {
		var task app.Task
		var status string
		if err := rows.Scan(&task.ID, &status, &task.Goal); err != nil {
			return nil, fmt.Errorf("tasks: list: %w", err)
		}
		task.Status = domain.Status(status)
		out = append(out, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tasks: list: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	if out == nil {
		out = []app.Task{}
	}
	return out, nil
}
