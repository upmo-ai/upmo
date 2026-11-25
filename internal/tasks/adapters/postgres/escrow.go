package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

// Fund raises a non-terminal root budget. An equal amount is a no-op.
func (s Store) Fund(ctx context.Context, access app.Access, taskID string, budget int64) error {
	if err := domain.BudgetFits(domain.TemplateBudgetMax, 0, budget); err != nil {
		return err
	}
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var isChild, paused bool
	var current, held, spent int64
	var status string
	err = tx.QueryRow(ctx, `
		SELECT parent_id IS NOT NULL, budget, held, spent, paused, status
		FROM tasks.tasks
		WHERE id = $1::uuid
		FOR UPDATE`, taskID).Scan(&isChild, &current, &held, &spent, &paused, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrGone
	}
	if err != nil {
		return fmt.Errorf("tasks: lock fund: %w", err)
	}
	if isChild || budget < current {
		return domain.ErrBudget
	}
	if budget == current {
		return tx.Commit(ctx)
	}
	if domain.Status(status) == domain.Done || domain.Status(status) == domain.Failed {
		return domain.ErrTerminal
	}
	nextPaused := paused
	// spent+held wraps when spent is near MaxInt64 and a child hold is live.
	if spent < budget && held < budget-spent {
		nextPaused = false
	}
	tag, err := tx.Exec(ctx, `
		UPDATE tasks.tasks
		SET budget = $2, paused = $3, updated_at = now()
		WHERE id = $1::uuid`, taskID, budget, nextPaused)
	if err != nil {
		return fmt.Errorf("tasks: fund: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return app.ErrGone
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.TaskUpdate,
		Outcome:  audit.Success,
		TargetID: taskID,
		Changes: audit.Changes{
			Fields: []string{"budget"},
			Values: map[string]audit.Change{"budget": {From: current, To: budget}},
		},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Spend charges the task. Reaching or crossing the budget pauses and keeps the full spend.
func (s Store) Spend(ctx context.Context, access app.Access, taskID string, amount int64) error {
	if amount < 1 {
		return domain.ErrBudget
	}
	tx, err := s.beginOwner(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var budget, spent int64
	var paused bool
	var ownerID string
	err = tx.QueryRow(ctx, `
		SELECT budget, spent, paused, owner_user_id::text
		FROM tasks.tasks
		WHERE id = $1::uuid
		FOR UPDATE`, taskID).Scan(&budget, &spent, &paused, &ownerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrGone
	}
	if err != nil {
		return fmt.Errorf("tasks: lock spend: %w", err)
	}
	if ownerID != access.UserID {
		return app.ErrAccess
	}
	nextSpent, nextPaused, chargeErr := domain.Charge(budget, spent, amount, paused)
	if chargeErr != nil {
		return chargeErr
	}
	tag, err := tx.Exec(ctx, spendUpdateSQL, taskID, nextSpent, nextPaused)
	if err != nil {
		return fmt.Errorf("tasks: spend: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return app.ErrGone
	}
	return tx.Commit(ctx)
}

// Taint marks the task. The database spreads the mark along the tree.
func (s Store) Taint(ctx context.Context, access app.Access, taskID string) error {
	tx, err := s.beginOwner(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var tainted bool
	var ownerID string
	err = tx.QueryRow(ctx, `
		SELECT tainted, owner_user_id::text
		FROM tasks.tasks
		WHERE id = $1::uuid
		FOR UPDATE`, taskID).Scan(&tainted, &ownerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrGone
	}
	if err != nil {
		return fmt.Errorf("tasks: lock taint: %w", err)
	}
	if ownerID != access.UserID {
		return app.ErrAccess
	}
	if tainted {
		return tx.Commit(ctx)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE tasks.tasks
		SET tainted = true
		WHERE id = $1::uuid
		  AND owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid`, taskID)
	if err != nil {
		return fmt.Errorf("tasks: taint: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return app.ErrGone
	}
	return tx.Commit(ctx)
}
