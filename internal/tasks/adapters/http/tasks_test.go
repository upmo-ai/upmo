package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

const taskID = "6f1c2a3b-4d5e-4f60-8a7b-9c0d1e2f3a4b"

func handler(book *fakeBook, allow bool) http.Handler {
	mux := http.NewServeMux()
	Handler{
		Book: book,
		Access: func(*http.Request) (app.Access, bool) {
			if !allow {
				return app.Access{}, false
			}
			return app.Access{UserID: "6f1c2a3b-4d5e-4f60-8a7b-9c0d1e2f3a4b", Allow: "task:create,task:edit"}, true
		},
	}.Mount(mux, nil)
	return mux
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
	taskID string
	input  domain.Input
	err    error
}

func (f *fakeBook) Transition(_ context.Context, _ app.Access, taskID string, input domain.Input) (app.Task, error) {
	f.calls++
	f.taskID = taskID
	f.input = input
	if f.err != nil {
		return app.Task{}, f.err
	}
	goal := strings.TrimSpace(input.Goal)
	if goal == "" {
		goal = "File the receipts"
	}
	id := taskID
	if id == "" {
		id = "task-1"
	}
	return app.Task{ID: id, Status: domain.Preparing, Goal: goal}, nil
}

func (f *fakeBook) Record(context.Context, app.Access, string, []domain.Check) (bool, error) {
	return false, nil
}
