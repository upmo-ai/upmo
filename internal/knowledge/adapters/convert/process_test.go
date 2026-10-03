package convert_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/upmo-ai/upmo/internal/knowledge/adapters/convert"
	"github.com/upmo-ai/upmo/internal/knowledge/app"
)

const (
	userID   = "018f3b2c-7a10-7c4a-8e11-111111111111"
	folderID = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaaa"
	sourceID = "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"
)

type book struct {
	failed string
	body   string
}

func (b *book) CreatePending(context.Context, app.Access, app.Pending) (string, error) {
	return sourceID, nil
}

func (b *book) MarkFailed(_ context.Context, _ app.Access, id, _ string) error {
	b.failed = id
	return nil
}

func (b *book) InTx(_ context.Context, _ app.Access, fn func(app.Tx) error) error {
	return fn(b)
}

func (b *book) CreateFolder(context.Context, app.Access, string) (string, error) {
	return folderID, nil
}

func (b *book) Current(context.Context, string, string) (app.Current, error) {
	return app.Current{Found: true, ID: sourceID}, nil
}

func (b *book) FindClip(context.Context, string) (string, string, error) {
	return "", "", nil
}

func (b *book) Save(_ context.Context, draft app.Draft, _ [32]byte, pieces []app.Piece) (app.Result, error) {
	b.body = draft.Body
	return app.Result{ID: sourceID, Revision: 1, Chunks: len(pieces)}, nil
}

func TestProcessFilePDFUsesSidecar(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"markdown":"Ada prefers oolong tea."}`))
	}))
	t.Cleanup(srv.Close)
	store := &book{}
	access := app.Access{UserID: userID, Allow: "folder:edit"}
	conv := convert.Client{Sidecar: &convert.Sidecar{BaseURL: srv.URL}}
	err := app.ProcessFile(context.Background(), store, store, access, conv, nil, nil,
		folderID, sourceID, "guide.pdf", "Guide", []byte("%PDF-1.4 fixture"))
	if err != nil || store.failed != "" || store.body != "Ada prefers oolong tea." {
		t.Fatalf("err %v failed %q body %q", err, store.failed, store.body)
	}
}

func TestProcessFilePDFFailsWhenSidecarDown(t *testing.T) {
	store := &book{}
	access := app.Access{UserID: userID, Allow: "folder:edit"}
	conv := convert.Client{Sidecar: &convert.Sidecar{BaseURL: "http://127.0.0.1:1"}}
	err := app.ProcessFile(context.Background(), store, store, access, conv, nil, nil,
		folderID, sourceID, "guide.pdf", "Guide", []byte("%PDF-1.4 fixture"))
	if err != nil || store.failed != sourceID || store.body != "" {
		t.Fatalf("err %v failed %q body %q", err, store.failed, store.body)
	}
}
