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
	Fund(ctx context.Context, access Access, taskID string, budget int) error
	Spend(ctx context.Context, access Access, taskID string, amount int) error
	Taint(ctx context.Context, access Access, taskID string) error
}

// MicroUSDLedger funds and charges tasks in micro-USD (1e-6 USD).
type MicroUSDLedger interface {
	FundMicroUSD(ctx context.Context, access Access, taskID string, budgetMicroUSD int64) error
	ChargeMicroUSD(ctx context.Context, access Access, taskID string, amountMicroUSD int64) (spent int64, paused bool, err error)
}

// Fund sets a root budget once. A child budget comes only from escrow.
func Fund(ctx context.Context, book Ledger, access Access, taskID string, budget int) error {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return ErrAccess
	}
	if _, err := uuid.Parse(taskID); err != nil {
		return ErrTask
	}
	if err := kernel.CheckRootBudget(int64(budget), int64(domain.TemplateBudgetMax)); err != nil {
		if errors.Is(err, kernel.ErrBudget) {
			return domain.ErrBudget
		}
		return err
	}
	return book.Fund(ctx, access, taskID, budget)
}

// FundMicroUSD sets a root budget in micro-USD (1e-6 USD) once.
func FundMicroUSD(ctx context.Context, book MicroUSDLedger, access Access, taskID string, budgetMicroUSD int64) error {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return ErrAccess
	}
	if _, err := uuid.Parse(taskID); err != nil {
		return ErrTask
	}
	if err := kernel.CheckRootBudget(budgetMicroUSD, kernel.DefaultMaxBudgetMicroUSD); err != nil {
		if errors.Is(err, kernel.ErrBudget) {
			return domain.ErrBudget
		}
		return err
	}
	return book.FundMicroUSD(ctx, access, taskID, budgetMicroUSD)
}

// ChargeMicroUSD records actual post-call spend in micro-USD and pauses if
// spent crosses budget - spent while preserving full actual spend.
func ChargeMicroUSD(ctx context.Context, book MicroUSDLedger, access Access, taskID string, amountMicroUSD int64) (int64, bool, error) {
	if _, err := uuid.Parse(access.UserID); err != nil || access.Allow == "" {
		return 0, false, ErrAccess
	}
	if _, err := uuid.Parse(taskID); err != nil {
		return 0, false, ErrTask
	}
	if amountMicroUSD < 0 {
		return 0, false, domain.ErrBudget
	}
	return book.ChargeMicroUSD(ctx, access, taskID, amountMicroUSD)
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
