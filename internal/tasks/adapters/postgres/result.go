package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

// Cancel fails the task. When it has open children, those fail with cause parent_cancelled.
func (s Store) Cancel(ctx context.Context, access app.Access, taskID string) (app.Task, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return app.Task{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status string
	err = tx.QueryRow(ctx, `
		SELECT status
		FROM tasks.tasks
		WHERE id = $1::uuid
		FOR UPDATE`, taskID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Task{}, app.ErrGone
	}
	if err != nil {
		return app.Task{}, fmt.Errorf("tasks: lock cancel: %w", err)
	}
	if domain.Status(status) == domain.Done || domain.Status(status) == domain.Failed {
		return app.Task{}, domain.ErrTerminal
	}

	rows, err := tx.Query(ctx, `
		SELECT id::text
		FROM tasks.tasks
		WHERE parent_id = $1::uuid
		  AND status NOT IN ('Done', 'Failed')
		ORDER BY id
		FOR UPDATE`, taskID)
	if err != nil {
		return app.Task{}, fmt.Errorf("tasks: lock children: %w", err)
	}
	var children []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return app.Task{}, fmt.Errorf("tasks: child id: %w", err)
		}
		children = append(children, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return app.Task{}, fmt.Errorf("tasks: children: %w", err)
	}
	rows.Close()

	task, err := stepLocked(ctx, tx, taskID, domain.Input{Kind: domain.Fail})
	if err != nil {
		return app.Task{}, err
	}
	for _, childID := range children {
		if _, err := stepLocked(ctx, tx, childID, domain.Input{Kind: domain.Fail}); err != nil {
			return app.Task{}, err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE tasks.tasks
			SET cause = $2
			WHERE id = $1::uuid`, childID, domain.ParentCancelled)
		if err != nil {
			return app.Task{}, fmt.Errorf("tasks: cause: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return app.Task{}, app.ErrGone
		}
	}
	if err := enqueueScout(ctx, tx, s, access.UserID, "task", taskID); err != nil {
		return app.Task{}, err
	}
	if err := enqueueExtract(ctx, tx, s, access.UserID, taskID, task.Goal); err != nil {
		return app.Task{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return app.Task{}, err
	}
	return task, nil
}

// SaveResult stores the fixed result on a child, once.
func (s Store) SaveResult(ctx context.Context, access app.Access, taskID string, result domain.Result) error {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var isChild bool
	var raw []byte
	err = tx.QueryRow(ctx, `
		SELECT parent_id IS NOT NULL, result
		FROM tasks.tasks
		WHERE id = $1::uuid
		FOR UPDATE`, taskID).Scan(&isChild, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrGone
	}
	if err != nil {
		return fmt.Errorf("tasks: lock result: %w", err)
	}
	if !isChild {
		return domain.ErrInput
	}
	if raw != nil {
		return domain.ErrResult
	}
	body, err := json.Marshal(result)
	if err != nil {
		return domain.ErrResult
	}
	tag, err := tx.Exec(ctx, `
		UPDATE tasks.tasks
		SET result = $2::jsonb
		WHERE id = $1::uuid`, taskID, body)
	if err != nil {
		return resultWriteErr(err)
	}
	if tag.RowsAffected() != 1 {
		return app.ErrGone
	}
	return tx.Commit(ctx)
}

// Claim records the worktree on one open task.
func (s Store) Claim(ctx context.Context, access app.Access, taskID, worktree string) error {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status, current string
	err = tx.QueryRow(ctx, `
		SELECT status, worktree
		FROM tasks.tasks
		WHERE id = $1::uuid
		FOR UPDATE`, taskID).Scan(&status, &current)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrGone
	}
	if err != nil {
		return fmt.Errorf("tasks: lock worktree: %w", err)
	}
	if domain.Status(status) == domain.Done || domain.Status(status) == domain.Failed {
		return domain.ErrTerminal
	}
	if current == worktree {
		return tx.Commit(ctx)
	}
	if current != "" {
		return domain.ErrWorktree
	}
	var holder string
	err = tx.QueryRow(ctx, `
		SELECT id::text
		FROM tasks.tasks
		WHERE worktree = $1
		  AND id <> $2::uuid
		  AND status NOT IN ('Done', 'Failed')`, worktree, taskID).Scan(&holder)
	if err == nil {
		return domain.ErrWorktree
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("tasks: worktree holder: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE tasks.tasks
		SET worktree = $2
		WHERE id = $1::uuid`, taskID, worktree)
	if err != nil {
		return resultWriteErr(err)
	}
	if tag.RowsAffected() != 1 {
		return app.ErrGone
	}
	return tx.Commit(ctx)
}

func resultWriteErr(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "tasks_worktree_writer"):
		return domain.ErrWorktree
	case strings.Contains(msg, "tasks: result"):
		return domain.ErrResult
	case strings.Contains(msg, "tasks: worktree"):
		return domain.ErrWorktree
	default:
		return fmt.Errorf("tasks: result: %w", err)
	}
}
