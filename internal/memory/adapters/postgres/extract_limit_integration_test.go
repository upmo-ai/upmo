//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/upmo-ai/upmo/internal/memory/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/memory/app"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestExtractLimitsRecentSource(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	src := canned(5, "Ada prefers tea every morning")
	createdAt := src.At.Add(-app.ExtractWindow / 2)
	for seq := 0; seq < app.ExtractLimit; seq++ {
		if _, err := owner.Exec(ctx, `
			INSERT INTO memory.extracts (user_id, source_kind, source_id, source_seq, created_at)
			VALUES ($1, 'chat', $2, $3, $4)`,
			person, sourceID, seq, createdAt); err != nil {
			t.Fatal(err)
		}
	}

	store := postgres.Facts{Pool: pgtest.App(t, owner)}
	prop := &scriptPropose{bodies: []string{`[{"claim":"Ada prefers tea","changes_behavior":true}]`}}
	rep, err := app.Extract(ctx, store, prop, &scriptEmbed{model: "model-a"}, src)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Limited || prop.calls != 0 {
		t.Fatalf("report=%+v proposer calls=%d", rep, prop.calls)
	}

	var facts, extracts int
	if err := owner.QueryRow(ctx,
		`SELECT count(*) FROM memory.facts WHERE user_id = $1`, person,
	).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `
		SELECT count(*) FROM memory.extracts
		WHERE user_id = $1 AND source_kind = 'chat' AND source_id = $2`,
		person, sourceID).Scan(&extracts); err != nil {
		t.Fatal(err)
	}
	if facts != 0 || extracts != app.ExtractLimit {
		t.Fatalf("facts=%d extracts=%d", facts, extracts)
	}
}

func TestExtractAllowsSourceOutsideLimitWindow(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	src := canned(15, "Ada prefers tea every morning")
	createdAt := src.At.Add(-2 * app.ExtractWindow)
	for seq := 10; seq < 10+app.ExtractLimit; seq++ {
		if _, err := owner.Exec(ctx, `
			INSERT INTO memory.extracts (user_id, source_kind, source_id, source_seq, created_at)
			VALUES ($1, 'chat', $2, $3, $4)`,
			person, sourceID, seq, createdAt); err != nil {
			t.Fatal(err)
		}
	}

	store := postgres.Facts{Pool: pgtest.App(t, owner)}
	prop := &scriptPropose{bodies: []string{`[{"claim":"Ada prefers tea","changes_behavior":true}]`}}
	rep, err := app.Extract(ctx, store, prop, &scriptEmbed{model: "model-a"}, src)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Limited || prop.calls != 1 || rep.Inserted != 1 {
		t.Fatalf("report=%+v proposer calls=%d", rep, prop.calls)
	}
}
