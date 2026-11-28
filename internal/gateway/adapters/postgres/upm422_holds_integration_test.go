//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/riverqueue/river"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/platform/jobs"
	"github.com/upmo-ai/upmo/internal/platform/metrics"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func TestFeature_UPM422_ReservePersistsHoldMetadata(t *testing.T) {
	w := newPinWorld(t, nil)
	ctx := context.Background()
	store := postgres.Store{Pool: w.pool}
	period := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	const estimate int64 = 500_000
	res, err := store.Reserve(ctx, app.ReserveRequest{
		UserID:      pinAuthor,
		Source:      domain.SourceChat,
		Estimate:    estimate,
		PeriodStart: period,
	})
	if err != nil {
		t.Fatal(err)
	}
	var holdUser, holdSource string
	var holdAmount int64
	if err := w.pool.QueryRow(ctx, `
		SELECT amount_micro_usd, COALESCE(user_id::text, ''), source
		FROM gateway.usage_holds WHERE id = $1::uuid`, res.ID).Scan(&holdAmount, &holdUser, &holdSource); err != nil {
		t.Fatal(err)
	}
	if holdAmount != estimate || holdUser != pinAuthor || holdSource != domain.SourceChat {
		t.Fatalf("hold row = %d %q %q, want metadata persisted", holdAmount, holdUser, holdSource)
	}
}

