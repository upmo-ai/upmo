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
	retries   int
	lists     int
	reads     int
	body      string
	route     string
	picks     app.Picks
	retried   string
	given     *app.Picks
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

func (f *fakeBook) Say(_ context.Context, _ app.Access, _, body string, picks app.Picks) error {
	f.says++
	f.body = body
	f.route = picks.Route
	f.picks = picks
	return f.err
}

func (f *fakeBook) Retry(_ context.Context, _ app.Access, _, line string, picks *app.Picks) error {
	f.retries++
	f.retried = line
	f.given = picks
	f.route = ""
	f.picks = app.Picks{}
	if picks != nil {
		f.route = picks.Route
		f.picks = *picks
	}
	return f.err
}
func (f *fakeBook) Stop(context.Context, app.Access, string) error   { return f.err }
func (f *fakeBook) Delete(context.Context, app.Access, string) error { return f.err }

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

func TestSayPassesTheRoute(t *testing.T) {
	book := &fakeBook{}
	rec := call(t, book, http.MethodPost, "/api/chats/"+chatID, `{"body":"hello","route":"draft"}`, true)
	if rec.Code != http.StatusAccepted || book.route != "draft" {
		t.Fatalf("status %d route %q", rec.Code, book.route)
	}
	rec = call(t, book, http.MethodPost, "/api/chats/"+chatID, `{"body":"hello","route":"two words"}`, true)
	if rec.Code != http.StatusBadRequest || book.says != 1 {
		t.Fatalf("bad route: status %d says %d", rec.Code, book.says)
	}
}

const pickAccount = "018f3b2c-7a10-7c4a-8e11-eeeeeeeeeeee"

func TestSayPassesThePinAndTheAgent(t *testing.T) {
	book := &fakeBook{}
	body := `{"body":"hello","route":"draft","pin":{"account_id":"` + pickAccount + `","model":"gpt-x"},"agent_id":"` + userID + `"}`
	rec := call(t, book, http.MethodPost, "/api/chats/"+chatID, body, true)
	want := app.Picks{Route: "draft", PinAccount: pickAccount, PinModel: "gpt-x", AgentID: userID}
	if rec.Code != http.StatusAccepted || book.picks != want {
		t.Fatalf("status %d picks %+v, want %+v", rec.Code, book.picks, want)
	}
	for name, bad := range map[string]string{
		"half pin":         `"pin":{"account_id":"` + pickAccount + `"}`,
		"pin not a uuid":   `"pin":{"account_id":"mine","model":"gpt-x"}`,
		"agent not a uuid": `"agent_id":"research"`,
	} {
		rec := call(t, book, http.MethodPost, "/api/chats/"+chatID, `{"body":"hello",`+bad+`}`, true)
		if rec.Code != http.StatusBadRequest || book.says != 1 {
			t.Errorf("%s: status %d says %d", name, rec.Code, book.says)
		}
	}
}

func TestRetryPassesThePicksOrNothing(t *testing.T) {
	path := "/api/chats/" + chatID + "/lines/" + pickAccount + "/retry"
	book := &fakeBook{}
	body := `{"route":"draft","pin":{"account_id":"` + pickAccount + `","model":"gpt-x"},"agent_id":"` + userID + `"}`
	rec := call(t, book, http.MethodPost, path, body, true)
	want := app.Picks{Route: "draft", PinAccount: pickAccount, PinModel: "gpt-x", AgentID: userID}
	if rec.Code != http.StatusAccepted || book.picks != want {
		t.Fatalf("status %d picks %+v, want %+v", rec.Code, book.picks, want)
	}
	rec = call(t, book, http.MethodPost, path, "", true)
	if rec.Code != http.StatusAccepted || book.given != nil {
		t.Fatalf("no body: status %d given %+v, want nil so the stored picks are reused", rec.Code, book.given)
	}
	for _, empty := range []string{`{}`, `{"route":"","pin":null,"agent_id":""}`} {
		rec = call(t, book, http.MethodPost, path, empty, true)
		if rec.Code != http.StatusAccepted || book.given == nil || !book.given.None() {
			t.Fatalf("body %s: status %d given %+v, want an explicit empty override", empty, rec.Code, book.given)
		}
	}
	rec = call(t, book, http.MethodPost, path, `{"pin":{"model":"gpt-x"}}`, true)
	if rec.Code != http.StatusBadRequest || book.retries != 4 {
		t.Fatalf("half pin: status %d retries %d", rec.Code, book.retries)
	}
}

