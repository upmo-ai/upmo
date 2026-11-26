//go:build integration

package provision_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/agents"
	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/brainops"
	"github.com/upmo-ai/upmo/internal/platform/httpserver"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/provision"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
	"github.com/upmo-ai/upmo/internal/testutil/workerkeys"
)

type testEnv struct {
	owner     *pgxpool.Pool
	app       *pgxpool.Pool
	handler   http.Handler
	keyring   gateway.Keyring
	keys      []provision.Key
	identity  identity.ProvisionStore
	log       *slog.Logger
	rawKey1   string
	rawKey2   string
	masterKey string
	now       time.Time
}

func newTestEnv(t *testing.T, withKeys bool) *testEnv {
	t.Helper()
	owner := pgtest.New(t)
	ctx := context.Background()
	for _, s := range []struct {
		name string
	}{
		{"audit"}, {"identity"}, {"gateway"}, {"agents"},
	} {
		switch s.name {
		case "audit":
			if err := audit.Up(ctx, owner); err != nil {
				t.Fatal(err)
			}
		case "identity":
			if err := migrate.Up(ctx, owner, identity.Schema, identity.Migrations()); err != nil {
				t.Fatal(err)
			}
		case "gateway":
			if err := migrate.Up(ctx, owner, gateway.Schema, gateway.Migrations()); err != nil {
				t.Fatal(err)
			}
		case "agents":
			if err := migrate.Up(ctx, owner, agents.Schema, agents.Migrations()); err != nil {
				t.Fatal(err)
			}
		}
	}
	appPool := pgtest.App(t, owner)

	masterBytes := bytes.Repeat([]byte{'m'}, 32)
	masterKeyB64 := base64.StdEncoding.EncodeToString(masterBytes)
	t.Setenv("UPMO_MASTER_KEY", masterKeyB64)
	t.Setenv("UPMO_MASTER_KEY_ID", "k1")
	keyring, err := gateway.KeyringFromEnv(func(k string) (string, bool) {
		switch k {
		case gateway.EnvMasterKey:
			return masterKeyB64, true
		case gateway.EnvMasterKeyID:
			return "k1", true
		default:
			return "", false
		}
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	k1 := svcsig.KeyPrefix + base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{'1'}, 32))
	k2 := svcsig.KeyPrefix + base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{'2'}, 32))
	var keys []provision.Key
	if withKeys {
		keys, err = provision.ParseKeys([]string{k1, k2})
		if err != nil {
			t.Fatal(err)
		}
	}

	now := time.Date(2026, 11, 16, 14, 0, 0, 0, time.UTC)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	env := &testEnv{
		owner:     owner,
		app:       appPool,
		keyring:   keyring,
		keys:      keys,
		identity:  identity.Provision(owner, brainops.NewEmailSealer(keyring), "http://localhost:5173"),
		log:       log,
		rawKey1:   k1,
		rawKey2:   k2,
		masterKey: masterKeyB64,
		now:       now,
	}
	env.identity.Now = func() time.Time { return env.now }

	mux := http.NewServeMux()
	sessions := identity.Sessions(appPool, identity.IdleTimeout, identity.MaxAge)
	passkeys, err := identity.Passkeys(appPool, brainops.NewEmailSealer(keyring), sessions, "http://localhost:5173", "localhost", log)
	if err != nil {
		t.Fatal(err)
	}
	identity.MountWorkspace(mux, identity.Workspace(appPool), passkeys)
	provision.Handler{
		Owner:      owner,
		Identity:   env.identity,
		MasterKeys: keyring,
		Keys:       keys,
		Log:        log,
		Now:        func() time.Time { return env.now },
	}.Mount(mux)

	env.handler = audit.Middleware(audit.Routes(), mux, appPool, "brain", nil, log)(httpserver.Protect(mux))
	return env
}

// sign returns a v2 provision bearer for method, target (path plus raw query
// string) and body at at.
func (e *testEnv) sign(t *testing.T, rawKey, method, target string, body []byte, at time.Time) string {
	t.Helper()
	master, err := svcsig.ParseKey(rawKey)
	if err != nil {
		t.Fatal(err)
	}
	derived, err := svcsig.Derive(master, svcsig.AudienceProvision)
	if err != nil {
		t.Fatal(err)
	}
	path, rawQuery, _ := strings.Cut(target, "?")
	tok, err := svcsig.SignRequest(derived, svcsig.AudienceProvision, method, path, rawQuery, body, at)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// do sends body to target with bearer ("" for none) through the mounted
// routes.
func (e *testEnv) do(t *testing.T, method, target, bearer string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func (e *testEnv) signedDo(t *testing.T, rawKey, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		switch v := body.(type) {
		case []byte:
			buf.Write(v)
		case string:
			buf.WriteString(v)
		default:
			if err := json.NewEncoder(&buf).Encode(v); err != nil {
				t.Fatal(err)
			}
		}
	}
	bearer := ""
	if rawKey != "" {
		bearer = e.sign(t, rawKey, method, path, buf.Bytes(), e.now)
	}
	return e.do(t, method, path, bearer, buf.Bytes())
}

func TestProvisionDisabledReturns404Integration(t *testing.T) {
	env := newTestEnv(t, false)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/provision/members"},
		{http.MethodPut, "/api/v1/provision/quotas"},
		{http.MethodGet, "/api/v1/provision/status"},
		{http.MethodGet, "/api/v1/provision/usage"},
	} {
		rec := env.signedDo(t, env.rawKey1, tc.method, tc.path, `{}`)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s = %d, want 404", tc.method, tc.path, rec.Code)
		}
	}
}

