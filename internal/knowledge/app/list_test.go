package app

import (
	"context"
	"errors"
	"testing"
)

type fakeFolders struct {
	calls int
	rows  []Folder
}

func (f *fakeFolders) ListFolders(context.Context, Access) ([]Folder, error) {
	f.calls++
	return append([]Folder(nil), f.rows...), nil
}

func TestListFoldersNeedsAccess(t *testing.T) {
	book := &fakeFolders{rows: []Folder{{ID: "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaaa", Name: "Specs"}}}
	access := Access{UserID: "018f3b2c-7a10-7c4a-8e11-111111111111", Allow: "folder:view"}
	got, err := ListFolders(context.Background(), book, access)
	if err != nil || len(got) != 1 || book.calls != 1 || got[0].Name != "Specs" {
		t.Fatalf("got %#v err %v calls %d", got, err, book.calls)
	}
	if _, err := ListFolders(context.Background(), book, Access{UserID: access.UserID}); !errors.Is(err, ErrAccess) || book.calls != 1 {
		t.Fatalf("error = %v calls %d", err, book.calls)
	}
}
