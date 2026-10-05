//go:build integration

package jobs_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/jobs"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestRollbackSkipsPing(t *testing.T) {
	// Not parallel: ping recording is package-global.
	owner := pgtest.New(t)
	migrateIdentity(t, owner)
	ctx := context.Background()
	if err := jobs.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	pool := pgtest.App(t, owner)
	client, err := jobs.Open(pool)
	if err != nil {
		t.Fatal(err)
	}
	jobs.ResetPing()
	t.Cleanup(jobs.ResetPing)
	stopClient := startClient(t, client)
	t.Cleanup(stopClient)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := client.InsertTx(ctx, tx, jobs.PingArgs{Label: "rollback"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if seen := jobs.PingSeen(); len(seen) != 0 {
		t.Fatalf("ping ran after rollback: %v", seen)
	}
}

func TestCommitRunsPing(t *testing.T) {
	// Not parallel: ping recording is package-global.
	owner := pgtest.New(t)
	migrateIdentity(t, owner)
	ctx := context.Background()
	if err := jobs.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	pool := pgtest.App(t, owner)
	client, err := jobs.Open(pool)
	if err != nil {
		t.Fatal(err)
	}
	jobs.ResetPing()
	t.Cleanup(jobs.ResetPing)
	done := make(chan string, 1)
	jobs.SetPingHook(func(label string) { done <- label })
	stopClient := startClient(t, client)
	t.Cleanup(stopClient)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := client.InsertTx(ctx, tx, jobs.PingArgs{Label: "commit"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case label := <-done:
		if label != "commit" {
			t.Fatalf("label = %q, want commit", label)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ping worker did not run")
	}
}

func TestOutboxInsert(t *testing.T) {
	t.Parallel()
	owner := pgtest.New(t)
	migrateIdentity(t, owner)
	ctx := context.Background()
	if err := jobs.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	pool := pgtest.App(t, owner)
	payload := []byte(`{"event":"test"}`)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := jobs.OutboxInsert(ctx, tx, "memory.fact", payload); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var topic string
	var got []byte
	if err := pool.QueryRow(ctx, `SELECT topic, payload FROM platform.outbox LIMIT 1`).Scan(&topic, &got); err != nil {
		t.Fatal(err)
	}
	if topic != "memory.fact" {
		t.Fatalf("topic = %q", topic)
	}
	var want, have any
	if err := json.Unmarshal(payload, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(got, &have); err != nil {
		t.Fatal(err)
	}
	wantJSON, _ := json.Marshal(want)
	haveJSON, _ := json.Marshal(have)
	if string(wantJSON) != string(haveJSON) {
		t.Fatalf("payload = %s, want %s", haveJSON, wantJSON)
	}
}

func startClient(t *testing.T, client *jobs.Client) func() {
	t.Helper()
	runCtx, cancel := context.WithCancel(context.Background())
	if err := client.Start(runCtx); err != nil {
		cancel()
		t.Fatal(err)
	}
	return func() {
		cancel()
		stop, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = client.Stop(stop)
	}
}

func migrateIdentity(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if err := migrate.Up(context.Background(), pool, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
}
