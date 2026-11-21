//go:build integration

package brain

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/brainops"
	"github.com/upmo-ai/upmo/internal/tasks"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

const (
	limOwner      = "40000000-0000-7000-8000-000000000001"
	limMemberA    = "40000000-0000-7000-8000-000000000002"
	limMemberB    = "40000000-0000-7000-8000-000000000003"
	limViewCustom = "40000000-0000-7000-8000-000000000004"
	limManageRole = "40000000-0000-7000-8000-000000000005"
	limDisabled   = "40000000-0000-7000-8000-000000000006"
	limAgent      = "40000000-0000-7000-8000-000000000099"
)

type limitRig struct {
	t            *testing.T
	owner        *pgxpool.Pool
	app          *pgxpool.Pool
	appLogin     *pgxpool.Pool
	handler      http.Handler
	automationID string
	now          time.Time
	seal         func() []audit.Row
}

func newLimitRig(t *testing.T) *limitRig {
	t.Helper()
	ctx := context.Background()
	pool := pgtest.New(t)
	if err := brainops.MigrateAll(ctx, pool); err != nil {
		t.Fatal(err)
	}
	appPool := pgtest.App(t, pool)
	appLogin := pgtest.AppLogin(t, pool)
	members := identity.Members(appPool, nil, "http://example.test", nil, nil)

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO identity.roles (name, permissions) VALUES ('UsageViewer', '{usage:view}')`)
	exec(`INSERT INTO identity.roles (name, permissions) VALUES ('LimitsManager', '{settings:manage}')`)
	for _, p := range []struct {
		id, email, role string
		disabled        bool
	}{
		{limOwner, "lim-owner@example.com", "owner", false},
		{limMemberA, "lim-a@example.com", "member", false},
		{limMemberB, "lim-b@example.com", "member", false},
		{limViewCustom, "lim-view@example.com", "UsageViewer", false},
		{limManageRole, "lim-mgr@example.com", "LimitsManager", false},
		{limDisabled, "lim-off@example.com", "member", true},
	} {
		if p.disabled {
			exec(`
				INSERT INTO identity.users (id, email, email_hash, email_hash_key_id, name, role_id, joined_at, disabled_at)
				VALUES ($1::uuid, $2, sha256(convert_to($2, 'UTF8')), 'k1', $2, (SELECT id FROM identity.roles WHERE key = $3 OR (key IS NULL AND name = $3)), now(), now())`,
				p.id, p.email, p.role)
		} else {
			exec(`
				INSERT INTO identity.users (id, email, email_hash, email_hash_key_id, name, role_id, joined_at)
				VALUES ($1::uuid, $2, sha256(convert_to($2, 'UTF8')), 'k1', $2, (SELECT id FROM identity.roles WHERE key = $3 OR (key IS NULL AND name = $3)), now())`,
				p.id, p.email, p.role)
		}
	}

	var tplID, autoID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO tasks.templates (owner_user_id, goal, agent_id, budget)
		VALUES ($1::uuid, 'Nightly summary', $2::uuid, 100)
		RETURNING id::text`, limMemberA, limAgent).Scan(&tplID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO tasks.automations (owner_user_id, template_id, kind, spec)
		VALUES ($1::uuid, $2::uuid, 'cron', '0 9 * * *')
		RETURNING id::text`, limMemberA, tplID).Scan(&autoID); err != nil {
		t.Fatal(err)
	}

	rig := &limitRig{
		t:            t,
		owner:        pool,
		app:          appPool,
		appLogin:     appLogin,
		automationID: autoID,
		now:          time.Date(2026, 12, 1, 3, 30, 0, 0, time.UTC),
	}

	svc := gateway.NewUsageLimits(appPool, nil)
	svc.Allow = func(ctx context.Context, user string) (string, error) {
		id, err := identity.ParseUserID(user)
		if err != nil {
			return "", err
		}
		return members.EffectiveAllow(ctx, id)
	}
	svc.TimeZone = func(ctx context.Context) (string, error) {
		return identity.WorkspaceTimeZone(ctx, pool)
	}
	svc.PersonExists = func(ctx context.Context, id string) (bool, error) {
		names, _, err := members.NamesOf(ctx, []string{id}, nil)
		if err != nil {
			return false, err
		}
		return names[id] != "", nil
	}
	svc.AutomationExists = func(ctx context.Context, id string) (bool, error) {
		return tasks.AutomationExists(ctx, appPool, id)
	}
	svc.Now = func() time.Time { return rig.now }

	mux := http.NewServeMux()
	gateway.UsageLimitsHandler{
		Service: svc,
		Access: func(r *http.Request) (string, bool) {
			u := r.Header.Get("X-Test-User")
			if u == "" {
				return "", false
			}
			audit.WithActor(r.Context(), audit.Actor{Type: "user", ID: u, CredentialType: "session"})
			return u, true
		},
	}.MountLimits(mux, nil)

	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	rig.handler = audit.Middleware(audit.Routes(), mux, appPool, "brain", nil, discard)(mux)

	var lastPos int64
	rig.seal = func() []audit.Row {
		t.Helper()
		if _, err := audit.SealOnce(ctx, pool, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		rows, err := pool.Query(ctx, `
			SELECT id::text, occurred_at, service, action, outcome, reason,
			       actor_type, COALESCE(actor_id::text, ''), COALESCE(actor_role_id::text, ''),
			       credential_type, COALESCE(credential_id::text, ''),
			       target_type, target_id, changes, position
			FROM audit.events
			WHERE position > $1
			ORDER BY position`, lastPos)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []audit.Row
		for rows.Next() {
			var row audit.Row
			var changes []byte
			var pos int64
			if err := rows.Scan(
				&row.ID, &row.OccurredAt, &row.Service, &row.Action, &row.Outcome, &row.Reason,
				&row.ActorType, &row.ActorID, &row.ActorRoleID, &row.CredentialType, &row.CredentialID,
				&row.TargetType, &row.TargetID, &changes, &pos,
			); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(changes, &row.Changes); err != nil {
				t.Fatal(err)
			}
			lastPos = pos
			out = append(out, row)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}

	// Drain the migration audit event so tests assert only their own events.
	rig.seal()
	return rig
}

func (r *limitRig) do(method, user, body string) *httptest.ResponseRecorder {
	r.t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "/api/usage/limits", reader)
	if user != "" {
		req.Header.Set("X-Test-User", user)
	}
	rec := httptest.NewRecorder()
	r.handler.ServeHTTP(rec, req)
	return rec
}

func assertSQLDenied(t *testing.T, label string, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("%s: err = %v, want SQLSTATE 42501", label, err)
	}
}

func TestFeature_UPM357_MemberWithoutSettingsManageCannotChangeLimits(t *testing.T) {
	rig := newLimitRig(t)
	ctx := context.Background()

	// Seed an existing person limit for Member A and a workspace limit.
	if _, err := rig.owner.Exec(ctx, `
		INSERT INTO gateway.usage_limits (kind, subject_id, amount_micro_usd)
		VALUES ('workspace', NULL, 10000), ('person', $1::uuid, 5000)`, limMemberA); err != nil {
		t.Fatal(err)
	}

	// Route tests: member without settings:manage (and custom role with usage:view only)
	// cannot set, update, or remove any limit, including their own.
	for _, tc := range []struct {
		name string
		user string
		body string
	}{
		{"member own person limit", limMemberA, `{"kind":"person","subject_id":"` + limMemberA + `","amount_micro_usd":9999}`},
		{"member remove own person limit", limMemberA, `{"kind":"person","subject_id":"` + limMemberA + `","amount_micro_usd":null}`},
		{"member other person limit", limMemberA, `{"kind":"person","subject_id":"` + limMemberB + `","amount_micro_usd":100}`},
		{"member workspace limit", limMemberA, `{"kind":"workspace","amount_micro_usd":99999}`},
		{"member person_default limit", limMemberA, `{"kind":"person_default","amount_micro_usd":1000}`},
		{"member automation limit", limMemberA, `{"kind":"automation","subject_id":"` + rig.automationID + `","amount_micro_usd":1000}`},
		{"member unknown person limit", limMemberA, `{"kind":"person","subject_id":"40000000-0000-7000-8000-999999999999","amount_micro_usd":1000}`},
		{"member unknown automation limit", limMemberA, `{"kind":"automation","subject_id":"40000000-0000-7000-8000-999999999999","amount_micro_usd":1000}`},
		{"usage:view without settings:manage", limViewCustom, `{"kind":"person","subject_id":"` + limViewCustom + `","amount_micro_usd":9999}`},
	} {
		rec := rig.do(http.MethodPut, tc.user, tc.body)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s: status = %d %s, want 403", tc.name, rec.Code, rec.Body.String())
		}
		events := rig.seal()
		if len(events) != 1 || events[0].Action != audit.UsageLimitUpdate || events[0].Outcome != audit.Denied || events[0].ActorID != tc.user {
			t.Fatalf("%s: audit events = %+v, want 1 denied %s", tc.name, events, audit.UsageLimitUpdate)
		}
	}

	// Direct DB writes and gateway.usage_limit_set under upmo_app without
	// settings:manage are blocked.
	for _, allow := range []string{"", "usage:view"} {
		tx, err := rig.appLogin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tx.Rollback(ctx) })
		if _, err := tx.Exec(ctx, `SELECT set_config('upmo.user_id', $1, true), set_config('upmo.allow', $2, true)`, limMemberA, allow); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SAVEPOINT sp`); err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO gateway.usage_limits (kind, subject_id, amount_micro_usd) VALUES ('person', $1::uuid, 123)`, limMemberB)
		assertSQLDenied(t, "direct insert person allow="+allow, err)
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT sp`); err != nil {
			t.Fatal(err)
		}

		_, err = tx.Exec(ctx, `INSERT INTO gateway.usage_limits (kind, subject_id, amount_micro_usd) VALUES ('workspace', NULL, 123)`)
		assertSQLDenied(t, "direct insert workspace allow="+allow, err)
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT sp`); err != nil {
			t.Fatal(err)
		}

		_, err = tx.Exec(ctx, `UPDATE gateway.usage_limits SET amount_micro_usd = 9999 WHERE kind = 'person' AND subject_id = $1::uuid`, limMemberA)
		assertSQLDenied(t, "direct update own person allow="+allow, err)
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT sp`); err != nil {
			t.Fatal(err)
		}

		_, err = tx.Exec(ctx, `DELETE FROM gateway.usage_limits WHERE kind = 'person' AND subject_id = $1::uuid`, limMemberA)
		assertSQLDenied(t, "direct delete own person allow="+allow, err)
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT sp`); err != nil {
			t.Fatal(err)
		}

		_, err = tx.Exec(ctx, `SELECT gateway.usage_limit_set('person', $1::uuid, 9999)`, limMemberA)
		assertSQLDenied(t, "usage_limit_set own person allow="+allow, err)
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT sp`); err != nil {
			t.Fatal(err)
		}

		_, err = tx.Exec(ctx, `SELECT gateway.usage_limit_set('workspace', NULL, 9999)`)
		assertSQLDenied(t, "usage_limit_set workspace allow="+allow, err)
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT sp`); err != nil {
			t.Fatal(err)
		}

		_, err = tx.Exec(ctx, `SELECT gateway.usage_limit_set('person', $1::uuid, NULL)`, limMemberA)
		assertSQLDenied(t, "usage_limit_set delete own person allow="+allow, err)
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT sp`); err != nil {
			t.Fatal(err)
		}
		_ = tx.Rollback(ctx)
	}

	// Calling gateway.usage_limit_set with settings:manage but without upmo.user_id is blocked.
	txNoUser, err := rig.appLogin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = txNoUser.Rollback(ctx) })
	if _, err := txNoUser.Exec(ctx, `SELECT set_config('upmo.allow', 'settings:manage', true)`); err != nil {
		t.Fatal(err)
	}
	_, err = txNoUser.Exec(ctx, `SELECT gateway.usage_limit_set('workspace', NULL, 9999)`)
	assertSQLDenied(t, "usage_limit_set without user_id", err)
	_ = txNoUser.Rollback(ctx)

	// Stored amounts remain untouched.
	var ownAmt int64
	if err := rig.owner.QueryRow(ctx, `SELECT amount_micro_usd FROM gateway.usage_limits WHERE kind = 'person' AND subject_id = $1::uuid`, limMemberA).Scan(&ownAmt); err != nil || ownAmt != 5000 {
		t.Fatalf("member A limit = %d (%v), want 5000", ownAmt, err)
	}
}

