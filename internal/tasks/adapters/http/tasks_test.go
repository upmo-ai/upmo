package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/runtime"
	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

func TestOpenCreatesAPreparingTask(t *testing.T) {
	book := &fakeBook{}
	rec := call(t, book, http.MethodPost, "/api/tasks", `{"goal":" File the receipts "}`, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["id"] != "task-1" || got["status"] != string(domain.Preparing) || got["goal"] != "File the receipts" {
		t.Fatalf("task = %#v", got)
	}
	if book.input.Kind != domain.Open || book.input.Goal != " File the receipts " {
		t.Fatalf("input = %#v", book.input)
	}
}

func TestStepAdvances(t *testing.T) {
	book := &fakeBook{}
	rec := call(t, book, http.MethodPost, "/api/tasks/"+taskID, `{"kind":"advance"}`, true)
	if rec.Code != http.StatusOK || book.taskID != taskID || book.input.Kind != domain.Advance {
		t.Fatalf("status %d id %q kind %q body %s", rec.Code, book.taskID, book.input.Kind, rec.Body.String())
	}
}

func TestBearerIsRefused(t *testing.T) {
	book := &fakeBook{}
	req := httptest.NewRequest(http.MethodPost, "/api/tasks", strings.NewReader(`{"goal":"File the receipts"}`))
	req.Header.Set("Authorization", "Bearer upmo_pat_secret")
	rec := httptest.NewRecorder()
	handler(book, true).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || book.calls != 0 {
		t.Fatalf("status %d calls %d", rec.Code, book.calls)
	}
}

func TestEmptyAllowDoesNotCallTheWriter(t *testing.T) {
	book := &fakeBook{}
	rec := call(t, book, http.MethodPost, "/api/tasks", `{"goal":"File the receipts"}`, false)
	if rec.Code != http.StatusUnauthorized || book.calls != 0 {
		t.Fatalf("status %d calls %d", rec.Code, book.calls)
	}
}

func TestMissingTaskIsNotFound(t *testing.T) {
	book := &fakeBook{err: app.ErrGone}
	rec := call(t, book, http.MethodPost, "/api/tasks/"+taskID, `{"kind":"fail"}`, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestUnknownKindIsRefused(t *testing.T) {
	book := &fakeBook{}
	rec := call(t, book, http.MethodPost, "/api/tasks/"+taskID, `{"kind":"sideways"}`, true)
	if rec.Code != http.StatusBadRequest || book.calls != 0 {
		t.Fatalf("status %d calls %d", rec.Code, book.calls)
	}
}

func TestListReturnsTasks(t *testing.T) {
	book := &fakeBook{listed: []app.TaskSummary{{
		ID: taskID, Title: "File the receipts", State: domain.Checking, Status: domain.ShownWorking, Phase: domain.Checking,
		SpentCents: 120, LimitCents: 500, UpdatedAt: time.Date(2026, 11, 7, 9, 30, 0, 0, time.UTC),
	}}}
	rec := call(t, book, http.MethodGet, "/api/tasks", "", true)
	if rec.Code != http.StatusOK || book.lists != 1 {
		t.Fatalf("status %d lists %d body %s", rec.Code, book.lists, rec.Body.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"id": taskID, "title": "File the receipts", "status": "Working", "phase": "Checking",
		"spent_cents": float64(120), "limit_cents": float64(500), "updated_at": "2026-11-07T09:30:00Z",
	}
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("tasks = %#v", got)
	}
}

func TestListBearerIsRefused(t *testing.T) {
	book := &fakeBook{}
	req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
	req.Header.Set("Authorization", "Bearer upmo_pat_secret")
	rec := httptest.NewRecorder()
	handler(book, true).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || book.lists != 0 {
		t.Fatalf("status %d lists %d", rec.Code, book.lists)
	}
}

func TestListEmptyAllowDoesNotCallTheLister(t *testing.T) {
	book := &fakeBook{}
	rec := call(t, book, http.MethodGet, "/api/tasks", "", false)
	if rec.Code != http.StatusUnauthorized || book.lists != 0 {
		t.Fatalf("status %d lists %d", rec.Code, book.lists)
	}
}

const taskID = "6f1c2a3b-4d5e-4f60-8a7b-9c0d1e2f3a4b"

func TestGetReturnsATask(t *testing.T) {
	at := time.Date(2026, 11, 7, 9, 30, 0, 0, time.UTC)
	book := &fakeBook{viewed: app.TaskView{
		TaskSummary: app.TaskSummary{
			ID: taskID, Title: "File the receipts", State: domain.NeedsApproval, Status: domain.ShownNeedsApproval, Phase: domain.Working,
			SpentCents: 120, LimitCents: 500, UpdatedAt: at,
		},
		Result:   "All filed",
		Detail:   "parent_canceled",
		Subtasks: []app.SubtaskView{{ID: "sub-1", Title: "Sort the mail", Status: domain.ShownDone}},
		Events:   []app.Event{{ID: "ev-1", Time: at, Phase: domain.Working, Type: "pause", Message: "Waiting for approval"}},
	}}
	rec := call(t, book, http.MethodGet, "/api/tasks/"+taskID, "", true)
	if rec.Code != http.StatusOK || book.views != 1 {
		t.Fatalf("status %d views %d body %s", rec.Code, book.views, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"id": taskID, "title": "File the receipts", "status": "Needs approval", "phase": "Working",
		"spent_cents": float64(120), "limit_cents": float64(500), "updated_at": "2026-11-07T09:30:00Z",
		"result": "All filed", "detail": "parent_canceled",
		"subtasks": []any{map[string]any{"id": "sub-1", "title": "Sort the mail", "status": "Done"}},
		"events": []any{map[string]any{
			"id": "ev-1", "time": "2026-11-07T09:30:00Z", "phase": "Working", "type": "pause", "message": "Waiting for approval",
		}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("task = %#v", got)
	}
}

func TestGetMissingIsNotFound(t *testing.T) {
	book := &fakeBook{err: app.ErrGone}
	rec := call(t, book, http.MethodGet, "/api/tasks/"+taskID, "", true)
	if rec.Code != http.StatusNotFound || book.views != 1 {
		t.Fatalf("status %d views %d", rec.Code, book.views)
	}
}

func TestGetEmptyAllowDoesNotCallTheViewer(t *testing.T) {
	book := &fakeBook{}
	rec := call(t, book, http.MethodGet, "/api/tasks/"+taskID, "", false)
	if rec.Code != http.StatusUnauthorized || book.views != 0 {
		t.Fatalf("status %d views %d", rec.Code, book.views)
	}
}

func TestProgressReturnsPhase(t *testing.T) {
	book := &fakeBook{}
	progress := fakeProgress{taskID: {Phase: "toolchain", Events: 3}}
	rec := callProgress(t, book, progress, "/api/tasks/"+taskID+"/progress", true)
	if rec.Code != http.StatusOK || book.finds != 1 || book.views != 0 {
		t.Fatalf("status %d finds %d body %s", rec.Code, book.finds, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["phase"] != "toolchain" || got["events"] != float64(3) {
		t.Fatalf("body = %#v", got)
	}
}

func TestProgressMissingIsNotFound(t *testing.T) {
	book := &fakeBook{}
	rec := callProgress(t, book, fakeProgress{}, "/api/tasks/"+taskID+"/progress", true)
	if rec.Code != http.StatusNotFound || book.finds != 1 || book.views != 0 {
		t.Fatalf("status %d finds %d", rec.Code, book.finds)
	}
}

func TestEditorReturnsURLTokenAndExpiry(t *testing.T) {
	book := &fakeBook{viewed: app.TaskView{TaskSummary: app.TaskSummary{ID: taskID, Title: "File the receipts", State: domain.Working}}}
	editor := &fakeEditorSandboxes{url: "http://127.0.0.1:30001"}
	now := time.Unix(1_700_000_000, 0)
	rec := callEditor(t, book, editor, fakeEditorSigner{token: "signed"}, now)
	if rec.Code != http.StatusOK || book.finds != 1 || book.views != 0 || editor.calls != 1 {
		t.Fatalf("status %d finds %d calls %d body %s", rec.Code, book.finds, editor.calls, rec.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	wantExp := now.UTC().Add(runtime.TokenTTL).Format(time.RFC3339)
	if got["url"] != "http://127.0.0.1:30001" || got["token"] != "signed" || got["expires_at"] != wantExp {
		t.Fatalf("body = %#v", got)
	}
}

func TestEditorNoSandboxIsConflict(t *testing.T) {
	book := &fakeBook{}
	editor := &fakeEditorSandboxes{err: app.ErrNoTaskSandbox}
	rec := callEditor(t, book, editor, fakeEditorSigner{token: "x"}, time.Now())
	if rec.Code != http.StatusConflict || book.finds != 1 || book.views != 0 {
		t.Fatalf("status %d finds %d", rec.Code, book.finds)
	}
}

func TestProgressGoneTaskIsNotFound(t *testing.T) {
	book := &fakeBook{err: app.ErrGone}
	rec := callProgress(t, book, fakeProgress{taskID: {Phase: "clone", Events: 1}}, "/api/tasks/"+taskID+"/progress", true)
	if rec.Code != http.StatusNotFound || book.finds != 1 || book.views != 0 {
		t.Fatalf("status %d finds %d", rec.Code, book.finds)
	}
}

func handler(book *fakeBook, allow bool) http.Handler {
	return handlerWithProgress(book, nil, allow)
}

type fakeEditorSandboxes struct {
	url   string
	err   error
	calls int
}

func (f *fakeEditorSandboxes) StartEditor(context.Context, string, string) (runtime.Endpoint, error) {
	f.calls++
	if f.err != nil {
		return runtime.Endpoint{}, f.err
	}
	return runtime.Endpoint{URL: f.url}, nil
}

type fakeEditorSigner struct {
	token string
}

func (f fakeEditorSigner) Sign(string, string, string, time.Time) (string, error) {
	return f.token, nil
}

func callEditor(t *testing.T, book *fakeBook, editor app.EditorSandboxes, signer app.EditorSigner, now time.Time) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Handler{
		Book:         book,
		List:         book,
		View:         book,
		PublicURL:    "http://localhost:5173",
		Editor:       editor,
		EditorSigner: signer,
		Now:          func() time.Time { return now },
		Access: func(*http.Request) (app.Access, bool) {
			return app.Access{UserID: "11111111-1111-1111-1111-111111111111", Allow: "task:view"}, true
		},
	}.Mount(mux, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/tasks/"+taskID+"/editor", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func handlerWithProgress(book *fakeBook, progress app.Progresses, allow bool) http.Handler {
	mux := http.NewServeMux()
	Handler{
		Book:     book,
		List:     book,
		View:     book,
		Progress: progress,
		Access: func(*http.Request) (app.Access, bool) {
			if !allow {
				return app.Access{}, false
			}
			return app.Access{UserID: "6f1c2a3b-4d5e-4f60-8a7b-9c0d1e2f3a4b", Allow: "task:create,task:edit"}, true
		},
	}.Mount(mux, nil)
	return mux
}

func callProgress(t *testing.T, book *fakeBook, progress app.Progresses, path string, allow bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	handlerWithProgress(book, progress, allow).ServeHTTP(rec, req)
	return rec
}

func call(t *testing.T, book *fakeBook, method, path, body string, allow bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler(book, allow).ServeHTTP(rec, req)
	return rec
}

type fakeBook struct {
	calls  int
	lists  int
	views  int
	finds  int
	viewed app.TaskView
	taskID string
	input  domain.Input
	inputs []domain.Input
	err    error
	listed []app.TaskSummary
	// openID is the id Open returns; empty means task-1.
	openID string
}

type fakeProgress map[string]runtime.Record

func (f fakeProgress) Progress(taskID string) (runtime.Record, bool) {
	rec, ok := f[taskID]
	return rec, ok
}

func (f *fakeBook) Transition(_ context.Context, _ app.Access, taskID string, input domain.Input) (app.Task, error) {
	f.calls++
	f.taskID = taskID
	f.input = input
	f.inputs = append(f.inputs, input)
	if f.err != nil {
		return app.Task{}, f.err
	}
	goal := strings.TrimSpace(input.Goal)
	if goal == "" {
		goal = "File the receipts"
	}
	id := taskID
	if id == "" {
		id = f.openID
	}
	if id == "" {
		id = "task-1"
	}
	status := domain.Preparing
	if input.Kind == domain.Fail {
		status = domain.Failed
	}
	return app.Task{ID: id, Status: status, Goal: goal}, nil
}

func (f *fakeBook) List(context.Context, app.Access) ([]app.TaskSummary, error) {
	f.lists++
	if f.err != nil {
		return nil, f.err
	}
	return append([]app.TaskSummary(nil), f.listed...), nil
}

func (f *fakeBook) View(_ context.Context, _ app.Access, taskID string) (app.TaskView, error) {
	f.views++
	if f.err != nil {
		return app.TaskView{}, f.err
	}
	if f.viewed.ID != "" {
		return f.viewed, nil
	}
	return app.TaskView{TaskSummary: app.TaskSummary{ID: taskID, Title: "File the receipts", State: domain.Preparing, Status: domain.ShownWorking, Phase: domain.Preparing}}, nil
}

func (f *fakeBook) Find(_ context.Context, _ app.Access, taskID string) (app.Task, error) {
	f.finds++
	if f.err != nil {
		return app.Task{}, f.err
	}
	if f.viewed.ID != "" {
		return app.Task{ID: f.viewed.ID, Goal: f.viewed.Title, Status: f.viewed.State}, nil
	}
	return app.Task{ID: taskID, Goal: "File the receipts", Status: domain.Preparing}, nil
}

func (f *fakeBook) Record(context.Context, app.Access, string, []domain.Check) (bool, error) {
	return false, nil
}
