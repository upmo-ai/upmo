//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/platform/notices"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
	"github.com/upmo-ai/upmo/internal/testutil/workerkeys"
)

// dispatchPool is the pool gateway runs on in production: a real upmo_gateway
// login, which is neither the table owner nor BYPASSRLS.
func dispatchPool(t *testing.T, owner *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	return pgtest.Login(t, owner, "upmo_gateway")
}

const noticeManagerID = "22222222-2222-7222-8222-222222222222"

func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// TestGatewayRoleGrants: with no upmo_ settings at all, upmo_gateway reads the
// house credential, account and model, records a ledger row and a status,
// and may change nothing else. upmo_app still may not write the ledger.
func TestGatewayRoleGrants(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := migrate.Up(ctx, owner, gateway.Schema, gateway.Migrations()); err != nil {
		t.Fatal(err)
	}
	var accountID string
	if err := owner.QueryRow(ctx, `
		INSERT INTO gateway.provider_accounts (name, adapter, credential_ref, plan_kind, house, visibility)
		VALUES ('local', 'fake', 'house', 'local', true, 'everyone') RETURNING id::text`).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO gateway.provider_models (account_id, model, position, listed) VALUES ($1::uuid, 'stub', 1, true)`, accountID); err != nil {
		t.Fatal(err)
	}
	sealed := []byte("sealed-house-key")
	if _, err := owner.Exec(ctx, `
		INSERT INTO gateway.credentials (ref, ciphertext, nonce, key_id, wrapped_key, wrap_nonce)
		VALUES ('house', $1, $2, 'k1', $3, $2)`, sealed, bytes.Repeat([]byte{7}, 12), []byte("wrapped-house-key")); err != nil {
		t.Fatal(err)
	}
	gw := dispatchPool(t, owner)
	store := postgres.Store{Pool: gw}

	got, err := store.SealedCredential(ctx, "house")
	if err != nil || !bytes.Equal(got.Ciphertext, sealed) || got.KeyID != "k1" {
		t.Fatalf("SealedCredential(house) = %+v %v", got, err)
	}
	if err := store.Record(ctx, domain.Entry{
		Route: "draft", LinkPosition: 1, AccountID: accountID, Model: "stub", PlanKind: "local",
		Class: domain.ClassOK, Principal: domain.Principal{Source: domain.SourceSystem},
	}); err != nil {
		t.Fatalf("ledger insert as upmo_gateway: %v", err)
	}
	// Settle reads hold_id; the unpriced-model notice filters and orders on
	// the rest. Nothing else on the ledger is readable to gateway.
	var tableSelect bool
	var ledgerCols string
	if err := owner.QueryRow(ctx, `
		SELECT EXISTS (
		         SELECT 1 FROM information_schema.table_privileges
		         WHERE grantee = 'upmo_gateway' AND table_schema = 'gateway'
		           AND table_name = 'ledger' AND privilege_type = 'SELECT'),
		       (SELECT string_agg(column_name, ',' ORDER BY column_name)
		        FROM information_schema.column_privileges
		        WHERE grantee = 'upmo_gateway' AND table_schema = 'gateway'
		          AND table_name = 'ledger' AND privilege_type = 'SELECT')`).Scan(&tableSelect, &ledgerCols); err != nil {
		t.Fatal(err)
	}
	if tableSelect || ledgerCols != "at,hold_id,id,model,price_source,provider_account_id,service_tier,unknown_notified_at" {
		t.Fatalf("upmo_gateway ledger SELECT: table=%t columns=%q", tableSelect, ledgerCols)
	}
	if _, err := gw.Exec(ctx, `SELECT cost_micro_usd FROM gateway.ledger`); sqlState(err) != "42501" {
		t.Fatalf("SELECT cost_micro_usd as upmo_gateway = %v, want 42501", err)
	}
	// Those columns are enough for the two statements: the settled-hold probe
	// and the unpriced-model notice, which marks the newest unknown row.
	var settled bool
	if err := gw.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM gateway.ledger WHERE hold_id = $1::uuid)`, uuid.NewString()).Scan(&settled); err != nil || settled {
		t.Fatalf("settled-hold probe as upmo_gateway = %t %v", settled, err)
	}
	if err := store.Record(ctx, domain.Entry{
		Route: "draft", LinkPosition: 1, AccountID: accountID, Model: "stub", PlanKind: "metered",
		Class: domain.ClassOK, Principal: domain.Principal{Source: domain.SourceSystem}, PriceSource: "unknown",
	}); err != nil {
		t.Fatalf("unknown-price ledger insert as upmo_gateway: %v", err)
	}
	var notified []string
	notifier := postgres.EstimateNotifier{
		Pool:             gw,
		SettingsManagers: func(context.Context) ([]string, error) { return []string{noticeManagerID}, nil },
		Notify: func(_ context.Context, _ pgx.Tx, userID, _ string) error {
			notified = append(notified, userID)
			return nil
		},
	}
	if err := notifier.NotifyEstimatedModel(ctx, domain.Account{ID: accountID}, "stub"); err != nil {
		t.Fatalf("NotifyEstimatedModel as upmo_gateway: %v", err)
	}
	var marked int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM gateway.ledger WHERE unknown_notified_at IS NOT NULL AND price_source = 'unknown'`).Scan(&marked); err != nil ||
		marked != 1 || len(notified) != 1 || notified[0] != noticeManagerID {
		t.Fatalf("notice marked %d rows, notified %v %v; want 1 row and [%s]", marked, notified, err, noticeManagerID)
	}
	if _, err := gw.Exec(ctx, `UPDATE gateway.provider_accounts SET name = 'x'`); sqlState(err) != "42501" {
		t.Fatalf("UPDATE name as upmo_gateway = %v, want 42501", err)
	}
	tag, err := gw.Exec(ctx, `UPDATE gateway.provider_accounts SET status = 'Connected', checked_at = now() WHERE credential_ref = 'house'`)
	if err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("UPDATE status as upmo_gateway = %v %v", tag, err)
	}
	var gotAccount, gotModel string
	if err := gw.QueryRow(ctx, `
		SELECT a.id::text, m.model FROM gateway.provider_accounts AS a
		JOIN gateway.provider_models AS m ON m.account_id = a.id WHERE a.house`).Scan(&gotAccount, &gotModel); err != nil || gotAccount != accountID || gotModel != "stub" {
		t.Fatalf("house account and model = %q %q %v", gotAccount, gotModel, err)
	}
	rows, err := gw.Query(ctx, `SELECT identity.member_providers_allowed()`)
	if err != nil {
		t.Fatal(err)
	}
	fields := rows.FieldDescriptions()
	var allowed bool
	for rows.Next() {
		if err := rows.Scan(&allowed); err != nil {
			t.Fatal(err)
		}
	}
	rows.Close()
	if rows.Err() != nil || len(fields) != 1 || fields[0].Name != "member_providers_allowed" || !allowed {
		t.Fatalf("member_providers_allowed = %t fields %v %v", allowed, fields, rows.Err())
	}
	var serving, dispatching, bypass, create bool
	if err := gw.QueryRow(ctx, `
		SELECT gateway.serving(), gateway.dispatching(),
		       (SELECT rolbypassrls FROM pg_roles WHERE rolname = current_user),
		       has_schema_privilege(current_user, 'gateway', 'CREATE')`).Scan(&serving, &dispatching, &bypass, &create); err != nil ||
		serving || !dispatching || bypass || create {
		t.Fatalf("serving=%t dispatching=%t bypassrls=%t create=%t %v", serving, dispatching, bypass, create, err)
	}
	// Gateway reads a credential's payload, never its wrapped data key: only
	// the worker unwraps.
	var ref string
	var ciphertext, nonce []byte
	var keyID string
	if err := gw.QueryRow(ctx, `SELECT ref, ciphertext, nonce, key_id FROM gateway.credentials`).Scan(&ref, &ciphertext, &nonce, &keyID); err != nil || ref != "house" {
		t.Fatalf("upmo_gateway SELECT ref, ciphertext, nonce, key_id = %q %v", ref, err)
	}
	for _, stmt := range []string{
		`SELECT wrapped_key FROM gateway.credentials`,
		`SELECT wrap_nonce FROM gateway.credentials`,
		`UPDATE gateway.credentials SET key_id = 'k2'`,
		`SELECT * FROM gateway.sandbox_credential_refs`,
		`SELECT email FROM identity.users`,
	} {
		if _, err := gw.Exec(ctx, stmt); sqlState(err) != "42501" {
			t.Errorf("upmo_gateway %q = %v, want 42501", stmt, err)
		}
	}
	app := pgtest.AppLogin(t, owner)
	if _, err := app.Exec(ctx, `
		INSERT INTO gateway.ledger (route, link_position, model, plan_kind, failure_class, source)
		VALUES ('draft', 1, 'stub', 'local', 'ok', 'system')`); sqlState(err) != "42501" {
		t.Fatalf("ledger insert as upmo_app = %v, want 42501", err)
	}
}

// identity.teams_of returns a person's team ids and nothing about anyone.
func TestTeamsOfReturnsTeamIDsOnly(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := migrate.Up(ctx, owner, gateway.Schema, gateway.Migrations()); err != nil {
		t.Fatal(err)
	}
	var person, team string
	if err := owner.QueryRow(ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
		VALUES ('ada@example.com', sha256('ada@example.com'::bytea), 'k1', 'Ada', (SELECT id FROM identity.roles WHERE key = 'owner'), now())
		RETURNING id::text`).Scan(&person); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `INSERT INTO identity.teams (name) VALUES ('Team T') RETURNING id::text`).Scan(&team); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO identity.team_members (team_id, user_id) VALUES ($1::uuid, $2::uuid)`, team, person); err != nil {
		t.Fatal(err)
	}
	gw := dispatchPool(t, owner)
	rows, err := gw.Query(ctx, `SELECT identity.teams_of($1::uuid)::text[]`, person)
	if err != nil {
		t.Fatal(err)
	}
	fields := rows.FieldDescriptions()
	var teams []string
	for rows.Next() {
		if err := rows.Scan(&teams); err != nil {
			t.Fatal(err)
		}
	}
	rows.Close()
	if len(fields) != 1 || fields[0].Name == "email" || len(teams) != 1 || teams[0] != team {
		t.Fatalf("teams_of = %v fields %v, want [%s]", teams, fields, team)
	}
	if got, err := (postgres.Store{Pool: gw}).TeamsOf(ctx, person); err != nil || len(got) != 1 || got[0] != team {
		t.Fatalf("Store.TeamsOf = %v %v", got, err)
	}
	if _, err := gw.Exec(ctx, `SELECT * FROM identity.team_members`); sqlState(err) != "42501" {
		t.Fatalf("upmo_gateway SELECT team_members = %v, want 42501", err)
	}
}

// The unpriced-model notice goes only to a settings manager, through the
// definer that is the gateway role's one notice write.
func TestGatewayNoticeOnlyForSettingsManagers(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := migrate.Up(ctx, owner, gateway.Schema, gateway.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := notices.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	var manager, member string
	for _, p := range []struct {
		into        *string
		email, role string
	}{{&manager, "owner@example.com", "owner"}, {&member, "member@example.com", "member"}} {
		if err := owner.QueryRow(ctx, `
			INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
			VALUES ($1, sha256(convert_to($2, 'UTF8')), 'k1', 'P', (SELECT id FROM identity.roles WHERE key = $3), now())
			RETURNING id::text`, p.email, p.email, p.role).Scan(p.into); err != nil {
			t.Fatal(err)
		}
	}
	gw := dispatchPool(t, owner)
	managers, err := gateway.SchemaVersions(ctx, gw)
	if err != nil || managers["gateway"] != 1 || managers["identity"] != 1 {
		t.Fatalf("schema versions as upmo_gateway = %v %v", managers, err)
	}
	insert := func(id string) error {
		tx, err := gw.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		uid, err := uuid.Parse(id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := notices.InsertForManagerTx(ctx, tx, uid, notices.KindFailed, "Provider local: model m price is unknown"); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if err := insert(manager); err != nil {
		t.Fatalf("notice for a settings manager: %v", err)
	}
	if err := insert(member); sqlState(err) != "42501" {
		t.Fatalf("notice for a member = %v, want 42501", err)
	}
	var n int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM notices.notifications`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("notifications = %d %v, want 1", n, err)
	}
	if _, err := gw.Exec(ctx, `SELECT * FROM notices.notifications`); sqlState(err) != "42501" {
		t.Fatalf("upmo_gateway SELECT notifications = %v, want 42501", err)
	}
}

