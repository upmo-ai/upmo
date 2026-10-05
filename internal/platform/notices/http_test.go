package notices

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

type fakeList struct {
	calls  int
	userID uuid.UUID
	rows   []Notice
}

func (f *fakeList) List(_ context.Context, userID uuid.UUID) ([]Notice, error) {
	f.calls++
	f.userID = userID
	return append([]Notice(nil), f.rows...), nil
}

func TestListNotifications(t *testing.T) {
	userID := uuid.MustParse("018f3b2c-7a10-7c4a-8e11-111111111111")
	book := &fakeList{rows: []Notice{{
		ID:   uuid.MustParse("018f3b2c-7a10-7c4a-8e11-222222222222"),
		Kind: KindProposal, Title: "Weekly close",
	}}}
	rec := callHTTP(t, book, userID, true)
	if rec.Code != http.StatusOK || book.calls != 1 || book.userID != userID {
		t.Fatalf("status %d calls %d user %v", rec.Code, book.calls, book.userID)
	}
	var got []map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got) != 1 || got[0]["kind"] != KindProposal {
		t.Fatalf("got %#v err %v", got, err)
	}
}

func TestListNotificationsMissingPerson(t *testing.T) {
	book := &fakeList{}
	rec := callHTTP(t, book, uuid.Nil, false)
	if rec.Code != http.StatusUnauthorized || book.calls != 0 {
		t.Fatalf("status %d calls %d", rec.Code, book.calls)
	}
}

func callHTTP(t *testing.T, book *fakeList, userID uuid.UUID, ok bool) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	Handler{
		List: book,
		User: func(*http.Request) (uuid.UUID, bool) {
			if !ok {
				return uuid.Nil, false
			}
			return userID, true
		},
	}.Mount(mux, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/notifications", nil))
	return rec
}
