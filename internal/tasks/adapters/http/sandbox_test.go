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
	"github.com/upmo-ai/upmo/internal/testutil/tasksandbox"
)

// fakeSandboxd answers Create, Destroy, Exec and the file calls behind the
// service HMAC for the sandboxd audience, as cmd/sandboxd does, and records
// what it was asked. Every command exits 0 unless failOn names one of its
// arguments, which then exits 128.
type fakeSandboxd struct {
	mu       sync.Mutex
	calls    []string
	created  []map[string]any
	destroys []string
	execs    [][]string
	files    map[string][]byte
	refuse   int
	failOn   string
	// gate, when set, holds every Exec until it is closed, after closing
	// entered on the first, so a phase is slow for as long as a test needs.
	gate    chan struct{}
	entered chan struct{}
	once    sync.Once
}

// provisionCalls is what a sandbox without a repository is provisioned
// with: the marker, the toolchain check, the probe and the marker read back.
var provisionCalls = []string{
	"/upmo.sandboxd.v1.Runtime/Create",
	"/upmo.sandboxd.v1.Runtime/WriteFile",
	"/upmo.sandboxd.v1.Runtime/Exec",
	"/upmo.sandboxd.v1.Runtime/Exec",
	"/upmo.sandboxd.v1.Runtime/ReadFile",
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
		if f.gate != nil && r.URL.Path == "/upmo.sandboxd.v1.Runtime/Exec" {
			f.once.Do(func() { close(f.entered) })
			<-f.gate
		}
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
		case "/upmo.sandboxd.v1.Runtime/Exec":
			var body struct {
				ID   string   `json:"id"`
				Args []string `json:"args"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID != "box-1" {
				t.Errorf("exec body: %+v %v", body, err)
			}
			f.execs = append(f.execs, body.Args)
			code := 0
			w.Header().Set("Content-Type", "application/x-ndjson")
			enc := json.NewEncoder(w)
			if f.failOn != "" && slices.Contains(body.Args, f.failOn) {
				code = 128
				_ = enc.Encode(map[string]any{"type": "output", "stream": "stderr", "data": []byte("fatal: no network")})
			}
			_ = enc.Encode(map[string]any{"type": "exit", "code": code})
		case "/upmo.sandboxd.v1.Runtime/WriteFile":
			var body struct {
				ID   string `json:"id"`
				Path string `json:"path"`
				Data []byte `json:"data"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("write body: %v", err)
			}
			if f.files == nil {
				f.files = map[string][]byte{}
			}
			f.files[body.Path] = body.Data
			_, _ = w.Write([]byte("{}"))
		case "/upmo.sandboxd.v1.Runtime/ReadFile":
			var body struct {
				Path string `json:"path"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("read body: %v", err)
			}
			data, ok := f.files[body.Path]
			if !ok {
				w.Header().Set("Upmo-Error", "no_file")
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte("{}"))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
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

func taskBoxes(t *testing.T, f *fakeSandboxd, key string) *runtime.TaskSandboxes {
	t.Helper()
	srv := f.serve(t, testKey('s'))
	provider, err := runtime.Open(srv.URL, key)
	if err != nil {
		t.Fatal(err)
	}
	return &runtime.TaskSandboxes{Provider: provider, Store: &tasksandbox.Memory{}}
}

// settle waits for the provisioning Open left running in the background.
func settle(t *testing.T, boxes *runtime.TaskSandboxes) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := boxes.Wait(ctx); err != nil {
		t.Fatalf("provisioning still running: %v", err)
	}
}

func held(t *testing.T, boxes *runtime.TaskSandboxes) (string, bool) {
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
	boxes := taskBoxes(t, sd, testKey('s'))
	book := &fakeBook{openID: taskID}
	h := sandboxHandler(book, boxes)

	rec := post(h, "/api/tasks", `{"goal":"Fix the build","repo":"org/name","tool_classes":["shell"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("open status = %d body %s", rec.Code, rec.Body.String())
	}
	settle(t, boxes)
	calls, created, _ := sd.seen()
	wantCalls := []string{
		"/upmo.sandboxd.v1.Runtime/Create",
		"/upmo.sandboxd.v1.Runtime/Exec", // clone
		"/upmo.sandboxd.v1.Runtime/WriteFile",
		"/upmo.sandboxd.v1.Runtime/Exec", // toolchain
		"/upmo.sandboxd.v1.Runtime/Exec", // probe
		"/upmo.sandboxd.v1.Runtime/Exec", // the clone has a commit
		"/upmo.sandboxd.v1.Runtime/ReadFile",
	}
	if !slices.Equal(calls, wantCalls) || len(created) != 1 {
		t.Fatalf("calls = %v", calls)
	}
	if rec, ok := boxes.Progress(taskID); !ok || rec.Phase != runtime.PhaseReady {
		t.Fatalf("progress = %+v %t", rec, ok)
	}
	if created[0]["task_id"] != taskID || created[0]["tier"] != "container" || created[0]["image"] != runtime.SandboxImage {
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

func TestOpenWithAShellProvisionsWithoutAClone(t *testing.T) {
	sd := &fakeSandboxd{}
	boxes := taskBoxes(t, sd, testKey('s'))
	book := &fakeBook{openID: taskID}
	rec := post(sandboxHandler(book, boxes), "/api/tasks", `{"goal":"Tidy the notes","tool_classes":["shell"]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got["status"] != string(domain.Preparing) {
		t.Fatalf("body = %v %v", got, err)
	}
	settle(t, boxes)
	if calls, _, _ := sd.seen(); !slices.Equal(calls, provisionCalls) {
		t.Fatalf("calls = %v", calls)
	}
	if progress, ok := boxes.Progress(taskID); !ok || progress.Phase != runtime.PhaseReady {
		t.Fatalf("progress = %+v %t", progress, ok)
	}
}

func TestOpenAnswersBeforeASlowCloneFinishes(t *testing.T) {
	sd := &fakeSandboxd{gate: make(chan struct{}), entered: make(chan struct{})}
	boxes := taskBoxes(t, sd, testKey('s'))
	book := &fakeBook{openID: taskID}
	h := sandboxHandler(book, boxes)

	answered := make(chan *httptest.ResponseRecorder, 1)
	go func() { answered <- post(h, "/api/tasks", `{"goal":"Fix the build","repo":"org/name"}`) }()
	var rec *httptest.ResponseRecorder
	select {
	case rec = <-answered:
	case <-time.After(5 * time.Second):
		close(sd.gate)
		t.Fatal("open waited on the clone")
	}
	<-sd.entered
	// The clone is still running in sandboxd; Open has already answered.
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusCreated || got["status"] != string(domain.Preparing) {
		t.Fatalf("open = %d %v %v", rec.Code, got, err)
	}
	if progress, ok := boxes.Progress(taskID); !ok || progress.Phase != runtime.PhaseClone {
		t.Fatalf("progress while cloning = %+v %t", progress, ok)
	}
	if id, ok := held(t, boxes); !ok || id != "box-1" {
		t.Fatalf("held while cloning = %q %t", id, ok)
	}

	close(sd.gate)
	settle(t, boxes)
	if progress, ok := boxes.Progress(taskID); !ok || progress.Phase != runtime.PhaseReady {
		t.Fatalf("progress = %+v %t", progress, ok)
	}
	if len(book.inputs) != 1 {
		t.Fatalf("inputs = %v", book.inputs)
	}
}

func TestOpenWhenACloneFailsFailsTheTaskAndDestroysTheSandbox(t *testing.T) {
	sd := &fakeSandboxd{failOn: "clone"}
	boxes := taskBoxes(t, sd, testKey('s'))
	book := &fakeBook{openID: taskID}
	rec := post(sandboxHandler(book, boxes), "/api/tasks", `{"goal":"Fix the build","repo":"org/name"}`)
	// Open answers before the clone runs, so the failure comes after.
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["id"] != taskID || got["status"] != string(domain.Preparing) {
		t.Fatalf("body = %v", got)
	}
	settle(t, boxes)
	if len(book.inputs) != 2 || book.inputs[1].Kind != domain.Fail {
		t.Fatalf("inputs = %v", book.inputs)
	}
	calls, _, destroys := sd.seen()
	if !slices.Equal(destroys, []string{"box-1"}) || slices.Contains(calls, "/upmo.sandboxd.v1.Runtime/WriteFile") {
		t.Fatalf("calls = %v destroys = %v", calls, destroys)
	}
	if _, ok := held(t, boxes); ok {
		t.Fatal("held after a failed clone")
	}
	if _, ok := boxes.Progress(taskID); ok {
		t.Fatal("progress kept after release")
	}
}

func TestOpenWithARepoItCannotCloneIsRefused(t *testing.T) {
	sd := &fakeSandboxd{}
	book := &fakeBook{openID: taskID}
	rec := post(sandboxHandler(book, taskBoxes(t, sd, testKey('s'))), "/api/tasks", `{"goal":"Fix the build","repo":"--upload-pack=sh"}`)
	if rec.Code != http.StatusBadRequest || book.calls != 0 || !strings.Contains(rec.Body.String(), "owner/name") {
		t.Fatalf("status = %d calls = %d body %s", rec.Code, book.calls, rec.Body.String())
	}
	if calls, _, _ := sd.seen(); len(calls) != 0 {
		t.Fatalf("sandboxd called: %v", calls)
	}
}

func TestOpenWithoutASandboxNeedDoesNotCallSandboxd(t *testing.T) {
	sd := &fakeSandboxd{}
	book := &fakeBook{openID: taskID}
	h := sandboxHandler(book, taskBoxes(t, sd, testKey('s')))
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
	h := sandboxHandler(book, taskBoxes(t, sd, testKey('s')))
	rec := post(h, "/api/tasks", `{"goal":"File the receipts","tool_classes":["sideways"]}`)
	if rec.Code != http.StatusBadRequest || book.calls != 0 {
		t.Fatalf("status = %d calls = %d", rec.Code, book.calls)
	}
}

func TestOpenNeedingASandboxWithSandboxdUnsetIsRefused(t *testing.T) {
	for name, boxes := range map[string]app.Sandboxes{
		"no sandboxes": nil,
		"no provider":  &runtime.TaskSandboxes{},
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
	boxes := taskBoxes(t, sd, testKey('s'))
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
	rec := post(sandboxHandler(book, taskBoxes(t, sd, testKey('x'))), "/api/tasks", `{"goal":"Fix the build","repo":"org/name"}`)
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
