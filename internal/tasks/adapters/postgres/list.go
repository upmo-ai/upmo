package postgres

import (
	"context"
	"fmt"

	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

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
