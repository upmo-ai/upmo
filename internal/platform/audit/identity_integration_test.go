//go:build integration

package audit_test

// The identity actions seal one chained event per change, in the transaction
// that makes the change. These tests drive the real handlers behind the audit
// middleware the way brain mounts them, then read the sealed rows.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	httpapi "github.com/upmo-ai/upmo/internal/identity/adapters/http"
	"github.com/upmo-ai/upmo/internal/identity/adapters/webauthn"
	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/testutil/sealer"
)

// kit is the identity area on one mux, wrapped by the audit middleware.
type kit struct {
	owner    *pgxpool.Pool
	app      *pgxpool.Pool
	served   http.Handler
	passkeys app.Passkeys
}

func newKit(t *testing.T) *kit {
	t.Helper()
	owner, appPool := setup(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions := identity.Sessions(appPool, identity.IdleTimeout, identity.MaxAge)
	emails := sealer.WorkerEmail(t, owner)
	passkeys, err := identity.Passkeys(appPool, emails, sessions, "http://localhost:5173", "localhost", log)
	if err != nil {
		t.Fatal(err)
	}
	passwords := identity.Passwords(appPool, emails, sessions)
	passwords.Keys = sealer.Passwords(t, owner)
	mux := http.NewServeMux()
	// MountAPITokens registers the session routes and accepts a bearer on
	// GET /api/session, as brain does; there is no separate MountSessions.
	identity.MountAPITokens(mux, sessions, identity.APITokens(appPool, log), log, passwords.HasPassword, appPool, nil)
	identity.MountPasskeys(mux, passkeys, passwords, nil)
	identity.MountMembers(mux, identity.Members(appPool, emails, "http://localhost:5173", nil, log), sessions)
	identity.MountRoles(mux, identity.Roles(appPool, log), sessions)
	identity.MountTeams(mux, identity.Teams(appPool, log), sessions)
	identity.MountConnectorGrants(mux, identity.ConnectorGrants(appPool), passkeys)
	identity.MountWorkspace(mux, identity.Workspace(appPool), passkeys)
	return &kit{owner: owner, app: appPool, served: mount(t, mux, appPool), passkeys: passkeys}
}

// call runs one request against the mounted area. cookies maps cookie names to
// values; a nil payload sends an empty body.
func (k *kit) call(method, path string, cookies map[string]string, payload any) *httptest.ResponseRecorder {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			panic(err)
		}
		body = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, body)
	req.RemoteAddr = "198.51.100.9:1000"
	for name, value := range cookies {
		req.AddCookie(&http.Cookie{Name: name, Value: value})
	}
	rec := httptest.NewRecorder()
	k.served.ServeHTTP(rec, req)
	return rec
}

func (k *kit) sessionCall(method, path, session string, payload any) *httptest.ResponseRecorder {
	return k.call(method, path, map[string]string{httpapi.CookieName: session}, payload)
}

// signInOwner bootstraps the first owner and signs them in through the audited
// link route. TestLinkSignInSealsTheEvent asserts that event.
func (k *kit) signInOwner(t *testing.T, email string) string {
	t.Helper()
	token, err := k.passkeys.Bootstrap(context.Background(), email, "Ada")
	if err != nil {
		t.Fatal(err)
	}
	soft, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	return k.finishLink(t, soft, token)
}

