package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/platform/netguard"
)

type pendingBook struct {
	pending Pending
	failed  string
	failMsg string
	err     error
}

func (p *pendingBook) CreatePending(_ context.Context, _ Access, pending Pending) (string, error) {
	if p.err != nil {
		return "", p.err
	}
	p.pending = pending
	return sourceID, nil
}

func (p *pendingBook) MarkFailed(_ context.Context, _ Access, id, message string) error {
	p.failed = id
	p.failMsg = message
	return p.err
}

func (p *pendingBook) ResetPending(context.Context, Access, string) error {
	return p.err
}

func (p *pendingBook) SetBlobKey(context.Context, Access, string, string) error {
	return p.err
}

type urlStore struct {
	ingestStore
	body string
}

func (s *urlStore) InTx(_ context.Context, _ Access, fn func(Tx) error) error {
	return fn(urlTx{store: s})
}

type urlTx struct {
	store *urlStore
}

func (urlTx) Current(context.Context, string, string) (Current, error) {
	return Current{Found: true, ID: sourceID, Revision: 0}, nil
}

func (t urlTx) Save(_ context.Context, draft Draft, _ [32]byte, pieces []Piece) (Result, error) {
	t.store.body = draft.Body
	return Result{ID: sourceID, Revision: 1, Chunks: len(pieces)}, nil
}

func (urlTx) FindClip(context.Context, string) (string, string, error) {
	return "", "", nil
}

type fakeFetch struct {
	ctype string
	body  []byte
	err   error
}

func (f fakeFetch) Fetch(context.Context, string) (string, []byte, error) {
	return f.ctype, f.body, f.err
}

func TestIngestURLCreatesPending(t *testing.T) {
	book := &pendingBook{}
	got, err := IngestURL(context.Background(), book, allowAll, URLDraft{
		FolderID: folderID,
		URL:      "https://example.com/guide.md",
		Title:    "Guide",
	})
	if err != nil || got.ID != sourceID || got.Status != "pending" {
		t.Fatalf("got %#v err %v", got, err)
	}
	if book.pending.Kind != "url" || book.pending.Provenance != "curated" || book.pending.SourceRef != "https://example.com/guide.md" {
		t.Fatalf("pending %#v", book.pending)
	}
}

func TestIngestURLRefusesPrivateHost(t *testing.T) {
	book := &pendingBook{}
	_, err := IngestURL(context.Background(), book, allowAll, URLDraft{
		FolderID: folderID,
		URL:      "http://127.0.0.1/secret",
	})
	if !errors.Is(err, ErrURLBlocked) || book.pending.Kind != "" {
		t.Fatalf("err %v pending %#v", err, book.pending)
	}
}

func TestIngestURLRefusesLocalhost(t *testing.T) {
	book := &pendingBook{}
	_, err := IngestURL(context.Background(), book, allowAll, URLDraft{
		FolderID: folderID,
		URL:      "http://localhost/doc",
	})
	if !errors.Is(err, ErrURLBlocked) {
		t.Fatalf("err %v", err)
	}
}

func TestProcessURLReadyFromPlainText(t *testing.T) {
	book := &pendingBook{}
	store := &urlStore{}
	err := ProcessURL(
		context.Background(),
		book,
		store,
		allowAll,
		fakeFetch{ctype: "text/plain; charset=utf-8", body: []byte("Ada prefers oolong tea.")},
		PassThroughText{},
		nil,
		nil,
		folderID,
		sourceID,
		"https://example.com/guide.txt",
		"Guide",
	)
	if err != nil || book.failed != "" || store.body != "Ada prefers oolong tea." {
		t.Fatalf("err %v failed %q body %q", err, book.failed, store.body)
	}
}

func TestProcessURLMarksFailedOnBlockedFetch(t *testing.T) {
	book := &pendingBook{}
	err := ProcessURL(
		context.Background(),
		book,
		&urlStore{},
		allowAll,
		fakeFetch{err: netguard.ErrBlocked},
		PassThroughText{},
		nil,
		nil,
		folderID,
		sourceID,
		"https://example.com/",
		"",
	)
	if err != nil || book.failed != sourceID || book.failMsg != "url blocked" {
		t.Fatalf("err %v failed %q msg %q", err, book.failed, book.failMsg)
	}
}

func TestProcessURLMarksFailedOnHTMLWithoutSidecar(t *testing.T) {
	book := &pendingBook{}
	store := &urlStore{}
	err := ProcessURL(
		context.Background(),
		book,
		store,
		allowAll,
		fakeFetch{ctype: "text/html", body: []byte("<p>hi</p>")},
		PassThroughText{},
		nil,
		nil,
		folderID,
		sourceID,
		"https://example.com/page",
		"",
	)
	if err != nil || book.failed != sourceID || !strings.Contains(book.failMsg, "conversion") || store.body != "" {
		t.Fatalf("err %v failed %q msg %q body %q", err, book.failed, book.failMsg, store.body)
	}
}

func TestPassThroughTextStripsNUL(t *testing.T) {
	got, err := PassThroughText{}.ToMarkdown(context.Background(), "text/markdown", []byte("hello\x00world"))
	if err != nil || got != "helloworld" {
		t.Fatalf("got %q err %v", got, err)
	}
}