func TestProvisionMembersTeamsRevocationAndAudit(t *testing.T) {
	env := newTestEnv(t, true)
	ctx := context.Background()

	// Unauthenticated request returns 401 and seals Denied event.
	unauth := env.signedDo(t, "", http.MethodPost, "/api/v1/provision/teams", map[string]any{
		"external_id": "team-eng", "name": "Engineering",
	})
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("unauth = %d, want 401", unauth.Code)
	}

	// HTTP-level stale/expired bearer with a configured key returns 401 and seals Denied event.
	staleBody := []byte(`{"external_id":"team-stale","name":"Stale"}`)
	staleTok := env.sign(t, env.rawKey1, http.MethodPost, "/api/v1/provision/teams", staleBody, env.now.Add(-2*time.Minute))
	staleReq := httptest.NewRequest(http.MethodPost, "/api/v1/provision/teams", bytes.NewReader(staleBody))
	staleReq.Header.Set("Content-Type", "application/json")
	staleReq.Header.Set("Authorization", "Bearer "+staleTok)
	staleRec := httptest.NewRecorder()
	env.handler.ServeHTTP(staleRec, staleReq)
	if staleRec.Code != http.StatusUnauthorized {
		t.Fatalf("stale bearer = %d, want 401", staleRec.Code)
	}

	// Create teams.
	rec := env.signedDo(t, env.rawKey1, http.MethodPost, "/api/v1/provision/teams", map[string]any{
		"external_id": "team-eng", "name": "Engineering",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create team = %d %s", rec.Code, rec.Body.String())
	}
	rec = env.signedDo(t, env.rawKey2, http.MethodPost, "/api/v1/provision/teams", map[string]any{
		"external_id": "team-ops", "name": "Operations",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create team 2 (rotated key) = %d %s", rec.Code, rec.Body.String())
	}
	// Duplicate external_id -> 409.
	if dup := env.signedDo(t, env.rawKey1, http.MethodPost, "/api/v1/provision/teams", map[string]any{
		"external_id": "team-eng", "name": "Other",
	}); dup.Code != http.StatusConflict {
		t.Fatalf("duplicate team external_id = %d, want 409", dup.Code)
	}

	// Create owner and operator (mapped to member).
	ownerRec := env.signedDo(t, env.rawKey1, http.MethodPost, "/api/v1/provision/members", map[string]any{
		"external_id": "idp-owner",
		"email":       "ada@example.com",
		"name":        "Ada",
		"role":        "Owner",
		"teams":       []string{"team-eng"},
	})
	if ownerRec.Code != http.StatusCreated {
		t.Fatalf("create owner = %d %s", ownerRec.Code, ownerRec.Body.String())
	}

	opRec := env.signedDo(t, env.rawKey2, http.MethodPost, "/api/v1/provision/members", map[string]any{
		"external_id": "idp-op",
		"email":       "bob@example.com",
		"name":        "Bob",
		"role":        "Operator",
		"teams":       []string{"team-eng", "team-ops"},
	})
	if opRec.Code != http.StatusCreated {
		t.Fatalf("create operator = %d %s", opRec.Code, opRec.Body.String())
	}
	var opResp identity.ProvisionMember
	if err := json.Unmarshal(opRec.Body.Bytes(), &opResp); err != nil {
		t.Fatal(err)
	}
	if opResp.Role != "member" || !opResp.Active || len(opResp.Teams) != 2 {
		t.Fatalf("operator response = %+v", opResp)
	}

	// Duplicate member external_id or email -> 409; invalid role -> 422.
	if dup := env.signedDo(t, env.rawKey1, http.MethodPost, "/api/v1/provision/members", map[string]any{
		"external_id": "idp-op", "email": "other@example.com", "name": "X", "role": "member",
	}); dup.Code != http.StatusConflict {
		t.Fatalf("duplicate member ext = %d, want 409", dup.Code)
	}
	if dupEmail := env.signedDo(t, env.rawKey1, http.MethodPost, "/api/v1/provision/members", map[string]any{
		"external_id": "idp-new", "email": "BOB@example.com", "name": "X", "role": "member",
	}); dupEmail.Code != http.StatusConflict {
		t.Fatalf("duplicate email = %d, want 409", dupEmail.Code)
	}
	if badRole := env.signedDo(t, env.rawKey1, http.MethodPost, "/api/v1/provision/members", map[string]any{
		"external_id": "idp-bad", "email": "bad@example.com", "name": "X", "role": "superuser",
	}); badRole.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad role = %d, want 422", badRole.Code)
	}

	// Update team name and member list by external_id.
	updTeam := env.signedDo(t, env.rawKey1, http.MethodPut, "/api/v1/provision/teams/team-ops", map[string]any{
		"name":    "Ops Core",
		"members": []string{"idp-owner", "idp-op"},
	})
	if updTeam.Code != http.StatusOK {
		t.Fatalf("update team = %d %s", updTeam.Code, updTeam.Body.String())
	}

	// Give idp-op an active session and an API token; verify disabling via PUT active:false revokes both immediately!
	opUID, err := identity.ParseUserID(opResp.ID)
	if err != nil {
		t.Fatal(err)
	}
	sessions := identity.Sessions(env.app, identity.IdleTimeout, identity.MaxAge)
	sessCookie, _, err := sessions.Start(ctx, opUID, "sso", "agent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.owner.Exec(ctx, `
		INSERT INTO identity.api_tokens (user_id, name, token_hash, scope, created_at, expires_at)
		VALUES ($1, 'ci', $2, ARRAY['task:view'], now(), now() + interval '1 day')`,
		opResp.ID, []byte("token-hash-op-1")); err != nil {
		t.Fatal(err)
	}

	disable := false
	updMember := env.signedDo(t, env.rawKey1, http.MethodPut, "/api/v1/provision/members/idp-op", map[string]any{
		"email":  "bob@example.com",
		"name":   "Bob",
		"role":   "viewer",
		"active": &disable,
	})
	if updMember.Code != http.StatusOK {
		t.Fatalf("disable member = %d %s", updMember.Code, updMember.Body.String())
	}
	if _, err := sessions.Lookup(ctx, sessCookie); err == nil {
		t.Fatal("disabled member session still valid")
	}
	var tokenCount int
	if err := env.owner.QueryRow(ctx, `SELECT count(*) FROM identity.api_tokens WHERE user_id = $1`, opResp.ID).Scan(&tokenCount); err != nil {
		t.Fatal(err)
	}
	if tokenCount != 0 {
		t.Fatalf("api_tokens remaining after disable = %d, want 0", tokenCount)
	}

	// Re-enable, issue new session & token, then DELETE /api/v1/provision/members/{external_id}.
	enable := true
	if re := env.signedDo(t, env.rawKey1, http.MethodPut, "/api/v1/provision/members/idp-op", map[string]any{
		"email": "bob@example.com", "name": "Bob", "role": "viewer", "active": &enable,
	}); re.Code != http.StatusOK {
		t.Fatalf("re-enable member = %d %s", re.Code, re.Body.String())
	}
	sessCookie2, _, err := sessions.Start(ctx, opUID, "sso", "agent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.owner.Exec(ctx, `
		INSERT INTO identity.api_tokens (user_id, name, token_hash, scope, created_at, expires_at)
		VALUES ($1, 'ci2', $2, ARRAY['task:view'], now(), now() + interval '1 day')`,
		opResp.ID, []byte("token-hash-op-2")); err != nil {
		t.Fatal(err)
	}
	if del := env.signedDo(t, env.rawKey1, http.MethodDelete, "/api/v1/provision/members/idp-op", nil); del.Code != http.StatusNoContent {
		t.Fatalf("delete member = %d %s", del.Code, del.Body.String())
	}
	if _, err := sessions.Lookup(ctx, sessCookie2); err == nil {
		t.Fatal("deleted member session still valid")
	}
	if err := env.owner.QueryRow(ctx, `SELECT count(*) FROM identity.api_tokens WHERE user_id = $1`, opResp.ID).Scan(&tokenCount); err != nil {
		t.Fatal(err)
	}
	if tokenCount != 0 {
		t.Fatalf("api_tokens remaining after delete = %d, want 0", tokenCount)
	}

	// Cannot delete the last joined owner -> 409.
	if delOwner := env.signedDo(t, env.rawKey1, http.MethodDelete, "/api/v1/provision/members/idp-owner", nil); delOwner.Code != http.StatusConflict {
		t.Fatalf("delete last owner = %d, want 409", delOwner.Code)
	}

	// Delete team.
	if delTeam := env.signedDo(t, env.rawKey1, http.MethodDelete, "/api/v1/provision/teams/team-ops", nil); delTeam.Code != http.StatusNoContent {
		t.Fatalf("delete team = %d %s", delTeam.Code, delTeam.Body.String())
	}
	if missing := env.signedDo(t, env.rawKey1, http.MethodDelete, "/api/v1/provision/teams/team-ops", nil); missing.Code != http.StatusNotFound {
		t.Fatalf("delete missing team = %d, want 404", missing.Code)
	}

	// Seal all pending audit events and verify hash chain + actor fields.
	sealed, err := audit.SealOnce(ctx, env.owner, io.Discard)
	if err != nil {
		t.Fatalf("audit.SealOnce: %v", err)
	}
	res, err := audit.Verify(ctx, env.owner, 0, 0)
	if err != nil || !res.OK || sealed == 0 {
		t.Fatalf("audit.Verify = %+v, sealed=%d, err=%v", res, sealed, err)
	}
	var sysServiceCount int
	if err := env.owner.QueryRow(ctx, `
		SELECT count(*) FROM audit.events
		WHERE action LIKE 'provision.%' AND outcome = 'success'
		  AND actor_type = 'system' AND actor_id IS NULL AND credential_type = 'service'`).Scan(&sysServiceCount); err != nil {
		t.Fatal(err)
	}
	if sysServiceCount < 7 {
		t.Fatalf("provision audit events = %d, want >= 7", sysServiceCount)
	}
	// The stale bearer verified under key 1, so its refusal names that key.
	var deniedMissing, deniedStale int
	if err := env.owner.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE outcome = 'denied' AND reason = 'missing_bearer'),
			count(*) FILTER (WHERE outcome = 'denied' AND reason = 'stale' AND target_id = $1)
		FROM audit.events
		WHERE action = 'provision.team_create'`, env.keys[0].ID).Scan(&deniedMissing, &deniedStale); err != nil {
		t.Fatal(err)
	}
	if deniedMissing != 1 || deniedStale != 1 {
		t.Fatalf("denied audit events = (missing=%d, stale=%d), want (1, 1)", deniedMissing, deniedStale)
	}
}