func TestReadShowsThePicksOfAUserLine(t *testing.T) {
	route, account, model, agent := "draft", pickAccount, "gpt-x", userID
	book := &fakeBook{lines: []app.Line{
		{ID: "l1", Role: "user", Body: "hello", Route: &route, PinAccount: &account, PinModel: &model, AgentID: &agent},
		{ID: "l2", Role: "user", Body: "again"},
	}}
	rec := call(t, book, http.MethodGet, "/api/chats/"+chatID, "", true)
	var got struct {
		Lines []struct {
			Route *string `json:"route"`
			Pin   *struct {
				AccountID string `json:"account_id"`
				Model     string `json:"model"`
			} `json:"pin"`
			AgentID *string `json:"agent_id"`
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	first, second := got.Lines[0], got.Lines[1]
	if first.Route == nil || *first.Route != route || first.Pin == nil || first.Pin.AccountID != account || first.Pin.Model != model ||
		first.AgentID == nil || *first.AgentID != agent {
		t.Fatalf("first line = %+v", first)
	}
	other := app.Line{ID: "l3", Role: "user", Body: "x", PinModel: &model}
	if raw, err := json.Marshal(lineBody(other)); err != nil || strings.Contains(string(raw), "account_id") {
		t.Fatalf("a line read by someone else shows %s %v", raw, err)
	}
	if second.Route != nil || second.Pin != nil || second.AgentID != nil {
		t.Fatalf("a line without picks shows %+v", second)
	}
}

func TestRetryAsksForANewReply(t *testing.T) {
	line := "018f3b2c-7a10-7c4a-8e11-dddddddddddd"
	path := "/api/chats/" + chatID + "/lines/" + line + "/retry"
	book := &fakeBook{}
	rec := call(t, book, http.MethodPost, path, `{"route":"draft"}`, true)
	if rec.Code != http.StatusAccepted || book.retried != line || book.route != "draft" {
		t.Fatalf("status %d line %q route %q", rec.Code, book.retried, book.route)
	}
	rec = call(t, book, http.MethodPost, path, "", true)
	if rec.Code != http.StatusAccepted || book.retries != 2 || book.route != "" {
		t.Fatalf("no body: status %d retries %d route %q", rec.Code, book.retries, book.route)
	}
	for err, want := range map[error]int{
		app.ErrNoLine:  http.StatusNotFound,
		app.ErrRetry:   http.StatusConflict,
		app.ErrBusy:    http.StatusConflict,
		app.ErrReplies: http.StatusServiceUnavailable,
		app.ErrSay:     http.StatusForbidden,
		app.ErrGone:    http.StatusNotFound,
	} {
		if rec := call(t, &fakeBook{err: err}, http.MethodPost, path, "", true); rec.Code != want {
			t.Fatalf("%v: status %d, want %d", err, rec.Code, want)
		}
	}
	if rec := call(t, book, http.MethodPost, path, "", false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no session: status %d", rec.Code)
	}
}

func TestReadShowsStatusReplyToAndAttemptReasons(t *testing.T) {
	model := "m2"
	user := "u1"
	book := &fakeBook{lines: []app.Line{{
		ID: "l2", Role: "assistant", Body: "half", Model: &model, Status: "failed", ReplyTo: &user,
		Attempts: []domain.Attempt{{Model: "m1", Class: "rate_limit"}, {Model: "m2", Class: "internal"}},
	}}}
	rec := call(t, book, http.MethodGet, "/api/chats/"+chatID, "", true)
	var got struct {
		Lines []struct {
			Status   string
			ReplyTo  *string `json:"reply_to"`
			Attempts []struct{ Model, Reason string }
		}
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	line := got.Lines[0]
	if line.Status != "failed" || line.ReplyTo == nil || *line.ReplyTo != "u1" ||
		len(line.Attempts) != 2 || line.Attempts[0].Reason != "rate limited" || line.Attempts[1].Reason != "stopped" {
		t.Fatalf("line = %+v", line)
	}
	if strings.Contains(rec.Body.String(), "rate_limit") || strings.Contains(rec.Body.String(), "internal") {
		t.Fatalf("a failure class leaked: %s", rec.Body.String())
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
