//go:build integration

package identity_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	httpapi "github.com/upmo-ai/upmo/internal/identity/adapters/http"
	"github.com/upmo-ai/upmo/internal/identity/adapters/webauthn"
	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/testutil/sealer"
)

const (
	stepUpPassword = "correct horse battery"
	grantBody      = `{"resource_scope":"these","resource_ids":["upmo-ai/upmo"],"changes":"allow"}`
	signInAgain    = "{\"error\":\"sign in again\"}\n"
)

// stepUpRig is the identity routes on a pinned clock, with Ada signed in
// through a real passkey from the soft authenticator. The clock starts at
// the database's now: the worker checks a session's expiry by that clock.
type stepUpRig struct {
	owner     *pgxpool.Pool
	pool      *pgxpool.Pool
	ctx       context.Context
	mux       *http.ServeMux
	clock     *time.Time
	day       time.Time
	passkeys  app.Passkeys
	passwords app.Passwords
	soft      *webauthn.Soft
	ada       domain.UserID
	cookie    string
}

func newStepUpRig(t *testing.T) *stepUpRig {
	t.Helper()
	owner, pool, ctx := upBoth(t)
	day := dbNow(t, ctx, owner).Truncate(time.Minute).Add(-11 * time.Minute)
	clock := day.Add(11 * time.Minute)
	now := func() time.Time { return clock }
	emails := sealer.NewEmail(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions := identity.Sessions(pool, domain.IdleTimeout, domain.MaxAge)
	sessions.Now = now
	passkeys, err := identity.Passkeys(pool, emails, sessions, "http://localhost:5173", "localhost", log)
	if err != nil {
		t.Fatal(err)
	}
	passkeys.Now = now
	passwords := identity.Passwords(pool, emails, sessions)
	passwords.Keys = sealer.Passwords(t, owner)
	passwords.Now = now
	mux := http.NewServeMux()
	identity.MountSessions(mux, sessions, log, passwords.HasPassword)
	identity.MountPasskeys(mux, passkeys, passwords, nil)
	identity.MountConnectorGrants(mux, identity.ConnectorGrants(pool), passkeys)
	identity.MountWorkspace(mux, identity.Workspace(pool), passkeys)

	token, err := passkeys.Bootstrap(ctx, "ada@example.com", "Ada")
	if err != nil {
		t.Fatal(err)
	}
	soft, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	r := &stepUpRig{owner: owner, pool: pool, ctx: ctx, mux: mux, clock: &clock, day: day, passkeys: passkeys, passwords: passwords, soft: soft}
	r.cookie = finishLink(t, mux, soft, token)
	r.ada = adaID(t, ctx, pool)
	return r
}

// when is h hours and m minutes after the rig's day began; a negative h is
// an earlier day.
func (r *stepUpRig) when(h, m int) time.Time {
	return r.day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
}

// at sets the session's sign-in method and time on its stored row.
func (r *stepUpRig) at(t *testing.T, user domain.UserID, method string, signedIn time.Time) {
	t.Helper()
	if _, err := r.owner.Exec(r.ctx, `UPDATE identity.sessions SET sign_in_method = $2, signed_in_at = $3 WHERE user_id = $1`, user, method, signedIn); err != nil {
		t.Fatal(err)
	}
}

// stepUp runs the passkey step-up ceremony with a real assertion.
func (r *stepUpRig) stepUp(t *testing.T) {
	t.Helper()
	passkeyStepUp(t, r.mux, r.soft, r.cookie, handleOf(t, r.ctx, r.pool))
}

// passkeyStepUp signs the session in again with the soft authenticator.
func passkeyStepUp(t *testing.T, mux http.Handler, soft *webauthn.Soft, session string, handle []byte) {
	t.Helper()
	options := call(mux, http.MethodPost, "/api/passkeys/step-up/options", session, nil)
	if options.Code != http.StatusOK {
		t.Fatalf("step-up options %d %s", options.Code, options.Body.String())
	}
	assertion, err := soft.Login("localhost", "http://localhost:5173", publicChallenge(t, options.Body.Bytes()), handle)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/passkeys/step-up/finish", bytes.NewReader(assertion))
	req.RemoteAddr = "198.51.100.9:1000"
	req.AddCookie(&http.Cookie{Name: httpapi.CookieName, Value: session})
	req.AddCookie(&http.Cookie{Name: httpapi.ChallengeCookie, Value: cookie(t, options, httpapi.ChallengeCookie)})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("step-up finish %d %s", rec.Code, rec.Body.String())
	}
}

