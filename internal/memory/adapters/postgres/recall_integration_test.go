//go:build integration

package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/memory/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/memory/app"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestLoadProfileAndActiveClaims(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	if _, err := owner.Exec(ctx, `
		INSERT INTO memory.profiles (user_id, body, updated_at)
		VALUES ($1, 'Ada lives in Dhaka', $2)`, person, at); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO memory.facts (
			user_id, trust, claim, claim_hash, valid_from, last_confirmed_at,
			source_kind, source_id, source_seq, created_at
		) VALUES (
			$1, 'user', 'Ada prefers tea', $2, $3, $3,
			'person', $4, 0, $3
		)`, person, bytes32(1), at, sourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO memory.facts (
			user_id, trust, claim, claim_hash, valid_from, invalid_at, last_confirmed_at,
			source_kind, source_id, source_seq, created_at
		) VALUES (
			$1, 'user', 'Ada drinks coffee', $2, $3, $3, $3,
			'person', $4, 1, $3
		)`, person, bytes32(2), at, sourceID); err != nil {
		t.Fatal(err)
	}
	mem, err := (postgres.Facts{Pool: pgtest.App(t, owner)}).Load(ctx, person)
	if err != nil {
		t.Fatal(err)
	}
	prefix := app.SystemPrefix("Be brief.", mem.Profile)
	if !strings.Contains(prefix, "Ada lives in Dhaka") {
		t.Fatalf("prefix = %q", prefix)
	}
	recall := app.Search("prefers tea", mem.Claims)
	if !strings.Contains(recall.Text, "Ada prefers tea") || strings.Contains(recall.Text, "coffee") {
		t.Fatalf("recall = %s claims = %v", recall.Text, mem.Claims)
	}
}
