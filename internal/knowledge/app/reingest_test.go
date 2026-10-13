package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type reingestBook struct {
	clipStore
	pendingBook
	detail SourceDetail
	viewed int
}

func (r *reingestBook) ViewSource(context.Context, Access, string, string) (SourceDetail, error) {
	r.viewed++
	if r.err != nil {
		return SourceDetail{}, r.err
	}
	return r.detail, nil
}

func (r *reingestBook) ListSources(context.Context, Access, string) ([]Source, error) {
	return nil, nil
}

func (r *reingestBook) InTx(_ context.Context, _ Access, fn func(Tx) error) error {
	r.calls++
	return fn(&r.clipStore)
}

func TestReingestURLResetsPendingAsync(t *testing.T) {
	book := &reingestBook{
		detail: SourceDetail{
			ID: sourceID, Title: "Guide", Kind: "url", SourceRef: "https://example.com/x",
			Status: "failed", Error: "fetch failed",
		},
	}
	ran := false
	got, err := Reingest(
		context.Background(),
		book,
		allowAll,
		fakeFetch{ctype: "text/plain", body: []byte("Ada prefers oolong tea.")},
		PassThroughText{},
		nil,
		nil,
		nil,
		folderID,
		sourceID,
		func(fn func()) { ran = true; fn() },
	)
	if err != nil || !got.Async || got.Status != "pending" || !ran {
		t.Fatalf("got %#v err %v ran %v", got, err, ran)
	}
	if book.saved.Body != "Ada prefers oolong tea." {
		t.Fatalf("body %q", book.saved.Body)
	}
}

func TestReingestMarkdownFromBody(t *testing.T) {
	book := &reingestBook{
		detail: SourceDetail{
			ID: sourceID, Title: "Guide", Kind: "markdown", Body: "Ada prefers oolong tea.",
			Status: "failed", Provenance: "curated",
		},
	}
	got, err := Reingest(context.Background(), book, allowAll, nil, nil, nil, nil, nil, folderID, sourceID, nil)
	if err != nil || got.Async || got.Status != "ready" || got.Chunks < 1 {
		t.Fatalf("got %#v err %v", got, err)
	}
}

func TestReingestFileFromBlobAsync(t *testing.T) {
	book := &reingestBook{
		detail: SourceDetail{
			ID: sourceID, Title: "Guide", Kind: "file", SourceRef: "guide.md",
			Status: "failed", BlobKey: sourceID, Error: "conversion failed",
		},
	}
	ran := false
	got, err := Reingest(
		context.Background(),
		book,
		allowAll,
		nil,
		PassThroughText{},
		nil,
		nil,
		memBlobs{sourceID: []byte("Ada prefers oolong tea.")},
		folderID,
		sourceID,
		func(fn func()) { ran = true; fn() },
	)
	if err != nil || !got.Async || got.Status != "pending" || !ran {
		t.Fatalf("got %#v err %v ran %v", got, err, ran)
	}
	if book.saved.Body != "Ada prefers oolong tea." {
		t.Fatalf("body %q", book.saved.Body)
	}
}

func TestReingestFileWithoutBlobOrBodyRefused(t *testing.T) {
	book := &reingestBook{
		detail: SourceDetail{ID: sourceID, Kind: "file", Status: "failed", SourceRef: "x.pdf"},
	}
	_, err := Reingest(context.Background(), book, allowAll, nil, nil, nil, nil, nil, folderID, sourceID, nil)
	if !errors.Is(err, ErrReingest) {
		t.Fatalf("err %v", err)
	}
}

func TestReingestPendingIsRefused(t *testing.T) {
	book := &reingestBook{
		detail: SourceDetail{ID: sourceID, Kind: "url", Status: "pending", SourceRef: "https://example.com/x"},
	}
	_, err := Reingest(context.Background(), book, allowAll, nil, nil, nil, nil, nil, folderID, sourceID, nil)
	if !errors.Is(err, ErrReingest) {
		t.Fatalf("err %v", err)
	}
}

func TestReingestGone(t *testing.T) {
	book := &reingestBook{pendingBook: pendingBook{err: ErrGone}}
	_, err := Reingest(context.Background(), book, allowAll, nil, nil, nil, nil, nil, folderID, sourceID, nil)
	if !errors.Is(err, ErrGone) {
		t.Fatalf("err %v", err)
	}
}

type memBlobs map[string][]byte

func (m memBlobs) Open(_ context.Context, key string) (io.ReadCloser, error) {
	raw, ok := m[key]
	if !ok {
		return nil, ErrReingest
	}
	return io.NopCloser(strings.NewReader(string(raw))), nil
}
