//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/chat"
	"github.com/upmo-ai/upmo/internal/chat/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/agentloop"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestChatSingleTeamRLSNarrowingHoldsTurnsAndActionLog(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := audit.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, chat.Schema, chat.Migrations()); err != nil {
		t.Fatal(err)
	}
	store := postgres.Store{Pool: pgtest.App(t, owner)}

	teamA := "018f3b2c-7a10-7c4a-8e11-bbbb00000001"
	teamB := "018f3b2c-7a10-7c4a-8e11-bbbb00000002"
	ownerAccess := app.Access{UserID: ownerUser, Allow: "chat:create,chat:view,chat:edit"}

	chatA, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	chatB, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `UPDATE chat.chats SET visibility = 'team', visibility_team_id = $2::uuid WHERE id = $1::uuid`, chatA.ID, teamA); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `UPDATE chat.chats SET visibility = 'team', visibility_team_id = $2::uuid WHERE id = $1::uuid`, chatB.ID, teamB); err != nil {
		t.Fatal(err)
	}

	const thirdUser = "018f3b2c-7a10-7c4a-8e11-333333333333"
	scope := agentloop.AccessScope{
		UserID:           thirdUser,
		Allow:            "chat:view,chat:edit",
		UserTeams:        []string{teamA, teamB},
		ObjectID:         chatA.ID,
		ObjectVisibility: agentloop.VisibilityTeam,
		VisibilityTeamID: teamA,
	}
	if err := store.NarrowedTx(ctx, scope, func(tx pgx.Tx) error {
		var countA, countB int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM chat.chats WHERE id = $1::uuid`, chatA.ID).Scan(&countA); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM chat.chats WHERE id = $1::uuid`, chatB.ID).Scan(&countB); err != nil {
			return err
		}
		if countA != 1 || countB != 0 {
			t.Fatalf("chat narrowed visibility: A=%d B=%d", countA, countB)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	ownerScope := agentloop.AccessScope{
		UserID:           ownerUser,
		Allow:            ownerAccess.Allow,
		UserTeams:        []string{teamA},
		ObjectID:         chatA.ID,
		ObjectVisibility: agentloop.VisibilityTeam,
		VisibilityTeamID: teamA,
	}
	batch := agentloop.BuildBatchHold("ask", []agentloop.HeldCall{
		agentloop.BuildHeldCall(agentloop.ToolCall{ID: "ch1", Name: "send_email", Arguments: json.RawMessage(`{"to":"a@b.com"}`)}, agentloop.ToolDef{Op: agentloop.OpChange}, "email", true),
	})
	holdID, err := store.SaveHold(ctx, ownerScope, chatA.ID, "", batch)
	if err != nil || holdID == "" {
		t.Fatalf("SaveHold = %q %v", holdID, err)
	}

	if err := store.NarrowedTx(ctx, ownerScope, func(tx pgx.Tx) error {
		return postgres.InsertTurnRows(ctx, tx, agentloop.TurnCommit{
			ObjectID: chatA.ID,
			SeqStart: 1,
			Messages: []agentloop.Message{{Role: agentloop.RoleAssistant, Text: "holding"}},
			Model:    "m1",
			Exposure: agentloop.ExposureState{Tainted: true},
		})
	}); err != nil {
		t.Fatal(err)
	}

	alog := postgres.ChatActionLog{Store: store, Scope: ownerScope}
	rec := agentloop.ActionRecord{
		CallID:   "chat-call-1",
		ObjectID: chatA.ID,
		UserID:   ownerUser,
		ToolName: "send_email",
		Action:   "change send_email",
		Preview:  "change send_email()",
	}
	if err := alog.BeginDial(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := alog.BeginDial(ctx, rec); !errors.Is(err, agentloop.ErrDuplicateAction) {
		t.Fatalf("chat replay while dialing = %v, want ErrDuplicateAction", err)
	}
	if err := alog.FinishDial(ctx, chatA.ID, rec.CallID, agentloop.ActionStateDone, "sha256:xyz", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	// Failed state retry under upmo_app must succeed without touching executed_at.
	failedRec := agentloop.ActionRecord{
		CallID:   "chat-call-failed-1",
		ObjectID: chatA.ID,
		UserID:   ownerUser,
		ToolName: "send_email",
		Action:   "change send_email",
		Preview:  "change send_email()",
	}
	if err := alog.BeginDial(ctx, failedRec); err != nil {
		t.Fatal(err)
	}
	if err := alog.FinishDial(ctx, chatA.ID, failedRec.CallID, agentloop.ActionStateFailed, "", "network error", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := alog.BeginDial(ctx, failedRec); err != nil {
		t.Fatalf("chat retry after failed = %v", err)
	}
}

func TestChatActionLogScopedAndCommitTurnAndEnqueue(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := audit.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, chat.Schema, chat.Migrations()); err != nil {
		t.Fatal(err)
	}
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ownerAccess := app.Access{UserID: ownerUser, Allow: "chat:create,chat:view,chat:edit"}

	chat1, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	chat2, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}

	// Same provider call_id = "call_0" across two chats succeeds independently!
	alog1 := postgres.ChatActionLog{Store: store, Scope: agentloop.AccessScope{UserID: ownerUser, Allow: ownerAccess.Allow, ObjectID: chat1.ID}}
	alog2 := postgres.ChatActionLog{Store: store, Scope: agentloop.AccessScope{UserID: ownerUser, Allow: ownerAccess.Allow, ObjectID: chat2.ID}}

	rec1 := agentloop.ActionRecord{CallID: "call_0", ObjectID: chat1.ID, UserID: ownerUser, ToolName: "send_email", Action: "change send_email"}
	rec2 := agentloop.ActionRecord{CallID: "call_0", ObjectID: chat2.ID, UserID: ownerUser, ToolName: "send_email", Action: "change send_email"}

	if err := alog1.BeginDial(ctx, rec1); err != nil {
		t.Fatalf("chat1 call_0 BeginDial: %v", err)
	}
	if err := alog2.BeginDial(ctx, rec2); err != nil {
		t.Fatalf("chat2 call_0 BeginDial collided with chat1: %v", err)
	}
	if err := alog1.FinishDial(ctx, chat1.ID, "call_0", agentloop.ActionStateDone, "sha256:c1", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := alog2.FinishDial(ctx, chat2.ID, "call_0", agentloop.ActionStateDone, "sha256:c2", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := alog1.BeginDial(ctx, rec1); !errors.Is(err, agentloop.ErrDuplicateAction) {
		t.Fatalf("chat1 replay = %v, want ErrDuplicateAction", err)
	}

	// ChatTurnStore.CommitTurnAndEnqueue persists turns, holds, and invokes EnqueueNext.
	var enqueued int
	turnStore := postgres.ChatTurnStore{
		Store: store,
		Scope: agentloop.AccessScope{UserID: ownerUser, Allow: ownerAccess.Allow, ObjectID: chat1.ID},
		EnqueueNext: func(_ context.Context, _ pgx.Tx, commit agentloop.TurnCommit) error {
			enqueued++
			if commit.ObjectID != chat1.ID {
				t.Fatalf("commit ObjectID = %q, want %q", commit.ObjectID, chat1.ID)
			}
			return nil
		},
	}

	if err := turnStore.CommitTurnAndEnqueue(ctx, agentloop.TurnCommit{
		ObjectID:    chat1.ID,
		UserID:      ownerUser,
		SeqStart:    1,
		Messages:    []agentloop.Message{{Role: agentloop.RoleAssistant, Text: "checking"}, {Role: agentloop.RoleTool, ToolName: "search", ToolID: "t1", Text: "found"}},
		Model:       "m1",
		EnqueueNext: true,
	}); err != nil {
		t.Fatal(err)
	}
	if enqueued != 1 {
		t.Fatalf("enqueued = %d, want 1", enqueued)
	}

	hold := agentloop.BuildBatchHold("ask", []agentloop.HeldCall{
		agentloop.BuildHeldCall(agentloop.ToolCall{ID: "ch2", Name: "send_email", Arguments: json.RawMessage(`{"to":"a@b.com"}`)}, agentloop.ToolDef{Op: agentloop.OpChange}, "email", true),
	})
	if err := turnStore.CommitTurnAndEnqueue(ctx, agentloop.TurnCommit{
		ObjectID: chat1.ID,
		UserID:   ownerUser,
		SeqStart: 3,
		Messages: []agentloop.Message{{Role: agentloop.RoleAssistant, Text: "approval needed"}, {Role: agentloop.RoleTool, ToolName: "send_email", ToolID: "ch2", Text: "awaiting approval"}},
		Model:    "m1",
		Exposure: agentloop.ExposureState{Tainted: true},
		Hold:     &hold,
	}); err != nil {
		t.Fatal(err)
	}
	var turnCount, holdCount int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM chat.turns WHERE chat_id = $1::uuid`, chat1.ID).Scan(&turnCount); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM chat.holds WHERE chat_id = $1::uuid AND state = 'pending'`, chat1.ID).Scan(&holdCount); err != nil {
		t.Fatal(err)
	}
	if turnCount != 4 || holdCount != 1 || enqueued != 1 {
		t.Fatalf("after chat commit: turns=%d holds=%d enqueued=%d", turnCount, holdCount, enqueued)
	}
}

func TestChatActionLogCrossTenantRLSIsolationAndUniqueViolation(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := audit.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, chat.Schema, chat.Migrations()); err != nil {
		t.Fatal(err)
	}
	appPool := pgtest.App(t, owner)
	store := postgres.Store{Pool: appPool}

	otherCreateAccess := app.Access{UserID: otherUser, Allow: "chat:create,chat:view,chat:edit"}
	chatA, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	chatB, err := app.Open(ctx, store, otherCreateAccess)
	if err != nil {
		t.Fatal(err)
	}

	callID := agentloop.ScopedCallID(0, "call_0")
	alogA := postgres.ChatActionLog{Store: store, Scope: agentloop.AccessScope{UserID: ownerUser, Allow: ownerAccess.Allow, ObjectID: chatA.ID}}
	alogB := postgres.ChatActionLog{Store: store, Scope: agentloop.AccessScope{UserID: otherUser, Allow: otherCreateAccess.Allow, ObjectID: chatB.ID}}

	recA := agentloop.ActionRecord{CallID: callID, ObjectID: chatA.ID, UserID: ownerUser, ToolName: "send_email", Action: "change send_email"}
	recB := agentloop.ActionRecord{CallID: callID, ObjectID: chatB.ID, UserID: otherUser, ToolName: "send_email", Action: "change send_email"}

	if err := alogA.BeginDial(ctx, recA); err != nil {
		t.Fatalf("user A BeginDial: %v", err)
	}
	if err := alogB.BeginDial(ctx, recB); err != nil {
		t.Fatalf("user B BeginDial collided with user A: %v", err)
	}
	if err := alogA.FinishDial(ctx, chatA.ID, callID, agentloop.ActionStateDone, "sha256:chat-a", "", time.Now()); err != nil {
		t.Fatal(err)
	}

	// User B cannot observe User A's row via SELECT, cannot BeginDial into chatA, and cannot FinishDial User A's row.
	forgedB := postgres.ChatActionLog{Store: store, Scope: agentloop.AccessScope{UserID: otherUser, Allow: otherCreateAccess.Allow, ObjectID: chatA.ID}}
	if err := store.NarrowedTx(ctx, forgedB.Scope, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM chat.action_logs WHERE scope_id = $1::uuid AND call_id = $2`, chatA.ID, callID).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatalf("user B observed user A chat action_log row: count=%d", count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := forgedB.BeginDial(ctx, agentloop.ActionRecord{
		CallID: callID, ObjectID: chatA.ID, UserID: otherUser, ToolName: "send_email", Action: "change send_email",
	}); err == nil {
		t.Fatal("user B BeginDial on user A chat succeeded; expected RLS refusal")
	}
	if err := forgedB.FinishDial(ctx, chatA.ID, callID, agentloop.ActionStateFailed, "", "tamper", time.Now()); !errors.Is(err, app.ErrGone) {
		t.Fatalf("user B FinishDial on user A chat row = %v, want app.ErrGone", err)
	}

	// User A's row remains intact, and User B's own row finishes cleanly.
	var stateA, digestA string
	if err := owner.QueryRow(ctx, `SELECT state, response_digest FROM chat.action_logs WHERE scope_id = $1::uuid AND call_id = $2`, chatA.ID, callID).Scan(&stateA, &digestA); err != nil {
		t.Fatal(err)
	}
	if stateA != string(agentloop.ActionStateDone) || digestA != "sha256:chat-a" {
		t.Fatalf("user A chat row mutated: state=%q digest=%q", stateA, digestA)
	}
	if err := alogB.FinishDial(ctx, chatB.ID, callID, agentloop.ActionStateDone, "sha256:chat-b", "", time.Now()); err != nil {
		t.Fatal(err)
	}

	// Concurrent INSERT hitting SQLSTATE 23505 maps to agentloop.ErrDuplicateAction.
	assertChatBeginDial23505(t, ctx, owner, appPool, alogA, chatA.ID)
}

func assertChatBeginDial23505(t *testing.T, ctx context.Context, owner, appPool *pgxpool.Pool, alog postgres.ChatActionLog, chatID string) {
	t.Helper()
	concurrentCallID := agentloop.ScopedCallID(2, "call_concurrent")
	tx, err := appPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := agentloop.ApplyRLSTx(ctx, tx, agentloop.NarrowRLS(alog.Scope)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO chat.action_logs (
			scope_id, call_id, chat_id, user_id, tool_name, action, preview, state
		) VALUES ($1::uuid, $2, $1::uuid, $3::uuid, 'send_email', 'change send_email', '', 'dialing')`,
		chatID, concurrentCallID, ownerUser); err != nil {
		t.Fatal(err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- alog.BeginDial(ctx, agentloop.ActionRecord{
			CallID:   concurrentCallID,
			ObjectID: chatID,
			UserID:   ownerUser,
			ToolName: "send_email",
			Action:   "change send_email",
		})
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		if qerr := owner.QueryRow(ctx, `
			SELECT count(*) FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND query LIKE '%INSERT INTO chat.action_logs%'`).Scan(&waiting); qerr == nil && waiting > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if got := <-errCh; !errors.Is(got, agentloop.ErrDuplicateAction) {
		t.Fatalf("concurrent BeginDial 23505 = %v, want ErrDuplicateAction", got)
	}
}
