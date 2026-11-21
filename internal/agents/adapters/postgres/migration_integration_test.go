//go:build integration

package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/agents"
	"github.com/upmo-ai/upmo/internal/agents/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/agents/app"
	"github.com/upmo-ai/upmo/internal/chat"
	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/memory"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestFreshAgentsMigrationAndDirectSeedDownRoundTrip(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()

	if err := audit.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, memory.Schema, memory.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, chat.Schema, chat.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, gateway.Schema, gateway.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, agents.Schema, agents.Migrations()); err != nil {
		t.Fatal(err)
	}

	// Removed legacy sources must be absent on a fresh install.
	for _, rel := range []string{"chat.agents", "gateway.agent_bundles", "memory.lessons"} {
		var reg *string
		if err := owner.QueryRow(ctx, `SELECT to_regclass($1)::text`, rel).Scan(&reg); err != nil {
			t.Fatal(err)
		}
		if reg != nil {
			t.Fatalf("%s still exists: %q", rel, *reg)
		}
	}

	// Seed an unrelated schema row to verify agents Down leaves it untouched.
	if _, err := owner.Exec(ctx, `INSERT INTO gateway.tool_bundles (name) VALUES ('core')`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO gateway.tool_bundle_tools (bundle, tool_name, position) VALUES
			('core', 'search', 1), ('core', 'knowledge', 2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO identity.teams (name) VALUES ('Platform')`); err != nil {
		t.Fatal(err)
	}

	// Seed rows directly into the final agents tables.
	var agentID string
	if err := owner.QueryRow(ctx, `
		INSERT INTO agents.agents (
			owner_user_id, visibility, name, slug, instructions,
			default_route, default_model_provider, default_model_id,
			tool_classes, pack_version
		)
		VALUES ($1::uuid, 'private', 'Foo Bar', 'foo-bar', 'Be concise.', 'work', 'Anthropic', 'claude-sonnet-4-5', ARRAY['search', 'knowledge'], '1.0.0')
		RETURNING id::text`, ownerUser).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO agents.versions (agent_id, version, digest, snapshot, created_by)
		VALUES ($1::uuid, 1, $2, '{"name":"Foo Bar"}'::jsonb, $3::uuid)`,
		agentID, strings.Repeat("a", 64), ownerUser); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO agents.lessons (agent_id, title, cue, body, always_inline, updated_at)
		VALUES ($1::uuid, 'VAT check', 'When EU invoice', 'Check VAT ID', false, $2)`,
		agentID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	store := postgres.Store{Pool: pgtest.App(t, owner)}
	got, err := app.GetAgent(ctx, store, memberAccess, agentID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Slug != "foo-bar" || got.Instructions != "Be concise." || got.Version != 1 {
		t.Fatalf("seeded agent = %+v", got)
	}
	cards, err := app.AgentIndex(ctx, store, agentID)
	if err != nil || len(cards) != 1 || cards[0].Title != "VAT check" {
		t.Fatalf("lessons index = %+v %v", cards, err)
	}

	// Run Down on agents and assert module objects are dropped while unrelated data survives.
	if err := migrate.Down(ctx, owner, agents.Schema, agents.Migrations()); err != nil {
		t.Fatal(err)
	}
	var ns *string
	if err := owner.QueryRow(ctx, `SELECT to_regnamespace('agents')::text`).Scan(&ns); err != nil {
		t.Fatal(err)
	}
	if ns != nil {
		t.Fatalf("agents schema still exists after down: %q", *ns)
	}
	for _, rel := range []string{"agents.agents", "agents.versions", "agents.lessons"} {
		var reg *string
		if err := owner.QueryRow(ctx, `SELECT to_regclass($1)::text`, rel).Scan(&reg); err != nil {
			t.Fatal(err)
		}
		if reg != nil {
			t.Fatalf("%s still exists after down: %q", rel, *reg)
		}
	}
	var teamCount, bundleToolCount int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM identity.teams WHERE name = 'Platform'`).Scan(&teamCount); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM gateway.tool_bundle_tools WHERE bundle = 'core'`).Scan(&bundleToolCount); err != nil {
		t.Fatal(err)
	}
	if teamCount != 1 || bundleToolCount != 2 {
		t.Fatalf("unrelated schema rows modified: teams=%d bundle_tools=%d", teamCount, bundleToolCount)
	}

	// Reapply Up and assert fresh final schema is present at version 1.
	if err := migrate.Up(ctx, owner, agents.Schema, agents.Migrations()); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"agents.agents", "agents.versions", "agents.lessons"} {
		var reg *string
		if err := owner.QueryRow(ctx, `SELECT to_regclass($1)::text`, rel).Scan(&reg); err != nil {
			t.Fatal(err)
		}
		if reg == nil {
			t.Fatalf("%s missing after re-up", rel)
		}
	}
	var version int
	if err := owner.QueryRow(ctx, `SELECT max(version_id) FROM agents.goose_db_version WHERE is_applied`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("agents version after re-up = %d, want 1", version)
	}
	var hasPackVersion bool
	if err := owner.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM information_schema.columns
		  WHERE table_schema = 'agents' AND table_name = 'agents' AND column_name = 'pack_version'
		)`).Scan(&hasPackVersion); err != nil || !hasPackVersion {
		t.Fatalf("agents.agents.pack_version present = %v, err = %v", hasPackVersion, err)
	}
}
