package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/decisions"
)

const userID = "018f3b2c-7a10-7c4a-8e11-111111111111"

type fakeList struct {
	calls int
	rows  []decisions.ListedGate
}

func (f *fakeList) List(context.Context, decisions.Access) ([]decisions.ListedGate, error) {
	f.calls++
	return append([]decisions.ListedGate(nil), f.rows...), nil
}

func TestListGates(t *testing.T) {
	book := &fakeList{rows: []decisions.ListedGate{{
		Route: "memory.check", Kind: decisions.KindYesNo, Fingerprint: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Verdict: decisions.VerdictPass, Hit: 9, Total: 10, Bar: 80, UpdatedAt: time.Unix(1, 0).UTC(),
	}}}
	rec := call(t, book, true)
	if rec.Code != http.StatusOK || book.calls != 1 {
		t.Fatalf("status %d calls %d body %s", rec.Code, book.calls, rec.Body.String())
	}
	var got []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got) != 1 || got[0]["route"] != "memory.check" {
		t.Fatalf("got %#v err %v", got, err)
	}
}

func TestListEmptyAllowIsRefused(t *testing.T) {
	book := &fakeList{}
	rec := call(t, book, false)
	if rec.Code != http.StatusUnauthorized || book.calls != 0 {
		t.Fatalf("status %d calls %d", rec.Code, book.calls)
	}
}

func TestListBearerIsRefused(t *testing.T) {
	book := &fakeList{}
	req := httptest.NewRequest(http.MethodGet, "/api/decisions/gates", nil)
	req.Header.Set("Authorization", "Bearer upmo_pat_secret")
	rec := httptest.NewRecorder()
	handler(book, true).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || book.calls != 0 {
		t.Fatalf("status %d calls %d", rec.Code, book.calls)
	}
}

func call(t *testing.T, book *fakeList, allow bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/decisions/gates", nil)
	rec := httptest.NewRecorder()
	handler(book, allow).ServeHTTP(rec, req)
	return rec
}

func handler(book *fakeList, allow bool) http.Handler {
	mux := http.NewServeMux()
	Handler{
		List: book,
		Access: func(*http.Request) (decisions.Access, bool) {
			if !allow {
				return decisions.Access{}, false
			}
			return decisions.Access{UserID: userID, Allow: "settings:manage"}, true
		},
	}.Mount(mux, nil)
	return mux
}
