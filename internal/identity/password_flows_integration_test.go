//go:build integration

package identity_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	httpapi "github.com/upmo-ai/upmo/internal/identity/adapters/http"
	"github.com/upmo-ai/upmo/internal/identity/adapters/webauthn"
	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/testutil/sealer"
	"github.com/upmo-ai/upmo/internal/testutil/workerkeys"
)

const (
	tryAgain          = `{"error":"try again"}` + "\n"
	wrongPasswordBody = `{"error":"Email or password is wrong"}` + "\n"
)

// storedPassword is a person's stored password as the owner reads it, ""
// for none. A set one must be five fields under k1 that open to a PHC.
func storedPassword(t *testing.T, ctx context.Context, owner *pgxpool.Pool, id string) string {
	t.Helper()
	var stored *string
	if err := owner.QueryRow(ctx, `SELECT password_hash FROM identity.users WHERE id = $1`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == nil {
		return ""
	}
	user, err := domain.ParseUserID(id)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(*stored, ":")
	hash, err := sealer.Open(t, user, *stored)
	if len(parts) != 5 || parts[0] != "k1" || strings.Contains(*stored, "$argon2id$") || err != nil || !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("stored password %s opens to a PHC %t (%v), want five fields under k1", *stored, strings.HasPrefix(hash, "$argon2id$"), err)
	}
	return *stored
}

