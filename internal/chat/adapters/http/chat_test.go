package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/chat/domain"
)

const (
	userID = "018f3b2c-7a10-7c4a-8e11-111111111111"
	chatID = "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"
)

var openedAt = time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)

type fakeBook struct {
	opens     int
	says      int
	lists     int
	reads     int
	body      string
	lines     []app.Line
	summaries []app.ChatSummary
	err       error
}

func (f *fakeBook) Open(context.Context, app.Access) (app.ChatSummary, error) {
	f.opens++
	if f.err != nil {
		return app.ChatSummary{}, f.err
	}
	return app.ChatSummary{ID: chatID, Title: domain.NewChatTitle, LastActivity: openedAt}, nil
}

func (f *fakeBook) List(context.Context, app.Access) ([]app.ChatSummary, error) {
	f.lists++
	if f.err != nil {
		return nil, f.err
	}
	return f.summaries, nil
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
	var got struct {
		ID           string `json:"id"`
		Title        string `json:"title"`
		Preview      string `json:"preview"`
		LastActivity string `json:"last_activity"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != chatID || got.Title != domain.NewChatTitle || got.Preview != "" || got.LastActivity != "2026-11-08T10:00:00Z" {
		t.Fatalf("created = %+v", got)
	}
}

func TestListReturnsTheSummaries(t *testing.T) {
	book := &fakeBook{summaries: []app.ChatSummary{
		{ID: chatID, Title: "Plan the week", Preview: "Plan the", LastActivity: openedAt},
	}}
	rec := call(t, book, http.MethodGet, "/api/chats", "", true)
	if rec.Code != http.StatusOK || book.lists != 1 {
		t.Fatalf("status %d lists %d", rec.Code, book.lists)
	}
	var got []struct {
		ID           string `json:"id"`
		Title        string `json:"title"`
		Preview      string `json:"preview"`
		LastActivity string `json:"last_activity"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != chatID || got[0].Title != "Plan the week" || got[0].Preview != "Plan the" || got[0].LastActivity != "2026-11-08T10:00:00Z" {
		t.Fatalf("list = %+v", got)
	}
}

func TestSayAppendsALine(t *testing.T) {
	book := &fakeBook{}
	rec := call(t, book, http.MethodPost, "/api/chats/"+chatID, `{"body":" hello "}`, true)
	if rec.Code != http.StatusAccepted || book.says != 1 || book.body != "hello" || rec.Body.Len() != 0 {
		t.Fatalf("status %d says %d body %q resp %q", rec.Code, book.says, book.body, rec.Body.String())
	}
}

func TestSayOnAChatWithoutEditIsForbidden(t *testing.T) {
	book := &fakeBook{err: app.ErrSay}
	rec := call(t, book, http.MethodPost, "/api/chats/"+chatID, `{"body":"hello"}`, true)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status %d", rec.Code)
	}
}

func TestReadReturnsTheLines(t *testing.T) {
	model := "claude-opus-5-5"
	cost := int64(1234)
	said := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	book := &fakeBook{lines: []app.Line{
		{ID: "l1", Role: "user", Body: "hello", At: said},
		{ID: "l2", Role: "assistant", Body: "hi", Model: &model, Tokens: 9, CostMicroUSD: &cost, At: said},
	}}
	rec := call(t, book, http.MethodGet, "/api/chats/"+chatID, "", true)
	if rec.Code != http.StatusOK || book.reads != 1 {
		t.Fatalf("status %d reads %d", rec.Code, book.reads)
	}
	var got struct {
		ID    string
		Lines []struct {
			ID      string
			Role    string
			Body    string
			Model   *string
			Tokens  int
			CostUSD *float64 `json:"cost_usd"`
			At      string
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID != chatID || len(got.Lines) != 2 {
		t.Fatalf("got = %+v", got)
	}
	first, second := got.Lines[0], got.Lines[1]
	if first.ID != "l1" || first.Role != "user" || first.Body != "hello" || first.Model != nil || first.Tokens != 0 || first.CostUSD != nil || first.At != "2026-11-08T10:00:00Z" {
		t.Fatalf("user line = %+v", first)
	}
	if second.Model == nil || *second.Model != model || second.Tokens != 9 || second.CostUSD == nil || *second.CostUSD != 0.001234 {
		t.Fatalf("assistant line = %+v", second)
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
