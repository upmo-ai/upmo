package app

import (
	"context"
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

func TestFundRejectsABadBudgetBeforeTheBook(t *testing.T) {
	book := &fakeLedger{}
	access := Access{UserID: userID, Allow: "task:edit"}
	taskID := "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"
	if err := Fund(context.Background(), book, access, taskID, 0); !errors.Is(err, domain.ErrBudget) {
		t.Fatalf("zero budget error = %v, want %v", err, domain.ErrBudget)
	}
	if err := Fund(context.Background(), book, Access{UserID: userID}, taskID, 10); !errors.Is(err, ErrAccess) {
		t.Fatalf("empty allow error = %v, want %v", err, ErrAccess)
	}
	if book.funds != 0 {
		t.Fatalf("fund calls = %d, want 0", book.funds)
	}
}

func TestSpendRejectsAZeroAmountBeforeTheBook(t *testing.T) {
	book := &fakeLedger{}
	access := Access{UserID: userID, Allow: "task:edit"}
	taskID := "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"
	if err := Spend(context.Background(), book, access, taskID, 0); !errors.Is(err, domain.ErrBudget) {
		t.Fatalf("zero spend error = %v, want %v", err, domain.ErrBudget)
	}
	if book.spends != 0 {
		t.Fatalf("spend calls = %d, want 0", book.spends)
	}
}

type fakeLedger struct {
	funds  int
	spends int
}

func (f *fakeLedger) Fund(context.Context, Access, string, int64) error {
	f.funds++
	return nil
}

func (f *fakeLedger) Spend(context.Context, Access, string, int64) error {
	f.spends++
	return nil
}

func (f *fakeLedger) Taint(context.Context, Access, string) error {
	return nil
}
