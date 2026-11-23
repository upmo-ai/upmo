//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/chat/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/platform/agentloop"
)

const claimToken = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaa01"
const outReplyID = "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"

// claimRun says into a fresh chat and claims its round-1 run.
func claimRun(t *testing.T, ctx context.Context, store postgres.Store, owner *pgxpool.Pool, chatID string) (app.Claim, string) {
	t.Helper()
	if err := app.Say(ctx, store, ownerAccess, chatID, "hello", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	var runID, lineID string
	if err := owner.QueryRow(ctx, `SELECT id::text, line_id::text FROM chat.runs WHERE chat_id = $1::uuid`, chatID).Scan(&runID, &lineID); err != nil {
		t.Fatal(err)
	}
	c, err := store.Claim(ctx, ownerAccess, app.ReplyJobArgs{ChatID: chatID, LineID: lineID, ReplyID: runID, Round: 1, UserID: ownerUser}, claimToken)
	if err != nil || c.State != app.ClaimGo {
		t.Fatalf("claim = %+v %v", c, err)
	}
	return c, runID
}

func TestCommitRoundWritesANilHistoryAsAnEmptyArray(t *testing.T) {
	owner := up(t)
	store := quietStore(t, owner)
	ctx := context.Background()
	opened, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	c, runID := claimRun(t, ctx, store, owner, opened.ID)
	err = store.CommitRound(ctx, ownerAccess, c, app.RoundCommit{
		Turn: agentloop.TurnCommit{
			RunID: runID, ObjectID: opened.ID, SeqStart: 1,
			Messages: []agentloop.Message{{Role: agentloop.RoleAssistant, Text: "x"}},
			History:  nil,
			Status:   agentloop.TurnContinue,
			Model:    "m1",
		},
		End: app.RoundNext,
	})
	if err != nil {
		t.Fatal(err)
	}
	var typeof string
	var length, round int
	if err := owner.QueryRow(ctx, `SELECT jsonb_typeof(history), jsonb_array_length(history), round FROM chat.runs WHERE id = $1::uuid`, runID).Scan(&typeof, &length, &round); err != nil {
		t.Fatal(err)
	}
	if typeof != "array" || length != 0 || round != 1 {
		t.Fatalf("history typeof=%s len=%d round=%d", typeof, length, round)
	}
}

func TestCommitRoundRefusesAHold(t *testing.T) {
	owner := up(t)
	store := quietStore(t, owner)
	ctx := context.Background()
	opened, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	c, runID := claimRun(t, ctx, store, owner, opened.ID)
	hold := agentloop.BuildBatchHold("ask", []agentloop.HeldCall{
		agentloop.BuildHeldCall(agentloop.ToolCall{ID: "c1", Name: "send_email"}, agentloop.ToolDef{Op: agentloop.OpChange}, "email", false),
	})
	err = store.CommitRound(ctx, ownerAccess, c, app.RoundCommit{
		Turn: agentloop.TurnCommit{
			Hold:     &hold,
			Messages: []agentloop.Message{{Role: agentloop.RoleAssistant, Text: "x"}},
			Status:   agentloop.TurnWaitingApproval,
		},
		End: app.RoundDone,
	})
	if !errors.Is(err, app.ErrHold) {
		t.Fatalf("CommitRound = %v, want ErrHold", err)
	}
	var holds, turns, round int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM chat.holds`).Scan(&holds); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM chat.turns WHERE chat_id = $1::uuid`, opened.ID).Scan(&turns); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `SELECT round FROM chat.runs WHERE id = $1::uuid`, runID).Scan(&round); err != nil {
		t.Fatal(err)
	}
	if holds != 0 || turns != 0 || round != 0 {
		t.Fatalf("holds=%d turns=%d round=%d", holds, turns, round)
	}
}

func TestAnOutputOfAnotherChatIsNotRetrievable(t *testing.T) {
	owner := up(t)
	store := quietStore(t, owner)
	ctx := context.Background()
	a, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	b, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	outsA := store.Outputs(a.ID, outReplyID, func() (app.Access, error) { return ownerAccess, nil })
	id, err := outsA.SaveOutput(ctx, &agentloop.RunState{ObjectID: a.ID, UserID: ownerUser}, "T", "body", false)
	if err != nil {
		t.Fatal(err)
	}
	outsB := store.Outputs(b.ID, outReplyID, func() (app.Access, error) { return ownerAccess, nil })
	if _, _, err := outsB.RetrieveOutput(ctx, &agentloop.RunState{ObjectID: b.ID}, id); err == nil || err.Error() != "no such output" {
		t.Fatalf("cross-chat read = %v", err)
	}
	outsA2 := store.Outputs(a.ID, outReplyID, func() (app.Access, error) { return ownerAccess, nil })
	if body, untrusted, err := outsA2.RetrieveOutput(ctx, &agentloop.RunState{ObjectID: a.ID}, id); err != nil || body != "body" || untrusted {
		t.Fatalf("own read = %q %v %v", body, untrusted, err)
	}
}

func TestAnOutputOverTheCapIsCutWithAMarker(t *testing.T) {
	owner := up(t)
	store := quietStore(t, owner)
	ctx := context.Background()
	opened, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	outs := store.Outputs(opened.ID, outReplyID, func() (app.Access, error) { return ownerAccess, nil })
	id, err := outs.SaveOutput(ctx, &agentloop.RunState{ObjectID: opened.ID, UserID: ownerUser}, "T", strings.Repeat("x", 300000), false)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	var body string
	if err := owner.QueryRow(ctx, `SELECT octet_length(body), body FROM chat.findings WHERE id = $1::uuid`, id).Scan(&n, &body); err != nil {
		t.Fatal(err)
	}
	if n != 262144 {
		t.Fatalf("octet_length = %d", n)
	}
	if !strings.HasSuffix(body, "\n[output truncated: 300000 bytes in all]") {
		t.Fatalf("body ends %q", body[len(body)-40:])
	}

	// Multi-byte runes are cut on a rune start.
	id2, err := outs.SaveOutput(ctx, &agentloop.RunState{ObjectID: opened.ID, UserID: ownerUser}, "T", strings.Repeat("é", 150000), false)
	if err != nil {
		t.Fatal(err)
	}
	var body2 string
	if err := owner.QueryRow(ctx, `SELECT body FROM chat.findings WHERE id = $1::uuid`, id2).Scan(&body2); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(body2, "\n[output truncated: 300000 bytes in all]") {
		t.Fatalf("rune body ends %q", body2[len(body2)-40:])
	}
}

func TestRunsAreReadByEditorsOnly(t *testing.T) {
	owner := up(t)
	store := quietStore(t, owner)
	ctx := context.Background()
	opened, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Say(ctx, store, ownerAccess, opened.ID, "hello", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	count := func(scope agentloop.AccessScope) int {
		t.Helper()
		var n int
		if err := store.NarrowedTx(ctx, scope, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM chat.runs WHERE chat_id = $1::uuid`, opened.ID).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got := count(agentloop.AccessScope{UserID: ownerUser, Allow: ownerAccess.Allow, ObjectID: opened.ID}); got != 1 {
		t.Fatalf("owner runs = %d", got)
	}
	if got := count(agentloop.AccessScope{UserID: otherUser, Allow: "chat:view", SharedView: "{" + opened.ID + "}", ObjectID: opened.ID}); got != 0 {
		t.Fatalf("viewer runs = %d", got)
	}
	if got := count(agentloop.AccessScope{UserID: otherUser, Allow: "chat:view,chat:edit", ObjectID: opened.ID}); got != 0 {
		t.Fatalf("stranger runs = %d", got)
	}
}
