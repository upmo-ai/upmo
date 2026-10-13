package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/knowledge/app"
)

const (
	userID   = "018f3b2c-7a10-7c4a-8e11-111111111111"
	folderID = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaaa"
	sourceID = "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"
)

type fakeStore struct {
	folders int
	lists   int
	ingests int
	pending int
	failed  int
	deletes int
	renames int
	name    string
	rows    []app.Folder
	sources []app.Source
	detail  app.SourceDetail
	err     error
}

func (f *fakeStore) RenameFolder(_ context.Context, _ app.Access, _, name string) error {
	f.renames++
	f.name = name
	return f.err
}

func (f *fakeStore) DeleteFolder(context.Context, app.Access, string) ([]string, error) {
	f.deletes++
	return nil, f.err
}

func (f *fakeStore) DeleteSource(context.Context, app.Access, string, string) (string, error) {
	f.deletes++
	return "", f.err
}

func (f *fakeStore) SetBlobKey(context.Context, app.Access, string, string) error {
	return f.err
}

func (f *fakeStore) CreatePending(context.Context, app.Access, app.Pending) (string, error) {
	f.pending++
	if f.err != nil {
		return "", f.err
	}
	return sourceID, nil
}

func (f *fakeStore) MarkFailed(context.Context, app.Access, string, string) error {
	f.failed++
	return f.err
}

func (f *fakeStore) ResetPending(context.Context, app.Access, string) error {
	f.pending++
	return f.err
}

func (f *fakeStore) ListFolders(context.Context, app.Access) ([]app.Folder, error) {
	f.lists++
	if f.err != nil {
		return nil, f.err
	}
	return append([]app.Folder(nil), f.rows...), nil
}

func (f *fakeStore) ListSources(context.Context, app.Access, string) ([]app.Source, error) {
	f.lists++
	if f.err != nil {
		return nil, f.err
	}
	return append([]app.Source(nil), f.sources...), nil
}

func (f *fakeStore) ViewSource(context.Context, app.Access, string, string) (app.SourceDetail, error) {
	f.lists++
	if f.err != nil {
		return app.SourceDetail{}, f.err
	}
	return f.detail, nil
}

func (f *fakeStore) InTx(_ context.Context, _ app.Access, fn func(app.Tx) error) error {
	f.ingests++
	if f.err != nil {
		return f.err
	}
	return fn(fakeTx{})
}

func (f *fakeStore) CreateFolder(_ context.Context, _ app.Access, name string) (string, error) {
	f.folders++
	f.name = name
	if f.err != nil {
		return "", f.err
	}
	return folderID, nil
}

type fakeTx struct{}

func (fakeTx) Current(context.Context, string, string) (app.Current, error) {
	return app.Current{}, nil
}

func (fakeTx) Save(_ context.Context, _ app.Draft, _ [32]byte, pieces []app.Piece) (app.Result, error) {
	return app.Result{ID: sourceID, Revision: 1, Chunks: len(pieces)}, nil
}

func (fakeTx) FindClip(context.Context, string) (string, string, error) {
	return "", "", nil
}

type fakeFinder struct {
	calls int
	hits  []app.Lexical
	err   error
}

func (f *fakeFinder) Lexical(context.Context, app.Access, string, string, int) ([]app.Lexical, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.hits, nil
}

func (f *fakeFinder) Near(context.Context, app.Access, string, []float32, int) ([]app.Lexical, error) {
	return nil, nil
}