func TestFeature_UPM357_NobodyInWorkspaceCanChangeCeiling(t *testing.T) {
	rig := newLimitRig(t)
	ctx := context.Background()

	// Seed control-plane ceiling as owner.
	if _, err := rig.owner.Exec(ctx, `
		INSERT INTO gateway.usage_limits (kind, subject_id, amount_micro_usd)
		VALUES ('ceiling', NULL, 25000), ('workspace', NULL, 10000)`); err != nil {
		t.Fatal(err)
	}

	// Route tests for an owner: setting or clearing kind = "ceiling" is refused (403).
	for _, body := range []string{
		`{"kind":"ceiling","amount_micro_usd":99999}`,
		`{"kind":"ceiling","amount_micro_usd":null}`,
	} {
		rec := rig.do(http.MethodPut, limOwner, body)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("owner PUT ceiling (%s) = %d %s, want 403", body, rec.Code, rec.Body.String())
		}
		events := rig.seal()
		if len(events) != 1 || events[0].Action != audit.UsageLimitUpdate || events[0].Outcome != audit.Denied || events[0].ActorID != limOwner {
			t.Fatalf("owner PUT ceiling audit = %+v, want 1 denied", events)
		}
	}

	// Direct DB writes and gateway.usage_limit_set under upmo_app as owner
	// (holding settings:manage,usage:view) cannot insert, update, delete, or
	// retarget a row to ceiling, or set an unknown kind.
	tx, err := rig.appLogin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	if _, err := tx.Exec(ctx, `SELECT set_config('upmo.user_id', $1, true), set_config('upmo.allow', 'settings:manage,usage:view', true)`, limOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SAVEPOINT sp`); err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO gateway.usage_limits (kind, subject_id, amount_micro_usd) VALUES ('ceiling', NULL, 99999)`)
	assertSQLDenied(t, "direct insert ceiling as owner", err)
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT sp`); err != nil {
		t.Fatal(err)
	}

	_, err = tx.Exec(ctx, `UPDATE gateway.usage_limits SET kind = 'ceiling' WHERE kind = 'workspace'`)
	assertSQLDenied(t, "direct retarget workspace to ceiling as owner", err)
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT sp`); err != nil {
		t.Fatal(err)
	}

	_, err = tx.Exec(ctx, `UPDATE gateway.usage_limits SET amount_micro_usd = 99999 WHERE kind = 'ceiling'`)
	assertSQLDenied(t, "direct update ceiling as owner", err)
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT sp`); err != nil {
		t.Fatal(err)
	}

	_, err = tx.Exec(ctx, `DELETE FROM gateway.usage_limits WHERE kind = 'ceiling'`)
	assertSQLDenied(t, "direct delete ceiling as owner", err)
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT sp`); err != nil {
		t.Fatal(err)
	}

	_, err = tx.Exec(ctx, `SELECT gateway.usage_limit_set('ceiling', NULL, 99999)`)
	assertSQLDenied(t, "usage_limit_set ceiling as owner", err)
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT sp`); err != nil {
		t.Fatal(err)
	}

	_, err = tx.Exec(ctx, `SELECT gateway.usage_limit_set('ceiling', NULL, NULL)`)
	assertSQLDenied(t, "usage_limit_set delete ceiling as owner", err)
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT sp`); err != nil {
		t.Fatal(err)
	}

	_, err = tx.Exec(ctx, `SELECT gateway.usage_limit_set('team', NULL, 99999)`)
	assertSQLDenied(t, "usage_limit_set unknown kind as owner", err)
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT sp`); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		label   string
		kind    string
		subject any
	}{
		{"usage_limit_set workspace with subject", "workspace", limMemberA},
		{"usage_limit_set person_default with subject", "person_default", limMemberA},
		{"usage_limit_set person without subject", "person", nil},
		{"usage_limit_set automation without subject", "automation", nil},
	} {
		_, err = tx.Exec(ctx, `SELECT gateway.usage_limit_set($1, $2::uuid, 9999)`, tc.kind, tc.subject)
		assertSQLDenied(t, tc.label, err)
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT sp`); err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(ctx, `SELECT gateway.usage_limit_set($1, $2::uuid, NULL)`, tc.kind, tc.subject)
		assertSQLDenied(t, tc.label+" (remove)", err)
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT sp`); err != nil {
			t.Fatal(err)
		}
	}
	_ = tx.Rollback(ctx)

	var ceiling int64
	if err := rig.owner.QueryRow(ctx, `SELECT amount_micro_usd FROM gateway.usage_limits WHERE kind = 'ceiling'`).Scan(&ceiling); err != nil || ceiling != 25000 {
		t.Fatalf("ceiling = %d (%v), want 25000", ceiling, err)
	}
}

func TestFeature_UPM357_AppSQLSelectEnforcesUsageViewAndAutomationExists(t *testing.T) {
	rig := newLimitRig(t)
	ctx := context.Background()

	if _, err := rig.owner.Exec(ctx, `
		INSERT INTO gateway.usage_limits (kind, subject_id, amount_micro_usd) VALUES
		  ('workspace', NULL, 10000),
		  ('ceiling', NULL, 5000),
		  ('person_default', NULL, 2000),
		  ('person', $1::uuid, 3000),
		  ('person', $2::uuid, 4000),
		  ('automation', $3::uuid, 1500)`,
		limMemberA, limMemberB, rig.automationID); err != nil {
		t.Fatal(err)
	}

	readVisibleKeys := func(userID, allow string) map[string]bool {
		t.Helper()
		tx, err := rig.appLogin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tx.Rollback(ctx) })
		if _, err := tx.Exec(ctx, `SELECT set_config('upmo.user_id', $1, true), set_config('upmo.allow', $2, true)`, userID, allow); err != nil {
			t.Fatal(err)
		}
		rows, err := tx.Query(ctx, `SELECT kind, COALESCE(subject_id::text, '') FROM gateway.usage_limits`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		keys := map[string]bool{}
		for rows.Next() {
			var kind, subject string
			if err := rows.Scan(&kind, &subject); err != nil {
				t.Fatal(err)
			}
			keys[kind+":"+subject] = true
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		_ = tx.Rollback(ctx)
		return keys
	}

	// Under upmo_app with settings:manage only, Member A sees workspace,
	// person_default, ceiling, and own person row — never Member B or automation.
	manageKeys := readVisibleKeys(limMemberA, "settings:manage")
	for _, want := range []string{"workspace:", "ceiling:", "person_default:", "person:" + limMemberA} {
		if !manageKeys[want] {
			t.Fatalf("settings:manage SELECT missing %s in %+v", want, manageKeys)
		}
	}
	for _, hidden := range []string{"person:" + limMemberB, "automation:" + rig.automationID} {
		if manageKeys[hidden] {
			t.Fatalf("settings:manage SELECT leaked %s in %+v", hidden, manageKeys)
		}
	}

	// With usage:view, SELECT returns every row.
	viewKeys := readVisibleKeys(limMemberA, "usage:view")
	if len(viewKeys) != 6 {
		t.Fatalf("usage:view SELECT rows = %+v, want 6", viewKeys)
	}
	for _, want := range []string{
		"workspace:", "ceiling:", "person_default:",
		"person:" + limMemberA, "person:" + limMemberB, "automation:" + rig.automationID,
	} {
		if !viewKeys[want] {
			t.Fatalf("usage:view SELECT missing %s in %+v", want, viewKeys)
		}
	}

	// upmo_app can call tasks.automation_exists across owners, but cannot SELECT
	// another owner's automation from tasks.automations.
	txAuto, err := rig.appLogin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = txAuto.Rollback(ctx) })
	if _, err := txAuto.Exec(ctx, `SELECT set_config('upmo.user_id', $1, true), set_config('upmo.allow', 'settings:manage', true)`, limManageRole); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err := txAuto.QueryRow(ctx, `SELECT tasks.automation_exists($1::uuid)`, rig.automationID).Scan(&exists); err != nil || !exists {
		t.Fatalf("tasks.automation_exists(known) = %v (%v), want true", exists, err)
	}
	if err := txAuto.QueryRow(ctx, `SELECT tasks.automation_exists($1::uuid)`, "40000000-0000-7000-8000-999999999999").Scan(&exists); err != nil || exists {
		t.Fatalf("tasks.automation_exists(unknown) = %v (%v), want false", exists, err)
	}
	var autoCount int
	if err := txAuto.QueryRow(ctx, `SELECT count(*) FROM tasks.automations WHERE id = $1::uuid`, rig.automationID).Scan(&autoCount); err != nil || autoCount != 0 {
		t.Fatalf("direct SELECT tasks.automations count = %d (%v), want 0", autoCount, err)
	}
	var probe string
	if err := txAuto.QueryRow(ctx, `SELECT COALESCE(current_setting('upmo.automation_probe', true), '')`).Scan(&probe); err != nil || probe != "" {
		t.Fatalf("upmo.automation_probe after tasks.automation_exists = %q (%v), want empty", probe, err)
	}
	_ = txAuto.Rollback(ctx)

	// When the owner pool is not a superuser or BYPASSRLS, direct SELECT on
	// tasks.automations without upmo.automation_probe sees 0 rows. Also verify
	// under an explicit NOSUPERUSER NOBYPASSRLS table owner role (even when the
	// test rig's default owner is a superuser).
	var ownerSuper, ownerBypass bool
	if err := rig.owner.QueryRow(ctx, `SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&ownerSuper, &ownerBypass); err != nil {
		t.Fatal(err)
	}
	if !ownerSuper && !ownerBypass {
		var ownerVisible int
		if err := rig.owner.QueryRow(ctx, `SELECT count(*) FROM tasks.automations`).Scan(&ownerVisible); err != nil || ownerVisible != 0 {
			t.Fatalf("owner pool SELECT tasks.automations without probe = %d (%v), want 0", ownerVisible, err)
		}
	}

	var roleBuf [6]byte
	if _, err := rand.Read(roleBuf[:]); err != nil {
		t.Fatal(err)
	}
	autoOwnerRole := pgx.Identifier{"upm357_auto_owner_" + hex.EncodeToString(roleBuf[:])}.Sanitize()
	if _, err := rig.owner.Exec(ctx, `CREATE ROLE `+autoOwnerRole+` NOSUPERUSER NOBYPASSRLS NOLOGIN`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, stmt := range []string{`REASSIGN OWNED BY ` + autoOwnerRole + ` TO CURRENT_USER`, `DROP OWNED BY ` + autoOwnerRole, `DROP ROLE IF EXISTS ` + autoOwnerRole} {
			_, _ = rig.owner.Exec(context.Background(), stmt)
		}
	})
	for _, stmt := range []string{
		`ALTER TABLE tasks.automations OWNER TO ` + autoOwnerRole,
		`ALTER FUNCTION tasks.automation_exists(uuid) OWNER TO ` + autoOwnerRole,
		`GRANT USAGE ON SCHEMA tasks TO ` + autoOwnerRole,
	} {
		if _, err := rig.owner.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	txOwner, err := rig.owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = txOwner.Rollback(ctx) })
	if _, err := txOwner.Exec(ctx, `SET LOCAL ROLE `+autoOwnerRole); err != nil {
		t.Fatal(err)
	}
	var ownerRoleVisible int
	if err := txOwner.QueryRow(ctx, `SELECT count(*) FROM tasks.automations`).Scan(&ownerRoleVisible); err != nil || ownerRoleVisible != 0 {
		t.Fatalf("table owner SELECT tasks.automations without probe = %d (%v), want 0", ownerRoleVisible, err)
	}
	_ = txOwner.Rollback(ctx)

	txAppOwned, err := rig.appLogin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = txAppOwned.Rollback(ctx) })
	if _, err := txAppOwned.Exec(ctx, `SELECT set_config('upmo.user_id', $1, true), set_config('upmo.allow', 'settings:manage', true)`, limManageRole); err != nil {
		t.Fatal(err)
	}
	if err := txAppOwned.QueryRow(ctx, `SELECT tasks.automation_exists($1::uuid)`, rig.automationID).Scan(&exists); err != nil || !exists {
		t.Fatalf("tasks.automation_exists(known) with non-superuser owner = %v (%v), want true", exists, err)
	}
	if err := txAppOwned.QueryRow(ctx, `SELECT tasks.automation_exists($1::uuid)`, "40000000-0000-7000-8000-999999999999").Scan(&exists); err != nil || exists {
		t.Fatalf("tasks.automation_exists(unknown) with non-superuser owner = %v (%v), want false", exists, err)
	}
	_ = txAppOwned.Rollback(ctx)
}