func TestFeature_UPM422_ReserveZeroEstimateNoHoldRow(t *testing.T) {
	w := newPinWorld(t, nil)
	ctx := context.Background()
	setUsageLimit(t, w, domain.LimitWorkspace, "", 1_000_000)
	store := postgres.Store{Pool: w.pool}
	period := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	res, err := store.Reserve(ctx, app.ReserveRequest{
		UserID:      pinAuthor,
		Source:      domain.SourceChat,
		Estimate:    0,
		PeriodStart: period,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != "" {
		t.Fatalf("reservation id = %q, want empty when estimate is 0", res.ID)
	}
	var holdCount int
	if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM gateway.usage_holds`).Scan(&holdCount); err != nil {
		t.Fatal(err)
	}
	if holdCount != 0 {
		t.Fatalf("usage_holds = %d, want 0 for zero estimate", holdCount)
	}
}

func TestFeature_UPM422_ReleaseStaleHoldsFailClosed(t *testing.T) {
	ctx := context.Background()
	w := newPinWorld(t, nil)
	if _, err := postgres.ReleaseStaleHolds(ctx, nil, time.Now(), 15*time.Minute); err == nil {
		t.Fatal("nil owner: want error")
	}
	if _, err := postgres.ReleaseStaleHolds(ctx, w.pool, time.Now(), 0); err == nil {
		t.Fatal("maxAge <= 0: want error")
	}
}

func TestFeature_UPM422_StaleBoundaryHold15mExact(t *testing.T) {
	w := newPinWorld(t, nil)
	ctx := context.Background()
	setUsageLimit(t, w, domain.LimitWorkspace, "", 10_000_000)
	setUsageLimit(t, w, domain.LimitPerson, pinAuthor, 10_000_000)

	const estimate = int64(1_000_000)
	period := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	sweepAt := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	createdAt := sweepAt.Add(-15 * time.Minute) // exactly on cutoff
	store := postgres.Store{Pool: w.pool, Now: func() time.Time { return createdAt }}

	if _, err := store.Reserve(ctx, app.ReserveRequest{
		UserID: pinAuthor, Source: domain.SourceChat, Estimate: estimate, PeriodStart: period,
	}); err != nil {
		t.Fatal(err)
	}
	released, err := postgres.ReleaseStaleHolds(ctx, w.pool, sweepAt, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if released != 0 {
		t.Fatalf("boundary hold released = %d, want 0 (strictly older than cutoff)", released)
	}
	var holdCount int
	if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM gateway.usage_holds`).Scan(&holdCount); err != nil {
		t.Fatal(err)
	}
	if holdCount != 1 {
		t.Fatalf("hold rows = %d, want 1 at boundary", holdCount)
	}
}

func TestFeature_UPM422_AutomationHoldSweepTargets(t *testing.T) {
	w := newPinWorld(t, nil)
	ctx := context.Background()
	const autoID = "018f3b2c-7a10-7c4a-8e11-eeeeeeeeeeee"
	const estimate = int64(2_000_000)
	period := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	setUsageLimit(t, w, domain.LimitWorkspace, "", 20_000_000)
	setUsageLimit(t, w, domain.LimitPerson, pinAuthor, 20_000_000)
	setUsageLimit(t, w, domain.LimitAutomation, autoID, 20_000_000)

	reserveAt := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	sweepAt := reserveAt.Add(16 * time.Minute)
	store := postgres.Store{Pool: w.pool, Now: func() time.Time { return reserveAt }}
	res, err := store.Reserve(ctx, app.ReserveRequest{
		UserID: pinAuthor, Source: domain.SourceAutomation, AutomationID: autoID,
		Estimate: estimate, PeriodStart: period,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.AutomationID != autoID {
		t.Fatalf("automation_id = %q, want %q", res.AutomationID, autoID)
	}
	released, err := postgres.ReleaseStaleHolds(ctx, w.pool, sweepAt, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if released != 1 {
		t.Fatalf("released = %d, want 1", released)
	}
	periodStr := period.Format("2006-01-02")
	for _, q := range []struct {
		kind, subject string
	}{
		{domain.LimitWorkspace, ""},
		{domain.LimitPerson, pinAuthor},
		{domain.LimitAutomation, autoID},
	} {
		var held int64
		var err error
		if q.subject == "" {
			err = w.pool.QueryRow(ctx, `
				SELECT held_micro_usd FROM gateway.usage_spend
				WHERE kind = $1 AND subject_id IS NULL AND period_start = $2::date`, q.kind, periodStr).Scan(&held)
		} else {
			err = w.pool.QueryRow(ctx, `
				SELECT held_micro_usd FROM gateway.usage_spend
				WHERE kind = $1 AND subject_id = $2::uuid AND period_start = $3::date`,
				q.kind, q.subject, periodStr).Scan(&held)
		}
		if err != nil {
			t.Fatal(err)
		}
		if held != 0 {
			t.Fatalf("%s held = %d, want 0 after sweep", q.kind, held)
		}
	}
}

func TestFeature_UPM422_SystemHoldSweepWorkspaceOnly(t *testing.T) {
	w := newPinWorld(t, nil)
	ctx := context.Background()
	const estimate = int64(800_000)
	period := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	setUsageLimit(t, w, domain.LimitWorkspace, "", 10_000_000)
	setUsageLimit(t, w, domain.LimitPerson, pinAuthor, 10_000_000)

	reserveAt := time.Date(2026, 11, 25, 1, 0, 0, 0, time.UTC)
	sweepAt := reserveAt.Add(20 * time.Minute)
	store := postgres.Store{Pool: w.pool, Now: func() time.Time { return reserveAt }}
	if _, err := store.Reserve(ctx, app.ReserveRequest{
		UserID: pinAuthor, Source: domain.SourceSystem, Estimate: estimate, PeriodStart: period,
	}); err != nil {
		t.Fatal(err)
	}
	periodStr := period.Format("2006-01-02")
	var personRows int
	if err := w.pool.QueryRow(ctx, `
		SELECT count(*) FROM gateway.usage_spend
		WHERE kind = 'person' AND subject_id = $1::uuid AND period_start = $2::date`,
		pinAuthor, periodStr).Scan(&personRows); err != nil {
		t.Fatal(err)
	}
	if personRows != 0 {
		t.Fatalf("person usage_spend rows = %d, want 0 for system reserve", personRows)
	}
	released, err := postgres.ReleaseStaleHolds(ctx, w.pool, sweepAt, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if released != 1 {
		t.Fatalf("released = %d, want 1", released)
	}
	var wsHeld int64
	if err := w.pool.QueryRow(ctx, `
		SELECT held_micro_usd FROM gateway.usage_spend
		WHERE kind = 'workspace' AND subject_id IS NULL AND period_start = $1::date`, periodStr).Scan(&wsHeld); err != nil {
		t.Fatal(err)
	}
	if wsHeld != 0 {
		t.Fatalf("workspace held = %d, want 0", wsHeld)
	}
}

func TestFeature_UPM422_StaleReleaseIdempotentSecondSweep(t *testing.T) {
	w := newPinWorld(t, nil)
	ctx := context.Background()
	setUsageLimit(t, w, domain.LimitWorkspace, "", 10_000_000)
	setUsageLimit(t, w, domain.LimitPerson, pinAuthor, 10_000_000)

	const estimate = int64(1_500_000)
	period := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	reserveAt := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
	sweepAt := reserveAt.Add(30 * time.Minute)
	store := postgres.Store{Pool: w.pool, Now: func() time.Time { return reserveAt }}
	if _, err := store.Reserve(ctx, app.ReserveRequest{
		UserID: pinAuthor, Source: domain.SourceChat, Estimate: estimate, PeriodStart: period,
	}); err != nil {
		t.Fatal(err)
	}
	n1, err := postgres.ReleaseStaleHolds(ctx, w.pool, sweepAt, 15*time.Minute)
	if err != nil || n1 != 1 {
		t.Fatalf("first sweep = %d %v, want 1 nil", n1, err)
	}
	n2, err := postgres.ReleaseStaleHolds(ctx, w.pool, sweepAt, 15*time.Minute)
	if err != nil || n2 != 0 {
		t.Fatalf("second sweep = %d %v, want 0 nil", n2, err)
	}
	var wsHeld int64
	periodStr := period.Format("2006-01-02")
	if err := w.pool.QueryRow(ctx, `
		SELECT held_micro_usd FROM gateway.usage_spend
		WHERE kind = 'workspace' AND subject_id IS NULL AND period_start = $1::date`, periodStr).Scan(&wsHeld); err != nil {
		t.Fatal(err)
	}
	if wsHeld < 0 {
		t.Fatalf("workspace held underflow = %d", wsHeld)
	}
	if wsHeld != 0 {
		t.Fatalf("workspace held = %d, want 0", wsHeld)
	}
}

func TestFeature_UPM422_SettleBeforeSweepNoDoubleRelease(t *testing.T) {
	w := newPinWorld(t, nil)
	ctx := context.Background()
	setUsageLimit(t, w, domain.LimitWorkspace, "", 10_000_000)
	setUsageLimit(t, w, domain.LimitPerson, pinAuthor, 10_000_000)

	const estimate = int64(75)
	period := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	reserveAt := time.Date(2026, 11, 25, 6, 0, 0, 0, time.UTC)
	sweepAt := reserveAt.Add(30 * time.Minute)
	store := postgres.Store{Pool: w.pool, Now: func() time.Time { return reserveAt }}
	res, err := store.Reserve(ctx, app.ReserveRequest{
		UserID: pinAuthor, Source: domain.SourceChat, Estimate: estimate, PeriodStart: period,
	})
	if err != nil {
		t.Fatal(err)
	}
	principal := domain.Principal{User: pinAuthor, Source: domain.SourceChat}
	if err := store.Settle(ctx, res, domain.Entry{
		Route: "pin-house", AccountID: pinHouse, Model: "m1", PlanKind: domain.PlanMetered,
		Class: domain.ClassOK, Principal: principal, PriceSource: "unknown",
	}); err != nil {
		t.Fatal(err)
	}
	var spentBefore, heldBefore int64
	periodStr := period.Format("2006-01-02")
	if err := w.pool.QueryRow(ctx, `
		SELECT spent_micro_usd, held_micro_usd FROM gateway.usage_spend
		WHERE kind = 'workspace' AND subject_id IS NULL AND period_start = $1::date`, periodStr,
	).Scan(&spentBefore, &heldBefore); err != nil {
		t.Fatal(err)
	}
	released, err := postgres.ReleaseStaleHolds(ctx, w.pool, sweepAt, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if released != 0 {
		t.Fatalf("sweep after settle released = %d, want 0", released)
	}
	var spentAfter, heldAfter int64
	if err := w.pool.QueryRow(ctx, `
		SELECT spent_micro_usd, held_micro_usd FROM gateway.usage_spend
		WHERE kind = 'workspace' AND subject_id IS NULL AND period_start = $1::date`, periodStr,
	).Scan(&spentAfter, &heldAfter); err != nil {
		t.Fatal(err)
	}
	if spentAfter != spentBefore || heldAfter != heldBefore {
		t.Fatalf("counters changed: spent %d->%d held %d->%d", spentBefore, spentAfter, heldBefore, heldAfter)
	}
}

func TestFeature_UPM422_GatewayRoleHoldsAccess(t *testing.T) {
	w := newPinWorld(t, nil)
	ctx := context.Background()
	setUsageLimit(t, w, domain.LimitWorkspace, "", 5_000_000)
	gw := pgtest.Login(t, w.pool, "upmo_gateway")
	appPool := pgtest.AppLogin(t, w.pool)
	store := postgres.Store{Pool: gw}

	period := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	res, err := store.Reserve(ctx, app.ReserveRequest{
		UserID: pinAuthor, Source: domain.SourceChat, Estimate: 100, PeriodStart: period,
	})
	if err != nil {
		t.Fatalf("Reserve as upmo_gateway: %v", err)
	}
	if err := store.Release(ctx, res); err != nil {
		t.Fatalf("Release as upmo_gateway: %v", err)
	}
	res, err = store.Reserve(ctx, app.ReserveRequest{
		UserID: pinAuthor, Source: domain.SourceChat, Estimate: 100, PeriodStart: period,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Settle(ctx, res, domain.Entry{
		Route: "pin-house", AccountID: pinHouse, Model: "m1", PlanKind: domain.PlanMetered,
		Class: domain.ClassOK, Principal: domain.Principal{User: pinAuthor, Source: domain.SourceChat},
		PriceSource: "unknown",
	}); err != nil {
		t.Fatalf("Settle as upmo_gateway: %v", err)
	}
	if _, err := gw.Exec(ctx, `UPDATE gateway.usage_holds SET source = 'forged' WHERE false`); sqlState(err) != "42501" {
		t.Fatalf("UPDATE usage_holds as upmo_gateway = %v (code %s), want 42501", err, sqlState(err))
	}
	if _, err := appPool.Exec(ctx, `SELECT 1 FROM gateway.usage_holds`); sqlState(err) != "42501" {
		t.Fatalf("SELECT usage_holds as upmo_app = %v (code %s), want 42501", err, sqlState(err))
	}
	if _, err := appPool.Exec(ctx, `
		INSERT INTO gateway.usage_holds (period_start, amount_micro_usd, source)
		VALUES ('2026-11-01', 1, '')`); sqlState(err) != "42501" {
		t.Fatalf("INSERT usage_holds as upmo_app = %v (code %s), want 42501", err, sqlState(err))
	}
}

func TestFeature_UPM422_SchemaUsageHoldsConstraints(t *testing.T) {
	w := newPinWorld(t, nil)
	ctx := context.Background()
	if _, err := w.pool.Exec(ctx, `
		INSERT INTO gateway.usage_holds (period_start, amount_micro_usd, source)
		VALUES ('2026-11-01', 0, '')`); err == nil {
		t.Fatal("amount_micro_usd <= 0: want CHECK violation")
	}
	if _, err := w.pool.Exec(ctx, `
		INSERT INTO gateway.usage_holds (period_start, amount_micro_usd, source)
		VALUES ('2026-11-01', 1, 'automation')`); err == nil {
		t.Fatal("automation without automation_id: want CHECK violation")
	}
	var indexOK bool
	if err := w.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_indexes
			WHERE schemaname = 'gateway' AND indexname = 'usage_holds_created_at_idx'
		)`).Scan(&indexOK); err != nil || !indexOK {
		t.Fatalf("usage_holds_created_at_idx exists = %v err=%v", indexOK, err)
	}
}

func TestFeature_UPM422_OwnerPoolStaleSweep(t *testing.T) {
	w := newPinWorld(t, nil)
	ctx := context.Background()
	setUsageLimit(t, w, domain.LimitWorkspace, "", 5_000_000)
	period := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	old := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	store := postgres.Store{Pool: w.pool, Now: func() time.Time { return old }}
	if _, err := store.Reserve(ctx, app.ReserveRequest{
		UserID: pinAuthor, Source: domain.SourceChat, Estimate: 50, PeriodStart: period,
	}); err != nil {
		t.Fatal(err)
	}
	n, err := postgres.ReleaseStaleHolds(ctx, w.pool, time.Now().UTC(), 15*time.Minute)
	if err != nil || n != 1 {
		t.Fatalf("owner sweep = %d %v, want 1 nil", n, err)
	}
}

func TestFeature_UPM422_RiverHoldSweepWorkerStarts(t *testing.T) {
	w := newPinWorld(t, nil)
	ctx := context.Background()
	if err := jobs.Up(ctx, w.pool); err != nil {
		t.Fatal(err)
	}
	reg := metrics.NewRegistry()
	logger := gatewayLogger(t)
	workers := river.NewWorkers()
	gateway.AddHoldSweeper(workers, w.pool, logger)
	workClient, err := jobs.Open(w.pool,
		jobs.WithSchema(jobs.WorkerSchema),
		jobs.WithWorkers(workers),
		jobs.WithPeriodic(gateway.HoldSweepPeriodic()),
		jobs.WithMetrics(reg),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := workClient.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := workClient.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func gatewayLogger(t *testing.T) *slog.Logger {
	t.Helper()
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
}