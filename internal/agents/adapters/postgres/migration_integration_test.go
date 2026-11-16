//go:build integration

package postgres_test

import (
	"context"
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

func TestMigrationFromChatAgentsMemoryLessonsAndGatewayBundles(t *testing.T) {
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

	// Seed gateway bundle, chat.agents (including colliding slug names & emoji name),
	// and memory.lessons (including an orphaned lesson).
	if _, err := owner.Exec(ctx, `INSERT INTO gateway.tool_bundles (name) VALUES ('core')`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO gateway.tool_bundle_tools (bundle, tool_name, position) VALUES
			('core', 'search', 1), ('core', 'knowledge', 2)`); err != nil {
		t.Fatal(err)
	}

	var id1, id2, id3 string
	if err := owner.QueryRow(ctx, `
		INSERT INTO chat.agents (owner_user_id, name, description, prompt_overlay, route, model_provider, model_id, enabled, visibility)
		VALUES ($1::uuid, 'Foo Bar', 'First', 'Be concise.', 'work', 'Anthropic', 'claude-sonnet-4-5', true, 'private')
		RETURNING id::text`, ownerUser).Scan(&id1); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `
		INSERT INTO chat.agents (owner_user_id, name, description, prompt_overlay, enabled, visibility)
		VALUES ($1::uuid, 'Foo!Bar', 'Colliding slug', 'Be careful.', true, 'private')
		RETURNING id::text`, ownerUser).Scan(&id2); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `
		INSERT INTO chat.agents (owner_user_id, name, visibility)
		VALUES ($1::uuid, '🤖', 'everyone')
		RETURNING id::text`, adminUser).Scan(&id3); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO gateway.agent_bundles (agent_id, bundle) VALUES ($1::uuid, 'core')`, id1); err != nil {
		t.Fatal(err)
	}

	const orphanAgent = "018f3b2c-7a10-7c4a-8e11-ffffffffffff"
	if _, err := owner.Exec(ctx, `
		INSERT INTO memory.lessons (agent_id, title, cue, body, updated_at) VALUES
			($1::uuid, 'VAT check', 'When EU invoice', 'Check VAT ID', $3),
			($2::uuid, 'Orphaned', 'Never migrated', 'Skipped cleanly', $3)`,
		id1, orphanAgent, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	// Run agents migration.
	if err := migrate.Up(ctx, owner, agents.Schema, agents.Migrations()); err != nil {
		t.Fatal(err)
	}

	// gateway.agent_bundles must be retired (dropped).
	var reg *string
	if err := owner.QueryRow(ctx, `SELECT to_regclass('gateway.agent_bundles')::text`).Scan(&reg); err != nil {
		t.Fatal(err)
	}
	if reg != nil {
		t.Fatalf("gateway.agent_bundles still exists: %q", *reg)
	}

	store := postgres.Store{Pool: pgtest.App(t, owner)}
	got1, err := app.GetAgent(ctx, store, memberAccess, id1)
	if err != nil {
		t.Fatal(err)
	}
	if got1.Slug != "foo-bar" || got1.Instructions != "Be concise." || got1.DefaultRoute != "work" ||
		got1.DefaultModelProvider != "Anthropic" || got1.DefaultModelID != "claude-sonnet-4-5" ||
		len(got1.ToolClasses) != 2 || got1.ToolClasses[0] != "search" || got1.ToolClasses[1] != "knowledge" ||
		got1.Version != 1 {
		t.Fatalf("migrated agent 1 = %+v", got1)
	}
	got2, err := app.GetAgent(ctx, store, memberAccess, id2)
	if err != nil {
		t.Fatal(err)
	}
	if got2.Slug == "" || got2.Slug == got1.Slug {
		t.Fatalf("colliding slug not disambiguated: %q vs %q", got2.Slug, got1.Slug)
	}
	got3, err := app.GetAgent(ctx, store, memberAccess, id3)
	if err != nil {
		t.Fatal(err)
	}
	if got3.Slug != "agent" {
		t.Fatalf("emoji fallback slug = %q, want agent", got3.Slug)
	}

	// Initial v1 rows exist in agents.versions.
	vers, err := app.ListVersions(ctx, store, memberAccess, id1)
	if err != nil || len(vers) != 1 || vers[0].Version != 1 || len(vers[0].Digest) != 64 {
		t.Fatalf("migrated versions = %+v %v", vers, err)
	}

	// Matching memory.lessons migrated as always_inline = false; orphan skipped.
	cards, err := app.AgentIndex(ctx, store, id1)
	if err != nil || len(cards) != 1 || cards[0].Title != "VAT check" {
		t.Fatalf("migrated cards = %+v %v", cards, err)
	}
	var totalLessons int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM agents.lessons`).Scan(&totalLessons); err != nil {
		t.Fatal(err)
	}
	if totalLessons != 1 {
		t.Fatalf("total migrated lessons = %d, want 1", totalLessons)
	}

	// Verify Down migration cleanly drops agents schema tables.
	if err := migrate.Down(ctx, owner, agents.Schema, agents.Migrations()); err != nil {
		t.Fatal(err)
	}
}