func TestProvisionSignInLinksEndpoint(t *testing.T) {
	env := newTestEnv(t, true)
	// Seed an active owner first (install keeps at least one owner), plus enabled and disabled members.
	if rec := env.signedDo(t, env.rawKey1, http.MethodPost, "/api/v1/provision/members", map[string]any{
		"external_id": "sso-owner", "email": "owner@example.com", "name": "Owner", "role": "owner",
	}); rec.Code != http.StatusCreated {
		t.Fatalf("create owner = %d %s", rec.Code, rec.Body.String())
	}
	if rec := env.signedDo(t, env.rawKey1, http.MethodPost, "/api/v1/provision/members", map[string]any{
		"external_id": "sso-1", "email": "sso1@example.com", "name": "SSO One", "role": "member",
	}); rec.Code != http.StatusCreated {
		t.Fatalf("create member = %d %s", rec.Code, rec.Body.String())
	}
	disabled := false
	if rec := env.signedDo(t, env.rawKey1, http.MethodPost, "/api/v1/provision/members", map[string]any{
		"external_id": "sso-disabled", "email": "disabled@example.com", "name": "Off", "role": "member", "active": &disabled,
	}); rec.Code != http.StatusCreated {
		t.Fatalf("create disabled = %d %s", rec.Code, rec.Body.String())
	}

	for _, purpose := range []string{"sso", "sso_stepup"} {
		rec := env.signedDo(t, env.rawKey1, http.MethodPost, "/api/v1/provision/sign-in-links", map[string]any{
			"external_id": "sso-1", "purpose": purpose,
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("issue %s link = %d %s", purpose, rec.Code, rec.Body.String())
		}
		var link identity.ProvisionLink
		if err := json.Unmarshal(rec.Body.Bytes(), &link); err != nil {
			t.Fatal(err)
		}
		if link.Token == "" || link.Purpose != purpose || !strings.HasPrefix(link.URL, "http://localhost:5173/login/link#") {
			t.Fatalf("unexpected link = %+v", link)
		}
	}

	// Reject non-SSO purposes with 422.
	for _, bad := range []string{"invite", "recover", "bootstrap", "step_up", ""} {
		rec := env.signedDo(t, env.rawKey1, http.MethodPost, "/api/v1/provision/sign-in-links", map[string]any{
			"external_id": "sso-1", "purpose": bad,
		})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("purpose %q = %d, want 422", bad, rec.Code)
		}
	}
	// Missing member -> 404; disabled member -> 422.
	if rec := env.signedDo(t, env.rawKey1, http.MethodPost, "/api/v1/provision/sign-in-links", map[string]any{
		"external_id": "missing", "purpose": "sso",
	}); rec.Code != http.StatusNotFound {
		t.Fatalf("missing user link = %d, want 404", rec.Code)
	}
	if rec := env.signedDo(t, env.rawKey1, http.MethodPost, "/api/v1/provision/sign-in-links", map[string]any{
		"external_id": "sso-disabled", "purpose": "sso",
	}); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("disabled user link = %d, want 422", rec.Code)
	}
}

