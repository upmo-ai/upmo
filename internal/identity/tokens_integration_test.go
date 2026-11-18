//go:build integration

package identity_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/identity"
	httpapi "github.com/upmo-ai/upmo/internal/identity/adapters/http"
	"github.com/upmo-ai/upmo/internal/identity/adapters/webauthn"
	"github.com/upmo-ai/upmo/internal/testutil/sealer"
)

func TestPersonalTokens(t *testing.T) {
	ownerPool, pool, ctx := upBoth(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions := identity.Sessions(pool, time.Hour, 24*time.Hour)
	passkeys, err := identity.Passkeys(pool, sealer.NewEmail(t), sessions, "http://localhost:5173", "localhost", log)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	identity.MountAPITokens(mux, sessions, identity.APITokens(pool, log), log, nil, pool, nil)
	httpapi.Passkeys{Pass: passkeys}.Mount(mux)

	token, err := passkeys.Bootstrap(ctx, "ada@example.com", "Ada")
	if err != nil {
		t.Fatal(err)
	}
	soft, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	cookie := finishLink(t, mux, soft, token)

	denied := call(mux, http.MethodPost, "/api/tokens", cookie, []byte(`{"name":"ci","scope":["task:delete"],"days":30}`))
	if denied.Code != http.StatusBadRequest || !strings.Contains(denied.Body.String(), "a token cannot hold that permission") {
		t.Fatalf("delete scope = %d %s", denied.Code, denied.Body.String())
	}
	created := call(mux, http.MethodPost, "/api/tokens", cookie, []byte(`{"name":"ci","scope":["task:view","task:edit"],"days":30}`))
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), "upmo_pat_") {
		t.Fatalf("create = %d %s", created.Code, created.Body.String())
	}
	var body struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE identity.sessions SET signed_in_at = now() - interval '11 minutes'`); err != nil {
		t.Fatal(err)
	}
	stale := call(mux, http.MethodPost, "/api/tokens", cookie, []byte(`{"name":"later","scope":["task:view"],"days":7}`))
	if stale.Code != http.StatusForbidden || !strings.Contains(stale.Body.String(), "sign in again") {
		t.Fatalf("stale = %d %s", stale.Code, stale.Body.String())
	}
	if _, err := pool.Exec(ctx, `UPDATE identity.sessions SET signed_in_at = now()`); err != nil {
		t.Fatal(err)
	}
	listed := call(mux, http.MethodGet, "/api/tokens", cookie, nil)
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), body.Secret) || !strings.Contains(listed.Body.String(), `"task:view"`) {
		t.Fatalf("list = %d %s", listed.Code, listed.Body.String())
	}
	with := bearer(mux, http.MethodGet, "/api/session", body.Secret)
	if with.Code != http.StatusOK || !strings.Contains(with.Body.String(), `"principal":"token"`) || !strings.Contains(with.Body.String(), "task:edit") {
		t.Fatalf("bearer session = %d %s", with.Code, with.Body.String())
	}
	var used time.Time
	if err := pool.QueryRow(ctx, `SELECT last_used_at FROM identity.api_tokens WHERE id = $1`, body.ID).Scan(&used); err != nil {
		t.Fatal(err)
	}
	held := bearer(mux, http.MethodGet, "/api/session", body.Secret)
	if held.Code != http.StatusOK {
		t.Fatalf("second use = %d %s", held.Code, held.Body.String())
	}
	var usedAgain time.Time
	if err := pool.QueryRow(ctx, `SELECT last_used_at FROM identity.api_tokens WHERE id = $1`, body.ID).Scan(&usedAgain); err != nil {
		t.Fatal(err)
	}
	if !usedAgain.Equal(used) {
		t.Fatalf("last use moved from %s to %s", used, usedAgain)
	}
	var owner string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM identity.roles WHERE key = 'owner'`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if _, err := ownerPool.Exec(ctx, `INSERT INTO identity.users (email, name, role_id, joined_at) VALUES ('bea@example.com', 'Bea', $1, now())`, owner); err != nil {
		t.Fatal(err)
	}
	var narrow string
	if err := ownerPool.QueryRow(ctx, `INSERT INTO identity.roles (name, permissions) VALUES ('Narrow', ARRAY['task:view']) RETURNING id::text`).Scan(&narrow); err != nil {
		t.Fatal(err)
	}
	if _, err := ownerPool.Exec(ctx, `UPDATE identity.users SET role_id = $1 WHERE email_hash = ANY($2)`, narrow, sealer.NewEmail(t).IndexHashes("ada@example.com")); err != nil {
		t.Fatal(err)
	}
	narrowed := bearer(mux, http.MethodGet, "/api/session", body.Secret)
	if narrowed.Code != http.StatusOK || strings.Contains(narrowed.Body.String(), "task:edit") || !strings.Contains(narrowed.Body.String(), "task:view") {
		t.Fatalf("narrowed = %d %s", narrowed.Code, narrowed.Body.String())
	}
	if _, err := ownerPool.Exec(ctx, `UPDATE identity.users SET role_id = $1 WHERE email_hash = ANY($2)`, owner, sealer.NewEmail(t).IndexHashes("ada@example.com")); err != nil {
		t.Fatal(err)
	}
	manage := bearer(mux, http.MethodDelete, "/api/session", body.Secret)
	if manage.Code != http.StatusForbidden || !strings.Contains(manage.Body.String(), "needs a signed-in person") {
		t.Fatalf("bearer sign-out = %d %s", manage.Code, manage.Body.String())
	}
	when := time.Now().UTC().AddDate(0, 0, 40).Format("2006-01-02")
	second := call(mux, http.MethodPost, "/api/tokens", cookie, []byte(`{"name":"nightly","scope":["task:view"],"expires":"`+when+`"}`))
	if second.Code != http.StatusCreated {
		t.Fatalf("second = %d %s", second.Code, second.Body.String())
	}
	var other struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &other); err != nil {
		t.Fatal(err)
	}
	if _, err := ownerPool.Exec(ctx, `UPDATE identity.api_tokens SET created_at = now() - interval '2 minutes', expires_at = now() - interval '1 minute' WHERE id = $1`, other.ID); err != nil {
		t.Fatal(err)
	}
	expired := bearer(mux, http.MethodGet, "/api/session", other.Secret)
	if expired.Code != http.StatusUnauthorized || !strings.Contains(expired.Body.String(), "invalid token") {
		t.Fatalf("expired = %d %s", expired.Code, expired.Body.String())
	}
	revoked := call(mux, http.MethodDelete, "/api/tokens/"+body.ID, cookie, nil)
	if revoked.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d %s", revoked.Code, revoked.Body.String())
	}
	after := bearer(mux, http.MethodGet, "/api/session", body.Secret)
	if after.Code != http.StatusUnauthorized || !strings.Contains(after.Body.String(), "invalid token") {
		t.Fatalf("revoked = %d %s", after.Code, after.Body.String())
	}
	again := bearer(mux, http.MethodGet, "/api/session", "upmo_pat_"+strings.Repeat("A", 43))
	if again.Code != http.StatusUnauthorized || !strings.Contains(again.Body.String(), "invalid token") {
		t.Fatalf("revoked bearer = %d %s", again.Code, again.Body.String())
	}
	service := bearer(mux, http.MethodGet, "/api/session", "upmo_s2s_v1.gateway.1.aaaa.bbbb")
	if service.Code != http.StatusUnauthorized || !strings.Contains(service.Body.String(), "invalid token") {
		t.Fatalf("service bearer = %d %s", service.Code, service.Body.String())
	}
	jwt := bearer(mux, http.MethodGet, "/api/session", "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiJ1In0.sig")
	if jwt.Code != http.StatusUnauthorized || !strings.Contains(jwt.Body.String(), "invalid token") {
		t.Fatalf("run jwt = %d %s", jwt.Code, jwt.Body.String())
	}
	kept := call(mux, http.MethodPost, "/api/tokens", cookie, []byte(`{"name":"kept","scope":["task:view"],"days":7}`))
	if kept.Code != http.StatusCreated {
		t.Fatalf("kept = %d %s", kept.Code, kept.Body.String())
	}
	var live struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(kept.Body.Bytes(), &live); err != nil {
		t.Fatal(err)
	}
	if _, err := ownerPool.Exec(ctx, `UPDATE identity.users SET disabled_at = now() WHERE email_hash = ANY($1)`, sealer.NewEmail(t).IndexHashes("ada@example.com")); err != nil {
		t.Fatal(err)
	}
	disabled := bearer(mux, http.MethodGet, "/api/session", live.Secret)
	if disabled.Code != http.StatusUnauthorized || !strings.Contains(disabled.Body.String(), "invalid token") {
		t.Fatalf("disabled = %d %s", disabled.Code, disabled.Body.String())
	}
}

func bearer(mux http.Handler, method, path, secret string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, http.NoBody)
	req.RemoteAddr = "198.51.100.9:1000"
	req.Header.Set("Authorization", "Bearer "+secret)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}
