package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

// Fund sets the budget of a root that has none.
func (s Store) Fund(ctx context.Context, access app.Access, taskID string, budget int) error {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var isChild bool
	var current int
	var status string
	err = tx.QueryRow(ctx, `
		SELECT parent_id IS NOT NULL, budget, status
		FROM tasks.tasks
		WHERE id = $1::uuid
		FOR UPDATE`, taskID).Scan(&isChild, &current, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrGone
	}
	if err != nil {
		return fmt.Errorf("tasks: lock fund: %w", err)
	}
	if isChild || current != 0 {
		return domain.ErrBudget
	}
	if domain.Status(status) == domain.Done || domain.Status(status) == domain.Failed {
		return domain.ErrTerminal
	}
	if err := domain.BudgetFits(domain.TemplateBudgetMax, 0, budget); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE tasks.tasks
		SET budget = $2
		WHERE id = $1::uuid`, taskID, budget)
	if err != nil {
		return fmt.Errorf("tasks: fund: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return app.ErrGone
	}
	return tx.Commit(ctx)
}

// Spend charges the task and pauses it when the budget is exhausted.
func (s Store) Spend(ctx context.Context, access app.Access, taskID string, amount int) error {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var budget, spent int
	var paused bool
	err = tx.QueryRow(ctx, `
		SELECT budget, spent, paused
		FROM tasks.tasks
		WHERE id = $1::uuid
		FOR UPDATE`, taskID).Scan(&budget, &spent, &paused)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrGone
	}
	if err != nil {
		return fmt.Errorf("tasks: lock spend: %w", err)
	}
	nextSpent, nextPaused, chargeErr := domain.Charge(budget, spent, amount, paused)
	if nextSpent != spent || nextPaused != paused {
		tag, err := tx.Exec(ctx, `
			UPDATE tasks.tasks
			SET spent = $2, paused = $3
			WHERE id = $1::uuid`, taskID, nextSpent, nextPaused)
		if err != nil {
			return fmt.Errorf("tasks: spend: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return app.ErrGone
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return chargeErr
}

// Taint marks the task. The database spreads the mark along the tree.
func (s Store) Taint(ctx context.Context, access app.Access, taskID string) error {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var tainted bool
	err = tx.QueryRow(ctx, `
		SELECT tainted
		FROM tasks.tasks
		WHERE id = $1::uuid
		FOR UPDATE`, taskID).Scan(&tainted)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrGone
	}
	if err != nil {
		return fmt.Errorf("tasks: lock taint: %w", err)
	}
	if tainted {
		return tx.Commit(ctx)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE tasks.tasks
		SET tainted = true
		WHERE id = $1::uuid`, taskID)
	if err != nil {
		return fmt.Errorf("tasks: taint: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return app.ErrGone
	}
	return tx.Commit(ctx)
}