func TestProvisionProvidersQuotasAndBypassResistance(t *testing.T) {
	env := newTestEnv(t, true)
	ctx := context.Background()

	// 1. Upsert house provider account; never echo secret.
	secret := "sk-house-super-secret-value"
	provRec := env.signedDo(t, env.rawKey1, http.MethodPut, "/api/v1/provision/providers", map[string]any{
		"providers": []map[string]any{
			{
				"name":      "Managed OpenAI",
				"provider":  "OpenAI",
				"adapter":   "openai_compat",
				"plan_kind": "metered",
				"base_url":  "https://api.openai.com/v1",
				"api_key":   secret,
				"models":    []string{"gpt-4o", "gpt-4o-mini"},
			},
		},
	})
	if provRec.Code != http.StatusOK {
		t.Fatalf("upsert providers = %d %s", provRec.Code, provRec.Body.String())
	}
	if strings.Contains(provRec.Body.String(), secret) || strings.Contains(provRec.Body.String(), "credential_ref") {
		t.Fatalf("provider response leaked secret or ref: %s", provRec.Body.String())
	}

	// Idempotent update without re-sending api_key preserves key and updates models.
	provRec2 := env.signedDo(t, env.rawKey1, http.MethodPut, "/api/v1/provision/providers", map[string]any{
		"providers": []map[string]any{
			{
				"name":      "Managed OpenAI",
				"provider":  "OpenAI",
				"adapter":   "openai_compat",
				"plan_kind": "metered",
				"base_url":  "https://api.openai.com/v1",
				"models":    []string{"gpt-4o"},
			},
		},
	})
	if provRec2.Code != http.StatusOK {
		t.Fatalf("update house provider without key = %d %s", provRec2.Code, provRec2.Body.String())
	}
	var house bool
	var vis string
	if err := env.owner.QueryRow(ctx, `SELECT house, visibility FROM gateway.provider_accounts WHERE name = 'Managed OpenAI'`).Scan(&house, &vis); err != nil {
		t.Fatal(err)
	}
	if !house || vis != "everyone" {
		t.Fatalf("house=%v visibility=%q, want true everyone", house, vis)
	}
	var (
		credRef string
		sealed  gateway.Sealed
	)
	if err := env.owner.QueryRow(ctx, `
		SELECT a.credential_ref, c.ciphertext, c.nonce, c.key_id, c.wrapped_key, c.wrap_nonce
		FROM gateway.provider_accounts AS a
		JOIN gateway.credentials AS c ON c.ref = a.credential_ref
		WHERE a.name = 'Managed OpenAI'`).Scan(&credRef, &sealed.Ciphertext, &sealed.Nonce, &sealed.KeyID, &sealed.WrappedKey, &sealed.WrapNonce); err != nil {
		t.Fatal(err)
	}
	if len(sealed.Ciphertext) == 0 || bytes.Contains(sealed.Ciphertext, []byte(secret)) || len(sealed.Nonce) != 12 || sealed.KeyID != "k1" || len(sealed.WrapNonce) != 12 {
		t.Fatalf("unexpected sealed credential: len=%d nonce=%d key_id=%q wrap nonce=%d", len(sealed.Ciphertext), len(sealed.Nonce), sealed.KeyID, len(sealed.WrapNonce))
	}
	// Provision seals under a data key the master wraps, never under the master.
	if _, err := env.keyring.Open(credRef, sealed); err == nil {
		t.Fatal("the master opened the payload")
	}
	openedSecret, err := env.keyring.OpenCredential(credRef, sealed)
	if err != nil || openedSecret != secret {
		t.Fatalf("keyring.OpenCredential = (%q, %v), want %q", openedSecret, err, secret)
	}

	// 2. Quotas: negative -> 422; valid -> 200; settings:manage PUT /api/workspace cannot change/raise it!
	if badQuota := env.signedDo(t, env.rawKey1, http.MethodPut, "/api/v1/provision/quotas", map[string]any{
		"spend_ceiling_micro_usd": -1,
	}); badQuota.Code != http.StatusUnprocessableEntity {
		t.Fatalf("negative quota = %d, want 422", badQuota.Code)
	}
	qRec := env.signedDo(t, env.rawKey1, http.MethodPut, "/api/v1/provision/quotas", map[string]any{
		"spend_ceiling_micro_usd": 42000000,
	})
	if qRec.Code != http.StatusOK {
		t.Fatalf("set quota = %d %s", qRec.Code, qRec.Body.String())
	}

	// Create an owner and test PUT /api/workspace with settings:manage.
	mRec := env.signedDo(t, env.rawKey1, http.MethodPost, "/api/v1/provision/members", map[string]any{
		"external_id": "owner-ws", "email": "ws@example.com", "name": "Owner", "role": "owner",
	})
	if mRec.Code != http.StatusCreated {
		t.Fatal(mRec.Body.String())
	}
	var ownerMember identity.ProvisionMember
	if err := json.Unmarshal(mRec.Body.Bytes(), &ownerMember); err != nil {
		t.Fatal(err)
	}
	ownerUID, _ := identity.ParseUserID(ownerMember.ID)
	sessCookie, _, err := identity.Sessions(env.app, identity.IdleTimeout, identity.MaxAge).Start(ctx, ownerUID, "sso", "agent")
	if err != nil {
		t.Fatal(err)
	}

	// Attempt to smuggle spend_ceiling_micro_usd into PUT /api/workspace -> 400.
	wsBody, _ := json.Marshal(map[string]any{
		"name":                    "Acme",
		"logo_url":                "",
		"time_zone":               "UTC",
		"locale":                  "en-GB",
		"default_route":           "",
		"default_agent_id":        nil,
		"allow_member_providers":  true,
		"allow_member_routes":     true,
		"allow_member_connectors": true,
		"allow_member_agents":     true,
		"sso_required":            false,
		"spend_ceiling_micro_usd": 999999999,
	})
	wsReq := httptest.NewRequest(http.MethodPut, "/api/workspace", bytes.NewReader(wsBody))
	wsReq.AddCookie(&http.Cookie{Name: "__Host-upmo_session", Value: sessCookie, Path: "/", Secure: true, HttpOnly: true})
	wsRec := httptest.NewRecorder()
	env.handler.ServeHTTP(wsRec, wsReq)
	if wsRec.Code != http.StatusBadRequest {
		t.Fatalf("PUT /api/workspace with spend_ceiling_micro_usd = %d, want 400", wsRec.Code)
	}

	// Valid PUT /api/workspace leaves spend_ceiling_micro_usd untouched.
	wsBody2, _ := json.Marshal(map[string]any{
		"name":                    "Acme",
		"logo_url":                "",
		"time_zone":               "UTC",
		"locale":                  "en-GB",
		"default_route":           "",
		"default_agent_id":        nil,
		"allow_member_providers":  true,
		"allow_member_routes":     true,
		"allow_member_connectors": true,
		"allow_member_agents":     true,
		"sso_required":            false,
	})
	wsReq2 := httptest.NewRequest(http.MethodPut, "/api/workspace", bytes.NewReader(wsBody2))
	wsReq2.AddCookie(&http.Cookie{Name: "__Host-upmo_session", Value: sessCookie, Path: "/", Secure: true, HttpOnly: true})
	wsRec2 := httptest.NewRecorder()
	env.handler.ServeHTTP(wsRec2, wsReq2)
	if wsRec2.Code != http.StatusOK {
		t.Fatalf("valid PUT /api/workspace = %d %s", wsRec2.Code, wsRec2.Body.String())
	}
	var ceiling *int64
	if err := env.owner.QueryRow(ctx, `SELECT spend_ceiling_micro_usd FROM identity.workspace WHERE id = true`).Scan(&ceiling); err != nil {
		t.Fatal(err)
	}
	if ceiling == nil || *ceiling != 42000000 {
		t.Fatalf("ceiling after PUT /api/workspace = %v, want 42000000", ceiling)
	}

	// Delete quota clears ceiling.
	if delQ := env.signedDo(t, env.rawKey1, http.MethodDelete, "/api/v1/provision/quotas", nil); delQ.Code != http.StatusNoContent {
		t.Fatalf("DELETE /quotas = %d %s", delQ.Code, delQ.Body.String())
	}
	if err := env.owner.QueryRow(ctx, `SELECT spend_ceiling_micro_usd FROM identity.workspace WHERE id = true`).Scan(&ceiling); err != nil {
		t.Fatal(err)
	}
	if ceiling != nil {
		t.Fatalf("ceiling after DELETE /quotas = %v, want nil", *ceiling)
	}
}

