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

type fakeSources struct {
	calls    int
	folderID string
	rows     []Source
	err      error
}

func (f *fakeSources) ListSources(_ context.Context, _ Access, folderID string) ([]Source, error) {
	f.calls++
	f.folderID = folderID
	if f.err != nil {
		return nil, f.err
	}
	return append([]Source(nil), f.rows...), nil
}

func TestListSourcesNeedsAccessAndFolder(t *testing.T) {
	book := &fakeSources{rows: []Source{{ID: "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb", Title: "Guide", Revision: 1, UpdatedAt: "2026-10-01T00:00:00Z"}}}
	access := Access{UserID: "018f3b2c-7a10-7c4a-8e11-111111111111", Allow: "folder:view"}
	folder := "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaaa"
	got, err := ListSources(context.Background(), book, access, folder)
	if err != nil || len(got) != 1 || book.calls != 1 || book.folderID != folder {
		t.Fatalf("got %#v err %v calls %d folder %q", got, err, book.calls, book.folderID)
	}
	if _, err := ListSources(context.Background(), book, Access{UserID: access.UserID}, folder); !errors.Is(err, ErrAccess) || book.calls != 1 {
		t.Fatalf("error = %v calls %d", err, book.calls)
	}
	if _, err := ListSources(context.Background(), book, access, "not-a-uuid"); !errors.Is(err, ErrFolder) || book.calls != 1 {
		t.Fatalf("error = %v calls %d", err, book.calls)
	}
}

func TestListSourcesPropagatesGone(t *testing.T) {
	book := &fakeSources{err: ErrGone}
	access := Access{UserID: "018f3b2c-7a10-7c4a-8e11-111111111111", Allow: "folder:view"}
	if _, err := ListSources(context.Background(), book, access, "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaaa"); !errors.Is(err, ErrGone) || book.calls != 1 {
		t.Fatalf("error = %v calls %d", err, book.calls)
	}
}
