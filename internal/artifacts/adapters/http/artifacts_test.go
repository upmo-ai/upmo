package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/artifacts"
)

const (
	userID = "018f3b2c-7a10-7c4a-8e11-111111111111"
	taskID = "018f3b2c-7a10-7c4a-8e11-333333333333"
	artID  = "018f3b2c-7a10-7c4a-8e11-444444444444"
)

type fakeCatalog struct {
	listed []artifacts.Record
	rec    artifacts.Record
	err    error
	lists  int
	gets   int
}

func (f *fakeCatalog) ListByTask(context.Context, artifacts.Access, string) ([]artifacts.Record, error) {
	f.lists++
	if f.err != nil {
		return nil, f.err
	}
	return f.listed, nil
}

func (f *fakeCatalog) Get(context.Context, artifacts.Access, string) (artifacts.Record, error) {
	f.gets++
	if f.err != nil {
		return artifacts.Record{}, f.err
	}
	return f.rec, nil
}

type fakeBlobs struct{ body string }

func (f fakeBlobs) Open(context.Context, string, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(f.body)), nil
}

func TestListReturnsArtifacts(t *testing.T) {
	cat := &fakeCatalog{listed: []artifacts.Record{{ID: artID, Name: "report.pdf", Store: artifacts.StoreVolume}}}
	rec := call(t, cat, fakeBlobs{}, http.MethodGet, "/api/tasks/"+taskID+"/artifacts", true)
	if rec.Code != http.StatusOK || cat.lists != 1 {
		t.Fatalf("status %d lists %d body %s", rec.Code, cat.lists, rec.Body.String())
	}
	var got []map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0]["id"] != artID || got[0]["name"] != "report.pdf" {
		t.Fatalf("body = %#v", got)
	}
}

func TestDownloadServesWithNosniff(t *testing.T) {
	cat := &fakeCatalog{rec: artifacts.Record{ID: artID, Name: "report.pdf", Store: artifacts.StoreVolume, Key: "task/report.pdf"}}
	rec := call(t, cat, fakeBlobs{body: "pdf"}, http.MethodGet, "/api/artifacts/"+artID, true)
	if rec.Code != http.StatusOK || cat.gets != 1 {
		t.Fatalf("status %d gets %d", rec.Code, cat.gets)
	}
	if rec.Header().Get(artifacts.HeaderSniff) != artifacts.SniffValue {
		t.Fatalf("sniff = %q", rec.Header().Get(artifacts.HeaderSniff))
	}
	if rec.Header().Get(artifacts.HeaderType) != "application/pdf" {
		t.Fatalf("type = %q", rec.Header().Get(artifacts.HeaderType))
	}
	if rec.Body.String() != "pdf" {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestDownloadHTMLIsOctetStream(t *testing.T) {
	cat := &fakeCatalog{rec: artifacts.Record{ID: artID, Name: "page.html", Store: artifacts.StoreVolume, Key: "task/page.html"}}
	rec := call(t, cat, fakeBlobs{body: "<html>"}, http.MethodGet, "/api/artifacts/"+artID, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Header().Get(artifacts.HeaderType) != artifacts.TypeBytes {
		t.Fatalf("type = %q", rec.Header().Get(artifacts.HeaderType))
	}
	if !strings.Contains(rec.Header().Get(artifacts.HeaderDisposition), "attachment") {
		t.Fatalf("disposition = %q", rec.Header().Get(artifacts.HeaderDisposition))
	}
}

func TestMissingArtifactIsNotFound(t *testing.T) {
	cat := &fakeCatalog{err: artifacts.ErrGone}
	rec := call(t, cat, fakeBlobs{}, http.MethodGet, "/api/artifacts/"+artID, true)
	if rec.Code != http.StatusNotFound || cat.gets != 1 {
		t.Fatalf("status %d gets %d", rec.Code, cat.gets)
	}
}

func TestEmptyAllowDoesNotCallTheStore(t *testing.T) {
	cat := &fakeCatalog{}
	rec := call(t, cat, fakeBlobs{}, http.MethodGet, "/api/tasks/"+taskID+"/artifacts", false)
	if rec.Code != http.StatusUnauthorized || cat.lists != 0 {
		t.Fatalf("status %d lists %d", rec.Code, cat.lists)
	}
}

func TestBearerIsRefused(t *testing.T) {
	cat := &fakeCatalog{}
	req := httptest.NewRequest(http.MethodGet, "/api/artifacts/"+artID, nil)
	req.Header.Set("Authorization", "Bearer upmo_pat_secret")
	rec := httptest.NewRecorder()
	handler(cat, fakeBlobs{}, true).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || cat.gets != 0 {
		t.Fatalf("status %d gets %d", rec.Code, cat.gets)
	}
}

func call(t *testing.T, cat *fakeCatalog, blobs artifacts.Blobs, method, path string, allow bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	handler(cat, blobs, allow).ServeHTTP(rec, req)
	return rec
}

func handler(cat *fakeCatalog, blobs artifacts.Blobs, allow bool) http.Handler {
	mux := http.NewServeMux()
	Handler{
		Catalog: cat,
		Blobs:   blobs,
		Access: func(*http.Request) (artifacts.Access, bool) {
			if !allow {
				return artifacts.Access{}, false
			}
			return artifacts.Access{UserID: userID, Allow: "artifact:view"}, true
		},
	}.Mount(mux, nil)
	return mux
}
