package app

import (
	"context"
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

func TestSaveResultNeutralisesBeforeTheBook(t *testing.T) {
	book := &fakeRecorder{}
	access := Access{UserID: userID, Allow: "task:edit"}
	taskID := "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"
	if err := SaveResult(context.Background(), book, access, taskID, "  See {{prior}}  ", "note"); err != nil {
		t.Fatal(err)
	}
	if book.saved.Summary != "See { {prior}}" || book.saved.Detail != "note" {
		t.Fatalf("saved = %+v", book.saved)
	}
	if err := SaveResult(context.Background(), book, access, taskID, "  ", "note"); !errors.Is(err, domain.ErrResult) {
		t.Fatalf("empty summary error = %v, want %v", err, domain.ErrResult)
	}
	if book.saves != 1 {
		t.Fatalf("saves = %d, want 1", book.saves)
	}
}

func TestClaimRejectsABlankNameBeforeTheBook(t *testing.T) {
	book := &fakeRecorder{}
	access := Access{UserID: userID, Allow: "task:edit"}
	taskID := "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"
	if err := Claim(context.Background(), book, access, taskID, "  "); !errors.Is(err, domain.ErrWorktree) {
		t.Fatalf("blank name error = %v, want %v", err, domain.ErrWorktree)
	}
	if book.claims != 0 {
		t.Fatalf("claims = %d, want 0", book.claims)
	}
}

type fakeRecorder struct {
	saves  int
	claims int
	saved  domain.Result
}

func (f *fakeRecorder) SaveResult(_ context.Context, _ Access, _ string, result domain.Result) error {
	f.saves++
	f.saved = result
	return nil
}

func (f *fakeRecorder) Claim(context.Context, Access, string, string) error {
	f.claims++
	return nil
}
