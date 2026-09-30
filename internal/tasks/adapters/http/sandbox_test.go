package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/runtime"
	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
	"github.com/upmo-ai/upmo/internal/testutil/seatbook"
)

// fakeSandboxd answers Create and Destroy behind the service HMAC for the
// sandboxd audience, as cmd/sandboxd does, and records what it was asked.
type fakeSandboxd struct {
	mu       sync.Mutex
	calls    []string
	created  []map[string]any
	destroys []string
	refuse   int
}

func (f *fakeSandboxd) serve(t *testing.T, secret string) *httptest.Server {
	t.Helper()
	master, err := svcsig.ParseKey(secret)
	if err != nil {
		t.Fatal(err)
	}
	derived, err := svcsig.Derive(master, svcsig.AudienceSandboxd)
	if err != nil {
		t.Fatal(err)
	}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, r.URL.Path)
		switch r.URL.Path {
		case "/upmo.sandboxd.v1.Runtime/Create":
			if f.refuse != 0 {
				w.WriteHeader(f.refuse)
				_, _ = w.Write([]byte("{}"))
				return
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("create body: %v", err)
			}
			f.created = append(f.created, body)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":      "box-1",
				"tier":    "container",
				"shape":   map[string]any{"scratch": "scratch", "cpu": 1, "burst": 2, "pids": 256, "exec_seconds": 900},
				"expires": time.Now().Add(time.Hour).UTC(),
			})
		case "/upmo.sandboxd.v1.Runtime/Destroy":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("destroy body: %v", err)
			}
			f.destroys = append(f.destroys, body["id"])
			_, _ = w.Write([]byte("{}"))
		default:
			http.NotFound(w, r)
		}
	})
	h, err := svcsig.Handler([][]byte{derived}, svcsig.AudienceSandboxd, inner)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeSandboxd) seen() ([]string, []map[string]any, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls), slices.Clone(f.created), slices.Clone(f.destroys)
}

func seats(t *testing.T, f *fakeSandboxd, key string) *runtime.Seats {
	t.Helper()
	srv := f.serve(t, testKey('s'))
	provider, err := runtime.Open(srv.URL, key)
	if err != nil {
		t.Fatal(err)
	}
	return &runtime.Seats{Provider: provider, Book: &seatbook.Memory{}}
}

func held(t *testing.T, boxes *runtime.Seats) (string, bool) {
	t.Helper()
	id, ok, err := boxes.Held(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	return id, ok
}

func sandboxHandler(book *fakeBook, boxes app.Sandboxes) http.Handler {
	mux := http.NewServeMux()
	Handler{
		Book:      book,
		List:      book,
		Sandboxes: boxes,
		Access: func(*http.Request) (app.Access, bool) {
			return app.Access{UserID: taskID, Allow: "task:create,task:edit"}, true
		},
	}.Mount(mux, nil)
	return mux
}

func post(h http.Handler, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return rec
}

func TestOpenWithRepoCreatesASignedSandboxAndFailDestroysIt(t *testing.T) {
	sd := &fakeSandboxd{}
	boxes := seats(t, sd, testKey('s'))
	book := &fakeBook{openID: taskID}
	h := sandboxHandler(book, boxes)

	rec := post(h, "/api/tasks", `{"goal":"Fix the build","repo":"org/name","tool_classes":["shell"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("open status = %d body %s", rec.Code, rec.Body.String())
	}
	calls, created, _ := sd.seen()
	if !slices.Equal(calls, []string{"/upmo.sandboxd.v1.Runtime/Create"}) || len(created) != 1 {
		t.Fatalf("calls = %v", calls)
	}
	if created[0]["task_id"] != taskID || created[0]["tier"] != "container" || created[0]["image"] != runtime.SeatImage {
		t.Fatalf("create body = %v", created[0])
	}
	if id, ok := held(t, boxes); !ok || id != "box-1" {
		t.Fatalf("held = %q %t", id, ok)
	}

	rec = post(h, "/api/tasks/"+taskID, `{"kind":"fail"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("fail status = %d body %s", rec.Code, rec.Body.String())
	}
	_, _, destroys := sd.seen()
	if !slices.Equal(destroys, []string{"box-1"}) {
		t.Fatalf("destroys = %v", destroys)
	}
	if _, ok := held(t, boxes); ok {
		t.Fatal("still held after the task failed")
	}
}

func TestOpenWithoutASandboxNeedDoesNotCallSandboxd(t *testing.T) {
	sd := &fakeSandboxd{}
	book := &fakeBook{openID: taskID}
	h := sandboxHandler(book, seats(t, sd, testKey('s')))
	rec := post(h, "/api/tasks", `{"goal":"File the receipts","tool_classes":["connector","knowledge"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if calls, _, _ := sd.seen(); len(calls) != 0 {
		t.Fatalf("sandboxd called: %v", calls)
	}
}

func TestOpenWithAnUnknownClassIsRefused(t *testing.T) {
	sd := &fakeSandboxd{}
	book := &fakeBook{openID: taskID}
	h := sandboxHandler(book, seats(t, sd, testKey('s')))
	rec := post(h, "/api/tasks", `{"goal":"File the receipts","tool_classes":["sideways"]}`)
	if rec.Code != http.StatusBadRequest || book.calls != 0 {
		t.Fatalf("status = %d calls = %d", rec.Code, book.calls)
	}
}

func TestOpenNeedingASandboxWithSandboxdUnsetIsRefused(t *testing.T) {
	for name, boxes := range map[string]app.Sandboxes{
		"no seats":    nil,
		"no provider": &runtime.Seats{},
	} {
		t.Run(name, func(t *testing.T) {
			book := &fakeBook{openID: taskID}
			rec := post(sandboxHandler(book, boxes), "/api/tasks", `{"goal":"Fix the build","repo":"org/name"}`)
			if rec.Code != http.StatusServiceUnavailable || book.calls != 0 {
				t.Fatalf("status = %d calls = %d body %s", rec.Code, book.calls, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "none is configured") {
				t.Fatalf("body = %s", rec.Body.String())
			}
		})
	}
}

func TestOpenWhenCreateFailsReturnsTheFailedTask(t *testing.T) {
	sd := &fakeSandboxd{refuse: http.StatusConflict}
	book := &fakeBook{openID: taskID}
	boxes := seats(t, sd, testKey('s'))
	rec := post(sandboxHandler(book, boxes), "/api/tasks", `{"goal":"Fix the build","repo":"org/name"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["id"] != taskID || got["status"] != string(domain.Failed) || got["error"] == "" {
		t.Fatalf("body = %v", got)
	}
	if len(book.inputs) != 2 || book.inputs[1].Kind != domain.Fail {
		t.Fatalf("inputs = %v", book.inputs)
	}
	if _, ok := held(t, boxes); ok {
		t.Fatal("held after a failed create")
	}
}

func TestOpenWithTheWrongServiceKeyFailsTheTask(t *testing.T) {
	sd := &fakeSandboxd{}
	book := &fakeBook{openID: taskID}
	rec := post(sandboxHandler(book, seats(t, sd, testKey('x'))), "/api/tasks", `{"goal":"Fix the build","repo":"org/name"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	if calls, _, _ := sd.seen(); len(calls) != 0 {
		t.Fatalf("unsigned call reached sandboxd: %v", calls)
	}
}

func testKey(b byte) string {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = b
	}
	return svcsig.KeyPrefix + base64.RawStdEncoding.EncodeToString(raw)
}
