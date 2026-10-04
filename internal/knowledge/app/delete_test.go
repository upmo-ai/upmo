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
	blobKey  string
	err      error
}

func (d *deleteBook) DeleteSource(_ context.Context, _ Access, folderID, sourceID string) (string, error) {
	d.calls++
	d.folderID = folderID
	d.sourceID = sourceID
	return d.blobKey, d.err
}

type deleteOriginals struct {
	removed string
	calls   int
}

func (o *deleteOriginals) Remove(_ context.Context, key string) error {
	o.calls++
	o.removed = key
	return nil
}

func TestDeleteSourceCallsStore(t *testing.T) {
	book := &deleteBook{}
	if err := DeleteSource(context.Background(), book, nil, allowAll, folderID, sourceID); err != nil {
		t.Fatal(err)
	}
	if book.calls != 1 || book.folderID != folderID || book.sourceID != sourceID {
		t.Fatalf("book %#v", book)
	}
}

func TestDeleteSourceRemovesBlob(t *testing.T) {
	book := &deleteBook{blobKey: sourceID}
	orig := &deleteOriginals{}
	if err := DeleteSource(context.Background(), book, orig, allowAll, folderID, sourceID); err != nil {
		t.Fatal(err)
	}
	if orig.calls != 1 || orig.removed != sourceID {
		t.Fatalf("originals %#v", orig)
	}
}

func TestDeleteSourceRefusesAccess(t *testing.T) {
	book := &deleteBook{}
	err := DeleteSource(context.Background(), book, nil, Access{}, folderID, sourceID)
	if !errors.Is(err, ErrAccess) || book.calls != 0 {
		t.Fatalf("err %v calls %d", err, book.calls)
	}
}

func TestDeleteSourceRefusesBadIDs(t *testing.T) {
	book := &deleteBook{}
	err := DeleteSource(context.Background(), book, nil, allowAll, "not-a-uuid", sourceID)
	if !errors.Is(err, ErrFolder) || book.calls != 0 {
		t.Fatalf("err %v calls %d", err, book.calls)
	}
}
