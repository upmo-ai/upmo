package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
)

func TestDisabledPersonIsSignedOut(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	token, text, err := domain.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	user, err := domain.ParseUserID("00000000-0000-7000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	sessions := app.Service{
		Store: liveSession{hash: domain.Hash(token), row: app.Row{
			ID: "session", UserID: user, LastSeen: now, Expires: now.Add(time.Hour), SignedIn: now, Method: "password",
		}},
		Idle: time.Hour, MaxAge: time.Hour, Now: func() time.Time { return now },
	}
	clock := func() time.Time { return now }
	handler := Passkeys{
		Pass:     app.Passkeys{Sessions: sessions, Store: disabledPass{}, Now: clock},
		Password: app.Passwords{Sessions: sessions, Store: disabledPassword{}, Now: clock},
	}
	mux := http.NewServeMux()
	handler.Mount(mux)

	set := request(mux, http.MethodPut, "/api/password", text, []byte(`{"password":"correct horse battery"}`))
	assertSignedOut(t, set)
	add := request(mux, http.MethodPost, "/api/passkeys/options", text, nil)
	assertSignedOut(t, add)
}

func request(mux http.Handler, method, path, session string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.AddCookie(&http.Cookie{
		Name: CookieName, Value: session, Path: "/",
		Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func assertSignedOut(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusUnauthorized || rec.Body.String() != unauthorized {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	cleared := false
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == CookieName && cookie.Value == "" && cookie.MaxAge <= 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("session cookie was not cleared")
	}
}

type liveSession struct {
	hash []byte
	row  app.Row
}

func (s liveSession) Find(_ context.Context, hash []byte) (app.Row, bool, error) {
	if !bytes.Equal(hash, s.hash) {
		return app.Row{}, false, nil
	}
	return s.row, true, nil
}

func (liveSession) Insert(context.Context, domain.UserID, []byte, time.Time, time.Time, string, string) (string, error) {
	return "", nil
}
func (liveSession) Touch(context.Context, string, time.Time) error { return nil }
func (liveSession) Delete(context.Context, string) error           { return nil }
func (liveSession) DeleteUser(context.Context, domain.UserID) error {
	return nil
}
func (liveSession) DeleteOthers(context.Context, domain.UserID, string) error { return nil }
func (liveSession) Refresh(context.Context, string, string, time.Time) error  { return nil }
func (liveSession) DeleteEnded(context.Context, domain.UserID, time.Time, time.Duration) error {
	return nil
}
func (liveSession) List(context.Context, domain.UserID) ([]app.Device, error) { return nil, nil }

type disabledPassword struct{}

func (disabledPassword) Take(context.Context, string, time.Time, float64, float64) (bool, time.Duration, error) {
	return true, 0, nil
}
func (disabledPassword) PasswordAccount(context.Context, string) (domain.UserID, string, bool, error) {
	return domain.UserID{}, "", false, nil
}
func (disabledPassword) PersonByEmail(context.Context, string) (domain.UserID, bool, bool, error) {
	return domain.UserID{}, false, false, nil
}
func (disabledPassword) Profile(context.Context, domain.UserID, []byte) (string, string, error) {
	return "", "", app.ErrNotSignedIn
}
func (disabledPassword) SetPassword(context.Context, domain.UserID, string) error { return nil }
func (disabledPassword) ClearPassword(context.Context, domain.UserID) error       { return nil }
func (disabledPassword) HasPassword(context.Context, domain.UserID) (bool, error) {
	return false, nil
}
func (disabledPassword) ClaimOffer(context.Context, domain.UserID, time.Time) (bool, error) {
	return false, nil
}
func (disabledPassword) SignInPolicy(context.Context, domain.UserID) (bool, string, error) {
	return false, "", nil
}

type disabledPass struct{}

func (disabledPass) SweepChallenges(context.Context, time.Time) error { return nil }
func (disabledPass) SweepLinks(context.Context, time.Time) error      { return nil }
func (disabledPass) SaveChallenge(context.Context, []byte, []byte, string, *domain.UserID, time.Time) error {
	return nil
}
func (disabledPass) TakeChallenge(context.Context, []byte, time.Time) ([]byte, string, domain.UserID, bool, error) {
	return nil, "", domain.UserID{}, false, nil
}
func (disabledPass) InsertLink(context.Context, []byte, domain.UserID, string, time.Time) error {
	return nil
}
func (disabledPass) FindLink(context.Context, []byte, time.Time) (domain.UserID, string, bool, error) {
	return domain.UserID{}, "", false, nil
}
func (disabledPass) SpendLink(context.Context, []byte, time.Time) error { return nil }
func (disabledPass) Handle(context.Context, domain.UserID, []byte) ([]byte, error) {
	return []byte{1}, nil
}
func (disabledPass) Account(context.Context, domain.UserID) (domain.Account, error) {
	return domain.Account{}, app.ErrNotSignedIn
}
func (disabledPass) OwnEmail(context.Context, []byte) (string, error) {
	return "", app.ErrNotSignedIn
}
func (disabledPass) LinkEmail(context.Context, []byte) (string, error) {
	return "", app.ErrInvalidLink
}
func (disabledPass) AccountByCredential(context.Context, []byte, []byte) (domain.Account, error) {
	return domain.Account{}, app.ErrNotSignedIn
}
func (disabledPass) InsertPasskey(context.Context, domain.UserID, domain.Credential, string, time.Time) (string, error) {
	return "", nil
}
func (disabledPass) TouchPasskey(context.Context, []byte, uint32, time.Time) error { return nil }
func (disabledPass) Join(context.Context, domain.UserID, time.Time) error          { return nil }
func (disabledPass) Take(context.Context, string, time.Time, float64, float64) (bool, time.Duration, error) {
	return true, 0, nil
}
func (disabledPass) UserByEmail(context.Context, string) (domain.UserID, bool, error) {
	return domain.UserID{}, false, nil
}
func (disabledPass) Bootstrap(context.Context, string, string, []byte, time.Time, time.Time) (domain.UserID, error) {
	return domain.UserID{}, nil
}
func (disabledPass) SignInPolicy(context.Context, domain.UserID) (bool, string, error) {
	return false, "", nil
}
func (disabledPass) ListPasskeys(context.Context, domain.UserID) ([]app.PasskeyRecord, error) {
	return nil, nil
}
func (disabledPass) RenamePasskey(context.Context, domain.UserID, string, string) error { return nil }
func (disabledPass) DeletePasskey(context.Context, domain.UserID, string) ([]byte, error) {
	return nil, nil
}