func gatewayEnv(t *testing.T, dbURL string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	t.Setenv("UPMO_ENV", "development")
	t.Setenv("UPMO_DATABASE_GATEWAY_URL", dbURL)
	t.Setenv("UPMO_PUBLIC_URL", "http://127.0.0.1:8082")
	t.Setenv("UPMO_LISTEN_ADDR", addr)
	t.Setenv("UPMO_GATEWAY_KEY", mustMaster(t))
	// A worker nobody listens on: a test that gets past the pool checks
	// starts a real one and points these at it.
	t.Setenv("UPMO_WORKER_URL", "http://127.0.0.1:1")
	t.Setenv("UPMO_WORKER_GATEWAY_KEY", svcsig.KeyPrefix+"worker-gateway_"+base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{'w'}, 32)))
	return addr
}

func ownerURL(t *testing.T, owner *pgxpool.Pool) string {
	t.Helper()
	u, err := url.Parse(owner.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + owner.Config().ConnConfig.Database
	return u.String()
}

// Gateway refuses the owner URL: a superuser would ignore every policy.
func TestGatewayStartupRefusesOwnerURL(t *testing.T) {
	owner := pgtest.New(t)
	if err := migrate.Up(context.Background(), owner, gateway.Schema, gateway.Migrations()); err != nil {
		t.Fatal(err)
	}
	gatewayEnv(t, ownerURL(t, owner))
	done := make(chan error, 1)
	go func() { done <- run() }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "BYPASSRLS") || !strings.Contains(err.Error(), "UPMO_DATABASE_GATEWAY_URL") {
			t.Fatalf("run with the owner URL = %v, want a refusal naming BYPASSRLS", err)
		}
	case <-time.After(20 * time.Second):
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		<-done
		t.Fatal("gateway started on the owner URL")
	}
}

