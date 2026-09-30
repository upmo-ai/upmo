package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/chat/domain"
)

const (
	userID = "018f3b2c-7a10-7c4a-8e11-111111111111"
	chatID = "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"
)

type fakeBook struct {
	opens int
	says  int
	reads int
	body  string
	lines []string
	err   error
}

func (f *fakeBook) Open(context.Context, app.Access) (string, error) {
	f.opens++
	if f.err != nil {
		return "", f.err
	}
	return chatID, nil
}

func (f *fakeBook) Say(_ context.Context, _ app.Access, _, body string) error {
	f.says++
	f.body = body
	return f.err
}

func (f *fakeBook) Read(context.Context, app.Access, string) (app.Chat, error) {
	f.reads++
	if f.err != nil {
		return app.Chat{}, f.err
	}
	return app.Chat{ID: chatID, Lines: f.lines}, nil
}

func TestOpenCreatesAChat(t *testing.T) {
	book := &fakeBook{}
	rec := call(t, book, http.MethodPost, "/api/chats", "", true)
	if rec.Code != http.StatusCreated || book.opens != 1 {
		t.Fatalf("status %d opens %d body %s", rec.Code, book.opens, rec.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["id"] != chatID {
		t.Fatalf("id = %q", got["id"])
	}
}

func TestSayAppendsALine(t *testing.T) {
	book := &fakeBook{}
	rec := call(t, book, http.MethodPost, "/api/chats/"+chatID, `{"body":" hello "}`, true)
	if rec.Code != http.StatusNoContent || book.says != 1 || book.body != "hello" {
		t.Fatalf("status %d says %d body %q resp %s", rec.Code, book.says, book.body, rec.Body.String())
	}
}

func TestReadReturnsLines(t *testing.T) {
	book := &fakeBook{lines: []string{"hello", "again"}}
	rec := call(t, book, http.MethodGet, "/api/chats/"+chatID, "", true)
	if rec.Code != http.StatusOK || book.reads != 1 {
		t.Fatalf("status %d reads %d", rec.Code, book.reads)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	lines, _ := got["lines"].([]any)
	if got["id"] != chatID || len(lines) != 2 || lines[0] != "hello" {
		t.Fatalf("got = %#v", got)
	}
}

func TestBearerIsRefused(t *testing.T) {
	book := &fakeBook{}
	req := httptest.NewRequest(http.MethodPost, "/api/chats", nil)
	req.Header.Set("Authorization", "Bearer upmo_pat_secret")
	rec := httptest.NewRecorder()
	handler(book, true).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || book.opens != 0 {
		t.Fatalf("status %d opens %d", rec.Code, book.opens)
	}
}

func TestEmptyAllowDoesNotCallTheWriter(t *testing.T) {
	book := &fakeBook{}
	rec := call(t, book, http.MethodPost, "/api/chats", "", false)
	if rec.Code != http.StatusUnauthorized || book.opens != 0 {
		t.Fatalf("status %d opens %d", rec.Code, book.opens)
	}
}

func TestMissingChatIsNotFound(t *testing.T) {
	book := &fakeBook{err: app.ErrGone}
	rec := call(t, book, http.MethodGet, "/api/chats/"+chatID, "", true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestEmptyLineIsRefused(t *testing.T) {
	book := &fakeBook{}
	rec := call(t, book, http.MethodPost, "/api/chats/"+chatID, `{"body":"   "}`, true)
	if rec.Code != http.StatusBadRequest || book.says != 0 {
		t.Fatalf("status %d says %d", rec.Code, book.says)
	}
}

func TestMissingBodyIsRefused(t *testing.T) {
	book := &fakeBook{}
	rec := call(t, book, http.MethodPost, "/api/chats/"+chatID, `{}`, true)
	if rec.Code != http.StatusBadRequest || book.says != 0 {
		t.Fatalf("status %d says %d", rec.Code, book.says)
	}
}

func TestLineTooLongIsRefused(t *testing.T) {
	book := &fakeBook{}
	body := `{"body":"` + strings.Repeat("a", domain.LineRunes+1) + `"}`
	rec := call(t, book, http.MethodPost, "/api/chats/"+chatID, body, true)
	if rec.Code != http.StatusBadRequest || book.says != 0 {
		t.Fatalf("status %d says %d", rec.Code, book.says)
	}
}

func call(t *testing.T, book *fakeBook, method, path, body string, allow bool) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	rec := httptest.NewRecorder()
	handler(book, allow).ServeHTTP(rec, r)
	return rec
}

func handler(book *fakeBook, allow bool) http.Handler {
	mux := http.NewServeMux()
	Handler{
		Book: book,
		Access: func(*http.Request) (app.Access, bool) {
			if !allow {
				return app.Access{}, false
			}
			return app.Access{UserID: userID, Allow: "chat:create,chat:view,chat:edit"}, true
		},
	}.Mount(mux, nil)
	return mux
}
