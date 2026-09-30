package decisions

import (
	"context"
	"errors"
	"testing"
)

type fakeLister struct {
	calls int
	rows  []ListedGate
}

func (f *fakeLister) List(context.Context, Access) ([]ListedGate, error) {
	f.calls++
	return append([]ListedGate(nil), f.rows...), nil
}

func TestListGatesNeedsSettingsManage(t *testing.T) {
	book := &fakeLister{rows: []ListedGate{{Route: "memory.check"}}}
	access := Access{UserID: "018f3b2c-7a10-7c4a-8e11-111111111111", Allow: "settings:manage"}
	got, err := ListGates(context.Background(), book, access)
	if err != nil || len(got) != 1 || book.calls != 1 {
		t.Fatalf("got %#v err %v calls %d", got, err, book.calls)
	}
	if _, err := ListGates(context.Background(), book, Access{UserID: access.UserID, Allow: "task:view"}); !errors.Is(err, ErrAccess) || book.calls != 1 {
		t.Fatalf("error = %v calls %d", err, book.calls)
	}
}
