package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

// Spawn locks the parent, applies the ceilings, and opens one child.
func (s Store) Spawn(ctx context.Context, access app.Access, parentID, goal string, budget int64, allow string) (app.Task, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return app.Task{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('tasks:child', 0::bigint))`); err != nil {
		return app.Task{}, fmt.Errorf("tasks: child lock: %w", err)
	}

	var status, parentAllow string
	var parentIsChild, paused, tainted bool
	var parentBudget, held, spent int64
	err = tx.QueryRow(ctx, `
		SELECT status, parent_id IS NOT NULL, budget, held, spent, paused, allow, tainted
		FROM tasks.tasks
		WHERE id = $1::uuid
		FOR UPDATE`, parentID).Scan(&status, &parentIsChild, &parentBudget, &held, &spent, &paused, &parentAllow, &tainted)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Task{}, app.ErrGone
	}
	if err != nil {
		return app.Task{}, fmt.Errorf("tasks: lock parent: %w", err)
	}
	if domain.Status(status) == domain.Done || domain.Status(status) == domain.Failed {
		return app.Task{}, domain.ErrTerminal
	}
	if paused {
		return app.Task{}, domain.ErrExhausted
	}

	depth := 1
	if parentIsChild {
		depth = domain.TreeLevels
	}
	siblings, err := countChildren(ctx, tx, parentID)
	if err != nil {
		return app.Task{}, err
	}
	openChildren, err := countOpenChildren(ctx, tx)
	if err != nil {
		return app.Task{}, err
	}
	if err := domain.Ceilings(depth, siblings, openChildren); err != nil {
		return app.Task{}, err
	}
	if err := domain.BudgetFits(parentBudget-spent, held, budget); err != nil {
		return app.Task{}, err
	}
	granted := domain.Intersect(parentAllow, domain.Intersect(access.Allow, allow))

	next, effect, err := domain.Step("", domain.Input{Kind: domain.Open, Goal: goal})
	if err != nil {
		return app.Task{}, err
	}
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO tasks.tasks (owner_user_id, visibility, goal, status, parent_id, budget, allow, tainted)
		VALUES ($1::uuid, 'private', $2, $3, $4::uuid, $5, $6, $7)
		RETURNING id::text`,
		access.UserID, effect.Goal, string(next), parentID, budget, granted, tainted).Scan(&id)
	if err != nil {
		return app.Task{}, childWriteErr(err)
	}
	if err := insertTaskEvent(ctx, tx, id, effect, "person"); err != nil {
		return app.Task{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return app.Task{}, err
	}
	return app.Task{ID: id, Status: next, Goal: effect.Goal}, nil
}

// Children lists the direct children of a parent the caller can see.
func (s Store) Children(ctx context.Context, access app.Access, parentID string) ([]app.Subtask, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var found int
	err = tx.QueryRow(ctx, `SELECT 1 FROM tasks.tasks WHERE id = $1::uuid`, parentID).Scan(&found)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, app.ErrGone
	}
	if err != nil {
		return nil, fmt.Errorf("tasks: parent: %w", err)
	}

	rows, err := tx.Query(ctx, `
		SELECT id::text, status, goal
		FROM tasks.tasks
		WHERE parent_id = $1::uuid
		ORDER BY id`, parentID)
	if err != nil {
		return nil, fmt.Errorf("tasks: children: %w", err)
	}
	defer rows.Close()

	children := make([]app.Subtask, 0)
	for rows.Next() {
		var child app.Subtask
		var status string
		if err := rows.Scan(&child.ID, &status, &child.Goal); err != nil {
			return nil, fmt.Errorf("tasks: child: %w", err)
		}
		child.Status = domain.Status(status)
		children = append(children, child)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tasks: children: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return children, nil
}

// Send appends a chat line when the task is a child.
func (s Store) Send(ctx context.Context, access app.Access, taskID, body string) error {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var isChild bool
	err = tx.QueryRow(ctx, `
		SELECT parent_id IS NOT NULL
		FROM tasks.tasks
		WHERE id = $1::uuid
		FOR UPDATE`, taskID).Scan(&isChild)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrGone
	}
	if err != nil {
		return fmt.Errorf("tasks: lock child: %w", err)
	}
	if !isChild {
		return domain.ErrInput
	}
	if _, _, err := stepLocked(ctx, tx, access.UserID, "person", taskID, domain.Input{Kind: domain.Chat, Body: body}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func countChildren(ctx context.Context, tx pgx.Tx, parentID string) (int, error) {
	var siblings int
	err := tx.QueryRow(ctx, `
		SELECT count(*)
		FROM tasks.tasks
		WHERE parent_id = $1::uuid`, parentID).Scan(&siblings)
	if err != nil {
		return 0, fmt.Errorf("tasks: count children: %w", err)
	}
	return siblings, nil
}

func countOpenChildren(ctx context.Context, tx pgx.Tx) (int, error) {
	var open int
	if err := tx.QueryRow(ctx, `SELECT tasks.open_child_count()`).Scan(&open); err != nil {
		return 0, fmt.Errorf("tasks: count open children: %w", err)
	}
	return open, nil
}

func childWriteErr(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "tasks: depth"):
		return domain.ErrDepth
	case strings.Contains(msg, "tasks: fanout"):
		return domain.ErrFanout
	case strings.Contains(msg, "tasks: ceiling"):
		return domain.ErrCeiling
	case strings.Contains(msg, "tasks: terminal"):
		return domain.ErrTerminal
	case strings.Contains(msg, "tasks: budget"):
		return domain.ErrBudget
	case strings.Contains(msg, "tasks: authority"):
		return domain.ErrAuthority
	case strings.Contains(msg, "tasks: taint"):
		return domain.ErrTaint
	default:
		return fmt.Errorf("tasks: child: %w", err)
	}
}
