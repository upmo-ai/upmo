package app

import (
	"context"
	"errors"
	"testing"
)

type folderBook struct {
	renames int
	deletes int
	name    string
	keys    []string
	err     error
}

func (f *folderBook) RenameFolder(_ context.Context, _ Access, _, name string) error {
	f.renames++
	f.name = name
	return f.err
}

func (f *folderBook) DeleteFolder(context.Context, Access, string) ([]string, error) {
	f.deletes++
	return append([]string(nil), f.keys...), f.err
}

func TestRenameFolder(t *testing.T) {
	book := &folderBook{}
	if err := RenameFolder(context.Background(), book, allowAll, folderID, " Specs "); err != nil {
		t.Fatal(err)
	}
	if book.renames != 1 || book.name != "Specs" {
		t.Fatalf("book %#v", book)
	}
}

func TestRenameFolderEmptyName(t *testing.T) {
	book := &folderBook{}
	if err := RenameFolder(context.Background(), book, allowAll, folderID, "  "); !errors.Is(err, ErrName) || book.renames != 0 {
		t.Fatalf("err %v renames %d", err, book.renames)
	}
}

func TestDeleteFolderRemovesBlobs(t *testing.T) {
	book := &folderBook{keys: []string{sourceID, ""}}
	orig := &deleteOriginals{}
	if err := DeleteFolder(context.Background(), book, orig, allowAll, folderID); err != nil {
		t.Fatal(err)
	}
	if book.deletes != 1 || orig.calls != 1 || orig.removed != sourceID {
		t.Fatalf("book %#v originals %#v", book, orig)
	}
}

func TestDeleteFolderRefusesAccess(t *testing.T) {
	book := &folderBook{}
	if err := DeleteFolder(context.Background(), book, nil, Access{}, folderID); !errors.Is(err, ErrAccess) || book.deletes != 0 {
		t.Fatalf("err %v deletes %d", err, book.deletes)
	}
}