func TestCreateFolder(t *testing.T) {
	store := &fakeStore{}
	rec := call(t, store, nil, http.MethodPost, "/api/knowledge/folders", `{"name":" Specs "}`, true)
	if rec.Code != http.StatusCreated || store.folders != 1 || store.name != "Specs" {
		t.Fatalf("status %d folders %d name %q body %s", rec.Code, store.folders, store.name, rec.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got["id"] != folderID {
		t.Fatalf("got = %#v err %v", got, err)
	}
}

func TestRenameFolder(t *testing.T) {
	store := &fakeStore{}
	rec := call(t, store, nil, http.MethodPatch, "/api/knowledge/folders/"+folderID, `{"name":" Specs "}`, true)
	if rec.Code != http.StatusNoContent || store.renames != 1 || store.name != "Specs" {
		t.Fatalf("status %d renames %d name %q body %s", rec.Code, store.renames, store.name, rec.Body.String())
	}
}

func TestDeleteFolder(t *testing.T) {
	store := &fakeStore{}
	rec := call(t, store, nil, http.MethodDelete, "/api/knowledge/folders/"+folderID, "", true)
	if rec.Code != http.StatusNoContent || store.deletes != 1 {
		t.Fatalf("status %d deletes %d body %s", rec.Code, store.deletes, rec.Body.String())
	}
}

func TestIngestSource(t *testing.T) {
	store := &fakeStore{}
	rec := call(t, store, nil, http.MethodPost, "/api/knowledge/folders/"+folderID+"/sources", `{"title":"Guide","body":"Hello world"}`, true)
	if rec.Code != http.StatusCreated || store.ingests != 1 {
		t.Fatalf("status %d ingests %d body %s", rec.Code, store.ingests, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["id"] != sourceID || got["skipped"] != false {
		t.Fatalf("got = %#v", got)
	}
}

func TestSearchReturnsHits(t *testing.T) {
	store := &fakeStore{}
	finder := &fakeFinder{hits: []app.Lexical{{SourceID: sourceID, Seq: 1, Body: "Hello world"}}}
	rec := call(t, store, finder, http.MethodGet, "/api/knowledge/folders/"+folderID+"/search?q=Hello", "", true)
	if rec.Code != http.StatusOK || finder.calls != 1 {
		t.Fatalf("status %d calls %d body %s", rec.Code, finder.calls, rec.Body.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got) != 1 || got[0]["body"] != "Hello world" {
		t.Fatalf("got = %#v err %v", got, err)
	}
}

func TestSearchUsesEmbedForPerson(t *testing.T) {
	store := &fakeStore{}
	finder := &fakeFinder{hits: []app.Lexical{{SourceID: sourceID, Seq: 1, Body: "Hello world"}}}
	var bound string
	mux := http.NewServeMux()
	Handler{
		Store:   store,
		Folders: store,
		Sources: store,
		Delete:  store,
		Finder:  finder,
		EmbedFor: func(id string) app.Embedder {
			bound = id
			return nil
		},
		Access: func(*http.Request) (app.Access, bool) {
			return app.Access{UserID: userID, Allow: "folder:view"}, true
		},
	}.Mount(mux, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/knowledge/folders/"+folderID+"/search?q=Hello", nil))
	if rec.Code != http.StatusOK || bound != userID {
		t.Fatalf("status %d bound %q", rec.Code, bound)
	}
}

func TestBearerIsRefused(t *testing.T) {
	store := &fakeStore{}
	req := httptest.NewRequest(http.MethodPost, "/api/knowledge/folders", strings.NewReader(`{"name":"Specs"}`))
	req.Header.Set("Authorization", "Bearer upmo_pat_secret")
	rec := httptest.NewRecorder()
	handler(store, nil, true).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || store.folders != 0 {
		t.Fatalf("status %d folders %d", rec.Code, store.folders)
	}
}

func TestEmptyAllowDoesNotCallTheStore(t *testing.T) {
	store := &fakeStore{}
	rec := call(t, store, nil, http.MethodPost, "/api/knowledge/folders", `{"name":"Specs"}`, false)
	if rec.Code != http.StatusUnauthorized || store.folders != 0 {
		t.Fatalf("status %d folders %d", rec.Code, store.folders)
	}
}

func TestEmptyNameIsRefused(t *testing.T) {
	store := &fakeStore{}
	rec := call(t, store, nil, http.MethodPost, "/api/knowledge/folders", `{"name":"  "}`, true)
	if rec.Code != http.StatusBadRequest || store.folders != 0 {
		t.Fatalf("status %d folders %d", rec.Code, store.folders)
	}
}

func TestEmptyQueryIsRefused(t *testing.T) {
	store := &fakeStore{}
	finder := &fakeFinder{}
	rec := call(t, store, finder, http.MethodGet, "/api/knowledge/folders/"+folderID+"/search?q=%20", "", true)
	if rec.Code != http.StatusBadRequest || finder.calls != 0 {
		t.Fatalf("status %d calls %d", rec.Code, finder.calls)
	}
}

func call(t *testing.T, store *fakeStore, finder app.Finder, method, path, body string, allow bool) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	rec := httptest.NewRecorder()
	handler(store, finder, allow).ServeHTTP(rec, r)
	return rec
}

func handler(store *fakeStore, finder app.Finder, allow bool) http.Handler {
	mux := http.NewServeMux()
	Handler{
		Store:       store,
		Pending:     store,
		Folders:     store,
		Mutate:      store,
		Sources:     store,
		Delete:      store,
		Finder:      finder,
		Fetcher:     stubFetch{ctype: "text/plain", body: []byte("Ada prefers oolong tea.")},
		Converter:   app.PassThroughText{},
		Blobs:       &memBlobs{data: map[string][]byte{}},
		AfterAccept: func(fn func()) { fn() },
		Access: func(*http.Request) (app.Access, bool) {
			if !allow {
				return app.Access{}, false
			}
			return app.Access{UserID: userID, Allow: "folder:create,folder:view,folder:edit,folder:delete"}, true
		},
	}.Mount(mux, nil)
	return mux
}

type memBlobs struct {
	data map[string][]byte
}

func (m *memBlobs) Put(_ context.Context, key string, raw []byte) error {
	m.data[key] = append([]byte(nil), raw...)
	return nil
}

func (m *memBlobs) Open(_ context.Context, key string) (io.ReadCloser, error) {
	raw, ok := m.data[key]
	if !ok {
		return nil, errors.New("missing")
	}
	return io.NopCloser(strings.NewReader(string(raw))), nil
}

func (m *memBlobs) Remove(_ context.Context, key string) error {
	delete(m.data, key)
	return nil
}

func TestDeleteSourceNoContent(t *testing.T) {
	store := &fakeStore{}
	rec := call(t, store, nil, http.MethodDelete, "/api/knowledge/folders/"+folderID+"/sources/"+sourceID, "", true)
	if rec.Code != http.StatusNoContent || store.deletes != 1 {
		t.Fatalf("status %d deletes %d body %s", rec.Code, store.deletes, rec.Body.String())
	}
}

func TestDeleteSourceGone(t *testing.T) {
	store := &fakeStore{err: app.ErrGone}
	rec := call(t, store, nil, http.MethodDelete, "/api/knowledge/folders/"+folderID+"/sources/"+sourceID, "", true)
	if rec.Code != http.StatusNotFound || store.deletes != 1 {
		t.Fatalf("status %d deletes %d", rec.Code, store.deletes)
	}
}

func TestDeleteSourceEmptyAllowIsRefused(t *testing.T) {
	store := &fakeStore{}
	rec := call(t, store, nil, http.MethodDelete, "/api/knowledge/folders/"+folderID+"/sources/"+sourceID, "", false)
	if rec.Code != http.StatusUnauthorized || store.deletes != 0 {
		t.Fatalf("status %d deletes %d", rec.Code, store.deletes)
	}
}

func TestReingestURLAccepted(t *testing.T) {
	store := &fakeStore{detail: app.SourceDetail{
		ID: sourceID, Title: "Guide", Kind: "url", SourceRef: "https://example.com/guide.txt",
		Status: "failed", Error: "fetch failed",
	}}
	rec := call(t, store, nil, http.MethodPost, "/api/knowledge/folders/"+folderID+"/sources/"+sourceID+"/reingest", "", true)
	if rec.Code != http.StatusAccepted || store.pending < 1 || store.ingests != 1 {
		t.Fatalf("status %d pending %d ingests %d body %s", rec.Code, store.pending, store.ingests, rec.Body.String())
	}
}

func TestReingestMarkdownOK(t *testing.T) {
	store := &fakeStore{detail: app.SourceDetail{
		ID: sourceID, Title: "Guide", Kind: "markdown", Body: "Ada prefers oolong tea.",
		Status: "failed", Provenance: "curated",
	}}
	rec := call(t, store, nil, http.MethodPost, "/api/knowledge/folders/"+folderID+"/sources/"+sourceID+"/reingest", "", true)
	if rec.Code != http.StatusOK || store.ingests != 1 {
		t.Fatalf("status %d ingests %d body %s", rec.Code, store.ingests, rec.Body.String())
	}
}

func TestReingestEmptyAllowIsRefused(t *testing.T) {
	store := &fakeStore{detail: app.SourceDetail{ID: sourceID, Kind: "markdown", Body: "x", Status: "failed"}}
	rec := call(t, store, nil, http.MethodPost, "/api/knowledge/folders/"+folderID+"/sources/"+sourceID+"/reingest", "", false)
	if rec.Code != http.StatusUnauthorized || store.ingests != 0 {
		t.Fatalf("status %d ingests %d", rec.Code, store.ingests)
	}
}

type stubFetch struct {
	ctype string
	body  []byte
	err   error
}

func (s stubFetch) Fetch(context.Context, string) (string, []byte, error) {
	return s.ctype, s.body, s.err
}

func TestIngestURLAccepted(t *testing.T) {
	store := &fakeStore{}
	rec := call(t, store, nil, http.MethodPost, "/api/knowledge/folders/"+folderID+"/sources/url", `{"url":"https://example.com/guide.txt","title":"Guide"}`, true)
	if rec.Code != http.StatusAccepted || store.pending != 1 || store.ingests != 1 {
		t.Fatalf("status %d pending %d ingests %d body %s", rec.Code, store.pending, store.ingests, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got["id"] != sourceID || got["status"] != "pending" {
		t.Fatalf("got %#v err %v", got, err)
	}
}

func TestIngestURLPrivateIsRefused(t *testing.T) {
	store := &fakeStore{}
	rec := call(t, store, nil, http.MethodPost, "/api/knowledge/folders/"+folderID+"/sources/url", `{"url":"http://127.0.0.1/x"}`, true)
	if rec.Code != http.StatusBadRequest || store.pending != 0 {
		t.Fatalf("status %d pending %d body %s", rec.Code, store.pending, rec.Body.String())
	}
}

func TestIngestURLEmptyAllowIsRefused(t *testing.T) {
	store := &fakeStore{}
	rec := call(t, store, nil, http.MethodPost, "/api/knowledge/folders/"+folderID+"/sources/url", `{"url":"https://example.com/x"}`, false)
	if rec.Code != http.StatusUnauthorized || store.pending != 0 {
		t.Fatalf("status %d pending %d", rec.Code, store.pending)
	}
}

func TestIngestFileMarkdownAccepted(t *testing.T) {
	store := &fakeStore{}
	rec := callMultipart(t, store, true, "guide.md", "Ada prefers oolong tea.", "Guide")
	if rec.Code != http.StatusAccepted || store.pending != 1 || store.ingests != 1 {
		t.Fatalf("status %d pending %d ingests %d body %s", rec.Code, store.pending, store.ingests, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got["status"] != "pending" {
		t.Fatalf("got %#v err %v", got, err)
	}
}

func TestIngestFileUnknownExtensionIsRefused(t *testing.T) {
	store := &fakeStore{}
	rec := callMultipart(t, store, true, "guide.exe", "x", "")
	if rec.Code != http.StatusBadRequest || store.pending != 0 {
		t.Fatalf("status %d pending %d body %s", rec.Code, store.pending, rec.Body.String())
	}
}

func TestIngestFileEmptyAllowIsRefused(t *testing.T) {
	store := &fakeStore{}
	rec := callMultipart(t, store, false, "guide.md", "hello", "")
	if rec.Code != http.StatusUnauthorized || store.pending != 0 {
		t.Fatalf("status %d pending %d", rec.Code, store.pending)
	}
}

func TestIngestClipCreated(t *testing.T) {
	store := &fakeStore{}
	rec := call(t, store, nil, http.MethodPost, "/api/knowledge/folders/"+folderID+"/sources/clip",
		`{"url":"https://example.com/page","markdown":"Clipped body","title":"Page"}`, true)
	if rec.Code != http.StatusCreated || store.ingests != 1 {
		t.Fatalf("status %d ingests %d body %s", rec.Code, store.ingests, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got["id"] != sourceID {
		t.Fatalf("got %#v err %v", got, err)
	}
}

func TestIngestClipEmptyAllowIsRefused(t *testing.T) {
	store := &fakeStore{}
	rec := call(t, store, nil, http.MethodPost, "/api/knowledge/folders/"+folderID+"/sources/clip",
		`{"url":"https://example.com/page","markdown":"body"}`, false)
	if rec.Code != http.StatusUnauthorized || store.ingests != 0 {
		t.Fatalf("status %d ingests %d", rec.Code, store.ingests)
	}
}

func TestIngestClipOversizedIsRefused(t *testing.T) {
	store := &fakeStore{}
	body := `{"url":"https://example.com/page","markdown":"` + strings.Repeat("a", 128<<10+1) + `"}`
	rec := call(t, store, nil, http.MethodPost, "/api/knowledge/folders/"+folderID+"/sources/clip", body, true)
	if rec.Code != http.StatusBadRequest || store.ingests != 0 {
		t.Fatalf("status %d ingests %d", rec.Code, store.ingests)
	}
}

func callMultipart(t *testing.T, store *fakeStore, allow bool, filename, content, title string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if title != "" {
		if err := w.WriteField("title", title); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/knowledge/folders/"+folderID+"/sources/file", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	handler(store, nil, allow).ServeHTTP(rec, req)
	return rec
}

func TestListFolders(t *testing.T) {
	store := &fakeStore{rows: []app.Folder{{ID: folderID, Name: "Specs"}}}
	rec := call(t, store, nil, http.MethodGet, "/api/knowledge/folders", "", true)
	if rec.Code != http.StatusOK || store.lists != 1 {
		t.Fatalf("status %d lists %d body %s", rec.Code, store.lists, rec.Body.String())
	}
	var got []map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got) != 1 || got[0]["name"] != "Specs" {
		t.Fatalf("got %#v err %v", got, err)
	}
}

func TestListFoldersEmptyAllowIsRefused(t *testing.T) {
	store := &fakeStore{}
	rec := call(t, store, nil, http.MethodGet, "/api/knowledge/folders", "", false)
	if rec.Code != http.StatusUnauthorized || store.lists != 0 {
		t.Fatalf("status %d lists %d", rec.Code, store.lists)
	}
}

func TestListFoldersBearerIsRefused(t *testing.T) {
	store := &fakeStore{}
	req := httptest.NewRequest(http.MethodGet, "/api/knowledge/folders", nil)
	req.Header.Set("Authorization", "Bearer upmo_pat_secret")
	rec := httptest.NewRecorder()
	handler(store, nil, true).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || store.lists != 0 {
		t.Fatalf("status %d lists %d", rec.Code, store.lists)
	}
}

func TestListSources(t *testing.T) {
	store := &fakeStore{sources: []app.Source{{
		ID: sourceID, Title: "Guide", Kind: "markdown", Provenance: "curated", Status: "ready",
		Revision: 2, UpdatedAt: "2026-10-01T00:00:00Z",
	}}}
	rec := call(t, store, nil, http.MethodGet, "/api/knowledge/folders/"+folderID+"/sources", "", true)
	if rec.Code != http.StatusOK || store.lists != 1 {
		t.Fatalf("status %d lists %d body %s", rec.Code, store.lists, rec.Body.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got) != 1 || got[0]["title"] != "Guide" {
		t.Fatalf("got %#v err %v", got, err)
	}
	if got[0]["kind"] != "markdown" || got[0]["provenance"] != "curated" || got[0]["status"] != "ready" {
		t.Fatalf("kinds = %#v", got[0])
	}
	if _, ok := got[0]["body"]; ok {
		t.Fatalf("body leaked: %s", rec.Body.String())
	}
}

func TestListSourcesGoneFolder(t *testing.T) {
	store := &fakeStore{err: app.ErrGone}
	rec := call(t, store, nil, http.MethodGet, "/api/knowledge/folders/"+folderID+"/sources", "", true)
	if rec.Code != http.StatusNotFound || store.lists != 1 {
		t.Fatalf("status %d lists %d", rec.Code, store.lists)
	}
}

func TestListSourcesEmptyAllowIsRefused(t *testing.T) {
	store := &fakeStore{}
	rec := call(t, store, nil, http.MethodGet, "/api/knowledge/folders/"+folderID+"/sources", "", false)
	if rec.Code != http.StatusUnauthorized || store.lists != 0 {
		t.Fatalf("status %d lists %d", rec.Code, store.lists)
	}
}

func TestGetSource(t *testing.T) {
	store := &fakeStore{detail: app.SourceDetail{
		ID: sourceID, Title: "Guide", Body: "Hello world", Kind: "markdown",
		Provenance: "curated", Status: "ready", Revision: 2, UpdatedAt: "2026-10-01T00:00:00Z",
	}}
	rec := call(t, store, nil, http.MethodGet, "/api/knowledge/folders/"+folderID+"/sources/"+sourceID, "", true)
	if rec.Code != http.StatusOK || store.lists != 1 {
		t.Fatalf("status %d lists %d body %s", rec.Code, store.lists, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got["body"] != "Hello world" || got["title"] != "Guide" {
		t.Fatalf("got %#v err %v", got, err)
	}
	if got["kind"] != "markdown" || got["status"] != "ready" {
		t.Fatalf("kinds = %#v", got)
	}
}

func TestGetSourceGone(t *testing.T) {
	store := &fakeStore{err: app.ErrGone}
	rec := call(t, store, nil, http.MethodGet, "/api/knowledge/folders/"+folderID+"/sources/"+sourceID, "", true)
	if rec.Code != http.StatusNotFound || store.lists != 1 {
		t.Fatalf("status %d lists %d", rec.Code, store.lists)
	}
}

func TestGetSourceEmptyAllowIsRefused(t *testing.T) {
	store := &fakeStore{}
	rec := call(t, store, nil, http.MethodGet, "/api/knowledge/folders/"+folderID+"/sources/"+sourceID, "", false)
	if rec.Code != http.StatusUnauthorized || store.lists != 0 {
		t.Fatalf("status %d lists %d", rec.Code, store.lists)
	}
}

func TestGetSourceBearerIsRefused(t *testing.T) {
	store := &fakeStore{}
	req := httptest.NewRequest(http.MethodGet, "/api/knowledge/folders/"+folderID+"/sources/"+sourceID, nil)
	req.Header.Set("Authorization", "Bearer upmo_pat_secret")
	rec := httptest.NewRecorder()
	handler(store, nil, true).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || store.lists != 0 {
		t.Fatalf("status %d lists %d", rec.Code, store.lists)
	}
}
