//go:build integration

package jobs_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

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

func TestWithQueueRunsJobsOnAQueueOfItsOwn(t *testing.T) {
	// Not parallel: ping recording is package-global.
	owner := pgtest.New(t)
	migrateIdentity(t, owner)
	ctx := context.Background()
	if err := jobs.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	pool := pgtest.App(t, owner)
	client, err := jobs.Open(pool, jobs.WithQueue("extra", 1))
	if err != nil {
		t.Fatal(err)
	}
	jobs.ResetPing()
	t.Cleanup(jobs.ResetPing)
	done := make(chan string, 1)
	jobs.SetPingHook(func(label string) { done <- label })
	t.Cleanup(startClient(t, client))

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := client.InsertTx(ctx, tx, jobs.PingArgs{Label: "extra"}, &river.InsertOpts{Queue: "extra"}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case label := <-done:
		if label != "extra" {
			t.Fatalf("label = %q, want extra", label)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the job on the extra queue did not run")
	}
}

func TestWithPeriodicRunsItsJobNextToTheOutboxDrain(t *testing.T) {
	// Not parallel: ping recording is package-global.
	owner := pgtest.New(t)
	migrateIdentity(t, owner)
	ctx := context.Background()
	if err := jobs.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	pool := pgtest.App(t, owner)
	periodic := river.NewPeriodicJob(
		river.PeriodicInterval(time.Hour),
		func() (river.JobArgs, *river.InsertOpts) { return jobs.PingArgs{Label: "periodic"}, nil },
		&river.PeriodicJobOpts{RunOnStart: true},
	)
	client, err := jobs.Open(pool, jobs.WithPeriodic(periodic))
	if err != nil {
		t.Fatal(err)
	}
	jobs.ResetPing()
	t.Cleanup(jobs.ResetPing)
	done := make(chan string, 1)
	jobs.SetPingHook(func(label string) { done <- label })
	t.Cleanup(startClient(t, client))
	select {
	case label := <-done:
		if label != "periodic" {
			t.Fatalf("label = %q, want periodic", label)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the periodic job did not run")
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

func TestOutboxDrainMarksProcessed(t *testing.T) {
	// Not parallel: handler registry is package-global.
	jobs.ClearHandlers()
	t.Cleanup(jobs.ClearHandlers)
	owner := pgtest.New(t)
	migrateIdentity(t, owner)
	ctx := context.Background()
	if err := jobs.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	pool := pgtest.App(t, owner)
	payload := []byte(`{"ok":true}`)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := jobs.OutboxInsert(ctx, tx, "test.drain", payload); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var sawTopic string
	var sawPayload []byte
	jobs.Register("test.drain", func(_ context.Context, topic string, p []byte) error {
		sawTopic = topic
		sawPayload = append([]byte(nil), p...)
		return nil
	})
	n, err := jobs.DrainOnce(ctx, pool, 32)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("processed = %d, want 1", n)
	}
	if sawTopic != "test.drain" {
		t.Fatalf("topic = %q", sawTopic)
	}
	var want, have any
	if err := json.Unmarshal(payload, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(sawPayload, &have); err != nil {
		t.Fatal(err)
	}
	wantJSON, _ := json.Marshal(want)
	haveJSON, _ := json.Marshal(have)
	if string(wantJSON) != string(haveJSON) {
		t.Fatalf("payload = %s, want %s", haveJSON, wantJSON)
	}
	var processed bool
	if err := pool.QueryRow(ctx, `
SELECT processed_at IS NOT NULL FROM platform.outbox LIMIT 1
`).Scan(&processed); err != nil {
		t.Fatal(err)
	}
	if !processed {
		t.Fatal("processed_at still null")
	}
}

func TestOutboxDrainHandlerErrorLeavesPending(t *testing.T) {
	jobs.ClearHandlers()
	t.Cleanup(jobs.ClearHandlers)
	owner := pgtest.New(t)
	migrateIdentity(t, owner)
	ctx := context.Background()
	if err := jobs.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	pool := pgtest.App(t, owner)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := jobs.OutboxInsert(ctx, tx, "test.fail", []byte(`{"n":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")
	jobs.Register("test.fail", func(context.Context, string, []byte) error { return boom })
	n, err := jobs.DrainOnce(ctx, pool, 32)
	if n != 0 {
		t.Fatalf("processed = %d, want 0", n)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	var pending bool
	if err := pool.QueryRow(ctx, `
SELECT processed_at IS NULL FROM platform.outbox LIMIT 1
`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if !pending {
		t.Fatal("row was marked processed")
	}
}

func TestOutboxDrainUnknownTopicLeavesPending(t *testing.T) {
	jobs.ClearHandlers()
	t.Cleanup(jobs.ClearHandlers)
	owner := pgtest.New(t)
	migrateIdentity(t, owner)
	ctx := context.Background()
	if err := jobs.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	pool := pgtest.App(t, owner)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := jobs.OutboxInsert(ctx, tx, "test.unknown", []byte(`{"n":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	n, err := jobs.DrainOnce(ctx, pool, 32)
	if n != 0 {
		t.Fatalf("processed = %d, want 0", n)
	}
	if !errors.Is(err, jobs.ErrOutboxUnknownTopic) {
		t.Fatalf("err = %v, want ErrOutboxUnknownTopic", err)
	}
	var pending bool
	if err := pool.QueryRow(ctx, `
SELECT processed_at IS NULL FROM platform.outbox LIMIT 1
`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if !pending {
		t.Fatal("row was marked processed")
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

func TestStuckJobsAreRescuedAfterTenMinutesAndNotBefore(t *testing.T) {
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
	// Two jobs that a process that died left running: one for fifteen
	// minutes, one for five, which a reply may still be streaming.
	for label, age := range map[string]string{"old": "15 minutes", "recent": "5 minutes"} {
		if _, err := owner.Exec(ctx, `
			INSERT INTO platform.river_job (state, attempt, max_attempts, attempted_at, args, kind, queue, priority)
			VALUES ('running', 1, 2, now() - $1::interval, jsonb_build_object('label', $2::text), 'platform_ping', 'default', 1)`,
			age, label); err != nil {
			t.Fatal(err)
		}
	}
	jobs.ResetPing()
	t.Cleanup(jobs.ResetPing)
	seen := make(chan string, 4)
	jobs.SetPingHook(func(label string) { seen <- label })
	t.Cleanup(startClient(t, client))

	select {
	case label := <-seen:
		if label != "old" {
			t.Fatalf("rescued %q first", label)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the job stuck for fifteen minutes was not rescued")
	}
	select {
	case label := <-seen:
		t.Fatalf("rescued %q, which had run for five minutes only", label)
	case <-time.After(time.Second):
	}
}
