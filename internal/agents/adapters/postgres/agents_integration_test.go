//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/agents"
	"github.com/upmo-ai/upmo/internal/agents/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/agents/app"
	"github.com/upmo-ai/upmo/internal/agents/domain"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

const (
	ownerUser = "018f3b2c-7a10-7c4a-8e11-111111111111"
	otherUser = "018f3b2c-7a10-7c4a-8e11-222222222222"
	adminUser = "018f3b2c-7a10-7c4a-8e11-999999999999"
)

var (
	memberAllow  = "agent:create,agent:view,agent:edit,agent:delete,agent:share"
	adminAllow   = memberAllow + ",settings:manage"
	memberAccess = app.Access{UserID: ownerUser, Allow: memberAllow}
	otherAccess  = app.Access{UserID: otherUser, Allow: memberAllow}
	adminAccess  = app.Access{UserID: adminUser, Allow: adminAllow}
)

func up(t *testing.T) *pgxpool.Pool {
	t.Helper()
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := audit.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, agents.Schema, agents.Migrations()); err != nil {
		t.Fatal(err)
	}
	return owner
}

func TestAgentVersioningAndImmutability(t *testing.T) {
	owner := up(t)
	appPool := pgtest.App(t, owner)
	store := postgres.Store{Pool: appPool}
	ctx := context.Background()

	made, err := app.CreateAgent(ctx, store, nil, memberAccess, app.AgentFields{
		Name:          "Bookkeeper",
		Role:          "AP Clerk",
		Instructions:  "Verify VAT.",
		DefaultRoute:  "work",
		ToolClasses:   []string{"connector"},
		PlanApproval:  domain.PlanApprovalAlways,
		CheckCriteria: []json.RawMessage{json.RawMessage(`{"rule":"vat"}`)},
		Enabled:       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if made.Version != 1 || made.Slug != "bookkeeper" || made.PlanApproval != domain.PlanApprovalAlways {
		t.Fatalf("created = %+v", made)
	}
	versions, err := app.ListVersions(ctx, store, memberAccess, made.ID)
	if err != nil || len(versions) != 1 || versions[0].Version != 1 || len(versions[0].Digest) != 64 {
		t.Fatalf("v1 versions = %+v %v", versions, err)
	}

	// Repeat update with identical fields does not bump version.
	same, err := app.ChangeAgent(ctx, store, nil, memberAccess, made.ID, app.AgentChange{})
	if err != nil || same.Version != 1 {
		t.Fatalf("no-op update = %+v %v", same, err)
	}

	newInst := "Verify VAT and PO."
	updated, err := app.ChangeAgent(ctx, store, nil, memberAccess, made.ID, app.AgentChange{Instructions: &newInst})
	if err != nil || updated.Version != 2 {
		t.Fatalf("updated = %+v %v", updated, err)
	}
	versions, err = app.ListVersions(ctx, store, memberAccess, made.ID)
	if err != nil || len(versions) != 2 || versions[0].Version != 2 || versions[1].Version != 1 {
		t.Fatalf("v2 versions = %+v %v", versions, err)
	}
	if versions[0].Digest == versions[1].Digest {
		t.Fatal("v1 and v2 digests must differ")
	}

	// agents.versions is immutable for upmo_app: UPDATE and DELETE are refused.
	tx, err := appPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('upmo.user_id', $1, true), set_config('upmo.allow', $2, true)`,
		ownerUser, memberAllow); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE agents.versions SET digest = $1 WHERE agent_id = $2::uuid`, strings.Repeat("0", 64), made.ID); err == nil {
		t.Fatal("app role updated immutable agents.versions")
	}
	_ = tx.Rollback(ctx)

	tx2, err := appPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx2.Rollback(ctx) }()
	if _, err := tx2.Exec(ctx, `SELECT set_config('upmo.user_id', $1, true), set_config('upmo.allow', $2, true)`,
		ownerUser, memberAllow); err != nil {
		t.Fatal(err)
	}
	if _, err := tx2.Exec(ctx, `DELETE FROM agents.versions WHERE agent_id = $1::uuid`, made.ID); err == nil {
		t.Fatal("app role deleted immutable agents.versions")
	}
}

