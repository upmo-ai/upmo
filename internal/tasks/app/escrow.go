package app

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/kernel"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

// Ledger funds a root, spends a budget, and marks taint.
type Ledger interface {
	Fund(ctx context.Context, access Access, taskID string, budget int64) error
	Spend(ctx context.Context, access Access, taskID string, amount int64) error
	Taint(ctx context.Context, access Access, taskID string) error
}

// Fund raises a non-terminal root budget. A child budget comes only from escrow.
func Fund(ctx context.Context, book Ledger, access Access, taskID string, budget int64) error {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return ErrAccess
	}
	if _, err := uuid.Parse(taskID); err != nil {
		return ErrTask
	}
	if err := kernel.CheckRootBudget(budget, domain.TemplateBudgetMax); err != nil {
		if errors.Is(err, kernel.ErrBudget) {
			return domain.ErrBudget
		}
		return err
	}
	return book.Fund(ctx, access, taskID, budget)
}

// FundView funds the task and returns the read model of the result.
func FundView(ctx context.Context, book Ledger, view Viewer, access Access, taskID string, budget int64) (TaskView, error) {
	if err := Fund(ctx, book, access, taskID, budget); err != nil {
		return TaskView{}, err
	}
	return ViewTask(ctx, view, access, taskID)
}

// Spend charges a task. Exhaustion pauses it and writes one notice.
func Spend(ctx context.Context, book Ledger, access Access, taskID string, amount int64) error {
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