// grant is the stored changes value of a person's github grant, or "" for none.
func (r *stepUpRig) grant(t *testing.T, user domain.UserID) string {
	t.Helper()
	var changes string
	err := r.owner.QueryRow(r.ctx, `SELECT coalesce((SELECT changes FROM identity.connector_grants WHERE user_id = $1 AND connector = 'github'), '')`, user).Scan(&changes)
	if err != nil {
		t.Fatal(err)
	}
	return changes
}

func (r *stepUpRig) signedIn(t *testing.T, user domain.UserID) (string, time.Time) {
	t.Helper()
	var method string
	var at time.Time
	if err := r.owner.QueryRow(r.ctx, `SELECT sign_in_method, signed_in_at FROM identity.sessions WHERE user_id = $1`, user).Scan(&method, &at); err != nil {
		t.Fatal(err)
	}
	return method, at.UTC()
}

func refused(t *testing.T, label string, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusForbidden || rec.Body.String() != signInAgain {
		t.Fatalf("%s = %d %q, want 403 %q", label, rec.Code, rec.Body.String(), signInAgain)
	}
}

func TestStepUpConnectorGrantPut(t *testing.T) {
	r := newStepUpRig(t)
	at := r.when

	r.at(t, r.ada, "password", at(0, 10))
	refused(t, "password session on a passkey account", call(r.mux, http.MethodPut, "/api/connector-grants/github", r.cookie, []byte(grantBody)))
	if got := r.grant(t, r.ada); got != "" {
		t.Fatalf("grant after password PUT = %q, want none", got)
	}

	r.at(t, r.ada, "passkey", at(0, 0))
	refused(t, "passkey 11 minutes ago", call(r.mux, http.MethodPut, "/api/connector-grants/github", r.cookie, []byte(grantBody)))
	if got := r.grant(t, r.ada); got != "" {
		t.Fatalf("grant after stale PUT = %q, want none", got)
	}

	*r.clock = at(0, 10)
	r.stepUp(t)
	*r.clock = at(0, 11)
	if method, signed := r.signedIn(t, r.ada); method != "passkey" || !signed.Equal(at(0, 10)) {
		t.Fatalf("session after step-up = %s %s, want passkey %s", method, signed, at(0, 10))
	}
	saved := call(r.mux, http.MethodPut, "/api/connector-grants/github", r.cookie, []byte(grantBody))
	if saved.Code != http.StatusOK {
		t.Fatalf("fresh passkey PUT = %d %s", saved.Code, saved.Body.String())
	}
	if got := r.grant(t, r.ada); got != "allow" {
		t.Fatalf("stored changes = %q, want allow", got)
	}
}

func TestStepUpConnectorGrantDelete(t *testing.T) {
	r := newStepUpRig(t)
	if saved := call(r.mux, http.MethodPut, "/api/connector-grants/github", r.cookie, []byte(grantBody)); saved.Code != http.StatusForbidden {
		t.Fatalf("link session PUT = %d %s, want 403", saved.Code, saved.Body.String())
	}
	r.stepUp(t)
	if saved := call(r.mux, http.MethodPut, "/api/connector-grants/github", r.cookie, []byte(grantBody)); saved.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", saved.Code, saved.Body.String())
	}

	r.at(t, r.ada, "passkey", r.when(-24, 11))
	refused(t, "stale DELETE", call(r.mux, http.MethodDelete, "/api/connector-grants/github", r.cookie, nil))
	if got := r.grant(t, r.ada); got != "allow" {
		t.Fatalf("grant after stale DELETE = %q, want allow", got)
	}

	r.stepUp(t)
	if removed := call(r.mux, http.MethodDelete, "/api/connector-grants/github", r.cookie, nil); removed.Code != http.StatusNoContent {
		t.Fatalf("fresh DELETE = %d %s", removed.Code, removed.Body.String())
	}
	if got := r.grant(t, r.ada); got != "" {
		t.Fatalf("grant after fresh DELETE = %q, want none", got)
	}
}