func (k *kit) finishLink(t *testing.T, soft *webauthn.Soft, token string) string {
	t.Helper()
	rec := k.call(http.MethodPost, "/api/passkeys/link/options", nil, map[string]string{"token": token})
	if rec.Code != http.StatusOK {
		t.Fatalf("link options = %d %s", rec.Code, rec.Body.String())
	}
	registered, err := soft.Register("localhost", "http://localhost:5173", challengeOf(t, rec.Body.Bytes()), userHandleOf(t, rec.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	cookies := map[string]string{httpapi.ChallengeCookie: cookieValue(t, rec, httpapi.ChallengeCookie)}
	finish := k.call(http.MethodPost, "/api/passkeys/link/finish", cookies, map[string]any{
		"token": token, "response": json.RawMessage(registered),
	})
	if finish.Code != http.StatusNoContent {
		t.Fatalf("link finish = %d %s", finish.Code, finish.Body.String())
	}
	return cookieValue(t, finish, httpapi.CookieName)
}

// passkeySessions marks the link sessions as passkey sign-ins, as a passkey
// step-up would, so step-up routes pass without the step-up's own event.
func (k *kit) passkeySessions(t *testing.T) {
	t.Helper()
	if _, err := k.owner.Exec(context.Background(), `UPDATE identity.sessions SET sign_in_method = 'passkey' WHERE sign_in_method = 'link'`); err != nil {
		t.Fatal(err)
	}
}

// addPasskey runs the add ceremony for one soft authenticator.
func (k *kit) addPasskey(t *testing.T, session string, soft *webauthn.Soft) {
	t.Helper()
	rec := k.sessionCall(http.MethodPost, "/api/passkeys/options", session, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("add options = %d %s", rec.Code, rec.Body.String())
	}
	created, err := soft.Register("localhost", "http://localhost:5173", challengeOf(t, rec.Body.Bytes()), nil)
	if err != nil {
		t.Fatal(err)
	}
	cookies := map[string]string{
		httpapi.CookieName:      session,
		httpapi.ChallengeCookie: cookieValue(t, rec, httpapi.ChallengeCookie),
	}
	finish := k.call(http.MethodPost, "/api/passkeys/finish", cookies, json.RawMessage(created))
	if finish.Code != http.StatusNoContent {
		t.Fatalf("add finish = %d %s", finish.Code, finish.Body.String())
	}
}

// lastEvent seals everything pending and returns the newest sealed row, chain
// verified. Every success must come from a store's own audit.Record: a success
// with the middleware's unrecorded fallback reason means a store skipped it,
// so the kit fails even though action and target look right.
func lastEvent(t *testing.T, owner *pgxpool.Pool) (audit.Row, int64) {
	t.Helper()
	ctx := context.Background()
	out := &bytes.Buffer{}
	if _, err := audit.SealOnce(ctx, owner, out); err != nil {
		t.Fatal(err)
	}
	var position int64
	if err := owner.QueryRow(ctx, `SELECT position FROM audit.head WHERE one`).Scan(&position); err != nil {
		t.Fatal(err)
	}
	checkChain(t, owner, position)
	var unrecorded int
	if err := owner.QueryRow(ctx,
		`SELECT count(*) FROM audit.events WHERE outcome = 'success' AND reason = 'unrecorded'`,
	).Scan(&unrecorded); err != nil {
		t.Fatal(err)
	}
	if unrecorded != 0 {
		t.Fatalf("%d sealed successes carry the unrecorded fallback: a store skipped its audit.Record", unrecorded)
	}
	row, _, _ := readEvent(t, owner, position)
	return row, position
}

// rowDump reads one sealed row as the JSON document the whole row makes.
func rowDump(t *testing.T, owner *pgxpool.Pool, position int64) string {
	t.Helper()
	var doc []byte
	if err := owner.QueryRow(context.Background(),
		`SELECT to_jsonb(e) FROM audit.events AS e WHERE position = $1`, position,
	).Scan(&doc); err != nil {
		t.Fatal(err)
	}
	return string(doc)
}

// freeOf proves the sealed row carries none of the secret words: no name, no
// email, no typed secret.
func freeOf(t *testing.T, doc string, secrets ...string) {
	t.Helper()
	// Hashes and ids are hex, so a short name such as "ada" can appear in them by
	// chance; only the fields a person could have typed are searched.
	var fields map[string]any
	if err := json.Unmarshal([]byte(doc), &fields); err == nil {
		for _, key := range []string{"id", "hash", "prev_hash", "request_id", "actor_id", "target_id", "credential_id", "actor_role_id"} {
			delete(fields, key)
		}
		if stripped, err := json.Marshal(fields); err == nil {
			doc = string(stripped)
		}
	}
	lower := strings.ToLower(doc)
	for _, secret := range secrets {
		if strings.Contains(lower, strings.ToLower(secret)) {
			t.Fatalf("the sealed row carries %q: %s", secret, doc)
		}
	}
}

func challengeOf(t *testing.T, options []byte) string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(options, &payload); err != nil {
		t.Fatal(err)
	}
	public, _ := payload["publicKey"].(map[string]any)
	challenge, _ := public["challenge"].(string)
	if challenge == "" {
		t.Fatalf("no challenge in %s", options)
	}
	return challenge
}

func userHandleOf(t *testing.T, options []byte) []byte {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(options, &payload); err != nil {
		t.Fatal(err)
	}
	public, _ := payload["publicKey"].(map[string]any)
	user, _ := public["user"].(map[string]any)
	encoded, _ := user["id"].(string)
	if encoded == "" {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func cookieValue(t *testing.T, rec *httptest.ResponseRecorder, name string) string {
	t.Helper()
	for _, item := range rec.Result().Cookies() {
		if item.Name == name && item.Value != "" {
			return item.Value
		}
	}
	t.Fatalf("no cookie %s", name)
	return ""
}

// stringSet reads a stored change value that is a list of names.
func stringSet(t *testing.T, got any) []string {
	t.Helper()
	list, ok := got.([]any)
	if !ok {
		t.Fatalf("change value %v is not a list", got)
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		out = append(out, item.(string))
	}
	sort.Strings(out)
	return out
}

func roleIDOf(t *testing.T, owner *pgxpool.Pool, key string) string {
	t.Helper()
	var id string
	if err := owner.QueryRow(context.Background(),
		`SELECT id::text FROM identity.roles WHERE key = $1`, key).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func memberIDByEmail(t *testing.T, owner *pgxpool.Pool, email string) string {
	t.Helper()
	var id string
	if err := owner.QueryRow(context.Background(),
		`SELECT id::text FROM identity.users WHERE email_hash = ANY($1)`, sealer.Hashes(t, email)).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func seedJoinedMember(t *testing.T, owner *pgxpool.Pool, email, name string) string {
	t.Helper()
	stored, hash, keyID, err := sealer.NewEmail(t).Seal(t.Context(), email)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err := owner.QueryRow(context.Background(), `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
		VALUES ($1, $2, $3, $4, (SELECT id FROM identity.roles WHERE key = 'member'), now())
		RETURNING id::text`, stored, hash, keyID, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func seedSession(t *testing.T, owner *pgxpool.Pool, user string) {
	t.Helper()
	if _, err := owner.Exec(context.Background(), `
		INSERT INTO identity.sessions (user_id, token_hash, created_at, last_seen_at, expires_at)
		VALUES ($1, decode(md5(random()::text) || md5(random()::text), 'hex'), now(), now(), now() + interval '1 day')`, user); err != nil {
		t.Fatal(err)
	}
}

func sessionCount(t *testing.T, owner *pgxpool.Pool, user string) int {
	t.Helper()
	var count int
	if err := owner.QueryRow(context.Background(),
		`SELECT count(*) FROM identity.sessions WHERE user_id = $1`, user).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func adaHandle(t *testing.T, owner *pgxpool.Pool) []byte {
	t.Helper()
	var handle []byte
	if err := owner.QueryRow(context.Background(),
		`SELECT webauthn_handle FROM identity.users WHERE email_hash = ANY($1)`, sealer.Hashes(t, "ada@example.com")).Scan(&handle); err != nil {
		t.Fatal(err)
	}
	return handle
}

func TestLinkSignInSealsTheEvent(t *testing.T) {
	k := newKit(t)
	cookie := k.signInOwner(t, "ada@example.com")
	if cookie == "" {
		t.Fatal("no session cookie")
	}

	row, position := lastEvent(t, k.owner)
	userID, roleID := lookupPerson(t, k.owner, "ada@example.com")
	if row.Action != audit.AuthSignIn || row.Outcome != audit.Success {
		t.Fatalf("event = %s %s", row.Action, row.Outcome)
	}
	if row.ActorType != "user" || row.ActorID != userID || row.ActorRoleID != roleID {
		t.Fatalf("actor = %s %s %s", row.ActorType, row.ActorID, row.ActorRoleID)
	}
	var sessionID string
	if err := k.owner.QueryRow(context.Background(),
		`SELECT id::text FROM identity.sessions WHERE user_id = $1`, userID).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	if row.CredentialType != "session" || row.CredentialID != sessionID {
		t.Fatalf("credential = %s %s, want the opened session", row.CredentialType, row.CredentialID)
	}
	if row.TargetType != "session" || row.TargetID != sessionID {
		t.Fatalf("target = %s %s", row.TargetType, row.TargetID)
	}
	if method, ok := row.Changes.Values["method"]; !ok || method.To != "link" {
		t.Fatalf("method = %v, want link", row.Changes.Values["method"])
	}
	if row.RequestID == "" {
		t.Fatal("the sign-in event carries no request id")
	}
	freeOf(t, rowDump(t, k.owner, position), "ada@example.com", "ada", "Ada")
}

func TestPasswordSignInSealsAndAnnotates(t *testing.T) {
	k := newKit(t)
	cookie := k.signInOwner(t, "ada@example.com")
	const password = "tide-copper-leaflet-mango"
	if rec := k.sessionCall(http.MethodPut, "/api/password", cookie, map[string]string{"password": password}); rec.Code != http.StatusNoContent {
		t.Fatalf("password set = %d %s", rec.Code, rec.Body.String())
	}
	userID, _ := lookupPerson(t, k.owner, "ada@example.com")
	row, position := lastEvent(t, k.owner)
	if row.Action != audit.PasswordSet || row.TargetID != userID {
		t.Fatalf("event = %s target %s", row.Action, row.TargetID)
	}
	freeOf(t, rowDump(t, k.owner, position), password)

	// A correct password opens a session and names the method.
	rec := k.call(http.MethodPost, "/api/sign-in/password", nil, map[string]string{"email": "ada@example.com", "password": password})
	if rec.Code != http.StatusOK {
		t.Fatalf("password sign-in = %d %s", rec.Code, rec.Body.String())
	}
	row, position = lastEvent(t, k.owner)
	if row.Action != audit.AuthSignIn || row.Outcome != audit.Success {
		t.Fatalf("event = %s %s", row.Action, row.Outcome)
	}
	if method, ok := row.Changes.Values["method"]; !ok || method.To != "password" {
		t.Fatalf("method = %v, want password", row.Changes.Values["method"])
	}
	if row.ActorID != userID {
		t.Fatalf("actor = %s, want %s", row.ActorID, userID)
	}
	freeOf(t, rowDump(t, k.owner, position), password, "ada@example.com")

	// An unknown account is denied and named, without a target.
	rec = k.call(http.MethodPost, "/api/sign-in/password", nil, map[string]string{"email": "nobody@example.com", "password": password})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown account = %d, want 401", rec.Code)
	}
	row, _ = lastEvent(t, k.owner)
	if row.Outcome != audit.Denied || row.Reason != "unknown_account" {
		t.Fatalf("event = %s %q", row.Outcome, row.Reason)
	}
	if row.ActorType != "anonymous" || row.TargetID != "" {
		t.Fatalf("actor %s target %s, want an anonymous denial with no target", row.ActorType, row.TargetID)
	}

	// A known account with the wrong password names the account.
	rec = k.call(http.MethodPost, "/api/sign-in/password", nil, map[string]string{"email": "ada@example.com", "password": "not-the-password"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d, want 401", rec.Code)
	}
	row, _ = lastEvent(t, k.owner)
	if row.Outcome != audit.Denied || row.Reason != "bad_credential" {
		t.Fatalf("event = %s %q", row.Outcome, row.Reason)
	}
	if row.TargetType != "user" || row.TargetID != userID {
		t.Fatalf("target = %s %s, want the known account", row.TargetType, row.TargetID)
	}

	// Guessing past the burst is throttled.
	rec = &httptest.ResponseRecorder{}
	for i := 0; i < 20 && rec.Code != http.StatusTooManyRequests; i++ {
		rec = k.call(http.MethodPost, "/api/sign-in/password", nil, map[string]string{"email": "ada@example.com", "password": "not-the-password"})
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the burst never filled: last %d", rec.Code)
	}
	row, _ = lastEvent(t, k.owner)
	if row.Outcome != audit.Denied || row.Reason != "throttled" {
		t.Fatalf("event = %s %q, want a throttled denial", row.Outcome, row.Reason)
	}
}

func TestDisabledSignInDeniedAudit(t *testing.T) {
	k := newKit(t)
	cookie := k.signInOwner(t, "ada@example.com")
	const password = "tide-copper-leaflet-mango"
	memberRole := roleIDOf(t, k.owner, "member")

	rec := k.sessionCall(http.MethodPost, "/api/members", cookie, map[string]string{
		"email": "carol@example.com", "name": "Carol", "role_id": memberRole,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("invite = %d %s", rec.Code, rec.Body.String())
	}
	carol := memberIDByEmail(t, k.owner, "carol@example.com")
	rec = k.sessionCall(http.MethodPost, "/api/members/"+carol+"/invite-link", cookie, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("invite link = %d %s", rec.Code, rec.Body.String())
	}
	var link struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &link); err != nil {
		t.Fatal(err)
	}
	carolSoft, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	if k.finishLink(t, carolSoft, link.Token) == "" {
		t.Fatal("carol did not join")
	}
	if rec := k.sessionCall(http.MethodDelete, "/api/members/"+carol, cookie, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("disable = %d %s", rec.Code, rec.Body.String())
	}

	const wrongPassword = `{"error":"Email or password is wrong"}` + "\n"

	disabled := k.call(http.MethodPost, "/api/sign-in/password", nil, map[string]string{
		"email": "carol@example.com", "password": password,
	})
	if disabled.Code != http.StatusUnauthorized || disabled.Body.String() != wrongPassword {
		t.Fatalf("disabled password = %d %q", disabled.Code, disabled.Body.String())
	}
	row, position := lastEvent(t, k.owner)
	if row.Outcome != audit.Denied || row.Reason != "disabled" || row.TargetID != carol {
		t.Fatalf("event = %s %q target %s", row.Outcome, row.Reason, row.TargetID)
	}
	freeOf(t, rowDump(t, k.owner, position), "carol@example.com", "carol", "Carol")

	unknown := k.call(http.MethodPost, "/api/sign-in/password", nil, map[string]string{
		"email": "nobody@example.com", "password": password,
	})
	wrong := k.call(http.MethodPost, "/api/sign-in/password", nil, map[string]string{
		"email": "ada@example.com", "password": "not-the-password",
	})
	if unknown.Code != http.StatusUnauthorized || unknown.Body.String() != wrongPassword {
		t.Fatalf("unknown password = %d %q", unknown.Code, unknown.Body.String())
	}
	if wrong.Code != http.StatusUnauthorized || wrong.Body.String() != wrongPassword {
		t.Fatalf("wrong password = %d %q", wrong.Code, wrong.Body.String())
	}
	if unknown.Body.String() != wrong.Body.String() || disabled.Body.String() != wrong.Body.String() {
		t.Fatal("password failure bodies differ")
	}
}

func TestPasskeyLifecycleSealsEvents(t *testing.T) {
	k := newKit(t)
	cookie := k.signInOwner(t, "ada@example.com")
	soft, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	k.addPasskey(t, cookie, soft)
	row, _ := lastEvent(t, k.owner)
	if row.Action != audit.PasskeyAdd || row.Outcome != audit.Success {
		t.Fatalf("event = %s %s", row.Action, row.Outcome)
	}

	// A second passkey keeps the first removable. The bootstrap link
	// redemption already gave Ada one, so the list holds three.
	second, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	k.addPasskey(t, cookie, second)
	ids := passkeyIDs(t, k, cookie)
	if len(ids) != 3 {
		t.Fatalf("passkeys = %v", ids)
	}
	row, _ = lastEvent(t, k.owner)
	if row.Action != audit.PasskeyAdd || row.TargetID != ids[2] {
		t.Fatalf("event = %s target %s, want %s", row.Action, row.TargetID, ids[2])
	}

	k.passkeySessions(t)
	if rec := k.sessionCall(http.MethodPatch, "/api/passkeys/"+ids[2], cookie, map[string]string{"name": "Travel key"}); rec.Code != http.StatusNoContent {
		t.Fatalf("rename = %d %s", rec.Code, rec.Body.String())
	}
	row, position := lastEvent(t, k.owner)
	if row.Action != audit.PasskeyRename || row.TargetID != ids[2] {
		t.Fatalf("event = %s target %s", row.Action, row.TargetID)
	}
	freeOf(t, rowDump(t, k.owner, position), "Travel key")

	// The stored passkey signs in again, discoverable.
	handle := adaHandle(t, k.owner)
	rec := k.call(http.MethodPost, "/api/passkeys/sign-in/options", nil, map[string]string{})
	if rec.Code != http.StatusOK {
		t.Fatalf("sign-in options = %d %s", rec.Code, rec.Body.String())
	}
	assertion, err := soft.Login("localhost", "http://localhost:5173", challengeOf(t, rec.Body.Bytes()), handle)
	if err != nil {
		t.Fatal(err)
	}
	cookies := map[string]string{httpapi.ChallengeCookie: cookieValue(t, rec, httpapi.ChallengeCookie)}
	finish := k.call(http.MethodPost, "/api/passkeys/sign-in/finish", cookies, json.RawMessage(assertion))
	if finish.Code != http.StatusNoContent {
		t.Fatalf("sign-in finish = %d %s", finish.Code, finish.Body.String())
	}
	row, _ = lastEvent(t, k.owner)
	if row.Action != audit.AuthSignIn {
		t.Fatalf("event = %s, want a sign-in", row.Action)
	}
	if method, ok := row.Changes.Values["method"]; !ok || method.To != "passkey" {
		t.Fatalf("method = %v, want passkey", row.Changes.Values["method"])
	}

	// The fresh sign-in may remove the spare passkey.
	fresh := cookieValue(t, finish, httpapi.CookieName)
	if rec := k.sessionCall(http.MethodDelete, "/api/passkeys/"+ids[2], fresh, nil); rec.Code != http.StatusOK {
		t.Fatalf("remove = %d %s", rec.Code, rec.Body.String())
	}
	row, _ = lastEvent(t, k.owner)
	if row.Action != audit.PasskeyRemove || row.TargetID != ids[2] {
		t.Fatalf("event = %s target %s", row.Action, row.TargetID)
	}
}

func passkeyIDs(t *testing.T, k *kit, session string) []string {
	t.Helper()
	rec := k.sessionCall(http.MethodGet, "/api/passkeys", session, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list passkeys = %d %s", rec.Code, rec.Body.String())
	}
	var rows []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ID)
	}
	return out
}

func TestStepUpSealsTheEvent(t *testing.T) {
	k := newKit(t)
	cookie := k.signInOwner(t, "ada@example.com")
	const password = "tide-copper-leaflet-mango"
	if rec := k.sessionCall(http.MethodPut, "/api/password", cookie, map[string]string{"password": password}); rec.Code != http.StatusNoContent {
		t.Fatalf("password set = %d %s", rec.Code, rec.Body.String())
	}

	if rec := k.sessionCall(http.MethodPost, "/api/session/verify/password", cookie, map[string]string{"password": password}); rec.Code != http.StatusNoContent {
		t.Fatalf("verify = %d %s", rec.Code, rec.Body.String())
	}
	row, _ := lastEvent(t, k.owner)
	if row.Action != audit.AuthStepUp || row.Outcome != audit.Success {
		t.Fatalf("event = %s %s", row.Action, row.Outcome)
	}
	if method, ok := row.Changes.Values["method"]; !ok || method.To != "password" {
		t.Fatalf("method = %v, want password", row.Changes.Values["method"])
	}
	if row.TargetType != "session" || row.TargetID == "" {
		t.Fatalf("target = %s %s", row.TargetType, row.TargetID)
	}

	// A wrong password is a denied step-up that names the account.
	userID, _ := lookupPerson(t, k.owner, "ada@example.com")
	rec := k.sessionCall(http.MethodPost, "/api/session/verify/password", cookie, map[string]string{"password": "not-it"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong verify = %d, want 401", rec.Code)
	}
	row, _ = lastEvent(t, k.owner)
	if row.Outcome != audit.Denied || row.Reason != "bad_credential" || row.TargetID != userID {
		t.Fatalf("event = %s %q target %s", row.Outcome, row.Reason, row.TargetID)
	}

	// A passkey step-up refreshes the session too.
	soft, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	k.addPasskey(t, cookie, soft)
	rec = k.sessionCall(http.MethodPost, "/api/passkeys/step-up/options", cookie, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("step-up options = %d %s", rec.Code, rec.Body.String())
	}
	assertion, err := soft.Login("localhost", "http://localhost:5173", challengeOf(t, rec.Body.Bytes()), adaHandle(t, k.owner))
	if err != nil {
		t.Fatal(err)
	}
	cookies := map[string]string{
		httpapi.CookieName:      cookie,
		httpapi.ChallengeCookie: cookieValue(t, rec, httpapi.ChallengeCookie),
	}
	finish := k.call(http.MethodPost, "/api/passkeys/step-up/finish", cookies, json.RawMessage(assertion))
	if finish.Code != http.StatusNoContent {
		t.Fatalf("step-up finish = %d %s", finish.Code, finish.Body.String())
	}
	row, _ = lastEvent(t, k.owner)
	if row.Action != audit.AuthStepUp || row.Outcome != audit.Success {
		t.Fatalf("event = %s %s", row.Action, row.Outcome)
	}
	if method, ok := row.Changes.Values["method"]; !ok || method.To != "passkey" {
		t.Fatalf("method = %v, want passkey", row.Changes.Values["method"])
	}
}

func TestFailedPasskeySignInAnnotates(t *testing.T) {
	k := newKit(t)
	cookie := k.signInOwner(t, "ada@example.com")
	soft, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	k.addPasskey(t, cookie, soft)
	handle := adaHandle(t, k.owner)

	// A credential nobody holds is an unknown account.
	stranger, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	rec := k.call(http.MethodPost, "/api/passkeys/sign-in/options", nil, map[string]string{})
	assertion, err := stranger.Login("localhost", "http://localhost:5173", challengeOf(t, rec.Body.Bytes()), handle)
	if err != nil {
		t.Fatal(err)
	}
	cookies := map[string]string{httpapi.ChallengeCookie: cookieValue(t, rec, httpapi.ChallengeCookie)}
	if finish := k.call(http.MethodPost, "/api/passkeys/sign-in/finish", cookies, json.RawMessage(assertion)); finish.Code != http.StatusUnauthorized {
		t.Fatalf("stranger finish = %d, want 401", finish.Code)
	}
	row, _ := lastEvent(t, k.owner)
	if row.Outcome != audit.Denied || row.Reason != "unknown_account" {
		t.Fatalf("event = %s %q", row.Outcome, row.Reason)
	}

	// A used challenge is a bad credential.
	rec = k.call(http.MethodPost, "/api/passkeys/sign-in/options", nil, map[string]string{})
	challengeCookie := cookieValue(t, rec, httpapi.ChallengeCookie)
	assertion, err = soft.Login("localhost", "http://localhost:5173", challengeOf(t, rec.Body.Bytes()), handle)
	if err != nil {
		t.Fatal(err)
	}
	cookies = map[string]string{httpapi.ChallengeCookie: challengeCookie}
	if finish := k.call(http.MethodPost, "/api/passkeys/sign-in/finish", cookies, json.RawMessage(assertion)); finish.Code != http.StatusNoContent {
		t.Fatalf("sign-in = %d %s", finish.Code, finish.Body.String())
	}
	replay := k.call(http.MethodPost, "/api/passkeys/sign-in/finish", cookies, json.RawMessage(assertion))
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("replay = %d, want 401", replay.Code)
	}
	row, _ = lastEvent(t, k.owner)
	if row.Outcome != audit.Denied || row.Reason != "bad_credential" {
		t.Fatalf("event = %s %q", row.Outcome, row.Reason)
	}
}

func TestSignOutEverywhereSealsTheEvent(t *testing.T) {
	k := newKit(t)
	first := k.signInOwner(t, "ada@example.com")
	const password = "tide-copper-leaflet-mango"
	cookie := passwordSession(t, k, first, password)

	if rec := k.sessionCall(http.MethodDelete, "/api/sessions", cookie, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("sign out everywhere = %d %s", rec.Code, rec.Body.String())
	}
	userID, _ := lookupPerson(t, k.owner, "ada@example.com")
	row, _ := lastEvent(t, k.owner)
	if row.Action != audit.AuthSignOutAll || row.Outcome != audit.Success {
		t.Fatalf("event = %s %s", row.Action, row.Outcome)
	}
	if row.TargetType != "user" || row.TargetID != userID {
		t.Fatalf("target = %s %s, want the person", row.TargetType, row.TargetID)
	}
	if row.ActorType != "user" || row.ActorID != userID {
		t.Fatalf("actor = %s %s", row.ActorType, row.ActorID)
	}
	if left := sessionCount(t, k.owner, userID); left != 0 {
		t.Fatalf("%d sessions survived", left)
	}
}

// passwordSession sets the password from an open session and opens a second
// session with it.
func passwordSession(t *testing.T, k *kit, first, password string) string {
	t.Helper()
	if rec := k.sessionCall(http.MethodPut, "/api/password", first, map[string]string{"password": password}); rec.Code != http.StatusNoContent {
		t.Fatalf("password set = %d %s", rec.Code, rec.Body.String())
	}
	rec := k.call(http.MethodPost, "/api/sign-in/password", nil, map[string]string{"email": "ada@example.com", "password": password})
	if rec.Code != http.StatusOK {
		t.Fatalf("password sign-in = %d %s", rec.Code, rec.Body.String())
	}
	return cookieValue(t, rec, httpapi.CookieName)
}

func TestSessionRevokeSealsTheEvent(t *testing.T) {
	k := newKit(t)
	first := k.signInOwner(t, "ada@example.com")
	kept := passwordSession(t, k, first, "tide-copper-leaflet-mango")
	// The password session is the one to revoke; the link session stays.
	userID, _ := lookupPerson(t, k.owner, "ada@example.com")
	var revoked string
	if err := k.owner.QueryRow(context.Background(), `
		SELECT id::text FROM identity.sessions
		WHERE user_id = $1 AND sign_in_method = 'password'`, userID).Scan(&revoked); err != nil {
		t.Fatal(err)
	}

	if rec := k.sessionCall(http.MethodDelete, "/api/sessions/"+revoked, kept, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d %s", rec.Code, rec.Body.String())
	}
	row, _ := lastEvent(t, k.owner)
	if row.Action != audit.AuthSessionRevoke || row.Outcome != audit.Success {
		t.Fatalf("event = %s %s", row.Action, row.Outcome)
	}
	if row.TargetType != "session" || row.TargetID != revoked {
		t.Fatalf("target = %s %s, want the revoked session", row.TargetType, row.TargetID)
	}
	if row.ActorID != userID || row.CredentialType != "session" {
		t.Fatalf("actor = %s %s", row.ActorID, row.CredentialType)
	}
	if left := sessionCount(t, k.owner, userID); left != 1 {
		t.Fatalf("%d sessions left, want the kept one", left)
	}
}

func TestMemberActionsSealEvents(t *testing.T) {
	k := newKit(t)
	cookie := k.signInOwner(t, "ada@example.com")
	memberRole := roleIDOf(t, k.owner, "member")

	rec := k.sessionCall(http.MethodPost, "/api/members", cookie, map[string]string{
		"email": "lee@example.com", "name": "Lee", "role_id": memberRole,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("invite = %d %s", rec.Code, rec.Body.String())
	}
	lee := memberIDByEmail(t, k.owner, "lee@example.com")
	row, position := lastEvent(t, k.owner)
	if row.Action != audit.MemberInvite || row.Outcome != audit.Success || row.TargetID != lee {
		t.Fatalf("event = %s target %s", row.Action, row.TargetID)
	}
	if row.ActorType != "user" {
		t.Fatalf("actor = %s", row.ActorType)
	}
	freeOf(t, rowDump(t, k.owner, position), "lee@example.com", "lee")

	if rec := k.sessionCall(http.MethodPost, "/api/members/"+lee+"/invite-link", cookie, nil); rec.Code != http.StatusOK {
		t.Fatalf("invite link = %d %s", rec.Code, rec.Body.String())
	}
	row, _ = lastEvent(t, k.owner)
	if row.Action != audit.MemberInviteLink || row.TargetID != lee {
		t.Fatalf("event = %s target %s", row.Action, row.TargetID)
	}

	carol := seedJoinedMember(t, k.owner, "carol@example.com", "Carol")
	if rec := k.sessionCall(http.MethodPost, "/api/members/"+carol+"/reset-link", cookie, nil); rec.Code != http.StatusOK {
		t.Fatalf("reset link = %d %s", rec.Code, rec.Body.String())
	}
	row, _ = lastEvent(t, k.owner)
	if row.Action != audit.MemberResetLink || row.TargetID != carol {
		t.Fatalf("event = %s target %s", row.Action, row.TargetID)
	}

	adminRole := roleIDOf(t, k.owner, "admin")
	if rec := k.sessionCall(http.MethodPut, "/api/members/"+carol+"/role", cookie, map[string]string{"role_id": adminRole}); rec.Code != http.StatusNoContent {
		t.Fatalf("role change = %d %s", rec.Code, rec.Body.String())
	}
	row, _ = lastEvent(t, k.owner)
	if row.Action != audit.MemberRoleChange || row.TargetID != carol {
		t.Fatalf("event = %s target %s", row.Action, row.TargetID)
	}
	change := row.Changes.Values["role_id"]
	if change.From != memberRole || change.To != adminRole {
		t.Fatalf("role_id = %v -> %v, want %s -> %s", change.From, change.To, memberRole, adminRole)
	}

	// The disable trigger ends the person's sessions; the event says so.
	seedSession(t, k.owner, carol)
	if rec := k.sessionCall(http.MethodDelete, "/api/members/"+carol, cookie, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("disable = %d %s", rec.Code, rec.Body.String())
	}
	row, _ = lastEvent(t, k.owner)
	if row.Action != audit.MemberDisable || row.TargetID != carol {
		t.Fatalf("event = %s target %s", row.Action, row.TargetID)
	}
	if ended, ok := row.Changes.Values["sessions_ended"]; !ok || ended.To != true {
		t.Fatalf("sessions_ended = %v, want true", row.Changes.Values["sessions_ended"])
	}
	if left := sessionCount(t, k.owner, carol); left != 0 {
		t.Fatalf("%d sessions survived the disable", left)
	}
	var disabled *time.Time
	if err := k.owner.QueryRow(context.Background(),
		`SELECT disabled_at FROM identity.users WHERE id = $1`, carol).Scan(&disabled); err != nil {
		t.Fatal(err)
	}
	if disabled == nil {
		t.Fatal("the disable did not disable")
	}
}

func TestRoleActionsSealEvents(t *testing.T) {
	k := newKit(t)
	cookie := k.signInOwner(t, "ada@example.com")

	rec := k.sessionCall(http.MethodPost, "/api/roles", cookie, map[string]any{
		"name": "Planner", "permissions": []string{"task:view"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	row, position := lastEvent(t, k.owner)
	if row.Action != audit.RoleCreate || row.TargetID != created.ID {
		t.Fatalf("event = %s target %s", row.Action, row.TargetID)
	}
	freeOf(t, rowDump(t, k.owner, position), "Planner")

	if rec := k.sessionCall(http.MethodPut, "/api/roles/"+created.ID, cookie, map[string]any{
		"name": "Planner", "permissions": []string{"task:view", "task:edit"},
	}); rec.Code != http.StatusNoContent {
		t.Fatalf("update = %d %s", rec.Code, rec.Body.String())
	}
	row, _ = lastEvent(t, k.owner)
	if row.Action != audit.RoleUpdate || row.TargetID != created.ID {
		t.Fatalf("event = %s target %s", row.Action, row.TargetID)
	}
	permissions := row.Changes.Values["permissions"]
	from, to := stringSet(t, permissions.From), stringSet(t, permissions.To)
	if len(from) != 1 || from[0] != "task:view" || len(to) != 2 {
		t.Fatalf("permissions = %v -> %v", from, to)
	}

	rec = k.sessionCall(http.MethodPost, "/api/roles/"+created.ID+"/duplicate", cookie, map[string]string{"name": "Planner Two"})
	if rec.Code != http.StatusOK {
		t.Fatalf("duplicate = %d %s", rec.Code, rec.Body.String())
	}
	var copied struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &copied); err != nil {
		t.Fatal(err)
	}
	row, _ = lastEvent(t, k.owner)
	// The duplicate is a role.create of its own row.
	if row.Action != audit.RoleCreate || row.TargetID != copied.ID || copied.ID == created.ID {
		t.Fatalf("event = %s target %s (source %s)", row.Action, row.TargetID, created.ID)
	}

	if rec := k.sessionCall(http.MethodDelete, "/api/roles/"+copied.ID, cookie, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete copy = %d %s", rec.Code, rec.Body.String())
	}
	row, _ = lastEvent(t, k.owner)
	if row.Action != audit.RoleDelete || row.TargetID != copied.ID {
		t.Fatalf("event = %s target %s", row.Action, row.TargetID)
	}

	if rec := k.sessionCall(http.MethodDelete, "/api/roles/"+created.ID, cookie, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	row, _ = lastEvent(t, k.owner)
	if row.Action != audit.RoleDelete || row.TargetID != created.ID {
		t.Fatalf("event = %s target %s", row.Action, row.TargetID)
	}
}

func TestTokenActionsSealEvents(t *testing.T) {
	k := newKit(t)
	cookie := k.signInOwner(t, "ada@example.com")

	rec := k.sessionCall(http.MethodPost, "/api/tokens", cookie, map[string]any{
		"name": "deploy-key", "scope": []string{"task:view"}, "days": 7,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	var made struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &made); err != nil {
		t.Fatal(err)
	}
	row, position := lastEvent(t, k.owner)
	if row.Action != audit.TokenCreate || row.TargetID != made.ID {
		t.Fatalf("event = %s target %s", row.Action, row.TargetID)
	}
	if scope := stringSet(t, row.Changes.Values["scope"].To); len(scope) != 1 || scope[0] != "task:view" {
		t.Fatalf("scope = %v", row.Changes.Values["scope"].To)
	}
	if _, err := time.Parse(time.RFC3339, row.Changes.Values["expires_at"].To.(string)); err != nil {
		t.Fatalf("expires_at = %v: %v", row.Changes.Values["expires_at"].To, err)
	}
	dump := rowDump(t, k.owner, position)
	freeOf(t, dump, made.Secret, "deploy-key")

	if rec := k.sessionCall(http.MethodDelete, "/api/tokens/"+made.ID, cookie, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d %s", rec.Code, rec.Body.String())
	}
	row, _ = lastEvent(t, k.owner)
	if row.Action != audit.TokenRevoke || row.TargetID != made.ID {
		t.Fatalf("event = %s target %s", row.Action, row.TargetID)
	}
}

func TestConnectorGrantActionsSealEvents(t *testing.T) {
	k := newKit(t)
	cookie := k.signInOwner(t, "ada@example.com")
	k.passkeySessions(t)

	rec := k.sessionCall(http.MethodPut, "/api/connector-grants/github", cookie, map[string]any{
		"resource_scope": "these", "resource_ids": []string{"upmo-ai/upmo"}, "changes": "allow",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("set = %d %s", rec.Code, rec.Body.String())
	}
	row, _ := lastEvent(t, k.owner)
	if row.Action != audit.ConnectorGrantSet || row.Outcome != audit.Success {
		t.Fatalf("event = %s %s", row.Action, row.Outcome)
	}
	if row.TargetType != "connector_grant" || row.TargetID != "github" {
		t.Fatalf("target = %s %s", row.TargetType, row.TargetID)
	}
	if scope := row.Changes.Values["resource_scope"].To; scope != "these" {
		t.Fatalf("resource_scope = %v", scope)
	}
	if policy := row.Changes.Values["changes"].To; policy != "allow" {
		t.Fatalf("changes = %v", policy)
	}

	if rec := k.sessionCall(http.MethodDelete, "/api/connector-grants/github", cookie, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("remove = %d %s", rec.Code, rec.Body.String())
	}
	row, _ = lastEvent(t, k.owner)
	if row.Action != audit.ConnectorGrantRemove || row.TargetID != "github" {
		t.Fatalf("event = %s target %s", row.Action, row.TargetID)
	}
}

func TestTeamActionsSealEvents(t *testing.T) {
	k := newKit(t)
	cookie := k.signInOwner(t, "ada@example.com")
	carol := seedJoinedMember(t, k.owner, "carol@example.com", "Carol")

	rec := k.sessionCall(http.MethodPost, "/api/teams", cookie, map[string]string{"name": "Ship"})
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	var made struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &made); err != nil {
		t.Fatal(err)
	}
	row, position := lastEvent(t, k.owner)
	if row.Action != audit.TeamCreate || row.TargetID != made.ID {
		t.Fatalf("event = %s target %s", row.Action, row.TargetID)
	}
	freeOf(t, rowDump(t, k.owner, position), "Ship")

	if rec := k.sessionCall(http.MethodPost, "/api/teams/"+made.ID+"/members", cookie, map[string]string{"user_id": carol}); rec.Code != http.StatusNoContent {
		t.Fatalf("add = %d %s", rec.Code, rec.Body.String())
	}
	row, _ = lastEvent(t, k.owner)
	if row.Action != audit.TeamMemberAdd || row.TargetID != made.ID {
		t.Fatalf("event = %s target %s", row.Action, row.TargetID)
	}
	if user := row.Changes.Values["user_id"].To; user != carol {
		t.Fatalf("user_id = %v, want %s", user, carol)
	}

	if rec := k.sessionCall(http.MethodDelete, "/api/teams/"+made.ID+"/members/"+carol, cookie, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("remove = %d %s", rec.Code, rec.Body.String())
	}
	row, _ = lastEvent(t, k.owner)
	if row.Action != audit.TeamMemberRemove || row.TargetID != made.ID {
		t.Fatalf("event = %s target %s", row.Action, row.TargetID)
	}
	if user := row.Changes.Values["user_id"].To; user != carol {
		t.Fatalf("user_id = %v, want %s", user, carol)
	}
}

func TestPasswordRemoveSealsTheEvent(t *testing.T) {
	k := newKit(t)
	cookie := k.signInOwner(t, "ada@example.com")
	const password = "tide-copper-leaflet-mango"
	if rec := k.sessionCall(http.MethodPut, "/api/password", cookie, map[string]string{"password": password}); rec.Code != http.StatusNoContent {
		t.Fatalf("password set = %d %s", rec.Code, rec.Body.String())
	}
	// A passkey must remain before the password may go.
	userID, _ := lookupPerson(t, k.owner, "ada@example.com")
	if _, err := k.owner.Exec(context.Background(), `
		INSERT INTO identity.passkeys (user_id, credential_id, public_key, sign_count, aaguid, transports, backup_eligible, backup_state, attestation, name, created_at)
		VALUES ($1, decode(md5(random()::text), 'hex'), decode(repeat('ab', 64), 'hex'), 0,
		        '00000000-0000-0000-0000-000000000000', '{}', false, false, 'none', 'Seed', now())`, userID); err != nil {
		t.Fatal(err)
	}

	if rec := k.sessionCall(http.MethodDelete, "/api/password", cookie, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("password remove = %d %s", rec.Code, rec.Body.String())
	}
	row, position := lastEvent(t, k.owner)
	if row.Action != audit.PasswordRemove || row.TargetID != userID {
		t.Fatalf("event = %s target %s", row.Action, row.TargetID)
	}
	freeOf(t, rowDump(t, k.owner, position), password)
}

// TestFailedAuditInsertRollsBackRoleCreate proves the coupling the other way:
// when the event cannot be written, the change does not happen either.
func TestFailedAuditInsertRollsBackRoleCreate(t *testing.T) {
	k := newKit(t)
	cookie := k.signInOwner(t, "ada@example.com")
	// The sign-in event is pending; seal it so the count below means only what
	// happens after the revoke.
	lastEvent(t, k.owner)
	if _, err := k.owner.Exec(context.Background(), `REVOKE INSERT ON audit.pending FROM upmo_app`); err != nil {
		t.Fatal(err)
	}

	rec := k.sessionCall(http.MethodPost, "/api/roles", cookie, map[string]any{
		"name": "Ghost", "permissions": []string{"task:view"},
	})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("create = %d %s, want 500", rec.Code, rec.Body.String())
	}
	var roles int
	if err := k.owner.QueryRow(context.Background(),
		`SELECT count(*) FROM identity.roles WHERE name = 'Ghost'`).Scan(&roles); err != nil {
		t.Fatal(err)
	}
	if roles != 0 {
		t.Fatal("the role survived its failed audit insert")
	}
	var pending int
	if err := k.owner.QueryRow(context.Background(), `SELECT count(*) FROM audit.pending`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("%d pending rows survived the refused insert", pending)
	}
}

// TestFailedAuditInsertRollsBackRoleChange proves the coupling for PUT
// /api/roles/{id}: when the event cannot be written, the role keeps its old
// name and permissions.
func TestFailedAuditInsertRollsBackRoleChange(t *testing.T) {
	k := newKit(t)
	cookie := k.signInOwner(t, "ada@example.com")
	rec := k.sessionCall(http.MethodPost, "/api/roles", cookie, map[string]any{
		"name": "Planner", "permissions": []string{"task:view"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	// Seal the sign-in and the create so the pending count below means only
	// what happens after the refused insert.
	lastEvent(t, k.owner)
	if _, err := k.owner.Exec(context.Background(), `REVOKE INSERT ON audit.pending FROM upmo_app`); err != nil {
		t.Fatal(err)
	}

	rec = k.sessionCall(http.MethodPut, "/api/roles/"+created.ID, cookie, map[string]any{
		"name": "Ghost", "permissions": []string{"task:edit"},
	})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("change = %d %s, want 500", rec.Code, rec.Body.String())
	}
	var name string
	var permissions []string
	if err := k.owner.QueryRow(context.Background(),
		`SELECT name, permissions FROM identity.roles WHERE id = $1`, created.ID).Scan(&name, &permissions); err != nil {
		t.Fatal(err)
	}
	if name != "Planner" || len(permissions) != 1 || permissions[0] != "task:view" {
		t.Fatalf("role = %s %v, the change survived its failed audit insert", name, permissions)
	}
	var pending int
	if err := k.owner.QueryRow(context.Background(), `SELECT count(*) FROM audit.pending`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("%d pending rows survived the refused insert", pending)
	}
}

// TestFailedAuditInsertRollsBackMemberRoleChange proves the same coupling for
// PUT /api/members/{id}/role: the person keeps the old role.
func TestFailedAuditInsertRollsBackMemberRoleChange(t *testing.T) {
	k := newKit(t)
	cookie := k.signInOwner(t, "ada@example.com")
	carol := seedJoinedMember(t, k.owner, "carol@example.com", "Carol")
	memberRole := roleIDOf(t, k.owner, "member")
	lastEvent(t, k.owner)
	if _, err := k.owner.Exec(context.Background(), `REVOKE INSERT ON audit.pending FROM upmo_app`); err != nil {
		t.Fatal(err)
	}

	adminRole := roleIDOf(t, k.owner, "admin")
	rec := k.sessionCall(http.MethodPut, "/api/members/"+carol+"/role", cookie, map[string]string{"role_id": adminRole})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("role change = %d %s, want 500", rec.Code, rec.Body.String())
	}
	var role string
	if err := k.owner.QueryRow(context.Background(),
		`SELECT role_id::text FROM identity.users WHERE id = $1`, carol).Scan(&role); err != nil {
		t.Fatal(err)
	}
	if role != memberRole {
		t.Fatalf("role = %s, the change survived its failed audit insert", role)
	}
	var pending int
	if err := k.owner.QueryRow(context.Background(), `SELECT count(*) FROM audit.pending`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("%d pending rows survived the refused insert", pending)
	}
}

// TestIdempotentWritesRecordNothing: a repeat add, a remove of someone not on
// the team and a remove of a grant that was never saved all answer 204 without
// changing a row, so they record no event of their own and get no unrecorded
// fallback either.
func TestIdempotentWritesRecordNothing(t *testing.T) {
	k := newKit(t)
	cookie := k.signInOwner(t, "ada@example.com")
	k.passkeySessions(t)
	carol := seedJoinedMember(t, k.owner, "carol@example.com", "Carol")

	rec := k.sessionCall(http.MethodPost, "/api/teams", cookie, map[string]string{"name": "Ship"})
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
	}
	var made struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &made); err != nil {
		t.Fatal(err)
	}
	if rec := k.sessionCall(http.MethodPost, "/api/teams/"+made.ID+"/members", cookie, map[string]string{"user_id": carol}); rec.Code != http.StatusNoContent {
		t.Fatalf("add = %d %s", rec.Code, rec.Body.String())
	}
	row, position := lastEvent(t, k.owner)
	if row.Action != audit.TeamMemberAdd {
		t.Fatalf("event = %s, want the real team.member_add", row.Action)
	}

	// Adding the same person again changes nothing.
	if rec := k.sessionCall(http.MethodPost, "/api/teams/"+made.ID+"/members", cookie, map[string]string{"user_id": carol}); rec.Code != http.StatusNoContent {
		t.Fatalf("repeat add = %d %s", rec.Code, rec.Body.String())
	}
	noNewEvent(t, k.owner, position)

	// Removing someone who was never on the team changes nothing.
	stranger := seedJoinedMember(t, k.owner, "omar@example.com", "Omar")
	if rec := k.sessionCall(http.MethodDelete, "/api/teams/"+made.ID+"/members/"+stranger, cookie, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("remove non-member = %d %s", rec.Code, rec.Body.String())
	}
	noNewEvent(t, k.owner, position)

	// Removing a grant that was never saved changes nothing.
	if rec := k.sessionCall(http.MethodDelete, "/api/connector-grants/github", cookie, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("remove missing grant = %d %s", rec.Code, rec.Body.String())
	}
	noNewEvent(t, k.owner, position)
}

// noNewEvent proves nothing was sealed since position: the pending table is
// empty and the chain head has not moved.
func noNewEvent(t *testing.T, owner *pgxpool.Pool, position int64) {
	t.Helper()
	var pending int
	if err := owner.QueryRow(context.Background(), `SELECT count(*) FROM audit.pending`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("%d pending rows after a request that changed nothing", pending)
	}
	var head int64
	if err := owner.QueryRow(context.Background(), `SELECT position FROM audit.head WHERE one`).Scan(&head); err != nil {
		t.Fatal(err)
	}
	if head != position {
		t.Fatalf("head moved to %d, want %d: a request that changed nothing must not seal an event", head, position)
	}
}

func TestWorkspaceUpdateAndSSOSignInAudit(t *testing.T) {
	k := newKit(t)
	ownerCookie := k.signInOwner(t, "ada@example.com")
	ownerSoft, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	k.addPasskey(t, ownerCookie, ownerSoft)
	lastEvent(t, k.owner)

	ctx := context.Background()
	ownerID, _ := lookupPerson(t, k.owner, "ada@example.com")
	ownerUID, err := identity.ParseUserID(ownerID)
	if err != nil {
		t.Fatal(err)
	}
	memberID := seedJoinedMember(t, k.owner, "lee@example.com", "Lee")
	sessions := identity.Sessions(k.app, identity.IdleTimeout, identity.MaxAge)
	memberUID, err := identity.ParseUserID(memberID)
	if err != nil {
		t.Fatal(err)
	}
	memberCookie, _, err := sessions.Start(ctx, memberUID, "passkey", "")
	if err != nil {
		t.Fatal(err)
	}
	memberSoft, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	k.addPasskey(t, memberCookie, memberSoft)
	var memberHandle []byte
	if err := k.owner.QueryRow(ctx, `SELECT webauthn_handle FROM identity.users WHERE id = $1`, memberID).Scan(&memberHandle); err != nil {
		t.Fatal(err)
	}
	lastEvent(t, k.owner)

	// Non-admin PUT /api/workspace is refused (403) and seals denied workspace.update.
	deniedPut := k.sessionCall(http.MethodPut, "/api/workspace", memberCookie, map[string]any{
		"name":                    "Nope",
		"time_zone":               "UTC",
		"locale":                  "en-GB",
		"allow_member_providers":  true,
		"allow_member_routes":     true,
		"allow_member_connectors": true,
		"allow_member_agents":     true,
		"sso_required":            false,
	})
	if deniedPut.Code != http.StatusForbidden {
		t.Fatalf("member PUT /api/workspace = %d %s, want 403", deniedPut.Code, deniedPut.Body.String())
	}
	row, _ := lastEvent(t, k.owner)
	if row.Action != audit.WorkspaceUpdate || row.Outcome != audit.Denied || row.ActorID != memberID {
		t.Fatalf("denied workspace row = %+v", row)
	}

	// Owner PUT /api/workspace updates settings and seals workspace.update in the same tx.
	workspacePayload := map[string]any{
		"name":                    "Secret Corp",
		"logo_url":                "",
		"time_zone":               "America/New_York",
		"locale":                  "en-US",
		"default_route":           "",
		"default_agent_id":        nil,
		"allow_member_providers":  true,
		"allow_member_routes":     true,
		"allow_member_connectors": true,
		"allow_member_agents":     true,
		"sso_required":            true,
	}
	k.passkeySessions(t)
	okPut := k.sessionCall(http.MethodPut, "/api/workspace", ownerCookie, workspacePayload)
	if okPut.Code != http.StatusOK {
		t.Fatalf("owner PUT /api/workspace = %d %s", okPut.Code, okPut.Body.String())
	}
	row, pos := lastEvent(t, k.owner)
	if row.Action != audit.WorkspaceUpdate || row.Outcome != audit.Success || row.TargetType != "workspace" || row.TargetID != "workspace" {
		t.Fatalf("workspace update row = %+v", row)
	}
	// Name value must not leak into sealed audit JSON; field name is recorded.
	freeOf(t, rowDump(t, k.owner, pos), "Secret Corp")
	if row.Changes.Values["time_zone"].To != "America/New_York" || row.Changes.Values["sso_required"].To != true {
		t.Fatalf("workspace changes = %+v", row.Changes)
	}

	// Repeating PUT /api/workspace with identical values seals no new workspace.update event.
	noopPut := k.sessionCall(http.MethodPut, "/api/workspace", ownerCookie, workspacePayload)
	if noopPut.Code != http.StatusOK {
		t.Fatalf("noop PUT /api/workspace = %d %s", noopPut.Code, noopPut.Body.String())
	}
	noNewEvent(t, k.owner, pos)

	// Set passwords for owner and member so we can test SSO sign-in audit events.
	const pw = "correct horse battery"
	if rec := k.sessionCall(http.MethodPut, "/api/password", ownerCookie, map[string]string{"password": pw}); rec.Code != http.StatusNoContent {
		t.Fatalf("owner set password = %d %s", rec.Code, rec.Body.String())
	}
	if rec := k.sessionCall(http.MethodPut, "/api/password", memberCookie, map[string]string{"password": pw}); rec.Code != http.StatusNoContent {
		t.Fatalf("member set password = %d %s", rec.Code, rec.Body.String())
	}
	lastEvent(t, k.owner)

	// Member password sign-in is refused with reason sso_required.
	memberDenied := k.call(http.MethodPost, "/api/sign-in/password", nil, map[string]string{
		"email": "lee@example.com", "password": pw,
	})
	if memberDenied.Code != http.StatusForbidden {
		t.Fatalf("member sso sign-in = %d %s, want 403", memberDenied.Code, memberDenied.Body.String())
	}
	row, _ = lastEvent(t, k.owner)
	if row.Action != audit.AuthSignIn || row.Outcome != audit.Denied || row.Reason != "sso_required" || row.TargetID != memberID {
		t.Fatalf("member sso refusal row = %+v", row)
	}

	// Owner break-glass password sign-in succeeds with reason sso_break_glass.
	ownerOK := k.call(http.MethodPost, "/api/sign-in/password", nil, map[string]string{
		"email": "ada@example.com", "password": pw,
	})
	if ownerOK.Code != http.StatusOK {
		t.Fatalf("owner break-glass = %d %s", ownerOK.Code, ownerOK.Body.String())
	}
	row, _ = lastEvent(t, k.owner)
	if row.Action != audit.AuthSignIn || row.Outcome != audit.Success || row.Reason != "sso_break_glass" {
		t.Fatalf("owner break-glass row = %+v", row)
	}

	// Clear throttle bucket between sign-in paths because ThrottleBurst is 10 per IP.
	if _, err := k.owner.Exec(ctx, `DELETE FROM identity.throttle`); err != nil {
		t.Fatal(err)
	}

	// Member passkey sign-in is refused with reason sso_required.
	opt := k.call(http.MethodPost, "/api/passkeys/sign-in/options", nil, map[string]string{})
	if opt.Code != http.StatusOK {
		t.Fatalf("member passkey options = %d %s", opt.Code, opt.Body.String())
	}
	memberAssertion, err := memberSoft.Login("localhost", "http://localhost:5173", challengeOf(t, opt.Body.Bytes()), memberHandle)
	if err != nil {
		t.Fatal(err)
	}
	memberPasskeyRes := k.call(http.MethodPost, "/api/passkeys/sign-in/finish",
		map[string]string{httpapi.ChallengeCookie: cookieValue(t, opt, httpapi.ChallengeCookie)},
		json.RawMessage(memberAssertion))
	if memberPasskeyRes.Code != http.StatusForbidden {
		t.Fatalf("member passkey sso sign-in = %d %s, want 403", memberPasskeyRes.Code, memberPasskeyRes.Body.String())
	}
	row, _ = lastEvent(t, k.owner)
	if row.Action != audit.AuthSignIn || row.Outcome != audit.Denied || row.Reason != "sso_required" || row.TargetID != memberID {
		t.Fatalf("member passkey sso refusal row = %+v", row)
	}

	// Owner break-glass passkey sign-in succeeds with reason sso_break_glass.
	ownerOpt := k.call(http.MethodPost, "/api/passkeys/sign-in/options", nil, map[string]string{})
	if ownerOpt.Code != http.StatusOK {
		t.Fatalf("owner passkey options = %d %s", ownerOpt.Code, ownerOpt.Body.String())
	}
	ownerAssertion, err := ownerSoft.Login("localhost", "http://localhost:5173", challengeOf(t, ownerOpt.Body.Bytes()), adaHandle(t, k.owner))
	if err != nil {
		t.Fatal(err)
	}
	ownerPasskeyRes := k.call(http.MethodPost, "/api/passkeys/sign-in/finish",
		map[string]string{httpapi.ChallengeCookie: cookieValue(t, ownerOpt, httpapi.ChallengeCookie)},
		json.RawMessage(ownerAssertion))
	if ownerPasskeyRes.Code != http.StatusNoContent {
		t.Fatalf("owner break-glass passkey = %d %s", ownerPasskeyRes.Code, ownerPasskeyRes.Body.String())
	}
	row, _ = lastEvent(t, k.owner)
	if row.Action != audit.AuthSignIn || row.Outcome != audit.Success || row.Reason != "sso_break_glass" {
		t.Fatalf("owner break-glass passkey row = %+v", row)
	}
	if method := row.Changes.Values["method"].To; method != "passkey" {
		t.Fatalf("owner break-glass passkey method = %v, want passkey", method)
	}

	if _, err := k.owner.Exec(ctx, `DELETE FROM identity.throttle`); err != nil {
		t.Fatal(err)
	}

	// Member non-SSO link sign-in is refused with reason sso_required.
	memberTok, err := k.passkeys.IssueLink(ctx, memberUID, "recover")
	if err != nil {
		t.Fatal(err)
	}
	linkOpt := k.call(http.MethodPost, "/api/passkeys/link/options", nil, map[string]string{"token": memberTok})
	if linkOpt.Code != http.StatusOK {
		t.Fatalf("member link options = %d %s", linkOpt.Code, linkOpt.Body.String())
	}
	memberLinkReg, err := memberSoft.Register("localhost", "http://localhost:5173", challengeOf(t, linkOpt.Body.Bytes()), userHandleOf(t, linkOpt.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	memberLinkRes := k.call(http.MethodPost, "/api/passkeys/link/finish",
		map[string]string{httpapi.ChallengeCookie: cookieValue(t, linkOpt, httpapi.ChallengeCookie)},
		map[string]any{"token": memberTok, "response": json.RawMessage(memberLinkReg)})
	if memberLinkRes.Code != http.StatusForbidden {
		t.Fatalf("member link sso sign-in = %d %s, want 403", memberLinkRes.Code, memberLinkRes.Body.String())
	}
	row, _ = lastEvent(t, k.owner)
	if row.Action != audit.AuthSignIn || row.Outcome != audit.Denied || row.Reason != "sso_required" || row.TargetID != memberID {
		t.Fatalf("member link sso refusal row = %+v", row)
	}

	// Owner break-glass link sign-in succeeds with reason sso_break_glass.
	ownerTok, err := k.passkeys.IssueLink(ctx, ownerUID, "recover")
	if err != nil {
		t.Fatal(err)
	}
	ownerLinkSoft, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	if c := k.finishLink(t, ownerLinkSoft, ownerTok); c == "" {
		t.Fatal("owner break-glass link returned empty session")
	}
	row, _ = lastEvent(t, k.owner)
	if row.Action != audit.AuthSignIn || row.Outcome != audit.Success || row.Reason != "sso_break_glass" {
		t.Fatalf("owner break-glass link row = %+v", row)
	}
	if method := row.Changes.Values["method"].To; method != "link" {
		t.Fatalf("owner break-glass link method = %v, want link", method)
	}
}
