//go:build integration

package postgres_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/platform/agentloop"
)

func editorOf(chatID string) app.Access {
	return app.Access{UserID: otherUser, Allow: "chat:view,chat:edit", SharedView: "{" + chatID + "}", SharedEdit: "{" + chatID + "}"}
}

func scopeOf(access app.Access, chatID string) agentloop.AccessScope {
	return agentloop.AccessScope{UserID: access.UserID, Allow: access.Allow, SharedView: access.SharedView, SharedEdit: access.SharedEdit, ObjectID: chatID}
}

func TestKeepChatTriggerEnforcesMonotonicExposureAndOwnerLock(t *testing.T) {
	owner := up(t)
	store := quietStore(t, owner)
	ctx := context.Background()
	opened, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	id := opened.ID
	exec := func(access app.Access, sql string, args ...any) error {
		return store.NarrowedTx(ctx, scopeOf(access, id), func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, sql, args...)
			return err
		})
	}
	editor := editorOf(id)

	if err := exec(ownerAccess, `UPDATE chat.chats SET tainted = true WHERE id = $1::uuid`, id); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("direct tainted update = %v", err)
	}
	if err := exec(otherAccess, `SELECT chat.advance_chat_exposure($1::uuid, true, true)`, id); err == nil || !strings.Contains(err.Error(), "chat: access") {
		t.Fatalf("stranger advance = %v", err)
	}
	if err := exec(editor, `SELECT chat.advance_chat_exposure($1::uuid, true, false)`, id); err != nil {
		t.Fatal(err)
	}
	if err := exec(editor, `SELECT chat.advance_chat_exposure($1::uuid, false, true)`, id); err != nil {
		t.Fatal(err)
	}
	var tainted, private bool
	if err := owner.QueryRow(ctx, `SELECT tainted, private_read FROM chat.chats WHERE id = $1::uuid`, id).Scan(&tainted, &private); err != nil || !tainted || !private {
		t.Fatalf("flags = %v %v %v", tainted, private, err)
	}
	for _, col := range []string{"tainted", "private_read"} {
		_, err := owner.Exec(ctx, `UPDATE chat.chats SET `+col+` = false WHERE id = $1::uuid`, id)
		if err == nil || !strings.Contains(err.Error(), "chat: taint") {
			t.Fatalf("%s downgrade = %v", col, err)
		}
	}

	if err := exec(editor, `UPDATE chat.chats SET visibility = 'everyone' WHERE id = $1::uuid`, id); err == nil || !strings.Contains(err.Error(), "chat: owner") {
		t.Fatalf("editor visibility = %v", err)
	}
	if err := exec(ownerAccess, `UPDATE chat.chats SET visibility = 'everyone' WHERE id = $1::uuid`, id); err != nil {
		t.Fatalf("owner visibility = %v", err)
	}
}

func TestToolAllowancesPermitSubsequentChangeCallsInUntaintedChatAndHoldWhenTaintedOrDelete(t *testing.T) {
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
	for _, tool := range []string{"change_tool", "delete_tool", "create_automation"} {
		if err := store.AllowToolForChat(ctx, ownerAccess, a.ID, tool); err != nil {
			t.Fatal(err)
		}
	}
	claim, _ := claimRun(t, ctx, store, owner, a.ID)
	if len(claim.AllowedTools) != 3 || !claim.AllowedTools["change_tool"] {
		t.Fatalf("allowed = %v", claim.AllowedTools)
	}
	decide := func(policy app.ChatWritePolicy, tool string, op agentloop.ToolOp) string {
		t.Helper()
		got, err := policy.Decide(ctx, ownerUser, tool, op, nil)
		if err != nil {
			t.Fatal(err)
		}
		return got.Verdict
	}
	snapshot := app.ChatWritePolicy{ChatID: a.ID, Run: &agentloop.RunState{Exposure: claim.Exposure}, Allowed: claim.AllowedTools}
	if got := decide(snapshot, "change_tool", agentloop.OpChange); got != "allow" {
		t.Fatalf("allowed change = %s", got)
	}
	if got := decide(snapshot, "delete_tool", agentloop.OpDelete); got != "ask" {
		t.Fatalf("delete = %s", got)
	}
	if got := decide(snapshot, "create_automation", agentloop.OpChange); got != "ask" {
		t.Fatalf("create_automation = %s", got)
	}
	tainted := snapshot
	tainted.Run = &agentloop.RunState{Exposure: agentloop.ExposureState{Tainted: true}}
	if got := decide(tainted, "change_tool", agentloop.OpChange); got != "ask" {
		t.Fatalf("tainted run change = %s", got)
	}

	// Asked of the database: one person, one chat, one tool.
	live := func(id string, access app.Access) app.ChatWritePolicy {
		return app.ChatWritePolicy{ChatID: id, Run: &agentloop.RunState{}, Allowances: store, Access: func() (app.Access, error) { return access, nil }}
	}
	if got := decide(live(a.ID, ownerAccess), "change_tool", agentloop.OpChange); got != "allow" {
		t.Fatalf("owner = %s", got)
	}
	if got := decide(live(a.ID, editorOf(a.ID)), "change_tool", agentloop.OpChange); got != "ask" {
		t.Fatalf("other person = %s", got)
	}
	if got := decide(live(b.ID, ownerAccess), "change_tool", agentloop.OpChange); got != "ask" {
		t.Fatalf("other chat = %s", got)
	}

	// A tainted chat holds even for a run that has not seen the taint yet.
	if err := store.NarrowedTx(ctx, scopeOf(ownerAccess, a.ID), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `SELECT chat.advance_chat_exposure($1::uuid, true, false)`, a.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := decide(live(a.ID, ownerAccess), "change_tool", agentloop.OpChange); got != "ask" {
		t.Fatalf("tainted chat = %s", got)
	}
	if err := store.AllowToolForChat(ctx, ownerAccess, a.ID, "other_tool"); err == nil {
		t.Fatal("allowance inserted on a tainted chat")
	}
}

func TestExposurePersistsAcrossRoundsAndReplies(t *testing.T) {
	owner := up(t)
	store := quietStore(t, owner)
	ctx := context.Background()
	opened, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	c, runID := claimRun(t, ctx, store, owner, opened.ID)
	if c.Exposure.Tainted || c.Exposure.PrivateRead {
		t.Fatalf("fresh exposure = %+v", c.Exposure)
	}
	err = store.CommitRound(ctx, ownerAccess, c, app.RoundCommit{
		Turn: agentloop.TurnCommit{
			RunID: runID, ObjectID: opened.ID, SeqStart: 1,
			Messages: []agentloop.Message{{Role: agentloop.RoleAssistant, Text: "x"}},
			Status:   agentloop.TurnContinue, Model: "m1",
			Exposure: agentloop.ExposureState{Tainted: true, PrivateRead: true},
		},
		End: app.RoundNext,
	})
	if err != nil {
		t.Fatal(err)
	}
	read, err := app.Read(ctx, store, ownerAccess, opened.ID)
	if err != nil || !read.Tainted || !read.PrivateRead {
		t.Fatalf("read = %+v %v", read, err)
	}
	job := c.Job
	job.Round = 2
	next, err := store.Claim(ctx, ownerAccess, job, "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaa02")
	if err != nil || next.State != app.ClaimGo {
		t.Fatalf("claim 2 = %+v %v", next, err)
	}
	if want := (agentloop.ExposureState{Tainted: true, PrivateRead: true}); next.Exposure != want {
		t.Fatalf("round 2 exposure = %+v", next.Exposure)
	}
}