func TestStepUpPasswordVerify(t *testing.T) {
	r := newStepUpRig(t)
	sessionID := sessionOf(t, r, r.ada)
	if err := r.passwords.Set(r.ctx, r.ada, sessionID, "ada@example.com", "Ada", stepUpPassword); err != nil {
		t.Fatal(err)
	}
	verify, _ := json.Marshal(map[string]string{"password": stepUpPassword})

	// One passkey: a password verify moves signed_in_at but is not a step-up.
	r.at(t, r.ada, "passkey", r.when(-24, 11))
	if rec := call(r.mux, http.MethodPost, "/api/session/verify/password", r.cookie, verify); rec.Code != http.StatusNoContent {
		t.Fatalf("verify = %d %s", rec.Code, rec.Body.String())
	}
	if method, signed := r.signedIn(t, r.ada); method != "password" || !signed.Equal(*r.clock) {
		t.Fatalf("session after verify = %s %s", method, signed)
	}
	refused(t, "PUT after password verify", call(r.mux, http.MethodPut, "/api/connector-grants/github", r.cookie, []byte(grantBody)))
	if got := r.grant(t, r.ada); got != "" {
		t.Fatalf("grant = %q, want none", got)
	}

	// Zero passkeys: the same verify is a step-up.
	emails := sealer.NewEmail(t)
	var id string
	if err := r.owner.QueryRow(r.ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
		VALUES ($1, $2, $3, 'Bob', (SELECT id FROM identity.roles WHERE key = 'member'), now())
		RETURNING id::text`, sealUser(t, emails, "bob@example.com")...).Scan(&id); err != nil {
		t.Fatal(err)
	}
	bob, err := domain.ParseUserID(id)
	if err != nil {
		t.Fatal(err)
	}
	bobCookie, bobSession, err := r.passkeys.Sessions.Start(r.ctx, bob, "password", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.passwords.Set(r.ctx, bob, bobSession.ID, "bob@example.com", "Bob", stepUpPassword); err != nil {
		t.Fatal(err)
	}
	r.at(t, bob, "password", r.when(-24, 11))
	refused(t, "stale password PUT", call(r.mux, http.MethodPut, "/api/connector-grants/github", bobCookie, []byte(grantBody)))
	if rec := call(r.mux, http.MethodPost, "/api/session/verify/password", bobCookie, verify); rec.Code != http.StatusNoContent {
		t.Fatalf("bob verify = %d %s", rec.Code, rec.Body.String())
	}
	if saved := call(r.mux, http.MethodPut, "/api/connector-grants/github", bobCookie, []byte(grantBody)); saved.Code != http.StatusOK {
		t.Fatalf("PUT after verify without a passkey = %d %s", saved.Code, saved.Body.String())
	}
	if got := r.grant(t, bob); got != "allow" {
		t.Fatalf("bob changes = %q, want allow", got)
	}
}

func TestStepUpPasskeyRename(t *testing.T) {
	r := newStepUpRig(t)
	var id, before string
	if err := r.owner.QueryRow(r.ctx, `SELECT id::text, name FROM identity.passkeys WHERE user_id = $1`, r.ada).Scan(&id, &before); err != nil {
		t.Fatal(err)
	}
	name := func() string {
		var got string
		if err := r.owner.QueryRow(r.ctx, `SELECT name FROM identity.passkeys WHERE id = $1::uuid`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		return got
	}

	r.at(t, r.ada, "password", r.when(0, 10))
	refused(t, "password rename", call(r.mux, http.MethodPatch, "/api/passkeys/"+id, r.cookie, []byte(`{"name":"desk"}`)))
	if got := name(); got != before {
		t.Fatalf("name after refused rename = %q, want %q", got, before)
	}

	r.stepUp(t)
	if rec := call(r.mux, http.MethodPatch, "/api/passkeys/"+id, r.cookie, []byte(`{"name":"desk"}`)); rec.Code != http.StatusNoContent {
		t.Fatalf("fresh rename = %d %s", rec.Code, rec.Body.String())
	}
	if got := name(); got != "desk" {
		t.Fatalf("name = %q, want desk", got)
	}
}

func TestStepUpWorkspacePut(t *testing.T) {
	r := newStepUpRig(t)
	ssoRequired := func() bool {
		var on bool
		if err := r.owner.QueryRow(r.ctx, `SELECT sso_required FROM identity.workspace`).Scan(&on); err != nil {
			t.Fatal(err)
		}
		return on
	}

	r.at(t, r.ada, "passkey", r.when(-24, 11))
	refused(t, "stale workspace PUT", callWorkspace(t, r.mux, http.MethodPut, "/api/workspace", r.cookie, workspacePayload(map[string]any{"sso_required": true})))
	if ssoRequired() {
		t.Fatal("sso_required = true after a refused PUT")
	}

	r.stepUp(t)
	if rec := callWorkspace(t, r.mux, http.MethodPut, "/api/workspace", r.cookie, workspacePayload(map[string]any{"sso_required": true})); rec.Code != http.StatusOK {
		t.Fatalf("fresh workspace PUT = %d %s", rec.Code, rec.Body.String())
	}
	if !ssoRequired() {
		t.Fatal("sso_required = false after a fresh PUT")
	}
}

func sessionOf(t *testing.T, r *stepUpRig, user domain.UserID) string {
	t.Helper()
	var id string
	if err := r.owner.QueryRow(r.ctx, `SELECT id::text FROM identity.sessions WHERE user_id = $1`, user).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// dbNow is the database's clock, in UTC to the second.
func dbNow(t *testing.T, ctx context.Context, pool *pgxpool.Pool) time.Time {
	t.Helper()
	var now time.Time
	if err := pool.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	return now.UTC().Truncate(time.Second)
}