func TestProvisionPacksVerificationAndIdempotency(t *testing.T) {
	env := newTestEnv(t, true)
	ctx := context.Background()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherPub, otherPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_ = otherPub
	t.Setenv(agents.EnvPackTrustKeys, "partner-v1="+base64.StdEncoding.EncodeToString(pub))

	pack := agents.Pack{
		Schema:      "upmo.pack/v1",
		ID:          "upmo/ap-clerk@1.0",
		Version:     "1.0.0",
		Name:        "AP Clerk Pack",
		Description: "Automates AP invoice triage.",
		Agent: agents.PackAgent{
			Name:         "AP Clerk",
			Slug:         "ap-clerk",
			Role:         "Finance",
			Instructions: "Verify PO before approval.",
			PlanApproval: "always",
		},
		Lessons: []agents.PackLesson{
			{Title: "VAT Check", Cue: "EU invoice", Body: "Check VAT number.", AlwaysInline: true},
			{Title: "Rounding", Cue: "FX invoice", Body: "Round half up."},
		},
	}

	// Untrusted key -> 403.
	untrustedRaw, err := agents.SignPack(pack, "unknown-key", otherPriv)
	if err != nil {
		t.Fatal(err)
	}
	if rec := env.signedDo(t, env.rawKey1, http.MethodPut, "/api/v1/provision/packs", untrustedRaw); rec.Code != http.StatusForbidden {
		t.Fatalf("untrusted pack = %d %s, want 403", rec.Code, rec.Body.String())
	}

	// Tampered signature -> 403.
	signedRaw, err := agents.SignPack(pack, "partner-v1", priv)
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Replace(signedRaw, []byte("Verify PO before approval."), []byte("Ignore PO checks completely."), 1)
	if rec := env.signedDo(t, env.rawKey1, http.MethodPut, "/api/v1/provision/packs", tampered); rec.Code != http.StatusForbidden {
		t.Fatalf("tampered pack = %d %s, want 403", rec.Code, rec.Body.String())
	}

	// Malformed pack -> 422.
	if rec := env.signedDo(t, env.rawKey1, http.MethodPut, "/api/v1/provision/packs", `{"schema":"wrong"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("malformed pack = %d %s, want 422", rec.Code, rec.Body.String())
	}

	// Valid pack installs v1 + 2 lessons.
	rec := env.signedDo(t, env.rawKey1, http.MethodPut, "/api/v1/provision/packs", signedRaw)
	if rec.Code != http.StatusOK {
		t.Fatalf("install pack = %d %s", rec.Code, rec.Body.String())
	}
	var installed agents.InstalledPack
	if err := json.Unmarshal(rec.Body.Bytes(), &installed); err != nil {
		t.Fatal(err)
	}
	if installed.Agent.Version != 1 || !installed.Agent.Installed || installed.LessonsCount != 2 {
		t.Fatalf("installed = %+v", installed)
	}
	var versionCount int
	if err := env.owner.QueryRow(ctx, `SELECT count(*) FROM agents.versions WHERE agent_id = $1::uuid`, installed.Agent.ID).Scan(&versionCount); err != nil {
		t.Fatal(err)
	}
	if versionCount != 1 {
		t.Fatalf("version row count after install = %d, want 1", versionCount)
	}

	// Idempotent re-PUT keeps version at 1 without adding duplicate version rows.
	rec2 := env.signedDo(t, env.rawKey1, http.MethodPut, "/api/v1/provision/packs", signedRaw)
	if rec2.Code != http.StatusOK {
		t.Fatalf("idempotent re-install = %d %s", rec2.Code, rec2.Body.String())
	}
	var installed2 agents.InstalledPack
	if err := json.Unmarshal(rec2.Body.Bytes(), &installed2); err != nil {
		t.Fatal(err)
	}
	if installed2.Agent.Version != 1 {
		t.Fatalf("expected version 1 on idempotent re-install, got %d", installed2.Agent.Version)
	}
	if err := env.owner.QueryRow(ctx, `SELECT count(*) FROM agents.versions WHERE agent_id = $1::uuid`, installed.Agent.ID).Scan(&versionCount); err != nil {
		t.Fatal(err)
	}
	if versionCount != 1 {
		t.Fatalf("version row count after idempotent re-install = %d, want 1", versionCount)
	}

	// Simulate admin customizing enabled, visibility, and team_ids; pack upgrade must preserve them.
	adminTeamID := uuid.NewString()
	if _, err := env.owner.Exec(ctx, `
		UPDATE agents.agents
		SET enabled = false, visibility = 'team', team_ids = ARRAY[$2::uuid]
		WHERE id = $1::uuid`, installed.Agent.ID, adminTeamID); err != nil {
		t.Fatal(err)
	}

	// Updated pack bumps version to 2 and removes dropped lesson.
	pack.Version = "1.1.0"
	pack.Agent.Instructions = "Verify PO and receipt before approval."
	pack.Lessons = []agents.PackLesson{
		{Title: "VAT Check", Cue: "EU invoice", Body: "Check VAT number via VIES.", AlwaysInline: true},
	}
	updatedRaw, err := agents.SignPack(pack, "partner-v1", priv)
	if err != nil {
		t.Fatal(err)
	}
	rec3 := env.signedDo(t, env.rawKey2, http.MethodPut, "/api/v1/provision/packs", updatedRaw)
	if rec3.Code != http.StatusOK {
		t.Fatalf("update pack = %d %s", rec3.Code, rec3.Body.String())
	}
	var installed3 agents.InstalledPack
	if err := json.Unmarshal(rec3.Body.Bytes(), &installed3); err != nil {
		t.Fatal(err)
	}
	if installed3.Agent.Version != 2 || installed3.LessonsCount != 1 {
		t.Fatalf("updated pack = %+v, want version 2 and 1 lesson", installed3)
	}
	if installed3.Agent.Enabled || installed3.Agent.Share.Visibility != "team" || len(installed3.Agent.Share.TeamIDs) != 1 || installed3.Agent.Share.TeamIDs[0] != adminTeamID {
		t.Fatalf("admin customizations overwritten on pack update: %+v", installed3.Agent)
	}
	if err := env.owner.QueryRow(ctx, `SELECT count(*) FROM agents.versions WHERE agent_id = $1::uuid`, installed3.Agent.ID).Scan(&versionCount); err != nil {
		t.Fatal(err)
	}
	if versionCount != 2 {
		t.Fatalf("version row count after update = %d, want 2", versionCount)
	}
	var lessonCount int
	if err := env.owner.QueryRow(ctx, `SELECT count(*) FROM agents.lessons WHERE agent_id = $1::uuid`, installed3.Agent.ID).Scan(&lessonCount); err != nil {
		t.Fatal(err)
	}
	if lessonCount != 1 {
		t.Fatalf("lesson count in DB = %d, want 1", lessonCount)
	}

	// Downgrading to older signed pack version (1.0.0 < 1.1.0) is refused with 409.
	if downgradeRec := env.signedDo(t, env.rawKey1, http.MethodPut, "/api/v1/provision/packs", signedRaw); downgradeRec.Code != http.StatusConflict {
		t.Fatalf("pack downgrade = %d %s, want 409", downgradeRec.Code, downgradeRec.Body.String())
	}
}

func TestProvisionStatusAndUsagePrivacy(t *testing.T) {
	env := newTestEnv(t, true)
	ctx := context.Background()

	if rec := env.signedDo(t, env.rawKey1, http.MethodPut, "/api/v1/provision/quotas", map[string]any{
		"spend_ceiling_micro_usd": 12345678,
	}); rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}

	statusRec := env.signedDo(t, env.rawKey1, http.MethodGet, "/api/v1/provision/status", nil)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("status = %d %s", statusRec.Code, statusRec.Body.String())
	}
	var statusBody struct {
		Ready                bool             `json:"ready"`
		Version              string           `json:"version"`
		SchemaVersions       map[string]int64 `json:"schema_versions"`
		SpendCeilingMicroUSD *int64           `json:"spend_ceiling_micro_usd"`
	}
	if err := json.Unmarshal(statusRec.Body.Bytes(), &statusBody); err != nil {
		t.Fatal(err)
	}
	if !statusBody.Ready || statusBody.SchemaVersions["identity"] != 1 || statusBody.SpendCeilingMicroUSD == nil || *statusBody.SpendCeilingMicroUSD != 12345678 {
		t.Fatalf("unexpected status = %+v", statusBody)
	}

	// Seed gateway.ledger rows with user_ids across task, chat, direct, unknown cost, and two months.
	userA := uuid.NewString()
	userB := uuid.NewString()
	runID := uuid.NewString()
	runID2 := uuid.NewString()
	agentID := uuid.NewString()
	nov := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	oct := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

	cost1 := 0.0125
	cost2 := 0.0030
	cost3 := 0.0005
	costOct := 9.99
	ptrI64 := func(v int64) *int64 { return &v }

	for _, row := range []struct {
		at          time.Time
		route       string
		model       string
		userID      string
		agentID     any
		runID       any
		inTok       int
		outTok      int
		cost        *int64
		estimated   bool
		priceSource string
		source      string
	}{
		{nov, "work", "gpt-4o", userA, nil, runID, 1000, 500, ptrI64(int64(cost1 * 1e6)), false, "models_dev", "task"},
		{nov, "custom-route", "gpt-4o", userB, agentID, nil, 200, 100, ptrI64(int64(cost2 * 1e6)), false, "models_dev", "chat"},
		{nov, "chat", "gpt-4o", userA, nil, runID2, 40, 20, ptrI64(100), false, "models_dev", "chat"},                    // a chat run with no agent stays chat
		{nov, "chat", "gpt-4o-mini", userA, nil, nil, 50, 25, ptrI64(int64(cost3 * 1e6)), false, "models_dev", "memory"}, // direct even if the route is named "chat"
		{nov, "memory-extract", "gpt-4o-mini", userA, nil, nil, 10, 5, ptrI64(1), true, "estimate", "memory"},
		{nov, "unpriced-direct", "gpt-4o-mini", userA, nil, nil, 20, 10, nil, false, "unknown", "memory"},
		{oct, "chat", "gpt-4o", userA, agentID, nil, 9999, 9999, ptrI64(int64(costOct * 1e6)), false, "models_dev", "chat"}, // different month
	} {
		if _, err := env.owner.Exec(ctx, `
			INSERT INTO gateway.ledger (
				at, route, link_position, model, plan_kind, failure_class,
				user_id, agent_id, run_id, input_tokens, output_tokens, usage_arrived,
				cost_micro_usd, estimated, source, price_source
			) VALUES (
				$1, $2, 1, $3, 'metered', '', $4::uuid, $5::uuid, $6::uuid, $7, $8, true,
				$9, $10, $11, $12
			)`,
			row.at, row.route, row.model, row.userID, row.agentID, row.runID, row.inTok, row.outTok, row.cost, row.estimated, row.source, row.priceSource); err != nil {
			t.Fatal(err)
		}
	}

	usageRec := env.signedDo(t, env.rawKey1, http.MethodGet, "/api/v1/provision/usage?month=2026-11", nil)
	if usageRec.Code != http.StatusOK {
		t.Fatalf("usage = %d %s", usageRec.Code, usageRec.Body.String())
	}
	rawBody := usageRec.Body.String()
	if strings.Contains(rawBody, userA) || strings.Contains(rawBody, userB) || strings.Contains(rawBody, "user_id") {
		t.Fatalf("usage response leaked user_id: %s", rawBody)
	}
	var usage gateway.MonthlyUsage
	if err := json.Unmarshal(usageRec.Body.Bytes(), &usage); err != nil {
		t.Fatal(err)
	}
	if usage.Month != "2026-11" || usage.Totals.InputTokens != 1320 || usage.Totals.OutputTokens != 660 || usage.Totals.TotalTokens != 1980 || usage.Totals.CostMicroUSD != 16101 || usage.Totals.UnknownCostCount != 1 || usage.Totals.UnpricedCallCount != 1 {
		t.Fatalf("unexpected usage totals = %+v", usage)
	}
	if len(usage.Breakdown) != 3 {
		t.Fatalf("breakdown len = %d, want 3: %+v", len(usage.Breakdown), usage.Breakdown)
	}
	byKind := make(map[string]gateway.UsageRow, len(usage.Breakdown))
	for _, b := range usage.Breakdown {
		byKind[b.Kind] = b
	}
	if byKind["task"].Model != "gpt-4o" || byKind["task"].TotalTokens != 1500 || byKind["task"].UnknownCostCount != 0 || byKind["task"].UnpricedCallCount != 0 {
		t.Fatalf("task bucket = %+v", byKind["task"])
	}
	if byKind["chat"].Model != "gpt-4o" || byKind["chat"].TotalTokens != 360 || byKind["chat"].UnknownCostCount != 0 || byKind["chat"].UnpricedCallCount != 0 {
		t.Fatalf("chat bucket = %+v", byKind["chat"])
	}
	if byKind["direct"].Model != "gpt-4o-mini" || byKind["direct"].TotalTokens != 120 || byKind["direct"].UnknownCostCount != 1 || byKind["direct"].UnpricedCallCount != 1 {
		t.Fatalf("direct bucket = %+v", byKind["direct"])
	}

	if badMonth := env.signedDo(t, env.rawKey1, http.MethodGet, "/api/v1/provision/usage?month=not-a-month", nil); badMonth.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid month = %d, want 422", badMonth.Code)
	}
}

func TestProvisionV2BindingIntegration(t *testing.T) {
	env := newTestEnv(t, true)
	ctx := context.Background()

	// The member every refused request below targets (an owner, because a
	// fresh install keeps one).
	if rec := env.signedDo(t, env.rawKey1, http.MethodPost, "/api/v1/provision/members", map[string]any{
		"external_id": "bind-1", "email": "bind@example.com", "name": "Bind", "role": "owner",
	}); rec.Code != http.StatusCreated {
		t.Fatalf("create member = %d %s", rec.Code, rec.Body.String())
	}

	linkCount := func() int {
		var n int
		if err := env.owner.QueryRow(ctx, `SELECT count(*) FROM identity.sign_in_links`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	linkBody := []byte(`{"external_id":"bind-1","purpose":"sso"}`)

	// A valid v2 bearer mints the link.
	bearer := env.sign(t, env.rawKey1, http.MethodPost, "/api/v1/provision/sign-in-links", linkBody, env.now)
	if rec := env.do(t, http.MethodPost, "/api/v1/provision/sign-in-links", bearer, linkBody); rec.Code != http.StatusCreated {
		t.Fatalf("valid v2 request = %d %s", rec.Code, rec.Body.String())
	}
	if got := linkCount(); got != 1 {
		t.Fatalf("links after valid request = %d, want 1", got)
	}

	// Sent a second time, the same bearer is a replay.
	if rec := env.do(t, http.MethodPost, "/api/v1/provision/sign-in-links", bearer, linkBody); rec.Code != http.StatusUnauthorized {
		t.Fatalf("replayed bearer = %d, want 401", rec.Code)
	}

	// A fresh bearer whose body was swapped after signing no longer matches
	// its MAC.
	swapped := env.sign(t, env.rawKey1, http.MethodPost, "/api/v1/provision/sign-in-links", linkBody, env.now)
	if rec := env.do(t, http.MethodPost, "/api/v1/provision/sign-in-links", swapped, []byte(`{"external_id":"bind-1","purpose":"sso_stepup"}`)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("changed body = %d, want 401", rec.Code)
	}

	// A v1 bearer no longer names a provision route.
	master, err := svcsig.ParseKey(env.rawKey1)
	if err != nil {
		t.Fatal(err)
	}
	v1Tok := "upmo_s2s_v1." + strings.TrimPrefix(env.sign(t, env.rawKey1, http.MethodPost, "/api/v1/provision/sign-in-links", linkBody, env.now), svcsig.BearerPrefixV2)
	if rec := env.do(t, http.MethodPost, "/api/v1/provision/sign-in-links", v1Tok, linkBody); rec.Code != http.StatusUnauthorized {
		t.Fatalf("v1 bearer = %d, want 401", rec.Code)
	}

	// A bearer signed for another audience, or for another path, is refused.
	gwDerived, err := svcsig.Derive(master, svcsig.AudienceGateway)
	if err != nil {
		t.Fatal(err)
	}
	wrongAudience, err := svcsig.SignRequest(gwDerived, svcsig.AudienceGateway, http.MethodPost, "/api/v1/provision/sign-in-links", "", linkBody, env.now)
	if err != nil {
		t.Fatal(err)
	}
	if rec := env.do(t, http.MethodPost, "/api/v1/provision/sign-in-links", wrongAudience, linkBody); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong audience = %d, want 401", rec.Code)
	}
	wrongPath := env.sign(t, env.rawKey1, http.MethodPost, "/api/v1/provision/members", linkBody, env.now)
	if rec := env.do(t, http.MethodPost, "/api/v1/provision/sign-in-links", wrongPath, linkBody); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bearer signed for another path = %d, want 401", rec.Code)
	}

	// A body over the route's cap is refused with the 413 the handlers
	// themselves returned before the guard took over the read, at both
	// limits: 64 KiB for JSON routes and 1 MiB for packs.
	bigJSON := bytes.Repeat([]byte("x"), 64<<10+1)
	jsonBearer := env.sign(t, env.rawKey1, http.MethodPost, "/api/v1/provision/members", bigJSON, env.now)
	if rec := env.do(t, http.MethodPost, "/api/v1/provision/members", jsonBearer, bigJSON); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized json body = %d, want 413", rec.Code)
	}
	bigPack := bytes.Repeat([]byte("p"), 1<<20+1)
	packBearer := env.sign(t, env.rawKey1, http.MethodPut, "/api/v1/provision/packs", bigPack, env.now)
	if rec := env.do(t, http.MethodPut, "/api/v1/provision/packs", packBearer, bigPack); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized pack body = %d, want 413", rec.Code)
	}

	// The query string is signed too. The refused request did not consume
	// the nonce (an invalid signature never does), so the properly signed
	// query of the same bearer still verifies.
	usageBearer := env.sign(t, env.rawKey1, http.MethodGet, "/api/v1/provision/usage?month=2026-11", nil, env.now)
	if rec := env.do(t, http.MethodGet, "/api/v1/provision/usage?month=2026-10", usageBearer, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("changed query = %d, want 401", rec.Code)
	}
	if rec := env.do(t, http.MethodGet, "/api/v1/provision/usage?month=2026-11", usageBearer, nil); rec.Code != http.StatusOK {
		t.Fatalf("signed query = %d, want 200", rec.Code)
	}

	// Every refusal wrote nothing: one link, no session.
	if got := linkCount(); got != 1 {
		t.Fatalf("links after refusals = %d, want 1", got)
	}
	var sessions int
	if err := env.owner.QueryRow(ctx, `SELECT count(*) FROM identity.sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 {
		t.Fatalf("sessions = %d, want 0", sessions)
	}

	// The replay is audited against the key that signed it, never the
	// bearer.
	if _, err := audit.SealOnce(ctx, env.owner, io.Discard); err != nil {
		t.Fatalf("audit.SealOnce: %v", err)
	}
	var replays int
	if err := env.owner.QueryRow(ctx, `
		SELECT count(*) FROM audit.events
		WHERE outcome = 'denied' AND reason = 'replay' AND target_id = $1`, env.keys[0].ID).Scan(&replays); err != nil {
		t.Fatal(err)
	}
	if replays != 1 {
		t.Fatalf("replay audit events naming key %s = %d, want 1", env.keys[0].ID, replays)
	}
}

func TestProvisionReplayRefusedAcrossInstancesIntegration(t *testing.T) {
	env := newTestEnv(t, true)
	ctx := context.Background()

	// A second brain instance: the same keys and database behind its own
	// Handler, as another replica would have.
	mux2 := http.NewServeMux()
	provision.Handler{
		Owner:      env.owner,
		Identity:   env.identity,
		MasterKeys: env.keyring,
		Keys:       env.keys,
		Log:        env.log,
		Now:        func() time.Time { return env.now },
	}.Mount(mux2)

	body := []byte(`{"external_id":"twins","name":"Twins"}`)
	bearer := env.sign(t, env.rawKey1, http.MethodPost, "/api/v1/provision/teams", body, env.now)
	signedReq := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/provision/teams", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+bearer)
		return req
	}

	rec := httptest.NewRecorder()
	env.handler.ServeHTTP(rec, signedReq())
	if rec.Code != http.StatusCreated {
		t.Fatalf("first instance = %d %s", rec.Code, rec.Body.String())
	}

	// The identical request bytes reach the second instance: the nonce is
	// already in the shared table, so it is a replay there.
	rec2 := httptest.NewRecorder()
	mux2.ServeHTTP(rec2, signedReq())
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("second instance = %d %s, want 401", rec2.Code, rec2.Body.String())
	}

	// A third instance lists the same keys in the opposite order, as a
	// replica mid-rotation might. The nonce is recorded by key id, not by
	// list position, so the replay is still refused.
	reversed := []provision.Key{env.keys[1], env.keys[0]}
	mux3 := http.NewServeMux()
	provision.Handler{
		Owner:      env.owner,
		Identity:   env.identity,
		MasterKeys: env.keyring,
		Keys:       reversed,
		Log:        env.log,
		Now:        func() time.Time { return env.now },
	}.Mount(mux3)
	rec3 := httptest.NewRecorder()
	mux3.ServeHTTP(rec3, signedReq())
	if rec3.Code != http.StatusUnauthorized {
		t.Fatalf("reversed-key instance = %d %s, want 401", rec3.Code, rec3.Body.String())
	}

	// One team, one nonce row: the refused replays wrote nothing.
	var teams, nonces int
	if err := env.owner.QueryRow(ctx, `SELECT count(*) FROM identity.teams`).Scan(&teams); err != nil {
		t.Fatal(err)
	}
	if teams != 1 {
		t.Fatalf("teams after replay = %d, want 1", teams)
	}
	if err := env.owner.QueryRow(ctx, `SELECT count(*) FROM identity.provision_nonces`).Scan(&nonces); err != nil {
		t.Fatal(err)
	}
	if nonces != 1 {
		t.Fatalf("nonce rows after replay = %d, want 1", nonces)
	}
}