func TestFeature_UPM357_GetShowsOwnLimitsWithoutUsageViewAndEverythingWithIt(t *testing.T) {
	rig := newLimitRig(t)
	ctx := context.Background()

	// Set workspace time zone to America/New_York: 2026-12-01 03:30 UTC is still
	// 2026-11-30 22:30 in New York, so the current period is 2026-11-01.
	if _, err := rig.owner.Exec(ctx, `UPDATE identity.workspace SET time_zone = 'America/New_York' WHERE id = true`); err != nil {
		t.Fatal(err)
	}
	if _, err := rig.owner.Exec(ctx, `
		INSERT INTO gateway.usage_limits (kind, subject_id, amount_micro_usd) VALUES
		  ('workspace', NULL, 10000),
		  ('ceiling', NULL, 5000),
		  ('person_default', NULL, 2000),
		  ('person', $1::uuid, 3000),
		  ('automation', $2::uuid, 1500)`,
		limMemberA, rig.automationID); err != nil {
		t.Fatal(err)
	}
	// Current period (2026-11-01) and previous period (2026-10-01) spend rows.
	if _, err := rig.owner.Exec(ctx, `
		INSERT INTO gateway.usage_spend (kind, subject_id, period_start, spent_micro_usd, held_micro_usd) VALUES
		  ('workspace', NULL, '2026-11-01', 900, 100),
		  ('person', $1::uuid, '2026-11-01', 400, 50),
		  ('person', $2::uuid, '2026-11-01', 250, 25),
		  ('automation', $3::uuid, '2026-11-01', 300, 0),
		  ('person', $1::uuid, '2026-10-01', 9999, 888)`,
		limMemberA, limMemberB, rig.automationID); err != nil {
		t.Fatal(err)
	}

	// Member A (has explicit person row 3000, no usage:view): sees own person limit (3000),
	// effective workspace limit (min(10000, 5000) = 5000), own current-period spend/held (400/50), and NO rows key.
	recA := rig.do(http.MethodGet, limMemberA, "")
	if recA.Code != http.StatusOK {
		t.Fatalf("member A GET = %d %s", recA.Code, recA.Body.String())
	}
	var rawA map[string]any
	if err := json.Unmarshal(recA.Body.Bytes(), &rawA); err != nil {
		t.Fatal(err)
	}
	if _, hasRows := rawA["rows"]; hasRows {
		t.Fatalf("member A without usage:view saw rows: %s", recA.Body.String())
	}
	var viewA gateway.UsageLimitsView
	if err := json.Unmarshal(recA.Body.Bytes(), &viewA); err != nil {
		t.Fatal(err)
	}
	if viewA.PeriodStart != "2026-11-01" ||
		viewA.Own.LimitMicroUSD == nil || *viewA.Own.LimitMicroUSD != 3000 ||
		viewA.Own.WorkspaceLimitMicroUSD == nil || *viewA.Own.WorkspaceLimitMicroUSD != 5000 ||
		viewA.Own.SpentMicroUSD != 400 || viewA.Own.HeldMicroUSD != 50 {
		t.Fatalf("member A view = %+v", viewA)
	}

	// Member B (no explicit person row -> falls back to person_default 2000, no usage:view).
	recB := rig.do(http.MethodGet, limMemberB, "")
	if recB.Code != http.StatusOK {
		t.Fatalf("member B GET = %d %s", recB.Code, recB.Body.String())
	}
	var viewB gateway.UsageLimitsView
	if err := json.Unmarshal(recB.Body.Bytes(), &viewB); err != nil {
		t.Fatal(err)
	}
	if viewB.Rows != nil ||
		viewB.Own.LimitMicroUSD == nil || *viewB.Own.LimitMicroUSD != 2000 ||
		viewB.Own.WorkspaceLimitMicroUSD == nil || *viewB.Own.WorkspaceLimitMicroUSD != 5000 ||
		viewB.Own.SpentMicroUSD != 250 || viewB.Own.HeldMicroUSD != 25 {
		t.Fatalf("member B view = %+v", viewB)
	}

	// Custom role with settings:manage ONLY (without usage:view) still sees only own limits.
	recMgr := rig.do(http.MethodGet, limManageRole, "")
	if recMgr.Code != http.StatusOK {
		t.Fatalf("settings:manage GET = %d %s", recMgr.Code, recMgr.Body.String())
	}
	var viewMgr gateway.UsageLimitsView
	if err := json.Unmarshal(recMgr.Body.Bytes(), &viewMgr); err != nil {
		t.Fatal(err)
	}
	if viewMgr.Rows != nil {
		t.Fatalf("settings:manage without usage:view saw rows: %+v", *viewMgr.Rows)
	}

	// Custom role with usage:view (and owner) sees every row for the current period.
	for _, viewer := range []string{limViewCustom, limOwner} {
		recV := rig.do(http.MethodGet, viewer, "")
		if recV.Code != http.StatusOK {
			t.Fatalf("%s GET = %d %s", viewer, recV.Code, recV.Body.String())
		}
		var viewV gateway.UsageLimitsView
		if err := json.Unmarshal(recV.Body.Bytes(), &viewV); err != nil {
			t.Fatal(err)
		}
		if viewV.Rows == nil || len(*viewV.Rows) != 6 {
			t.Fatalf("%s rows = %v, want 6 rows (workspace, ceiling, person_default, 2 people, automation)", viewer, viewV.Rows)
		}
		byKey := map[string]gateway.UsageLimitRow{}
		for _, row := range *viewV.Rows {
			byKey[row.Kind+":"+row.SubjectID] = row
		}
		checkRow := func(key string, wantAmt *int64, wantSpent, wantHeld int64) {
			t.Helper()
			got, ok := byKey[key]
			if !ok {
				t.Fatalf("%s missing row %s in %+v", viewer, key, *viewV.Rows)
			}
			if (got.AmountMicroUSD == nil) != (wantAmt == nil) || (got.AmountMicroUSD != nil && *got.AmountMicroUSD != *wantAmt) {
				t.Fatalf("%s %s amount = %v, want %v", viewer, key, got.AmountMicroUSD, wantAmt)
			}
			if got.SpentMicroUSD != wantSpent || got.HeldMicroUSD != wantHeld {
				t.Fatalf("%s %s spend/held = %d/%d, want %d/%d", viewer, key, got.SpentMicroUSD, got.HeldMicroUSD, wantSpent, wantHeld)
			}
		}
		p := func(v int64) *int64 { return &v }
		checkRow("workspace:", p(10000), 900, 100)
		checkRow("ceiling:", p(5000), 0, 0)
		checkRow("person_default:", p(2000), 0, 0)
		checkRow("person:"+limMemberA, p(3000), 400, 50)
		checkRow("person:"+limMemberB, nil, 250, 25)
		checkRow("automation:"+rig.automationID, p(1500), 300, 0)
	}
}

