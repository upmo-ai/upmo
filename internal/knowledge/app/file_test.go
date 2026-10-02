package app

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestIngestFileCreatesPending(t *testing.T) {
	book := &pendingBook{}
	got, err := IngestFile(context.Background(), book, allowAll, FileDraft{
		FolderID: folderID,
		Title:    "Guide",
		Name:     "guide.md",
		Bytes:    []byte("# Hello"),
	})
	if err != nil || got.Status != "pending" {
		t.Fatalf("got %#v err %v", got, err)
	}
	if book.pending.Kind != "file" || book.pending.SourceRef != "guide.md" || book.pending.Provenance != "curated" {
		t.Fatalf("pending %#v", book.pending)
	}
}

func TestIngestFileRefusesUnknownExtension(t *testing.T) {
	book := &pendingBook{}
	_, err := IngestFile(context.Background(), book, allowAll, FileDraft{
		FolderID: folderID,
		Name:     "guide.exe",
		Bytes:    []byte("x"),
	})
	if !errors.Is(err, ErrFile) || book.pending.Kind != "" {
		t.Fatalf("err %v pending %#v", err, book.pending)
	}
}

func TestIngestFileRefusesOversized(t *testing.T) {
	book := &pendingBook{}
	raw := make([]byte, maxUpload+1)
	_, err := IngestFile(context.Background(), book, allowAll, FileDraft{
		FolderID: folderID,
		Name:     "big.txt",
		Bytes:    raw,
	})
	if !errors.Is(err, ErrFileSize) {
		t.Fatalf("err %v", err)
	}
}

func TestProcessFileMarkdownSkipsSidecar(t *testing.T) {
	book := &pendingBook{}
	store := &urlStore{}
	err := ProcessFile(
		context.Background(),
		book,
		store,
		allowAll,
		PassThroughText{},
		nil,
		nil,
		folderID,
		sourceID,
		"guide.md",
		"Guide",
		[]byte("Ada prefers oolong tea."),
	)
	if err != nil || book.failed != "" || store.body != "Ada prefers oolong tea." {
		t.Fatalf("err %v failed %q body %q", err, book.failed, store.body)
	}
}

func TestProcessFilePDFFailsWithoutSidecar(t *testing.T) {
	book := &pendingBook{}
	store := &urlStore{}
	err := ProcessFile(
		context.Background(),
		book,
		store,
		allowAll,
		PassThroughText{},
		nil,
		nil,
		folderID,
		sourceID,
		"guide.pdf",
		"Guide",
		[]byte("%PDF-1.4"),
	)
	if err != nil || book.failed != sourceID || !strings.Contains(book.failMsg, "conversion") || store.body != "" {
		t.Fatalf("err %v failed %q msg %q body %q", err, book.failed, book.failMsg, store.body)
	}
}