// Invariant 9: brain's password routes over the real worker. Change, clear
// while a passkey remains, a recover link then a new password, an invite
// accepted with a password, and the step-up re-check all work; with the
// worker stopped, sign-in and set answer try again and start no session.
func TestPasswordFlowsThroughTheWorker(t *testing.T) {
	owner, pool, ctx := upBoth(t)
	clock := dbNow(t, ctx, owner)
	now := func() time.Time { return clock }
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	worker := workerkeys.Start(t, owner, sealer.Ring(t))
	emails := sealer.WorkerEmailOn(t, worker)
	sessions := identity.Sessions(pool, domain.IdleTimeout, domain.MaxAge)
	sessions.Now = now
	passkeys, err := identity.Passkeys(pool, emails, sessions, "http://localhost:5173", "localhost", log)
	if err != nil {
		t.Fatal(err)
	}
	passkeys.Now = now
	passwords := identity.Passwords(pool, emails, sessions)
	passwords.Keys = sealer.PasswordsOn(t, worker)
	passwords.Now = now
	members := identity.Members(pool, emails, "https://upmo.test", nil, log)
	members.Now = now
	mux := http.NewServeMux()
	identity.MountSessions(mux, sessions, log, passwords.HasPassword)
	identity.MountPasskeys(mux, passkeys, passwords, nil)
	identity.MountMembers(mux, members, sessions)
	signIn := func(email, password string) *httptest.ResponseRecorder {
		return postPassword(t, mux, http.MethodPost, "/api/sign-in/password", "", map[string]string{"email": email, "password": password})
	}
	set := func(session, password string) *httptest.ResponseRecorder {
		return postPassword(t, mux, http.MethodPut, "/api/password", session, map[string]string{"password": password})
	}

	link, err := passkeys.Bootstrap(ctx, "ada@example.com", "Ada")
	if err != nil {
		t.Fatal(err)
	}
	soft, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	ada := finishLink(t, mux, soft, link)
	adaID := userByEmail(t, pool, ctx, "ada@example.com")

	// Set, then change: each is a new five-field value, and only the new
	// password signs in.
	if rec := set(ada, "correct horse battery"); rec.Code != http.StatusNoContent {
		t.Fatalf("set = %d %s", rec.Code, rec.Body.String())
	}
	first := storedPassword(t, ctx, owner, adaID)
	if rec := set(ada, "another horse battery"); rec.Code != http.StatusNoContent {
		t.Fatalf("change = %d %s", rec.Code, rec.Body.String())
	}
	if changed := storedPassword(t, ctx, owner, adaID); changed == first {
		t.Fatal("the change kept the stored value")
	}
	if rec := signIn("ada@example.com", "correct horse battery"); rec.Code != http.StatusUnauthorized || rec.Body.String() != wrongPasswordBody {
		t.Fatalf("old password = %d %q", rec.Code, rec.Body.String())
	}
	if rec := signIn("ada@example.com", "another horse battery"); rec.Code != http.StatusOK {
		t.Fatalf("new password = %d %s", rec.Code, rec.Body.String())
	}

	// Clear while the passkey remains: nothing is stored and the password no
	// longer signs in.
	if rec := postPassword(t, mux, http.MethodDelete, "/api/password", ada, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("clear = %d %s", rec.Code, rec.Body.String())
	}
	if stored := storedPassword(t, ctx, owner, adaID); stored != "" {
		t.Fatalf("stored after clear = %q", stored)
	}
	if rec := call(mux, http.MethodGet, "/api/session", ada, nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"has_password":false`) {
		t.Fatalf("session after clear = %d %s", rec.Code, rec.Body.String())
	}
	if rec := signIn("ada@example.com", "another horse battery"); rec.Code != http.StatusUnauthorized || rec.Body.String() != wrongPasswordBody {
		t.Fatalf("cleared password = %d %q", rec.Code, rec.Body.String())
	}

	// A recover link, then a new password.
	token, err := members.Recover(ctx, "ada@example.com")
	if err != nil {
		t.Fatal(err)
	}
	// A recover link is for a lost device: a new authenticator.
	lost, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	recovered := finishLink(t, mux, lost, token)
	if rec := set(recovered, "recovered horse battery"); rec.Code != http.StatusNoContent {
		t.Fatalf("set after recover = %d %s", rec.Code, rec.Body.String())
	}
	storedPassword(t, ctx, owner, adaID)
	if rec := signIn("ada@example.com", "recovered horse battery"); rec.Code != http.StatusOK {
		t.Fatalf("sign-in after recover = %d %s", rec.Code, rec.Body.String())
	}

	// An invite accepted, then a password for the new member.
	var memberRole string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM identity.roles WHERE key = 'member'`).Scan(&memberRole); err != nil {
		t.Fatal(err)
	}
	rec := call(mux, http.MethodPost, "/api/members", recovered, jsonBody(t, map[string]string{
		"email": "bob@example.com", "name": "Bob", "role_id": memberRole,
	}))
	var invited struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &invited); rec.Code != http.StatusOK || err != nil || invited.Token == "" {
		t.Fatalf("invite = %d %s", rec.Code, rec.Body.String())
	}
	bobSoft, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	bob := finishLink(t, mux, bobSoft, invited.Token)
	if rec := set(bob, "tide copper leaflet"); rec.Code != http.StatusNoContent {
		t.Fatalf("invited set = %d %s", rec.Code, rec.Body.String())
	}
	storedPassword(t, ctx, owner, invited.ID)
	signed := signIn("bob@example.com", "tide copper leaflet")
	if signed.Code != http.StatusOK {
		t.Fatalf("invited sign-in = %d %s", signed.Code, signed.Body.String())
	}
	bobPassword := cookie(t, signed, httpapi.CookieName)

	// The step-up re-check: a stale session may not set, the session's own
	// password makes it fresh, and another person's password does not.
	clock = clock.Add(11 * time.Minute)
	if rec := set(bobPassword, "tide copper leaflet!"); rec.Code != http.StatusForbidden {
		t.Fatalf("stale set = %d %s", rec.Code, rec.Body.String())
	}
	if rec := postPassword(t, mux, http.MethodPost, "/api/session/verify/password", bobPassword, map[string]string{"password": "recovered horse battery"}); rec.Code != http.StatusUnauthorized || rec.Body.String() != wrongPasswordBody {
		t.Fatalf("verify with Ada's password = %d %q", rec.Code, rec.Body.String())
	}
	if rec := postPassword(t, mux, http.MethodPost, "/api/session/verify/password", bobPassword, map[string]string{"password": "tide copper leaflet"}); rec.Code != http.StatusNoContent {
		t.Fatalf("verify = %d %s", rec.Code, rec.Body.String())
	}
	if rec := set(bobPassword, "tide copper leaflet!"); rec.Code != http.StatusNoContent {
		t.Fatalf("set after verify = %d %s", rec.Code, rec.Body.String())
	}

	// The worker stops: try again, never a match, and no session starts.
	before := storedPassword(t, ctx, owner, invited.ID)
	var count int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM identity.sessions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	worker.Stop()
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"sign-in": signIn("bob@example.com", "tide copper leaflet!"),
		"set":     set(bobPassword, "tide copper leaflet?"),
	} {
		if rec.Code != http.StatusServiceUnavailable || rec.Body.String() != tryAgain || rec.Header().Get("Retry-After") != "1" {
			t.Errorf("%s with the worker down = %d %q retry %q, want 503 %q", name, rec.Code, rec.Body.String(), rec.Header().Get("Retry-After"), tryAgain)
		}
	}
	var after int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM identity.sessions`).Scan(&after); err != nil || after != count {
		t.Fatalf("sessions = %d %v, want %d", after, err, count)
	}
	if stored := storedPassword(t, ctx, owner, invited.ID); stored != before {
		t.Fatal("the stored password changed while the worker was down")
	}
}
