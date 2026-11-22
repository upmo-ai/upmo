//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

const staleOwner = "018f3b2c-7a10-7c4a-8e11-cccccccccccc"

// TestStaleOutcomeDoesNotOverwriteAReset runs the two writers against one
// account: a model call whose outcome started before an edit, and the edit
// that resets the status to Not connected. The stale outcome is dropped and
// a fresh one still lands.
func TestStaleOutcomeDoesNotOverwriteAReset(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.New(t)
	if err := migrate.Up(ctx, pool, gateway.Schema, gateway.Migrations()); err != nil {
		t.Fatal(err)
	}
	providers := postgres.Providers{Pool: pgtest.App(t, pool)}
	store := postgres.Store{Pool: pool}

	made, err := providers.Create(ctx, app.Caller{User: staleOwner}, domain.Provider{
		Provider:      "fake",
		Name:          "stale-check",
		Adapter:       domain.AdapterFake,
		PlanKind:      domain.PlanLocal,
		Status:        domain.StatusNotConnected,
		Models:        []string{"m1"},
		Own:           true,
		CredentialRef: "own_stale",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// The attempt starts, then the person edits the account, which resets
	// the status.
	started := time.Now()
	stale := made
	stale.Name = "renamed"
	if _, err := providers.Update(ctx, app.Caller{User: staleOwner}, stale, nil, true); err != nil {
		t.Fatal(err)
	}

	if err := store.RecordStatus(ctx, made.CredentialRef, domain.StatusConnected, started); err != nil {
		t.Fatal(err)
	}
	got, err := providers.Get(ctx, app.Caller{User: staleOwner}, made.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusNotConnected {
		t.Fatalf("a stale outcome overwrote the reset: status = %q", got.Status)
	}

	// An outcome that started after the reset still writes.
	if err := store.RecordStatus(ctx, made.CredentialRef, domain.StatusConnected, time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err = providers.Get(ctx, app.Caller{User: staleOwner}, made.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusConnected {
		t.Fatalf("a fresh outcome was dropped: status = %q", got.Status)
	}
}

// TestALaterOutcomeOfARunningCallStillLands is a fallback: the first hop is
// rate limited, the next hop starts before that outcome is stored and then
// answers. The answer is newer and must win, so the account reads Connected.
func TestALaterOutcomeOfARunningCallStillLands(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.New(t)
	if err := migrate.Up(ctx, pool, gateway.Schema, gateway.Migrations()); err != nil {
		t.Fatal(err)
	}
	providers := postgres.Providers{Pool: pgtest.App(t, pool)}
	store := postgres.Store{Pool: pool}
	made, err := providers.Create(ctx, app.Caller{User: staleOwner}, domain.Provider{
		Provider:      "fake",
		Name:          "fallback-check",
		Adapter:       domain.AdapterFake,
		PlanKind:      domain.PlanLocal,
		Status:        domain.StatusNotConnected,
		Models:        []string{"m1"},
		Own:           true,
		CredentialRef: "own_fallback",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	firstHop := time.Now()
	// A millisecond apart, so the two starts never round to one stored value.
	nextHop := firstHop.Add(time.Millisecond)
	if err := store.RecordStatus(ctx, made.CredentialRef, domain.StatusRateLimited, firstHop); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordStatus(ctx, made.CredentialRef, domain.StatusConnected, nextHop); err != nil {
		t.Fatal(err)
	}
	got, err := providers.Get(ctx, app.Caller{User: staleOwner}, made.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusConnected {
		t.Fatalf("the answer of the next hop was dropped: status = %q, want %q", got.Status, domain.StatusConnected)
	}

	// A slow call that started before the answer and ends after it says
	// nothing newer: Connected stays.
	if err := store.RecordStatus(ctx, made.CredentialRef, domain.StatusOffline, firstHop); err != nil {
		t.Fatal(err)
	}
	got, err = providers.Get(ctx, app.Caller{User: staleOwner}, made.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.StatusConnected {
		t.Fatalf("an older call overwrote a newer outcome: status = %q, want %q", got.Status, domain.StatusConnected)
	}
}

// TestAnUnchangedStatusIsSkippedUntilAReset walks the write the recorder
// makes: the same outcome inside a minute is skipped, an edit resets the
// account to Not connected, and then the same outcome lands again.
func TestAnUnchangedStatusIsSkippedUntilAReset(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.New(t)
	if err := migrate.Up(ctx, pool, gateway.Schema, gateway.Migrations()); err != nil {
		t.Fatal(err)
	}
	providers := postgres.Providers{Pool: pgtest.App(t, pool)}
	store := postgres.Store{Pool: pool}

	made, err := providers.Create(ctx, app.Caller{User: staleOwner}, domain.Provider{
		Provider:      "fake",
		Name:          "dedupe-check",
		Adapter:       domain.AdapterFake,
		PlanKind:      domain.PlanLocal,
		Status:        domain.StatusNotConnected,
		Models:        []string{"m1"},
		Own:           true,
		CredentialRef: "own_dedupe",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	checked := func() time.Time {
		var at time.Time
		if err := pool.QueryRow(ctx,
			`SELECT checked_at FROM gateway.provider_accounts WHERE id = $1::uuid`, made.ID).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	status := func() string {
		got, err := providers.Get(ctx, app.Caller{User: staleOwner}, made.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got.Status
	}

	// The first outcome lands and marks the account checked.
	if err := store.RecordStatus(ctx, made.CredentialRef, domain.StatusConnected, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := status(); got != domain.StatusConnected {
		t.Fatalf("the first outcome was dropped: status = %q", got)
	}

	// The same outcome inside a minute writes nothing: checked_at stays.
	first := checked()
	time.Sleep(20 * time.Millisecond)
	if err := store.RecordStatus(ctx, made.CredentialRef, domain.StatusConnected, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := checked(); !got.Equal(first) {
		t.Fatalf("an unchanged outcome inside a minute wrote again: %v, was %v", got, first)
	}

	// The person edits the account, which resets the status, and then the
	// same outcome starts again: it lands.
	edited := made
	edited.Name = "renamed"
	if _, err := providers.Update(ctx, app.Caller{User: staleOwner}, edited, nil, true); err != nil {
		t.Fatal(err)
	}
	if got := status(); got != domain.StatusNotConnected {
		t.Fatalf("the edit did not reset the account: status = %q", got)
	}
	if err := store.RecordStatus(ctx, made.CredentialRef, domain.StatusConnected, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := status(); got != domain.StatusConnected {
		t.Fatalf("the same outcome after a reset was dropped: status = %q", got)
	}
}
