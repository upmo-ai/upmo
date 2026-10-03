package app

import (
	"context"
	"errors"
	"testing"
)

type deleteBook struct {
	calls    int
	folderID string
	sourceID string
	err      error
}

func (d *deleteBook) DeleteSource(_ context.Context, _ Access, folderID, sourceID string) error {
	d.calls++
	d.folderID = folderID
	d.sourceID = sourceID
	return d.err
}

func TestDeleteSourceCallsStore(t *testing.T) {
	book := &deleteBook{}
	if err := DeleteSource(context.Background(), book, allowAll, folderID, sourceID); err != nil {
		t.Fatal(err)
	}
	if book.calls != 1 || book.folderID != folderID || book.sourceID != sourceID {
		t.Fatalf("book %#v", book)
	}
}

func TestDeleteSourceRefusesAccess(t *testing.T) {
	book := &deleteBook{}
	err := DeleteSource(context.Background(), book, Access{}, folderID, sourceID)
	if !errors.Is(err, ErrAccess) || book.calls != 0 {
		t.Fatalf("err %v calls %d", err, book.calls)
	}
}

func TestDeleteSourceRefusesBadIDs(t *testing.T) {
	book := &deleteBook{}
	err := DeleteSource(context.Background(), book, allowAll, "not-a-uuid", sourceID)
	if !errors.Is(err, ErrFolder) || book.calls != 0 {
		t.Fatalf("err %v calls %d", err, book.calls)
	}
}
