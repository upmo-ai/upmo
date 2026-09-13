package app

import (
	"context"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

// Ledger funds a root, spends a budget, and marks taint.
type Ledger interface {
	Fund(ctx context.Context, access Access, taskID string, budget int) error
	Spend(ctx context.Context, access Access, taskID string, amount int) error
	Taint(ctx context.Context, access Access, taskID string) error
}

// Fund sets a root budget once. A child budget comes only from escrow.
func Fund(ctx context.Context, book Ledger, access Access, taskID string, budget int) error {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return ErrAccess
	}
	if _, err := uuid.Parse(taskID); err != nil {
		return ErrTask
	}
	if err := domain.BudgetFits(domain.TemplateBudgetMax, 0, budget); err != nil {
		return err
	}
	return book.Fund(ctx, access, taskID, budget)
}

// Spend charges a task. Exhaustion pauses it and writes one notice.
func Spend(ctx context.Context, book Ledger, access Access, taskID string, amount int) error {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return ErrAccess
	}
	if _, err := uuid.Parse(taskID); err != nil {
		return ErrTask
	}
	if amount < 1 {
		return domain.ErrBudget
	}
	return book.Spend(ctx, access, taskID, amount)
}

// Taint marks a task. The store spreads that mark to the parent and the children.
func Taint(ctx context.Context, book Ledger, access Access, taskID string) error {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return ErrAccess
	}
	if _, err := uuid.Parse(taskID); err != nil {
		return ErrTask
	}
	return book.Taint(ctx, access, taskID)
}