func TestFeature_UPM357_PutSetChangeRemoveAuditAndValidation(t *testing.T) {
	rig := newLimitRig(t)
	ctx := context.Background()

	checkAmountChange := func(ev audit.Row, wantTarget string, wantFrom, wantTo *float64) {
		t.Helper()
		if ev.Action != audit.UsageLimitUpdate || ev.Outcome != audit.Success || ev.TargetType != "usage_limit" || ev.TargetID != wantTarget {
			t.Fatalf("event = %+v, want %s success on %s", ev, audit.UsageLimitUpdate, wantTarget)
		}
		ch, ok := ev.Changes.Values["amount_micro_usd"]
		if !ok {
			t.Fatalf("missing amount_micro_usd change in %+v", ev.Changes)
		}
		checkSide := func(label string, got any, want *float64) {
			if want == nil {
				if got != nil {
					t.Fatalf("%s %s = %#v, want nil", wantTarget, label, got)
				}
				return
			}
			n, ok := got.(float64)
			if !ok || n != *want {
				t.Fatalf("%s %s = %#v, want %v", wantTarget, label, got, *want)
			}
		}
		checkSide("from", ch.From, wantFrom)
		checkSide("to", ch.To, wantTo)
	}
	f := func(v float64) *float64 { return &v }

	// 1. Set workspace limit (nil -> 10000) writes 1 audit row.
	rec := rig.do(http.MethodPut, limManageRole, `{"kind":"workspace","amount_micro_usd":10000}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("set workspace = %d %s", rec.Code, rec.Body.String())
	}
	events := rig.seal()
	if len(events) != 1 {
		t.Fatalf("set workspace events = %d, want 1", len(events))
	}
	checkAmountChange(events[0], "workspace", nil, f(10000))

	// 2. Repeat PUT with same amount writes 0 audit rows (NoChange).
	rec = rig.do(http.MethodPut, limManageRole, `{"kind":"workspace","amount_micro_usd":10000}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("repeat workspace = %d %s", rec.Code, rec.Body.String())
	}
	if events := rig.seal(); len(events) != 0 {
		t.Fatalf("repeat workspace events = %+v, want 0", events)
	}

	// 3. Change workspace limit to 0 (10000 -> 0) writes 1 audit row.
	rec = rig.do(http.MethodPut, limManageRole, `{"kind":"workspace","amount_micro_usd":0}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("change workspace = %d %s", rec.Code, rec.Body.String())
	}
	events = rig.seal()
	if len(events) != 1 {
		t.Fatalf("change workspace events = %d, want 1", len(events))
	}
	checkAmountChange(events[0], "workspace", f(10000), f(0))

	// 4. Remove workspace limit (0 -> null) deletes row and writes 1 audit row.
	rec = rig.do(http.MethodPut, limManageRole, `{"kind":"workspace","amount_micro_usd":null}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("remove workspace = %d %s", rec.Code, rec.Body.String())
	}
	events = rig.seal()
	if len(events) != 1 {
		t.Fatalf("remove workspace events = %d, want 1", len(events))
	}
	checkAmountChange(events[0], "workspace", f(0), nil)
	var wsRows int
	if err := rig.owner.QueryRow(ctx, `SELECT count(*) FROM gateway.usage_limits WHERE kind = 'workspace'`).Scan(&wsRows); err != nil || wsRows != 0 {
		t.Fatalf("workspace rows after remove = %d (%v), want 0", wsRows, err)
	}

	// 5. Repeat remove writes 0 audit rows.
	rec = rig.do(http.MethodPut, limManageRole, `{"kind":"workspace","amount_micro_usd":null}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("repeat remove workspace = %d %s", rec.Code, rec.Body.String())
	}
	if events := rig.seal(); len(events) != 0 {
		t.Fatalf("repeat remove workspace events = %+v, want 0", events)
	}

	// 6. Set, change, remove person_default, person, and automation limits.
	for _, tc := range []struct {
		kind, subject, target string
	}{
		{"person_default", "", "person_default"},
		{"person", limMemberA, "person:" + limMemberA},
		{"automation", rig.automationID, "automation:" + rig.automationID},
	} {
		subj := ""
		if tc.subject != "" {
			subj = `,"subject_id":"` + tc.subject + `"`
		}
		rec = rig.do(http.MethodPut, limOwner, `{"kind":"`+tc.kind+`"`+subj+`,"amount_micro_usd":2500}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("set %s = %d %s", tc.kind, rec.Code, rec.Body.String())
		}
		events = rig.seal()
		if len(events) != 1 {
			t.Fatalf("set %s events = %d, want 1", tc.kind, len(events))
		}
		checkAmountChange(events[0], tc.target, nil, f(2500))

		rec = rig.do(http.MethodPut, limOwner, `{"kind":"`+tc.kind+`"`+subj+`,"amount_micro_usd":3500}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("change %s = %d %s", tc.kind, rec.Code, rec.Body.String())
		}
		events = rig.seal()
		if len(events) != 1 {
			t.Fatalf("change %s events = %d, want 1", tc.kind, len(events))
		}
		checkAmountChange(events[0], tc.target, f(2500), f(3500))

		rec = rig.do(http.MethodPut, limOwner, `{"kind":"`+tc.kind+`"`+subj+`,"amount_micro_usd":null}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("remove %s = %d %s", tc.kind, rec.Code, rec.Body.String())
		}
		events = rig.seal()
		if len(events) != 1 {
			t.Fatalf("remove %s events = %d, want 1", tc.kind, len(events))
		}
		checkAmountChange(events[0], tc.target, f(3500), nil)
	}

	// 7. Validation refusals write no audit events and no DB rows.
	unknownUUID := "40000000-0000-7000-8000-999999999999"
	for _, tc := range []struct {
		name string
		body string
	}{
		{"negative amount", `{"kind":"workspace","amount_micro_usd":-1}`},
		{"unknown person", `{"kind":"person","subject_id":"` + unknownUUID + `","amount_micro_usd":500}`},
		{"disabled person", `{"kind":"person","subject_id":"` + limDisabled + `","amount_micro_usd":500}`},
		{"unknown automation", `{"kind":"automation","subject_id":"` + unknownUUID + `","amount_micro_usd":500}`},
		{"workspace with subject_id", `{"kind":"workspace","subject_id":"` + limMemberA + `","amount_micro_usd":500}`},
		{"person_default with subject_id", `{"kind":"person_default","subject_id":"` + limMemberA + `","amount_micro_usd":500}`},
		{"person without subject_id", `{"kind":"person","amount_micro_usd":500}`},
		{"automation without subject_id", `{"kind":"automation","amount_micro_usd":500}`},
		{"unknown kind", `{"kind":"team","subject_id":"` + unknownUUID + `","amount_micro_usd":500}`},
	} {
		rec = rig.do(http.MethodPut, limOwner, tc.body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status = %d %s, want 422", tc.name, rec.Code, rec.Body.String())
		}
		if events := rig.seal(); len(events) != 0 {
			t.Fatalf("%s: events = %+v, want 0", tc.name, events)
		}
	}

	var totalRows int
	if err := rig.owner.QueryRow(ctx, `SELECT count(*) FROM gateway.usage_limits`).Scan(&totalRows); err != nil || totalRows != 0 {
		t.Fatalf("usage_limits rows = %d (%v), want 0", totalRows, err)
	}
}