func TestProvisionNonceSweepIntegration(t *testing.T) {
	env := newTestEnv(t, true)
	ctx := context.Background()

	// Nonces consumed at ages either side of the retention window (twice the
	// 30s timestamp tolerance): 10s and 45s are inside it, 90s and an hour
	// are past it. A retention of one tolerance, a second, or "anything
	// before now" would sweep a nonce whose bearer can still verify.
	sweepAt := env.now
	for _, row := range []struct {
		nonce string
		age   time.Duration
	}{
		{"nonce-10s", 10 * time.Second},
		{"nonce-45s", 45 * time.Second},
		{"nonce-90s", 90 * time.Second},
		{"nonce-1h", time.Hour},
	} {
		env.now = sweepAt.Add(-row.age)
		if err := env.identity.ConsumeNonce(ctx, env.keys[0].ID, row.nonce); err != nil {
			t.Fatal(err)
		}
	}

	// The sweep the periodic job runs, called with the fake clock. A swept
	// nonce is harmless to forget: its bearer is outside the timestamp
	// tolerance by then, so the verifier refuses it before the nonce is even
	// consulted.
	removed, err := env.identity.SweepProvisionNonces(ctx, sweepAt)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("swept = %d nonces, want 2", removed)
	}
	rows, err := env.owner.Query(ctx, `SELECT nonce FROM identity.provision_nonces ORDER BY nonce`)
	if err != nil {
		t.Fatal(err)
	}
	left, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"nonce-10s", "nonce-45s"}; !slices.Equal(left, want) {
		t.Fatalf("nonces after sweep = %v, want %v", left, want)
	}
}

