//go:build integration

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/brainops"
	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
	"github.com/upmo-ai/upmo/internal/testutil/sealer"
)

// The link-issued events name the event alone: who got the link is already in
// the audit trail, and the log must not carry the address.
func TestLinkIssuedLogsCarryNoEmailAddress(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	for schema, files := range map[string]fs.FS{audit.Schema: audit.Migrations(), identity.Schema: identity.Migrations()} {
		if err := migrate.Up(ctx, owner, schema, files); err != nil {
			t.Fatal(err)
		}
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions := identity.Sessions(owner, identity.IdleTimeout, identity.MaxAge)
	passkeys, err := identity.Passkeys(owner, sealer.NewEmail(t), sessions, "http://localhost:5173", "localhost", log)
	if err != nil {
		t.Fatal(err)
	}
	members := identity.Members(owner, sealer.NewEmail(t), "http://localhost:5173", brainops.LinkMail(config.SMTP{}), log)

	logs := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	if err := bootstrap(ctx, passkeys, []string{"--email", "founder@example.com", "--name", "Founder"}); err != nil {
		t.Fatal(err)
	}
	if err := recover(ctx, passkeys, members, []string{"--email", "founder@example.com"}); err != nil {
		t.Fatal(err)
	}

	out := logs.String()
	if !strings.Contains(out, "bootstrap link issued") || !strings.Contains(out, "recover link issued") {
		t.Fatalf("log = %s, want both link-issued events", out)
	}
	if strings.Contains(out, "founder@example.com") {
		t.Fatalf("log = %s, want no address", out)
	}
}

func TestMigrateCommandAppliesAllSchemasAndAgents(t *testing.T) {
	t.Setenv("UPMO_ENV", "development")
	owner := pgtest.New(t)
	u, err := url.Parse(owner.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + owner.Config().ConnConfig.Database
	t.Setenv("UPMO_DATABASE_URL", u.String())
	if err := command("migrate", nil); err != nil {
		t.Fatalf("command(migrate) = %v", err)
	}
	ctx := context.Background()
	supported, err := brainops.SupportedSchemaVersions()
	if err != nil {
		t.Fatalf("SupportedSchemaVersions: %v", err)
	}
	wantSchemas := []string{
		"audit", "identity", "memory", "knowledge", "tasks", "chat",
		"decisions", "artifacts", "runtime", "gateway", "notices", "platform", "agents",
	}
	if len(supported) != len(wantSchemas) {
		t.Fatalf("SupportedSchemaVersions count = %d, want %d: %v", len(supported), len(wantSchemas), supported)
	}
	for _, s := range wantSchemas {
		if supported[s] != 1 {
			t.Fatalf("supported[%s] = %d, want 1", s, supported[s])
		}
	}

	versions, err := migrate.SchemaVersions(ctx, owner)
	if err != nil {
		t.Fatalf("SchemaVersions: %v", err)
	}
	if len(versions) != len(wantSchemas) {
		t.Fatalf("applied schemas = %d, want %d: %v", len(versions), len(wantSchemas), versions)
	}
	for _, s := range wantSchemas {
		if versions[s] != 1 {
			t.Fatalf("applied schema[%s] = %d, want 1", s, versions[s])
		}
	}

	if err := audit.Migrated(ctx, owner); err != nil {
		t.Fatalf("audit.Migrated: %v", err)
	}
	var migratedChanges string
	if err := owner.QueryRow(ctx, `SELECT changes::text FROM audit.pending WHERE action = 'system.migrated'`).Scan(&migratedChanges); err != nil {
		t.Fatalf("read audit.pending system.migrated: %v", err)
	}
	for _, s := range wantSchemas {
		if !strings.Contains(migratedChanges, fmt.Sprintf("%s=1", s)) {
			t.Fatalf("system.migrated missing %s=1: %s", s, migratedChanges)
		}
	}

	for _, obj := range []string{
		"gateway.agent_bundles",
		"gateway.task_tokens",
		"identity.principals",
		"chat.agents",
		"memory.lessons",
		"runtime.seats",
		"gateway.provider_account_teams",
	} {
		var reg *string
		if err := owner.QueryRow(ctx, `SELECT to_regclass($1)::text`, obj).Scan(&reg); err != nil {
			t.Fatal(err)
		}
		if reg != nil {
			t.Fatalf("%s still exists after migrate", obj)
		}
	}
	var alertProc *string
	if err := owner.QueryRow(ctx, `SELECT to_regprocedure('gateway.cache_hit_alert(integer, integer)')::text`).Scan(&alertProc); err != nil {
		t.Fatal(err)
	}
	if alertProc != nil {
		t.Fatalf("gateway.cache_hit_alert still exists: %q", *alertProc)
	}
	for _, present := range []string{"runtime.task_sandboxes", "agents.lessons", "agents.agents"} {
		var reg *string
		if err := owner.QueryRow(ctx, `SELECT to_regclass($1)::text`, present).Scan(&reg); err != nil {
			t.Fatal(err)
		}
		if reg == nil {
			t.Fatalf("%s was not created by migrate", present)
		}
	}
	var lessonCol int
	if err := owner.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'memory' AND table_name = 'facts' AND column_name = 'lesson'`).Scan(&lessonCol); err != nil {
		t.Fatal(err)
	}
	if lessonCol != 0 {
		t.Fatal("memory.facts still has lesson column")
	}
	var budgetCols int
	if err := owner.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'tasks' AND table_name = 'tasks'
		  AND column_name IN ('budget', 'held', 'spent', 'budget_micro_usd', 'held_micro_usd', 'spent_micro_usd')`).Scan(&budgetCols); err != nil {
		t.Fatal(err)
	}
	if budgetCols != 6 {
		t.Fatalf("tasks.tasks budget columns = %d, want 6", budgetCols)
	}
	var tplBudgetCols int
	if err := owner.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_schema = 'tasks' AND table_name = 'templates' AND column_name = 'budget'`).Scan(&tplBudgetCols); err != nil {
		t.Fatal(err)
	}
	if tplBudgetCols != 1 {
		t.Fatalf("tasks.templates budget columns = %d, want 1", tplBudgetCols)
	}

	var isSuper, bypassRLS bool
	if err := owner.QueryRow(ctx, `SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = 'upmo_app'`).Scan(&isSuper, &bypassRLS); err != nil {
		t.Fatal(err)
	}
	if isSuper || bypassRLS {
		t.Fatalf("upmo_app flags super=%v bypassrls=%v, want false/false", isSuper, bypassRLS)
	}
	var ownedTables int
	if err := owner.QueryRow(ctx, `
		SELECT count(*) FROM pg_tables
		WHERE tableowner = 'upmo_app' AND schemaname = ANY($1)`, wantSchemas).Scan(&ownedTables); err != nil {
		t.Fatal(err)
	}
	if ownedTables != 0 {
		t.Fatalf("upmo_app owns %d app tables, want 0", ownedTables)
	}

	for _, tbl := range []string{"identity.provision_nonces", "audit.push_cursor", "platform.river_migration"} {
		for _, priv := range []string{"SELECT", "INSERT", "UPDATE", "DELETE"} {
			var has bool
			if err := owner.QueryRow(ctx, `SELECT has_table_privilege('upmo_app', $1, $2)`, tbl, priv).Scan(&has); err != nil {
				t.Fatal(err)
			}
			if has {
				t.Fatalf("upmo_app has %s on %s, want false", priv, tbl)
			}
		}
	}
	for _, s := range wantSchemas {
		var has bool
		if err := owner.QueryRow(ctx, `SELECT has_table_privilege('upmo_app', $1 || '.goose_db_version', 'SELECT')`, s).Scan(&has); err != nil {
			t.Fatal(err)
		}
		if has {
			t.Fatalf("upmo_app has SELECT on %s.goose_db_version, want false", s)
		}
	}
	for _, check := range []struct {
		tbl, col string
		want     bool
	}{
		{"identity.workspace", "name", true},
		{"identity.workspace", "spend_ceiling_micro_usd", false},
		{"gateway.provider_accounts", "name", true},
		{"gateway.provider_accounts", "pricing_provider", false},
		{"gateway.provider_accounts", "pricing_models", false},
		{"agents.agents", "name", true},
		{"agents.agents", "pack_version", false},
	} {
		var has bool
		if err := owner.QueryRow(ctx, `SELECT has_column_privilege('upmo_app', $1, $2, 'UPDATE')`, check.tbl, check.col).Scan(&has); err != nil {
			t.Fatal(err)
		}
		if has != check.want {
			t.Fatalf("upmo_app UPDATE on %s.%s = %v, want %v", check.tbl, check.col, has, check.want)
		}
	}
}
