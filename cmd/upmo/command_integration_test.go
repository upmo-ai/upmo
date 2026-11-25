//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"slices"
	"sort"
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
	var gotChanges audit.Changes
	if err := json.Unmarshal([]byte(migratedChanges), &gotChanges); err != nil {
		t.Fatalf("unmarshal system.migrated changes: %v", err)
	}
	const wantMigratedVersions = "agents=1 artifacts=1 audit=1 chat=1 decisions=1 gateway=1 identity=1 knowledge=1 memory=1 notices=1 platform=1 runtime=1 tasks=1"
	if got := gotChanges.Values["versions"].To; got != wantMigratedVersions {
		t.Fatalf("system.migrated versions = %v, want %q", got, wantMigratedVersions)
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

	type rolePrivs struct {
		Table, Select, Insert, Update []string
	}
	wantPrivs := map[string]rolePrivs{
		"agents.agents": {
			Table:  []string{"DELETE", "INSERT", "SELECT"},
			Select: []string{"check_criteria", "connector_ids", "created_at", "default_model_id", "default_model_provider", "default_route", "description", "enabled", "id", "installed", "instructions", "knowledge_folder_ids", "name", "owner_user_id", "pack_version", "plan_approval", "role", "slug", "team_ids", "tool_classes", "updated_at", "version", "visibility"},
			Insert: []string{"check_criteria", "connector_ids", "created_at", "default_model_id", "default_model_provider", "default_route", "description", "enabled", "id", "installed", "instructions", "knowledge_folder_ids", "name", "owner_user_id", "pack_version", "plan_approval", "role", "slug", "team_ids", "tool_classes", "updated_at", "version", "visibility"},
			Update: []string{"check_criteria", "connector_ids", "default_model_id", "default_model_provider", "default_route", "description", "enabled", "instructions", "knowledge_folder_ids", "name", "plan_approval", "role", "slug", "team_ids", "tool_classes", "updated_at", "version", "visibility"},
		},
		"agents.goose_db_version": {},
		"agents.lessons": {
			Table:  []string{"DELETE", "INSERT", "SELECT"},
			Select: []string{"agent_id", "always_inline", "body", "cue", "id", "title", "updated_at"},
			Insert: []string{"agent_id", "always_inline", "body", "cue", "id", "title", "updated_at"},
			Update: []string{"always_inline", "body", "cue", "title", "updated_at"},
		},
		"agents.versions": {
			Table:  []string{"INSERT", "SELECT"},
			Select: []string{"agent_id", "created_at", "created_by", "digest", "snapshot", "version"},
			Insert: []string{"agent_id", "created_at", "created_by", "digest", "snapshot", "version"},
		},
		"artifacts.artifacts": {
			Table:  []string{"DELETE", "INSERT", "SELECT"},
			Select: []string{"created_at", "id", "kind", "mime", "name", "object_key", "object_type", "owner_user_id", "size_bytes", "store", "task_id", "visibility", "visibility_team_id"},
			Insert: []string{"created_at", "id", "kind", "mime", "name", "object_key", "object_type", "owner_user_id", "size_bytes", "store", "task_id", "visibility", "visibility_team_id"},
		},
		"artifacts.goose_db_version": {},
		"audit.anchors":              {},
		"audit.events": {
			Table:  []string{"SELECT"},
			Select: []string{"action", "actor_id", "actor_role_id", "actor_type", "changes", "credential_id", "credential_type", "hash", "id", "occurred_at", "outcome", "position", "prev_hash", "reason", "request_id", "sealed_at", "service", "source_ip", "target_id", "target_type", "user_agent"},
		},
		"audit.events_default":   {},
		"audit.events_y2026m11":  {},
		"audit.events_y2026m12":  {},
		"audit.events_y2027m01":  {},
		"audit.goose_db_version": {},
		"audit.head":             {},
		"audit.pending": {
			Table:  []string{"INSERT"},
			Insert: []string{"action", "actor_id", "actor_role_id", "actor_type", "changes", "credential_id", "credential_type", "id", "occurred_at", "outcome", "reason", "request_id", "service", "source_ip", "target_id", "target_type", "user_agent"},
		},
		"audit.push_cursor": {},
		"chat.action_logs": {
			Table:  []string{"INSERT", "SELECT"},
			Select: []string{"action", "args_digest", "call_id", "chat_id", "completed_at", "dry_run", "error_text", "executed_at", "line_id", "preview", "response_digest", "scope_id", "state", "tool_name", "untrusted_origin", "user_id", "user_line_id"},
			Insert: []string{"action", "args_digest", "call_id", "chat_id", "completed_at", "dry_run", "error_text", "executed_at", "line_id", "preview", "response_digest", "scope_id", "state", "tool_name", "untrusted_origin", "user_id", "user_line_id"},
			Update: []string{"completed_at", "error_text", "response_digest", "state"},
		},
		"chat.chats": {
			Table:  []string{"DELETE", "INSERT", "SELECT"},
			Select: []string{"created_at", "id", "object_type", "owner_user_id", "private_read", "rolling_summary", "summary_up_to_line_id", "summary_up_to_seq", "tainted", "title", "visibility", "visibility_team_id"},
			Insert: []string{"created_at", "id", "object_type", "owner_user_id", "private_read", "rolling_summary", "summary_up_to_line_id", "summary_up_to_seq", "tainted", "title", "visibility", "visibility_team_id"},
			Update: []string{"id", "object_type", "title", "visibility", "visibility_team_id"},
		},
		"chat.deltas": {
			Table:  []string{"DELETE", "INSERT", "SELECT"},
			Select: []string{"at", "body", "chat_id", "id", "kind", "line_id", "seq"},
			Insert: []string{"at", "body", "chat_id", "id", "kind", "line_id", "seq"},
		},
		"chat.findings": {
			Table:  []string{"INSERT", "SELECT"},
			Select: []string{"author_id", "body", "chat_id", "created_at", "id", "kind", "line_id", "private_read", "source_ref", "title", "untrusted"},
			Insert: []string{"author_id", "body", "chat_id", "created_at", "id", "kind", "line_id", "private_read", "source_ref", "title", "untrusted"},
		},
		"chat.goose_db_version": {},
		"chat.holds": {
			Table:  []string{"INSERT", "SELECT"},
			Select: []string{"action", "calls", "chat_id", "created_at", "decided_at", "hold_turn_seq", "id", "line_id", "preview", "reason", "state", "untrusted_origin", "user_id"},
			Insert: []string{"action", "calls", "chat_id", "created_at", "decided_at", "hold_turn_seq", "id", "line_id", "preview", "reason", "state", "untrusted_origin", "user_id"},
			Update: []string{"decided_at", "state"},
		},
		"chat.lines": {
			Table:  []string{"INSERT", "SELECT"},
			Select: []string{"agent_id", "at", "attachments", "attempts", "author_id", "body", "body_tsv", "chat_id", "cost_micro_usd", "feed_id", "id", "model", "pin_account_id", "pin_model", "reply_to", "role", "route", "seq", "status", "tokens"},
			Insert: []string{"agent_id", "at", "attachments", "attempts", "author_id", "body", "body_tsv", "chat_id", "cost_micro_usd", "feed_id", "id", "model", "pin_account_id", "pin_model", "reply_to", "role", "route", "seq", "status", "tokens"},
		},
		"chat.runs": {
			Table:  []string{"INSERT", "SELECT"},
			Select: []string{"author_id", "chat_id", "claim", "claim_round", "cost_known", "cost_micro_usd", "created_at", "finished_at", "history", "id", "line_id", "model", "round", "round_seq", "state", "tokens"},
			Insert: []string{"author_id", "chat_id", "claim", "claim_round", "cost_known", "cost_micro_usd", "created_at", "finished_at", "history", "id", "line_id", "model", "round", "round_seq", "state", "tokens"},
			Update: []string{"claim", "claim_round", "cost_known", "cost_micro_usd", "created_at", "finished_at", "history", "model", "round", "round_seq", "state", "tokens"},
		},
		"chat.tool_allowances": {
			Table:  []string{"DELETE", "INSERT", "SELECT"},
			Select: []string{"chat_id", "created_at", "tool", "user_id"},
			Insert: []string{"chat_id", "created_at", "tool", "user_id"},
		},
		"chat.turns": {
			Table:  []string{"INSERT", "SELECT"},
			Select: []string{"author_id", "body", "chat_id", "cost_micro_usd", "created_at", "id", "input_tokens", "is_error", "line_id", "model", "offloaded", "output_tokens", "private_read", "role", "seq", "tainted", "tool_calls", "tool_id", "tool_name"},
			Insert: []string{"author_id", "body", "chat_id", "cost_micro_usd", "created_at", "id", "input_tokens", "is_error", "line_id", "model", "offloaded", "output_tokens", "private_read", "role", "seq", "tainted", "tool_calls", "tool_id", "tool_name"},
		},
		"decisions.gates": {
			Table:  []string{"INSERT", "SELECT", "UPDATE"},
			Select: []string{"bar", "fingerprint", "hit", "kind", "route", "total", "updated_at", "verdict"},
			Insert: []string{"bar", "fingerprint", "hit", "kind", "route", "total", "updated_at", "verdict"},
			Update: []string{"bar", "fingerprint", "hit", "kind", "route", "total", "updated_at", "verdict"},
		},
		"decisions.goose_db_version": {},
		"gateway.account_models_seen": {
			Table:  []string{"DELETE", "INSERT", "SELECT"},
			Select: []string{"account_id", "context_length", "created", "display_name", "modalities", "model_id", "position", "pricing", "seen_at"},
			Insert: []string{"account_id", "context_length", "created", "display_name", "modalities", "model_id", "position", "pricing", "seen_at"},
		},
		"gateway.catalog_meta": {},
		"gateway.credentials": {
			Table:  []string{"DELETE", "INSERT", "SELECT"},
			Select: []string{"ciphertext", "created_at", "key_id", "nonce", "ref"},
			Insert: []string{"ciphertext", "created_at", "key_id", "nonce", "ref"},
			Update: []string{"ciphertext", "created_at", "key_id", "nonce"},
		},
		"gateway.goose_db_version": {},
		"gateway.ledger": {
			Table:  []string{"SELECT"},
			Select: []string{"agent_id", "at", "automation_id", "cache_read", "cache_write", "cache_write_1h", "charged_to_limit", "cost_micro_usd", "estimated", "failure_class", "hold_id", "id", "input_tokens", "latency_ms", "link_position", "list_cost_micro_usd", "model", "output_tokens", "owner_team_id", "parent_run_id", "plan_kind", "price_source", "price_version", "provider_account_id", "reasoning", "route", "run_id", "service_tier", "source", "source_id", "unknown_notified_at", "usage_arrived", "user_id"},
		},
		"gateway.price_overrides": {
			Table:  []string{"DELETE", "INSERT", "SELECT", "UPDATE"},
			Select: []string{"account_id", "cache_read_micro_usd_per_mtok", "cache_write_micro_usd_per_mtok", "input_micro_usd_per_mtok", "model", "output_micro_usd_per_mtok", "updated_at", "updated_by"},
			Insert: []string{"account_id", "cache_read_micro_usd_per_mtok", "cache_write_micro_usd_per_mtok", "input_micro_usd_per_mtok", "model", "output_micro_usd_per_mtok", "updated_at", "updated_by"},
			Update: []string{"account_id", "cache_read_micro_usd_per_mtok", "cache_write_micro_usd_per_mtok", "input_micro_usd_per_mtok", "model", "output_micro_usd_per_mtok", "updated_at", "updated_by"},
		},
		"gateway.price_tiers": {
			Table:  []string{"SELECT"},
			Select: []string{"cache_read_usd_per_mtok", "cache_write_usd_per_mtok", "context_min_tokens", "input_usd_per_mtok", "model", "output_usd_per_mtok", "vendor"},
		},
		"gateway.prices": {
			Table:  []string{"SELECT"},
			Select: []string{"cache_read_usd_per_mtok", "cache_write_usd_per_mtok", "capabilities", "context_window", "input_usd_per_mtok", "inputs", "kind", "lifecycle", "max_output", "model", "output_usd_per_mtok", "outputs", "reasoning", "retires_at", "vendor", "version"},
		},
		"gateway.provider_accounts": {
			Table:  []string{"DELETE", "INSERT", "SELECT"},
			Select: []string{"adapter", "base_url", "checked_at", "credential_ref", "enabled", "house", "id", "key_changed_at", "key_changed_by", "key_hint", "name", "notified_causes", "owner_user_id", "plan_kind", "pricing_models", "pricing_provider", "provider", "status", "status_started_at", "team_ids", "updated_at", "visibility"},
			Insert: []string{"adapter", "base_url", "checked_at", "credential_ref", "enabled", "house", "id", "key_changed_at", "key_changed_by", "key_hint", "name", "notified_causes", "owner_user_id", "plan_kind", "pricing_models", "pricing_provider", "provider", "status", "status_started_at", "team_ids", "updated_at", "visibility"},
			Update: []string{"base_url", "checked_at", "enabled", "key_changed_at", "key_changed_by", "key_hint", "name", "plan_kind", "status", "status_started_at", "team_ids", "updated_at", "visibility"},
		},
		"gateway.provider_models": {
			Table:  []string{"DELETE", "INSERT", "SELECT"},
			Select: []string{"account_id", "listed", "misses", "model", "position", "retired"},
			Insert: []string{"account_id", "listed", "misses", "model", "position", "retired"},
		},
		"gateway.route_links": {
			Table:  []string{"DELETE", "INSERT", "SELECT", "UPDATE"},
			Select: []string{"id", "model", "position", "provider_account_id", "required_capabilities", "retry_budget", "route_id", "weight"},
			Insert: []string{"id", "model", "position", "provider_account_id", "required_capabilities", "retry_budget", "route_id", "weight"},
			Update: []string{"id", "model", "position", "provider_account_id", "required_capabilities", "retry_budget", "route_id", "weight"},
		},
		"gateway.routes": {
			Table:  []string{"DELETE", "INSERT", "SELECT"},
			Select: []string{"capability", "decision", "id", "installed", "name", "owner_user_id", "team_ids", "updated_at", "visibility"},
			Insert: []string{"capability", "decision", "id", "installed", "name", "owner_user_id", "team_ids", "updated_at", "visibility"},
			Update: []string{"capability", "decision", "name", "team_ids", "updated_at", "visibility"},
		},
		"gateway.tool_bundle_tools": {},
		"gateway.tool_bundles":      {},
		"gateway.usage_daily": {
			Table:  []string{"SELECT"},
			Select: []string{"agent_id", "cache_read", "cache_write", "cache_write_1h", "calls", "cost_micro_usd", "day", "errors", "estimated", "input_tokens", "list_cost_micro_usd", "model", "output_tokens", "owner_team_id", "plan_kind", "provider_account_id", "reasoning", "source", "unpriced_calls", "user_id"},
		},
		"gateway.usage_daily_state": {
			Table:  []string{"SELECT"},
			Select: []string{"id", "rolled_through", "time_zone", "updated_at"},
		},
		"gateway.usage_holds": {},
		"gateway.usage_limits": {
			Select: []string{"amount_micro_usd", "kind", "subject_id", "updated_at"},
		},
		"gateway.usage_spend": {
			Select: []string{"held_micro_usd", "kind", "period_start", "spent_micro_usd", "subject_id"},
		},
		"identity.api_tokens": {
			Table:  []string{"DELETE"},
			Select: []string{"created_at", "expires_at", "id", "last_used_at", "name", "scope", "user_id"},
			Update: []string{"last_used_at"},
		},
		"identity.connector_grants": {
			Table:  []string{"DELETE", "INSERT", "SELECT", "UPDATE"},
			Select: []string{"changes", "connector", "resource_ids", "resource_scope", "user_id"},
			Insert: []string{"changes", "connector", "resource_ids", "resource_scope", "user_id"},
			Update: []string{"changes", "connector", "resource_ids", "resource_scope", "user_id"},
		},
		"identity.goose_db_version": {},
		"identity.passkeys": {
			Table:  []string{"DELETE", "INSERT", "SELECT", "UPDATE"},
			Select: []string{"aaguid", "attestation", "backup_eligible", "backup_state", "created_at", "credential_id", "id", "last_used_at", "name", "public_key", "sign_count", "transports", "user_id"},
			Insert: []string{"aaguid", "attestation", "backup_eligible", "backup_state", "created_at", "credential_id", "id", "last_used_at", "name", "public_key", "sign_count", "transports", "user_id"},
			Update: []string{"aaguid", "attestation", "backup_eligible", "backup_state", "created_at", "credential_id", "id", "last_used_at", "name", "public_key", "sign_count", "transports", "user_id"},
		},
		"identity.provision_nonces": {},
		"identity.roles": {
			Table:  []string{"SELECT"},
			Select: []string{"id", "key", "name", "permissions"},
		},
		"identity.sessions": {
			Table:  []string{"DELETE", "INSERT"},
			Select: []string{"created_at", "expires_at", "id", "last_seen_at", "sign_in_method", "signed_in_at", "user_agent", "user_id"},
			Insert: []string{"created_at", "expires_at", "id", "last_seen_at", "sign_in_method", "signed_in_at", "token_hash", "user_agent", "user_id"},
			Update: []string{"last_seen_at", "sign_in_method", "signed_in_at"},
		},
		"identity.shares": {
			Table:  []string{"DELETE", "INSERT", "SELECT", "UPDATE"},
			Select: []string{"granted_by", "grantee_role_id", "id", "level", "object_id", "object_type", "team_id", "user_id"},
			Insert: []string{"granted_by", "grantee_role_id", "id", "level", "object_id", "object_type", "team_id", "user_id"},
			Update: []string{"granted_by", "grantee_role_id", "id", "level", "object_id", "object_type", "team_id", "user_id"},
		},
		"identity.sign_in_links": {
			Table:  []string{"DELETE", "INSERT", "UPDATE"},
			Select: []string{"expires_at", "id", "purpose", "used_at", "user_id"},
			Insert: []string{"expires_at", "id", "purpose", "token_hash", "used_at", "user_id"},
			Update: []string{"expires_at", "id", "purpose", "token_hash", "used_at", "user_id"},
		},
		"identity.team_members": {
			Table:  []string{"DELETE", "INSERT", "SELECT", "UPDATE"},
			Select: []string{"team_id", "user_id"},
			Insert: []string{"team_id", "user_id"},
			Update: []string{"team_id", "user_id"},
		},
		"identity.teams": {
			Table:  []string{"DELETE", "INSERT", "SELECT", "UPDATE"},
			Select: []string{"external_id", "id", "name"},
			Insert: []string{"external_id", "id", "name"},
			Update: []string{"external_id", "id", "name"},
		},
		"identity.throttle": {
			Table:  []string{"DELETE", "INSERT", "SELECT", "UPDATE"},
			Select: []string{"key", "tokens", "updated_at"},
			Insert: []string{"key", "tokens", "updated_at"},
			Update: []string{"key", "tokens", "updated_at"},
		},
		"identity.users": {
			Table:  []string{"SELECT"},
			Select: []string{"avatar_key", "disabled_at", "email", "email_hash", "email_hash_key_id", "external_id", "id", "invited_at", "joined_at", "locale", "name", "passkey_offered_at", "password_hash", "role_id", "time_zone", "webauthn_handle"},
			Update: []string{"joined_at", "passkey_offered_at", "password_hash", "webauthn_handle"},
		},
		"identity.webauthn_challenges": {
			Table:  []string{"DELETE", "INSERT", "UPDATE"},
			Select: []string{"expires_at", "purpose", "user_id"},
			Insert: []string{"expires_at", "purpose", "session", "token_hash", "user_id"},
			Update: []string{"expires_at", "purpose", "session", "token_hash", "user_id"},
		},
		"identity.workspace": {
			Table:  []string{"SELECT"},
			Select: []string{"allow_member_agents", "allow_member_connectors", "allow_member_providers", "allow_member_routes", "default_agent_id", "default_route", "id", "locale", "logo_url", "name", "spend_ceiling_micro_usd", "sso_required", "time_zone", "updated_at"},
			Update: []string{"allow_member_agents", "allow_member_connectors", "allow_member_providers", "allow_member_routes", "default_agent_id", "default_route", "locale", "logo_url", "name", "sso_required", "time_zone", "updated_at"},
		},
		"knowledge.chunks": {
			Table:  []string{"DELETE", "INSERT", "SELECT"},
			Select: []string{"body", "body_tsv", "context_prefix", "embedding", "embedding_model", "folder_id", "heading_path", "language", "prefix_tsv", "revision", "seq", "source_id", "trust"},
			Insert: []string{"body", "body_tsv", "context_prefix", "embedding", "embedding_model", "folder_id", "heading_path", "language", "prefix_tsv", "revision", "seq", "source_id", "trust"},
		},
		"knowledge.embed_models": {
			Table:  []string{"INSERT", "SELECT"},
			Select: []string{"model"},
			Insert: []string{"model"},
		},
		"knowledge.folders": {
			Table:  []string{"DELETE", "INSERT", "SELECT"},
			Select: []string{"id", "name", "object_type", "owner_user_id", "visibility", "visibility_team_id"},
			Insert: []string{"id", "name", "object_type", "owner_user_id", "visibility", "visibility_team_id"},
			Update: []string{"id", "name", "object_type", "visibility", "visibility_team_id"},
		},
		"knowledge.goose_db_version": {},
		"knowledge.sources": {
			Table:  []string{"DELETE", "INSERT", "SELECT"},
			Select: []string{"blob_key", "body", "error", "folder_id", "id", "kind", "language", "provenance", "revision", "sha256", "source_ref", "status", "title", "updated_at"},
			Insert: []string{"blob_key", "body", "error", "folder_id", "id", "kind", "language", "provenance", "revision", "sha256", "source_ref", "status", "title", "updated_at"},
			Update: []string{"blob_key", "body", "error", "kind", "language", "provenance", "revision", "sha256", "source_ref", "status", "title", "updated_at"},
		},
		"memory.embed_models": {
			Table:  []string{"DELETE", "INSERT", "SELECT"},
			Select: []string{"model", "user_id"},
			Insert: []string{"model", "user_id"},
		},
		"memory.extracts": {
			Table:  []string{"INSERT", "SELECT"},
			Select: []string{"created_at", "source_id", "source_kind", "source_seq", "user_id"},
			Insert: []string{"created_at", "source_id", "source_kind", "source_seq", "user_id"},
		},
		"memory.facts": {
			Table:  []string{"INSERT", "SELECT"},
			Select: []string{"claim", "claim_hash", "claim_tsv", "created_at", "embedding", "embedding_model", "id", "invalid_at", "last_confirmed_at", "source_id", "source_kind", "source_seq", "superseded_by", "trust", "user_id", "valid_from"},
			Insert: []string{"claim", "claim_hash", "claim_tsv", "created_at", "embedding", "embedding_model", "id", "invalid_at", "last_confirmed_at", "source_id", "source_kind", "source_seq", "superseded_by", "trust", "user_id", "valid_from"},
			Update: []string{"embedding", "embedding_model", "invalid_at", "last_confirmed_at", "superseded_by"},
		},
		"memory.goose_db_version": {},
		"memory.profiles": {
			Table:  []string{"INSERT", "SELECT"},
			Select: []string{"body", "updated_at", "user_id"},
			Insert: []string{"body", "updated_at", "user_id"},
			Update: []string{"body", "updated_at"},
		},
		"notices.goose_db_version": {},
		"notices.notifications": {
			Table:  []string{"INSERT", "SELECT"},
			Select: []string{"created_at", "dismissed_at", "id", "kind", "task_id", "title", "user_id"},
			Insert: []string{"created_at", "dismissed_at", "id", "kind", "task_id", "title", "user_id"},
			Update: []string{"dismissed_at"},
		},
		"platform.goose_db_version": {},
		"platform.outbox": {
			Table:  []string{"DELETE", "INSERT", "SELECT", "UPDATE"},
			Select: []string{"created_at", "id", "payload", "processed_at", "topic"},
			Insert: []string{"created_at", "id", "payload", "processed_at", "topic"},
			Update: []string{"created_at", "id", "payload", "processed_at", "topic"},
		},
		"platform.river_job": {
			Table:  []string{"DELETE", "INSERT", "SELECT", "UPDATE"},
			Select: []string{"args", "attempt", "attempted_at", "attempted_by", "created_at", "errors", "finalized_at", "id", "kind", "max_attempts", "metadata", "priority", "queue", "scheduled_at", "state", "tags", "unique_key", "unique_states"},
			Insert: []string{"args", "attempt", "attempted_at", "attempted_by", "created_at", "errors", "finalized_at", "id", "kind", "max_attempts", "metadata", "priority", "queue", "scheduled_at", "state", "tags", "unique_key", "unique_states"},
			Update: []string{"args", "attempt", "attempted_at", "attempted_by", "created_at", "errors", "finalized_at", "id", "kind", "max_attempts", "metadata", "priority", "queue", "scheduled_at", "state", "tags", "unique_key", "unique_states"},
		},
		"platform.river_leader": {
			Table:  []string{"DELETE", "INSERT", "SELECT", "UPDATE"},
			Select: []string{"elected_at", "expires_at", "leader_id", "name"},
			Insert: []string{"elected_at", "expires_at", "leader_id", "name"},
			Update: []string{"elected_at", "expires_at", "leader_id", "name"},
		},
		"platform.river_migration": {},
		"platform.river_notification": {
			Table:  []string{"DELETE", "INSERT", "SELECT", "UPDATE"},
			Select: []string{"created_at", "id", "payload", "topic"},
			Insert: []string{"created_at", "id", "payload", "topic"},
			Update: []string{"created_at", "id", "payload", "topic"},
		},
		"platform.river_queue": {
			Table:  []string{"DELETE", "INSERT", "SELECT", "UPDATE"},
			Select: []string{"created_at", "metadata", "name", "paused_at", "updated_at"},
			Insert: []string{"created_at", "metadata", "name", "paused_at", "updated_at"},
			Update: []string{"created_at", "metadata", "name", "paused_at", "updated_at"},
		},
		"runtime.goose_db_version": {},
		"runtime.task_sandboxes": {
			Table:  []string{"DELETE", "INSERT", "SELECT"},
			Select: []string{"created_at", "expires_at", "sandbox_id", "task_id"},
			Insert: []string{"created_at", "expires_at", "sandbox_id", "task_id"},
			Update: []string{"expires_at", "sandbox_id"},
		},
		"tasks.action_logs": {
			Table:  []string{"INSERT", "SELECT"},
			Select: []string{"action", "call_id", "completed_at", "dry_run", "error_text", "executed_at", "preview", "response_digest", "run_id", "scope_id", "state", "task_id", "tool_name", "untrusted_origin", "user_id"},
			Insert: []string{"action", "call_id", "completed_at", "dry_run", "error_text", "executed_at", "preview", "response_digest", "run_id", "scope_id", "state", "task_id", "tool_name", "untrusted_origin", "user_id"},
			Update: []string{"completed_at", "error_text", "response_digest", "state"},
		},
		"tasks.approvals": {
			Table:  []string{"INSERT", "SELECT"},
			Select: []string{"action", "calls", "created_at", "decided_at", "id", "preview", "reason", "state", "task_id", "tool", "untrusted_origin", "user_id"},
			Insert: []string{"action", "calls", "created_at", "decided_at", "id", "preview", "reason", "state", "task_id", "tool", "untrusted_origin", "user_id"},
			Update: []string{"decided_at", "state"},
		},
		"tasks.automation_fires": {
			Table:  []string{"INSERT", "SELECT"},
			Select: []string{"automation_id", "dedup_key", "task_id"},
			Insert: []string{"automation_id", "dedup_key", "task_id"},
		},
		"tasks.automations": {
			Table:  []string{"INSERT", "SELECT"},
			Select: []string{"failures", "id", "kind", "mode", "owner_user_id", "reason", "spec", "template_id"},
			Insert: []string{"failures", "id", "kind", "mode", "owner_user_id", "reason", "spec", "template_id"},
			Update: []string{"failures", "mode", "owner_user_id", "reason"},
		},
		"tasks.chat_events": {
			Table:  []string{"INSERT", "SELECT"},
			Select: []string{"at", "body", "id", "seq", "task_id"},
			Insert: []string{"at", "body", "id", "seq", "task_id"},
		},
		"tasks.checks": {
			Table:  []string{"INSERT", "SELECT"},
			Select: []string{"at", "batch", "code", "name", "seq", "task_id"},
			Insert: []string{"at", "batch", "code", "name", "seq", "task_id"},
		},
		"tasks.findings": {
			Table:  []string{"INSERT", "SELECT"},
			Select: []string{"body", "created_at", "id", "kind", "run_id", "source_ref", "task_id", "title", "untrusted"},
			Insert: []string{"body", "created_at", "id", "kind", "run_id", "source_ref", "task_id", "title", "untrusted"},
		},
		"tasks.goose_db_version": {},
		"tasks.run_events": {
			Table:  []string{"INSERT", "SELECT"},
			Select: []string{"at", "from_state", "id", "input", "run_id", "seq", "to_state"},
			Insert: []string{"at", "from_state", "id", "input", "run_id", "seq", "to_state"},
		},
		"tasks.runs": {
			Table:  []string{"INSERT", "SELECT"},
			Select: []string{"created_at", "id", "state", "task_id"},
			Insert: []string{"created_at", "id", "state", "task_id"},
			Update: []string{"state"},
		},
		"tasks.task_events": {
			Table:  []string{"INSERT", "SELECT"},
			Select: []string{"at", "from_status", "id", "input", "seq", "task_id", "to_status"},
			Insert: []string{"at", "from_status", "id", "input", "seq", "task_id", "to_status"},
		},
		"tasks.tasks": {
			Table:  []string{"DELETE", "INSERT", "SELECT"},
			Select: []string{"allow", "automation_id", "budget", "budget_micro_usd", "cause", "goal", "held", "held_micro_usd", "id", "object_type", "owner_user_id", "parent_id", "passes", "paused", "result", "spent", "spent_micro_usd", "status", "tainted", "updated_at", "visibility", "visibility_team_id", "worktree"},
			Insert: []string{"allow", "automation_id", "budget", "budget_micro_usd", "cause", "goal", "held", "held_micro_usd", "id", "object_type", "owner_user_id", "parent_id", "passes", "paused", "result", "spent", "spent_micro_usd", "status", "tainted", "updated_at", "visibility", "visibility_team_id", "worktree"},
			Update: []string{"allow", "automation_id", "budget", "budget_micro_usd", "cause", "goal", "held", "held_micro_usd", "id", "object_type", "parent_id", "passes", "paused", "result", "spent", "spent_micro_usd", "status", "tainted", "updated_at", "visibility", "visibility_team_id", "worktree"},
		},
		"tasks.template_deliveries": {
			Table:  []string{"DELETE", "INSERT", "SELECT", "UPDATE"},
			Select: []string{"seq", "target", "template_id"},
			Insert: []string{"seq", "target", "template_id"},
			Update: []string{"seq", "target", "template_id"},
		},
		"tasks.template_steps": {
			Table:  []string{"DELETE", "INSERT", "SELECT", "UPDATE"},
			Select: []string{"seq", "template_id", "title"},
			Insert: []string{"seq", "template_id", "title"},
			Update: []string{"seq", "template_id", "title"},
		},
		"tasks.templates": {
			Table:  []string{"DELETE", "INSERT", "SELECT"},
			Select: []string{"agent_id", "budget", "goal", "id", "owner_user_id", "updated_at"},
			Insert: []string{"agent_id", "budget", "goal", "id", "owner_user_id", "updated_at"},
			Update: []string{"agent_id", "budget", "goal", "updated_at"},
		},
		"tasks.turns": {
			Table:  []string{"INSERT", "SELECT"},
			Select: []string{"body", "cost_micro_usd", "created_at", "id", "input_tokens", "is_error", "model", "offloaded", "output_tokens", "private_read", "role", "run_id", "seq", "tainted", "task_id", "tool_calls", "tool_id", "tool_name"},
			Insert: []string{"body", "cost_micro_usd", "created_at", "id", "input_tokens", "is_error", "model", "offloaded", "output_tokens", "private_read", "role", "run_id", "seq", "tainted", "task_id", "tool_calls", "tool_id", "tool_name"},
		},
	}

	gotPrivs := make(map[string]rolePrivs, len(wantPrivs))
	tblRows, err := owner.Query(ctx, `
		SELECT table_schema || '.' || table_name
		FROM information_schema.tables
		WHERE table_schema = ANY($1)
		  AND table_type IN ('BASE TABLE', 'VIEW')
		ORDER BY 1`, wantSchemas)
	if err != nil {
		t.Fatal(err)
	}
	for tblRows.Next() {
		var tbl string
		if err := tblRows.Scan(&tbl); err != nil {
			tblRows.Close()
			t.Fatal(err)
		}
		gotPrivs[tbl] = rolePrivs{}
	}
	if err := tblRows.Err(); err != nil {
		tblRows.Close()
		t.Fatal(err)
	}
	tblRows.Close()

	tpRows, err := owner.Query(ctx, `
		SELECT table_schema || '.' || table_name, privilege_type
		FROM information_schema.table_privileges
		WHERE grantee = 'upmo_app' AND table_schema = ANY($1)
		ORDER BY 1, 2`, wantSchemas)
	if err != nil {
		t.Fatal(err)
	}
	for tpRows.Next() {
		var tbl, priv string
		if err := tpRows.Scan(&tbl, &priv); err != nil {
			tpRows.Close()
			t.Fatal(err)
		}
		p := gotPrivs[tbl]
		p.Table = append(p.Table, priv)
		gotPrivs[tbl] = p
	}
	if err := tpRows.Err(); err != nil {
		tpRows.Close()
		t.Fatal(err)
	}
	tpRows.Close()

	cpRows, err := owner.Query(ctx, `
		SELECT table_schema || '.' || table_name, column_name, privilege_type
		FROM information_schema.column_privileges
		WHERE grantee = 'upmo_app' AND table_schema = ANY($1)
		ORDER BY 1, 3, 2`, wantSchemas)
	if err != nil {
		t.Fatal(err)
	}
	for cpRows.Next() {
		var tbl, col, priv string
		if err := cpRows.Scan(&tbl, &col, &priv); err != nil {
			cpRows.Close()
			t.Fatal(err)
		}
		p := gotPrivs[tbl]
		switch priv {
		case "SELECT":
			p.Select = append(p.Select, col)
		case "INSERT":
			p.Insert = append(p.Insert, col)
		case "UPDATE":
			p.Update = append(p.Update, col)
		default:
			cpRows.Close()
			t.Fatalf("unexpected column privilege %s on %s.%s", priv, tbl, col)
		}
		gotPrivs[tbl] = p
	}
	if err := cpRows.Err(); err != nil {
		cpRows.Close()
		t.Fatal(err)
	}
	cpRows.Close()

	for tbl := range gotPrivs {
		if _, ok := wantPrivs[tbl]; !ok {
			t.Errorf("unexpected table %s in schemas", tbl)
		}
	}
	var wantTables []string
	for tbl := range wantPrivs {
		wantTables = append(wantTables, tbl)
	}
	sort.Strings(wantTables)
	for _, tbl := range wantTables {
		got, ok := gotPrivs[tbl]
		if !ok {
			t.Errorf("missing table %s in schemas", tbl)
			continue
		}
		want := wantPrivs[tbl]
		if !slices.Equal(got.Table, want.Table) {
			t.Errorf("%s table privileges = %v, want %v", tbl, got.Table, want.Table)
		}
		if !slices.Equal(got.Select, want.Select) {
			t.Errorf("%s SELECT columns = %v, want %v", tbl, got.Select, want.Select)
		}
		if !slices.Equal(got.Insert, want.Insert) {
			t.Errorf("%s INSERT columns = %v, want %v", tbl, got.Insert, want.Insert)
		}
		if !slices.Equal(got.Update, want.Update) {
			t.Errorf("%s UPDATE columns = %v, want %v", tbl, got.Update, want.Update)
		}
	}
	var redactedTurnsAppExec, redactedTurnsPublicExec bool
	if err := owner.QueryRow(ctx, `
		SELECT has_function_privilege('upmo_app', p.oid, 'EXECUTE'),
		       EXISTS (
		         SELECT 1 FROM pg_catalog.aclexplode(COALESCE(p.proacl, pg_catalog.acldefault('f', p.proowner))) AS acl
		         WHERE acl.grantee = 0 AND acl.privilege_type = 'EXECUTE'
		       )
		FROM pg_catalog.pg_proc AS p
		WHERE p.oid = 'chat.redacted_turn_metadata(uuid)'::regprocedure`).Scan(&redactedTurnsAppExec, &redactedTurnsPublicExec); err != nil {
		t.Fatal(err)
	}
	if !redactedTurnsAppExec || redactedTurnsPublicExec {
		t.Fatalf("chat.redacted_turn_metadata EXECUTE: upmo_app=%t PUBLIC=%t, want true/false", redactedTurnsAppExec, redactedTurnsPublicExec)
	}
	var branchAppExec, branchPublicExec bool
	if err := owner.QueryRow(ctx, `
		SELECT has_function_privilege('upmo_app', p.oid, 'EXECUTE'),
		       EXISTS (
		         SELECT 1 FROM pg_catalog.aclexplode(COALESCE(p.proacl, pg_catalog.acldefault('f', p.proowner))) AS acl
		         WHERE acl.grantee = 0 AND acl.privilege_type = 'EXECUTE'
		       )
		FROM pg_catalog.pg_proc AS p
		WHERE p.oid = 'chat.branch_chat(uuid, uuid)'::regprocedure`).Scan(&branchAppExec, &branchPublicExec); err != nil {
		t.Fatal(err)
	}
	if !branchAppExec || branchPublicExec {
		t.Fatalf("chat.branch_chat EXECUTE: upmo_app=%t PUBLIC=%t, want true/false", branchAppExec, branchPublicExec)
	}
}