// Gateway starts as upmo_gateway, which has no CREATE on schema gateway, and
// serves /readyz: it runs no migration of its own.
func TestGatewayStartsAsUpmoGatewayWithoutMigrating(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := migrate.Up(ctx, owner, gateway.Schema, gateway.Migrations()); err != nil {
		t.Fatal(err)
	}
	var create bool
	if err := owner.QueryRow(ctx, `SELECT has_schema_privilege('upmo_gateway', 'gateway', 'CREATE')`).Scan(&create); err != nil || create {
		t.Fatalf("upmo_gateway CREATE on gateway = %t %v", create, err)
	}
	addr := gatewayEnv(t, pgtest.LoginURL(t, owner, "upmo_gateway"))
	// The real entry point checks the worker, signed, before it serves.
	ring, err := gateway.KeyringFromEnv(func(name string) (string, bool) {
		if name == gateway.EnvMasterKey {
			return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'1'}, 32)), true
		}
		return "", false
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	worker := workerkeys.Start(t, owner, ring)
	t.Setenv("UPMO_WORKER_URL", worker.URL)
	t.Setenv("UPMO_WORKER_GATEWAY_KEY", worker.Key[svcsig.AudienceWorkerGateway])
	done := make(chan error, 1)
	go func() { done <- run() }()
	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case err := <-done:
			t.Fatalf("gateway stopped: %v", err)
		default:
		}
		resp, err := http.Get("http://" + addr + "/readyz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("gateway never served /readyz: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	// run stops on SIGTERM, which its own signal context catches.
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("gateway shutdown: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("gateway did not stop on SIGTERM")
	}
}
