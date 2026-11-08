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
	limit  int
	rows   []Notice
}

func (f *fakeList) List(_ context.Context, userID uuid.UUID, limit int) ([]Notice, error) {
	f.calls++
	f.userID = userID
	f.limit = limit
	return append([]Notice(nil), f.rows...), nil
}

func TestListNotifications(t *testing.T) {
	userID := uuid.MustParse("018f3b2c-7a10-7c4a-8e11-111111111111")
	book := &fakeList{rows: []Notice{{
		ID:   uuid.MustParse("018f3b2c-7a10-7c4a-8e11-222222222222"),
		Kind: KindProposal, Title: "Weekly close",
	}}}
	rec := callHTTP(t, book, userID, true, "/api/notifications")
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
	rec := callHTTP(t, book, uuid.Nil, false, "/api/notifications")
	if rec.Code != http.StatusUnauthorized || book.calls != 0 {
		t.Fatalf("status %d calls %d", rec.Code, book.calls)
	}
}

func TestListNotificationsCarriesTheTaskIdOnlyWhenThereIsOne(t *testing.T) {
	userID := uuid.MustParse("018f3b2c-7a10-7c4a-8e11-111111111111")
	taskID := uuid.MustParse("018f3b2c-7a10-7c4a-8e11-333333333333")
	book := &fakeList{rows: []Notice{
		{ID: uuid.MustParse("018f3b2c-7a10-7c4a-8e11-222222222222"), Kind: KindFinished, Title: "Receipts filed", TaskID: &taskID},
		{ID: uuid.MustParse("018f3b2c-7a10-7c4a-8e11-444444444444"), Kind: KindProposal, Title: "Weekly close"},
	}}
	rec := callHTTP(t, book, userID, true, "/api/notifications")
	var got []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got) != 2 {
		t.Fatalf("got %s err %v", rec.Body.String(), err)
	}
	if got[0]["task_id"] != taskID.String() {
		t.Errorf("finished = %v", got[0])
	}
	if _, has := got[1]["task_id"]; has {
		t.Errorf("proposal carries a task_id key: %v", got[1])
	}
}

func TestListNotificationsLimit(t *testing.T) {
	userID := uuid.MustParse("018f3b2c-7a10-7c4a-8e11-111111111111")
	tests := []struct {
		query string
		code  int
		limit int
	}{
		{"", http.StatusOK, DefaultLimit},
		{"?limit=1", http.StatusOK, 1},
		{"?limit=100", http.StatusOK, 100},
		{"?limit=101", http.StatusOK, MaxLimit},
		{"?limit=100000", http.StatusOK, MaxLimit},
		{"?limit=0", http.StatusBadRequest, 0},
		{"?limit=-5", http.StatusBadRequest, 0},
		{"?limit=abc", http.StatusBadRequest, 0},
		{"?limit=", http.StatusBadRequest, 0},
		{"?limit=1.5", http.StatusBadRequest, 0},
		{"?limit=99999999999999999999", http.StatusBadRequest, 0},
	}
	for _, test := range tests {
		book := &fakeList{}
		rec := callHTTP(t, book, userID, true, "/api/notifications"+test.query)
		if rec.Code != test.code || book.limit != test.limit {
			t.Errorf("%q: status %d limit %d; want %d %d", test.query, rec.Code, book.limit, test.code, test.limit)
		}
		if test.code == http.StatusBadRequest && book.calls != 0 {
			t.Errorf("%q: the store was read", test.query)
		}
	}
}

func TestNotificationRoutesRefuseABearerToken(t *testing.T) {
	book := &fakeList{}
	mux := http.NewServeMux()
	Handler{
		List: book,
		User: func(*http.Request) (uuid.UUID, bool) { return uuid.New(), true },
	}.Mount(mux, nil)
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/notifications", nil),
		httptest.NewRequest(http.MethodDelete, "/api/notifications/"+uuid.NewString(), nil),
	} {
		request.Header.Set("Authorization", "Bearer upmo_pat_x")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, request)
		if rec.Code != http.StatusForbidden || book.calls != 0 {
			t.Errorf("%s: status %d calls %d", request.Method, rec.Code, book.calls)
		}
	}
}

func callHTTP(t *testing.T, book *fakeList, userID uuid.UUID, ok bool, target string) *httptest.ResponseRecorder {
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
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}