func TestProvisionNonceStoreFailureIntegration(t *testing.T) {
	env := newTestEnv(t, true)
	ctx := context.Background()

	// An instance whose nonce store cannot be reached: a closed pool fails
	// every query with an error that is not a replay.
	closed, err := pgxpool.NewWithConfig(ctx, env.owner.Config())
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	mux := http.NewServeMux()
	provision.Handler{
		Owner:      env.owner,
		Identity:   identity.Provision(closed, brainops.NewEmailSealer(env.keyring), "http://localhost:5173"),
		MasterKeys: env.keyring,
		Keys:       env.keys,
		Log:        env.log,
		Now:        func() time.Time { return env.now },
	}.Mount(mux)

	body := []byte(`{"external_id":"unrecorded","name":"Unrecorded"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/provision/teams", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+env.sign(t, env.rawKey1, http.MethodPost, "/api/v1/provision/teams", body, env.now))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("nonce store failure = %d %s, want 500", rec.Code, rec.Body.String())
	}

	// A nonce that could not be recorded never reaches the handler.
	var teams int
	if err := env.owner.QueryRow(ctx, `SELECT count(*) FROM identity.teams`).Scan(&teams); err != nil {
		t.Fatal(err)
	}
	if teams != 0 {
		t.Fatalf("teams after nonce store failure = %d, want 0", teams)
	}
}

// Invariants 10 and 12: a key provision stores opens for gateway through the
// worker's unwrap, as upmo_gateway, and for the daily checker in the
// worker's own ring; each upstream call carries the provisioned key.
func TestProvisionedKeyOpensForDispatchAndTheChecker(t *testing.T) {
	env := newTestEnv(t, true)
	ctx := context.Background()
	const secret = "sk-test-allowed"
	var hits atomic.Int32
	var auth atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		auth.Store(r.Header.Get("Authorization"))
		if r.URL.Path == "/v1/chat/completions" {
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"hello from provider"}}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"m1"}]}`)
	}))
	t.Cleanup(upstream.Close)
	if rec := env.signedDo(t, env.rawKey1, http.MethodPut, "/api/v1/provision/providers", map[string]any{
		"providers": []map[string]any{{
			"name": "Provisioned", "provider": "custom", "adapter": "openai_compat", "plan_kind": "local",
			"base_url": upstream.URL, "api_key": secret, "models": []string{"m1"},
		}},
	}); rec.Code != http.StatusOK {
		t.Fatalf("provision = %d %s", rec.Code, rec.Body.String())
	}
	var account string
	if err := env.owner.QueryRow(ctx, `SELECT id::text FROM gateway.provider_accounts WHERE name = 'Provisioned'`).Scan(&account); err != nil {
		t.Fatal(err)
	}
	if _, err := env.owner.Exec(ctx, `INSERT INTO gateway.routes (name, capability, installed) VALUES ('provisioned', 'chat', true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.owner.Exec(ctx, `
		INSERT INTO gateway.route_links (route_id, position, provider_account_id, model, retry_budget)
		SELECT id, 1, $1::uuid, 'm1', 0 FROM gateway.routes WHERE name = 'provisioned'`, account); err != nil {
		t.Fatal(err)
	}

	// Dispatch: gateway as upmo_gateway, its key from the worker's unwrap.
	worker := workerkeys.Start(t, env.owner, env.keyring)
	gatewayPool := pgtest.Login(t, env.owner, "upmo_gateway")
	mux := http.NewServeMux()
	gateway.Mount(mux, gatewayPool, gateway.Chain{gateway.DBSecrets{Pool: gatewayPool, Unwrap: worker.Client(t, svcsig.AudienceWorkerGateway)}}, nil)
	// A house account serves a call that names no key of its own.
	raw, _ := json.Marshal(map[string]any{"route": "provisioned", "prompt": "hi", "source": "system"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/upmo.gateway.v1.Gateway/Chat", bytes.NewReader(raw)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "hello from provider") || auth.Load() != "Bearer "+secret {
		t.Fatalf("dispatch = %d %s, upstream saw %v", rec.Code, rec.Body.String(), auth.Load())
	}
	if strings.Contains(rec.Body.String(), secret) {
		t.Fatalf("the dispatch answer holds the key: %s", rec.Body.String())
	}

	// The daily checker: the worker opens the same row in process.
	before := hits.Load()
	auth.Store("")
	deps := gateway.ProviderCheckDeps{Owner: env.owner, Secrets: gateway.Chain{gateway.DBSecrets{Pool: env.owner, Keys: env.keyring}}}
	if err := gateway.CheckAccountOnce(ctx, deps, account); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := env.owner.QueryRow(ctx, `SELECT status FROM gateway.provider_accounts WHERE id = $1::uuid`, account).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != before+1 || auth.Load() != "Bearer "+secret || status != "Connected" {
		t.Fatalf("checker: %d calls, auth %v, status %q", hits.Load()-before, auth.Load(), status)
	}
}
