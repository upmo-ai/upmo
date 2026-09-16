//go:build integration

package decisions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/decisions"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestAcceptKeepsTheOldModelWhenTheGateFails(t *testing.T) {
	pool := up(t)
	store := decisions.Store{Pool: pgtest.App(t, pool)}
	ctx := context.Background()
	access := decisions.Access{
		UserID: "018f3b2c-7a10-7c4a-8e11-111111111111",
		Allow:  "settings:manage",
	}
	cfg := decisions.Config{Name: "memory-check", Kind: decisions.KindYesNo, Hops: []decisions.Hop{
		{Position: 1, Adapter: "openai", Model: "small", Weight: 1, Retry: 0},
	}}
	examples := []decisions.Example{{Prompt: "keep?", Yes: true}}
	if err := decisions.Accept(ctx, store, access, cfg, 100, examples, scripted{yes: true}); err != nil {
		t.Fatal(err)
	}
	first, err := decisions.Fingerprint(cfg)
	if err != nil {
		t.Fatal(err)
	}

	cfg.Hops[0].Model = "large"
	if err := decisions.Accept(ctx, store, access, cfg, 100, examples, scripted{yes: false}); !errors.Is(err, decisions.ErrGate) {
		t.Fatalf("fail error = %v", err)
	}
	var stored string
	if err := pool.QueryRow(ctx, `SELECT fingerprint FROM decisions.gates WHERE route = 'memory-check'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != first {
		t.Fatalf("stored = %s, want %s", stored, first)
	}

	if err := decisions.Accept(ctx, store, access, cfg, 100, examples, scripted{yes: true}); err != nil {
		t.Fatal(err)
	}
	next, err := decisions.Fingerprint(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT fingerprint FROM decisions.gates WHERE route = 'memory-check'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != next || stored == first {
		t.Fatalf("updated = %s", stored)
	}

	other := decisions.Access{UserID: access.UserID, Allow: "task:edit"}
	if err := decisions.Accept(ctx, store, other, cfg, 100, examples, scripted{yes: true}); !errors.Is(err, decisions.ErrAccess) {
		t.Fatalf("other caller error = %v", err)
	}
}

type scripted struct{ yes bool }

func (s scripted) Decide(context.Context, string, []string) (bool, string, error) {
	return s.yes, "", nil
}

func up(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := pgtest.New(t)
	if err := migrate.Up(context.Background(), pool, decisions.Schema, decisions.Migrations()); err != nil {
		t.Fatal(err)
	}
	return pool
}