func TestTwoTierHouseRulesAndVersioning(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	at := time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)

	a, err := app.CreateAgent(ctx, store, nil, memberAccess, app.AgentFields{
		Name: "Auditor", Instructions: "Base rules.", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := app.CreateAgent(ctx, store, nil, memberAccess, app.AgentFields{
		Name: "Other", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Non-inline lesson: does NOT bump version, appears in Index (no body), opens via Open.
	cardID, err := app.SaveLesson(ctx, store, memberAccess, a.ID, app.LessonFields{
		Title: "FX rounding", Cue: "When multi-currency invoice", Body: "Round half up SECRET_VALUE_1", AlwaysInline: false,
	}, at)
	if err != nil {
		t.Fatal(err)
	}
	gotA, err := app.GetAgent(ctx, store, memberAccess, a.ID)
	if err != nil || gotA.Version != 1 {
		t.Fatalf("after non-inline save version = %d %v, want 1", gotA.Version, err)
	}

	// Always-inline lesson: bumps version to 2 and appears in InlineLessons & AgentSystem.
	inlineID, err := app.SaveLesson(ctx, store, memberAccess, a.ID, app.LessonFields{
		Title: "Compliance", Cue: "Always", Body: "Never book without invoice PDF. Contact ada@example.com.", AlwaysInline: true,
	}, at)
	if err != nil {
		t.Fatal(err)
	}
	gotA, err = app.GetAgent(ctx, store, memberAccess, a.ID)
	if err != nil || gotA.Version != 2 {
		t.Fatalf("after inline save version = %d %v, want 2", gotA.Version, err)
	}

	inline, err := app.AgentInlineLessons(ctx, store, a.ID)
	if err != nil || len(inline) != 1 || inline[0].Title != "Compliance" || strings.Contains(inline[0].Body, "ada@example.com") {
		t.Fatalf("inline = %+v %v", inline, err)
	}
	cards, err := app.AgentIndex(ctx, store, a.ID)
	if err != nil || len(cards) != 1 || cards[0].Title != "FX rounding" || strings.Contains(cards[0].Cue, "SECRET_VALUE_1") {
		t.Fatalf("cards = %+v %v", cards, err)
	}
	opened, err := app.AgentOpen(ctx, store, a.ID, cardID)
	if err != nil || !strings.Contains(opened.Body, "SECRET_VALUE_1") {
		t.Fatalf("opened = %+v %v", opened, err)
	}
	if _, err := app.AgentOpen(ctx, store, other.ID, cardID); !errors.Is(err, domain.ErrLessonGone) {
		t.Fatalf("cross-agent open = %v, want ErrLessonGone", err)
	}

	// Removing an always_inline lesson bumps version to 3.
	if err := app.RemoveLesson(ctx, store, memberAccess, a.ID, inlineID, at); err != nil {
		t.Fatal(err)
	}
	gotA, err = app.GetAgent(ctx, store, memberAccess, a.ID)
	if err != nil || gotA.Version != 3 {
		t.Fatalf("after inline remove version = %d %v, want 3", gotA.Version, err)
	}

	// Index cap of 24 non-inline lessons.
	for i := 2; i <= domain.LessonIndexCap; i++ {
		if _, err := app.SaveLesson(ctx, store, memberAccess, a.ID, app.LessonFields{
			Title: fmt.Sprintf("Card %02d", i), Cue: "When needed", Body: "Body", AlwaysInline: false,
		}, at); err != nil {
			t.Fatalf("save card %d: %v", i, err)
		}
	}
	if _, err := app.SaveLesson(ctx, store, memberAccess, a.ID, app.LessonFields{
		Title: "Card 25", Cue: "When needed", Body: "Body", AlwaysInline: false,
	}, at); !errors.Is(err, domain.ErrLessonFull) {
		t.Fatalf("25th card = %v, want ErrLessonFull", err)
	}
}

func TestConcurrentUpdateAgentVersioning(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()

	made, err := app.CreateAgent(ctx, store, nil, memberAccess, app.AgentFields{
		Name:         "Concurrent",
		Instructions: "v1",
		Enabled:      true,
	})
	if err != nil {
		t.Fatal(err)
	}

	const workers = 4
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			inst := fmt.Sprintf("update-%d", idx)
			_, err := app.ChangeAgent(ctx, store, nil, memberAccess, made.ID, app.AgentChange{
				Instructions: &inst,
			})
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent ChangeAgent failed: %v", err)
		}
	}

	versions, err := app.ListVersions(ctx, store, memberAccess, made.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != workers+1 {
		t.Fatalf("versions count = %d, want %d", len(versions), workers+1)
	}
	if versions[0].Version != workers+1 {
		t.Fatalf("latest version = %d, want %d", versions[0].Version, workers+1)
	}
}
