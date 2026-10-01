package app

import (
	"context"
	"testing"
)

type fakeBook struct {
	calls int
	rows  []ListedRoute
}

func (f *fakeBook) List(context.Context) ([]ListedRoute, error) {
	f.calls++
	return append([]ListedRoute(nil), f.rows...), nil
}

func TestListRoutesReturnsRows(t *testing.T) {
	book := &fakeBook{rows: []ListedRoute{{Name: "memory-check", Capability: "decide", Decision: "yesno"}}}
	got, err := ListRoutes(context.Background(), book)
	if err != nil || len(got) != 1 || book.calls != 1 || got[0].Name != "memory-check" {
		t.Fatalf("got %#v err %v calls %d", got, err, book.calls)
	}
}

func TestListRoutesNilBookIsEmpty(t *testing.T) {
	got, err := ListRoutes(context.Background(), nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %#v err %v", got, err)
	}
}
