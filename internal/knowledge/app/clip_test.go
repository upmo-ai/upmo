package app

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type clipStore struct {
	calls      int
	clipID     string
	clipFolder string
	current    Current
	saved      Draft
	pieces     []Piece
}

func (s *clipStore) InTx(_ context.Context, _ Access, fn func(Tx) error) error {
	s.calls++
	return fn(s)
}

func (s *clipStore) CreateFolder(context.Context, Access, string) (string, error) {
	return folderID, nil
}

func (s *clipStore) Current(context.Context, string, string) (Current, error) {
	return s.current, nil
}

func (s *clipStore) FindClip(context.Context, string) (string, string, error) {
	return s.clipID, s.clipFolder, nil
}

func (s *clipStore) Save(_ context.Context, draft Draft, _ [32]byte, pieces []Piece) (Result, error) {
	s.saved = draft
	s.pieces = pieces
	id := sourceID
	if draft.SourceID != "" {
		id = draft.SourceID
	}
	return Result{ID: id, Revision: 1, Chunks: len(pieces)}, nil
}

func TestIngestClipCreatesWebProvenance(t *testing.T) {
	store := &clipStore{}
	got, err := IngestClip(context.Background(), store, allowAll, nil, nil, ClipDraft{
		FolderID: folderID,
		URL:      "https://Example.com/Page#frag",
		Title:    "Page",
		Markdown: "Clipped body from Pounce.",
	})
	if err != nil || got.ID != sourceID || got.Chunks < 1 {
		t.Fatalf("got %#v err %v", got, err)
	}
	if store.saved.Kind != "clip" || store.saved.Provenance != "web" {
		t.Fatalf("saved %#v", store.saved)
	}
	if store.saved.SourceRef != "https://example.com/Page" {
		t.Fatalf("ref %q", store.saved.SourceRef)
	}
}

func TestIngestClipRevisesSameURL(t *testing.T) {
	existing := "018f3b2c-7a10-7c4a-8e11-cccccccccccc"
	store := &clipStore{clipID: existing, clipFolder: folderID}
	got, err := IngestClip(context.Background(), store, allowAll, nil, nil, ClipDraft{
		FolderID: folderID,
		URL:      "https://example.com/Page",
		Markdown: "Updated clip body.",
	})
	if err != nil || got.ID != existing || store.saved.SourceID != existing {
		t.Fatalf("got %#v saved %#v err %v", got, store.saved, err)
	}
}

func TestIngestClipRefusesOversized(t *testing.T) {
	store := &clipStore{}
	_, err := IngestClip(context.Background(), store, allowAll, nil, nil, ClipDraft{
		FolderID: folderID,
		URL:      "https://example.com/x",
		Markdown: strings.Repeat("a", maxClip+1),
	})
	if !errors.Is(err, ErrFileSize) || store.calls != 0 {
		t.Fatalf("err %v calls %d", err, store.calls)
	}
}

func TestIngestClipOtherFolderIsRefused(t *testing.T) {
	store := &clipStore{
		clipID:     sourceID,
		clipFolder: "018f3b2c-7a10-7c4a-8e11-ffffffffffff",
	}
	_, err := IngestClip(context.Background(), store, allowAll, nil, nil, ClipDraft{
		FolderID: folderID,
		URL:      "https://example.com/x",
		Markdown: "body",
	})
	if !errors.Is(err, ErrFolder) {
		t.Fatalf("err %v", err)
	}
}
