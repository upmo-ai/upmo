//go:build integration

package identity_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/upmo-ai/upmo/internal/identity"
	httpapi "github.com/upmo-ai/upmo/internal/identity/adapters/http"
	"github.com/upmo-ai/upmo/internal/identity/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/identity/adapters/webauthn"
	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
	"github.com/upmo-ai/upmo/internal/testutil/sealer"
)

func TestWorkspaceMigrationAndRLS(t *testing.T) {
	owner, appPool, ctx := upBoth(t)
	appLogin := pgtest.AppLogin(t, owner)
	person := seedOwner(t, owner, ctx, "ada@example.com")

	// identity.workspace must enforce RLS even for table owners and grant only SELECT/UPDATE to upmo_app.
	var rlsEnabled, rlsForced bool
	if err := owner.QueryRow(ctx, `
		SELECT relrowsecurity, relforcerowsecurity
		FROM pg_class
		WHERE oid = 'identity.workspace'::regclass`).Scan(&rlsEnabled, &rlsForced); err != nil {
		t.Fatal(err)
	}
	if !rlsEnabled || !rlsForced {
		t.Fatalf("identity.workspace rls = (%v, %v), want (true, true)", rlsEnabled, rlsForced)
	}
	for _, priv := range []struct {
		name string
		want bool
	}{
		{"SELECT", true},
		{"UPDATE", false},
		{"INSERT", false},
		{"DELETE", false},
		{"TRUNCATE", false},
	} {
		var got bool
		if err := owner.QueryRow(ctx, `SELECT has_table_privilege('upmo_app', 'identity.workspace'::regclass, $1)`, priv.name).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != priv.want {
			t.Fatalf("upmo_app %s on identity.workspace = %v, want %v", priv.name, got, priv.want)
		}
	}
	var privs string
	if err := owner.QueryRow(ctx, `
		SELECT coalesce(string_agg(privilege_type, ',' ORDER BY privilege_type), '')
		FROM information_schema.table_privileges
		WHERE grantee = 'upmo_app' AND table_schema = 'identity' AND table_name = 'workspace'`).Scan(&privs); err != nil {
		t.Fatal(err)
	}
	if privs != "SELECT" {
		t.Fatalf("upmo_app table privileges = %q, want SELECT (column-scoped UPDATE only)", privs)
	}
	var nameUpdate, ceilingUpdate bool
	if err := owner.QueryRow(ctx, `
		SELECT has_column_privilege('upmo_app', 'identity.workspace', 'name', 'UPDATE'),
		       has_column_privilege('upmo_app', 'identity.workspace', 'spend_ceiling_micro_usd', 'UPDATE')`).Scan(&nameUpdate, &ceilingUpdate); err != nil {
		t.Fatal(err)
	}
	if !nameUpdate {
		t.Fatal("upmo_app lacks UPDATE on identity.workspace.name, want true")
	}
	if ceilingUpdate {
		t.Fatal("upmo_app has UPDATE on identity.workspace.spend_ceiling_micro_usd, want false")
	}

	// Single-row CHECK rejects inserting a second workspace row even as owner.
	if _, err := owner.Exec(ctx, `INSERT INTO identity.workspace (id) VALUES (false)`); err == nil {
		t.Fatal("second workspace row with id=false was accepted")
	}

	// upmo_app cannot INSERT or DELETE the workspace row.
	_, err := appLogin.Exec(ctx, `INSERT INTO identity.workspace (id) VALUES (true)`)
	assertDenied(t, "insert workspace as upmo_app", err)
	_, err = appLogin.Exec(ctx, `DELETE FROM identity.workspace WHERE id = true`)
	assertDenied(t, "delete workspace as upmo_app", err)

	// Direct store UpdateWorkspace without settings:manage maps RLS ErrNoRows to ErrSettingsOnly.
	personID, err := domain.ParseUserID(person)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (postgres.Passkeys{Pool: appPool}).UpdateWorkspace(ctx, personID, "", domain.WorkspaceUpdate{
		Name: "Hijacked", TimeZone: "UTC", Locale: "en-GB",
	}, time.Now()); !errors.Is(err, domain.ErrSettingsOnly) {
		t.Fatalf("UpdateWorkspace without settings:manage = %v, want ErrSettingsOnly", err)
	}

	// Without upmo.user_id, SELECT on identity.workspace returns 0 rows.
	var name string
	if err := appPool.QueryRow(ctx, `SELECT name FROM identity.workspace WHERE id = true`).Scan(&name); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("anonymous workspace select = %q, %v; want ErrNoRows", name, err)
	}

	// With upmo.user_id set, SELECT succeeds.
	tx, err := appPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('upmo.user_id', $1, true)`, person); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT name FROM identity.workspace WHERE id = true`).Scan(&name); err != nil {
		t.Fatalf("signed-in workspace select: %v", err)
	}
	if name != "Workspace" {
		t.Fatalf("default workspace name = %q, want Workspace", name)
	}
	// Without settings:manage, UPDATE affects 0 rows under RLS.
	tag, err := tx.Exec(ctx, `UPDATE identity.workspace SET name = 'Hijacked' WHERE id = true`)
	if err != nil {
		t.Fatalf("unexpected error on non-admin update: %v", err)
	}
	if tag.RowsAffected() != 0 {
		t.Fatalf("non-admin update affected %d rows, want 0", tag.RowsAffected())
	}
	// With settings:manage, UPDATE succeeds.
	if _, err := tx.Exec(ctx, `SELECT set_config('upmo.allow', 'settings:manage', true)`); err != nil {
		t.Fatal(err)
	}
	tag, err = tx.Exec(ctx, `UPDATE identity.workspace SET name = 'Acme' WHERE id = true`)
	if err != nil {
		t.Fatalf("admin update: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("admin update affected %d rows, want 1", tag.RowsAffected())
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// Widened sign_in_method and sign_in_links.purpose checks accept new values.
	if _, err := owner.Exec(ctx, `
		INSERT INTO identity.sessions (user_id, token_hash, created_at, last_seen_at, expires_at, sign_in_method)
		VALUES ($1, $2, now(), now(), now() + interval '1 hour', 'sso')`, person, []byte{41, 41, 41}); err != nil {
		t.Fatalf("sso session insert: %v", err)
	}
	for i, purpose := range []string{"invite", "recover", "bootstrap", "sign_in", "step_up", "sso", "sso_stepup"} {
		if _, err := owner.Exec(ctx, `
			INSERT INTO identity.sign_in_links (token_hash, user_id, purpose, expires_at)
			VALUES ($1, $2, $3, now() + interval '1 hour')`, []byte{50, byte(i), 1}, person, purpose); err != nil {
			t.Fatalf("link purpose %q insert: %v", purpose, err)
		}
	}

	// Users time_zone and locale columns exist, default NULL, and stay protected from direct upmo_app writes.
	var tz, locale *string
	if err := owner.QueryRow(ctx, `SELECT time_zone, locale FROM identity.users WHERE id = $1`, person).Scan(&tz, &locale); err != nil {
		t.Fatal(err)
	}
	if tz != nil || locale != nil {
		t.Fatalf("user prefs = (%v, %v), want (nil, nil)", tz, locale)
	}
	_, err = appLogin.Exec(ctx, `UPDATE identity.users SET time_zone = 'UTC' WHERE id = $1`, person)
	assertDenied(t, "update users.time_zone as upmo_app", err)
	_, err = appLogin.Exec(ctx, `UPDATE identity.users SET locale = 'en-US' WHERE id = $1`, person)
	assertDenied(t, "update users.locale as upmo_app", err)
}

func TestWorkspaceMigrationDownUp(t *testing.T) {
	owner, ctx := upOwner(t)
	person := seedOwner(t, owner, ctx, "ada@example.com")

	// Seed session and sign-in link rows across methods and purposes prior to Down.
	if _, err := owner.Exec(ctx, `
		INSERT INTO identity.sessions (user_id, token_hash, created_at, last_seen_at, expires_at, sign_in_method)
		VALUES ($1, $2, now(), now(), now() + interval '1 hour', 'sso'),
		       ($1, $3, now(), now(), now() + interval '1 hour', 'passkey')`,
		person, []byte{60, 1}, []byte{60, 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO identity.sign_in_links (token_hash, user_id, purpose, expires_at)
		VALUES ($1, $2, 'sso', now() + interval '1 hour'),
		       ($3, $2, 'sign_in', now() + interval '1 hour'),
		       ($4, $2, 'invite', now() + interval '1 hour')`,
		[]byte{61, 1}, person, []byte{61, 2}, []byte{61, 3}); err != nil {
		t.Fatal(err)
	}

	db := stdlib.OpenDB(*owner.Config().ConnConfig)
	defer func() { _ = db.Close() }()
	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		identity.Migrations(),
		goose.WithTableName(identity.Schema+".goose_db_version"),
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := provider.DownTo(ctx, 0); err != nil {
		t.Fatalf("down to 0: %v", err)
	}

	var regclass *string
	if err := owner.QueryRow(ctx, `SELECT to_regclass('identity.workspace')::text`).Scan(&regclass); err != nil {
		t.Fatal(err)
	}
	if regclass != nil {
		t.Fatalf("identity.workspace still exists after down: %s", *regclass)
	}

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("re-up: %v", err)
	}
	var version int
	if err := owner.QueryRow(ctx, `SELECT max(version_id) FROM identity.goose_db_version WHERE is_applied`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("version after re-up = %d, want 1", version)
	}
	person = seedOwner(t, owner, ctx, "ada@example.com")
	var forced bool
	if err := owner.QueryRow(ctx, `SELECT relforcerowsecurity FROM pg_class WHERE oid = 'identity.workspace'::regclass`).Scan(&forced); err != nil || !forced {
		t.Fatalf("relforcerowsecurity after re-up = %v, %v", forced, err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO identity.sessions (user_id, token_hash, created_at, last_seen_at, expires_at, sign_in_method)
		VALUES ($1, $2, now(), now(), now() + interval '1 hour', 'sso')`, person, []byte{62, 1}); err != nil {
		t.Fatalf("sso session after re-up: %v", err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO identity.sign_in_links (token_hash, user_id, purpose, expires_at)
		VALUES ($1, $2, 'sso', now() + interval '1 hour')`, []byte{62, 2}, person); err != nil {
		t.Fatalf("sso link after re-up: %v", err)
	}
}

func TestWorkspaceHTTP(t *testing.T) {
	ownerPool, pool, ctx := upBoth(t)
	sessions := identity.Sessions(pool, domain.IdleTimeout, domain.MaxAge)
	mux := http.NewServeMux()
	identity.MountWorkspace(mux, identity.Workspace(pool), sessions)

	seedUserWithRole(t, ownerPool, ctx, "ada@example.com", "Ada", "owner")
	adminID := seedUserWithRole(t, ownerPool, ctx, "bob@example.com", "Bob", "admin")
	memberID := seedUserWithRole(t, ownerPool, ctx, "lee@example.com", "Lee", "member")

	adminToken, _, err := sessions.Start(ctx, adminID, "passkey", "")
	if err != nil {
		t.Fatal(err)
	}
	memberToken, _, err := sessions.Start(ctx, memberID, "passkey", "")
	if err != nil {
		t.Fatal(err)
	}

	// Unauthenticated requests are refused with 401.
	if rec := call(mux, http.MethodGet, "/api/workspace", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth GET /api/workspace = %d, want 401", rec.Code)
	}
	if rec := callWorkspace(t, mux, http.MethodPut, "/api/workspace", "", workspacePayload(map[string]any{"name": "X"})); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth PUT /api/workspace = %d, want 401", rec.Code)
	}

	// Member can read default workspace settings.
	getMember := call(mux, http.MethodGet, "/api/workspace", memberToken, nil)
	if getMember.Code != http.StatusOK {
		t.Fatalf("member GET /api/workspace = %d %s", getMember.Code, getMember.Body.String())
	}
	var got struct {
		Name                  string  `json:"name"`
		LogoURL               string  `json:"logo_url"`
		TimeZone              string  `json:"time_zone"`
		Locale                string  `json:"locale"`
		DefaultRoute          string  `json:"default_route"`
		DefaultAgentID        *string `json:"default_agent_id"`
		AllowMemberProviders  bool    `json:"allow_member_providers"`
		AllowMemberRoutes     bool    `json:"allow_member_routes"`
		AllowMemberConnectors bool    `json:"allow_member_connectors"`
		AllowMemberAgents     bool    `json:"allow_member_agents"`
		SSORequired           bool    `json:"sso_required"`
	}
	if err := json.Unmarshal(getMember.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "Workspace" || got.TimeZone != "UTC" || got.Locale != "en-GB" || got.SSORequired || !got.AllowMemberProviders {
		t.Fatalf("defaults = %+v", got)
	}

	// Member cannot update workspace settings (403).
	putMember := callWorkspace(t, mux, http.MethodPut, "/api/workspace", memberToken, workspacePayload(map[string]any{
		"name": "Evil",
	}))
	if putMember.Code != http.StatusForbidden {
		t.Fatalf("member PUT /api/workspace = %d %s, want 403", putMember.Code, putMember.Body.String())
	}

	// Unknown JSON fields and trailing tokens return 400.
	unknownField := callWorkspace(t, mux, http.MethodPut, "/api/workspace", adminToken, workspacePayload(map[string]any{
		"extra": true,
	}))
	if unknownField.Code != http.StatusBadRequest {
		t.Fatalf("unknown field PUT /api/workspace = %d %s, want 400", unknownField.Code, unknownField.Body.String())
	}
	validRaw, err := json.Marshal(workspacePayload(nil))
	if err != nil {
		t.Fatal(err)
	}
	trailing := call(mux, http.MethodPut, "/api/workspace", adminToken, append(validRaw, []byte(` {"extra":1}`)...))
	if trailing.Code != http.StatusBadRequest {
		t.Fatalf("trailing JSON PUT /api/workspace = %d %s, want 400", trailing.Code, trailing.Body.String())
	}

	// Validation errors return 422.
	invalidPayloads := []struct {
		name string
		body map[string]any
	}{
		{"invalid tz", workspacePayload(map[string]any{"name": "Acme", "time_zone": "Mars/Olympus"})},
		{"local tz", workspacePayload(map[string]any{"name": "Acme", "time_zone": "Local"})},
		{"empty name", workspacePayload(map[string]any{"name": "   "})},
		{"bad logo", workspacePayload(map[string]any{"name": "Acme", "logo_url": "javascript:alert(1)"})},
		{"bad agent uuid", workspacePayload(map[string]any{"name": "Acme", "default_agent_id": "not-a-uuid"})},
	}
	for _, tc := range invalidPayloads {
		rec := callWorkspace(t, mux, http.MethodPut, "/api/workspace", adminToken, tc.body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: PUT /api/workspace = %d %s, want 422", tc.name, rec.Code, rec.Body.String())
		}
	}

	// Admin updates workspace settings and any signed-in user reads them back.
	agentID := uuid.MustParse("0199a2f5-7a01-7c1e-9b3a-5c2d8e4f6a01").String()
	putAdmin := callWorkspace(t, mux, http.MethodPut, "/api/workspace", adminToken, map[string]any{
		"name":                    "Acme Corp",
		"logo_url":                "https://acme.example/logo.svg",
		"time_zone":               "America/New_York",
		"locale":                  "en-US",
		"default_route":           "primary",
		"default_agent_id":        agentID,
		"allow_member_providers":  false,
		"allow_member_routes":     true,
		"allow_member_connectors": false,
		"allow_member_agents":     true,
		"sso_required":            true,
	})
	if putAdmin.Code != http.StatusOK {
		t.Fatalf("admin PUT /api/workspace = %d %s", putAdmin.Code, putAdmin.Body.String())
	}
	readBack := call(mux, http.MethodGet, "/api/workspace", memberToken, nil)
	if readBack.Code != http.StatusOK {
		t.Fatalf("read back = %d %s", readBack.Code, readBack.Body.String())
	}
	if err := json.Unmarshal(readBack.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "Acme Corp" || got.LogoURL != "https://acme.example/logo.svg" ||
		got.TimeZone != "America/New_York" || got.Locale != "en-US" || got.DefaultRoute != "primary" ||
		got.DefaultAgentID == nil || *got.DefaultAgentID != agentID ||
		got.AllowMemberProviders || !got.AllowMemberRoutes || got.AllowMemberConnectors || !got.AllowMemberAgents || !got.SSORequired {
		t.Fatalf("persisted workspace = %+v", got)
	}

	// Omitting any required boolean returns 422 and never flips sso_required or any allow_member_* flag.
	for _, omitted := range []string{
		"sso_required",
		"allow_member_providers",
		"allow_member_routes",
		"allow_member_connectors",
		"allow_member_agents",
	} {
		partial := workspacePayload(map[string]any{"name": "Partial", "sso_required": true})
		delete(partial, omitted)
		rec := callWorkspace(t, mux, http.MethodPut, "/api/workspace", adminToken, partial)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("omitting %s: PUT /api/workspace = %d %s, want 422", omitted, rec.Code, rec.Body.String())
		}
	}
	afterPartial := call(mux, http.MethodGet, "/api/workspace", memberToken, nil)
	if afterPartial.Code != http.StatusOK {
		t.Fatalf("read back after partial = %d %s", afterPartial.Code, afterPartial.Body.String())
	}
	if err := json.Unmarshal(afterPartial.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "Acme Corp" || !got.SSORequired || got.AllowMemberProviders || !got.AllowMemberRoutes || got.AllowMemberConnectors || !got.AllowMemberAgents {
		t.Fatalf("workspace mutated after partial PUT: %+v", got)
	}
}

func TestSSOEnforcement(t *testing.T) {
	ownerPool, pool, ctx := upBoth(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	clock := time.Now().UTC()
	nowFn := func() time.Time { return clock }
	sessions := identity.Sessions(pool, domain.IdleTimeout, domain.MaxAge)
	sessions.Now = nowFn
	passwords := identity.Passwords(pool, sealer.NewEmail(t), sessions)
	passwords.Sealer = sealer.New(t)
	passwords.Now = nowFn
	passkeys, err := identity.Passkeys(pool, sealer.NewEmail(t), sessions, "http://localhost:5173", "localhost", log)
	if err != nil {
		t.Fatal(err)
	}
	passkeys.Now = nowFn
	mux := http.NewServeMux()
	identity.MountSessions(mux, sessions, log, passwords.HasPassword)
	identity.MountPasskeys(mux, passkeys, passwords, nil)
	identity.MountWorkspace(mux, identity.Workspace(pool), sessions)

	ownerID := seedUserWithRole(t, ownerPool, ctx, "ada@example.com", "Ada", "owner")
	adminID := seedUserWithRole(t, ownerPool, ctx, "bob@example.com", "Bob", "admin")
	memberID := seedUserWithRole(t, ownerPool, ctx, "lee@example.com", "Lee", "member")

	// Set passwords and register passkeys for owner, admin, and member before enabling sso_required.
	const pw = "correct horse battery"
	for _, u := range []struct {
		id    domain.UserID
		email string
		name  string
	}{
		{ownerID, "ada@example.com", "Ada"},
		{adminID, "bob@example.com", "Bob"},
		{memberID, "lee@example.com", "Lee"},
	} {
		if err := passwords.Set(ctx, u.id, "", u.email, u.name, pw); err != nil {
			t.Fatal(err)
		}
	}

	ownerSoft, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	memberSoft, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	ownerLink, err := passkeys.IssueLink(ctx, ownerID, "recover")
	if err != nil {
		t.Fatal(err)
	}
	finishLink(t, mux, ownerSoft, ownerLink)
	memberLink, err := passkeys.IssueLink(ctx, memberID, "recover")
	if err != nil {
		t.Fatal(err)
	}
	finishLink(t, mux, memberSoft, memberLink)

	// Enable sso_required via owner session.
	ownerSess, _, err := sessions.Start(ctx, ownerID, "passkey", "")
	if err != nil {
		t.Fatal(err)
	}
	putRec := callWorkspace(t, mux, http.MethodPut, "/api/workspace", ownerSess, workspacePayload(map[string]any{
		"sso_required": true,
	}))
	if putRec.Code != http.StatusOK {
		t.Fatalf("enable sso_required = %d %s", putRec.Code, putRec.Body.String())
	}

	// 1. Wrong password for non-owner still returns 401 (no role/SSO leak).
	wrongRec := postPassword(t, mux, http.MethodPost, "/api/sign-in/password", "", map[string]string{
		"email": "lee@example.com", "password": "wrong horse battery",
	})
	if wrongRec.Code != http.StatusUnauthorized {
		t.Fatalf("member wrong password = %d %s, want 401", wrongRec.Code, wrongRec.Body.String())
	}

	// 2. Non-owner password sign-in refused with 403 sso required.
	for _, email := range []string{"bob@example.com", "lee@example.com"} {
		rec := postPassword(t, mux, http.MethodPost, "/api/sign-in/password", "", map[string]string{
			"email": email, "password": pw,
		})
		if rec.Code != http.StatusForbidden || !bytes.Contains(rec.Body.Bytes(), []byte("sso required")) {
			t.Fatalf("%s password sign-in = %d %s, want 403 sso required", email, rec.Code, rec.Body.String())
		}
	}

	// 3. Non-owner passkey sign-in refused with 403 sso required.
	var memberHandle []byte
	if err := ownerPool.QueryRow(ctx, `SELECT webauthn_handle FROM identity.users WHERE id = $1`, memberID).Scan(&memberHandle); err != nil {
		t.Fatal(err)
	}
	signInOpt := callJSON(mux, http.MethodPost, "/api/passkeys/sign-in/options", "", map[string]string{})
	if signInOpt.Code != http.StatusOK {
		t.Fatalf("sign-in options = %d", signInOpt.Code)
	}
	memberAssertion, err := memberSoft.Login("localhost", "http://localhost:5173", publicChallenge(t, signInOpt.Body.Bytes()), memberHandle)
	if err != nil {
		t.Fatal(err)
	}
	memberPasskeyRes := callJSON(mux, http.MethodPost, "/api/passkeys/sign-in/finish", cookie(t, signInOpt, httpapi.ChallengeCookie), json.RawMessage(memberAssertion))
	if memberPasskeyRes.Code != http.StatusForbidden || !bytes.Contains(memberPasskeyRes.Body.Bytes(), []byte("sso required")) {
		t.Fatalf("member passkey sign-in = %d %s, want 403 sso required", memberPasskeyRes.Code, memberPasskeyRes.Body.String())
	}

	// 4. Non-owner non-SSO link sign-in refused with 403 and link remains unspent.
	for _, purpose := range []string{"recover", "invite", "sign_in"} {
		clock = clock.Add(2 * time.Minute)
		tok, err := passkeys.IssueLink(ctx, memberID, purpose)
		if err != nil {
			t.Fatal(err)
		}
		opt := callJSON(mux, http.MethodPost, "/api/passkeys/link/options", "", map[string]string{"token": tok})
		if opt.Code != http.StatusOK {
			t.Fatalf("%s link options = %d %s", purpose, opt.Code, opt.Body.String())
		}
		reg, err := memberSoft.Register("localhost", "http://localhost:5173", publicChallenge(t, opt.Body.Bytes()), userHandle(t, opt.Body.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		fin := callJSON(mux, http.MethodPost, "/api/passkeys/link/finish", cookie(t, opt, httpapi.ChallengeCookie), map[string]any{
			"token": tok, "response": json.RawMessage(reg),
		})
		if fin.Code != http.StatusForbidden || !bytes.Contains(fin.Body.Bytes(), []byte("sso required")) {
			t.Fatalf("member %s link = %d %s, want 403 sso required", purpose, fin.Code, fin.Body.String())
		}
		var usedAt *time.Time
		if err := ownerPool.QueryRow(ctx, `SELECT used_at FROM identity.sign_in_links WHERE token_hash = $1`, domain.Hash(mustParseToken(t, tok))).Scan(&usedAt); err != nil {
			t.Fatal(err)
		}
		if usedAt != nil {
			t.Fatalf("refused %s link was spent", purpose)
		}
	}

	// 5. Non-owner 'sso' link sign-in succeeds and records sign_in_method='sso'.
	clock = clock.Add(2 * time.Minute)
	ssoTok, err := passkeys.IssueLink(ctx, memberID, "sso")
	if err != nil {
		t.Fatal(err)
	}
	memberSSOSoft, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	ssoSessCookie := finishLink(t, mux, memberSSOSoft, ssoTok)
	ssoSessRes := call(mux, http.MethodGet, "/api/session", ssoSessCookie, nil)
	if ssoSessRes.Code != http.StatusOK || !bytes.Contains(ssoSessRes.Body.Bytes(), []byte(`"sign_in_method":"sso"`)) {
		t.Fatalf("sso link session = %d %s", ssoSessRes.Code, ssoSessRes.Body.String())
	}

	// 6. Direct sessions.Start with non-'sso' method for non-owner is also refused at store boundary.
	if _, _, err := sessions.Start(ctx, adminID, "password", ""); !errors.Is(err, identity.ErrSSORequired) {
		t.Fatalf("direct admin sessions.Start(password) = %v, want ErrSSORequired", err)
	}
	if _, _, err := sessions.Start(ctx, memberID, "sso", ""); err != nil {
		t.Fatalf("direct member sessions.Start(sso) = %v", err)
	}

	// 7. Owner break-glass works for password, passkey, and link sign-in.
	clock = clock.Add(2 * time.Minute)
	ownerPwRes := postPassword(t, mux, http.MethodPost, "/api/sign-in/password", "", map[string]string{
		"email": "ada@example.com", "password": pw,
	})
	if ownerPwRes.Code != http.StatusOK {
		t.Fatalf("owner break-glass password = %d %s", ownerPwRes.Code, ownerPwRes.Body.String())
	}

	ownerOpt := callJSON(mux, http.MethodPost, "/api/passkeys/sign-in/options", "", map[string]string{})
	if ownerOpt.Code != http.StatusOK {
		t.Fatalf("owner sign-in options = %d", ownerOpt.Code)
	}
	ownerAssertion, err := ownerSoft.Login("localhost", "http://localhost:5173", publicChallenge(t, ownerOpt.Body.Bytes()), handleOf(t, ctx, pool))
	if err != nil {
		t.Fatal(err)
	}
	ownerPasskeyRes := callJSON(mux, http.MethodPost, "/api/passkeys/sign-in/finish", cookie(t, ownerOpt, httpapi.ChallengeCookie), json.RawMessage(ownerAssertion))
	if ownerPasskeyRes.Code != http.StatusNoContent {
		t.Fatalf("owner break-glass passkey = %d %s", ownerPasskeyRes.Code, ownerPasskeyRes.Body.String())
	}

	ownerRecoverTok, err := passkeys.IssueLink(ctx, ownerID, "recover")
	if err != nil {
		t.Fatal(err)
	}
	ownerRecoverSoft, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	if c := finishLink(t, mux, ownerRecoverSoft, ownerRecoverTok); c == "" {
		t.Fatal("owner break-glass link returned empty session")
	}
}

func seedUserWithRole(t *testing.T, pool *pgxpool.Pool, ctx context.Context, email, name, roleKey string) domain.UserID {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
		VALUES ($1, $2, $3, $4, (SELECT id FROM identity.roles WHERE key = $5), now())
		RETURNING id::text`, append(sealUser(t, sealer.NewEmail(t), email), name, roleKey)...).Scan(&id); err != nil {
		t.Fatal(err)
	}
	parsed, err := domain.ParseUserID(id)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func callWorkspace(t *testing.T, mux http.Handler, method, path, session string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return call(mux, method, path, session, raw)
}

func workspacePayload(overrides map[string]any) map[string]any {
	payload := map[string]any{
		"name":                    "Workspace",
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
	}
	maps.Copy(payload, overrides)
	return payload
}

func mustParseToken(t *testing.T, text string) domain.Token {
	t.Helper()
	tok, err := domain.ParseToken(text)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}
