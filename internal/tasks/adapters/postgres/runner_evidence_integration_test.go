//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/platform/agentloop"
	"github.com/upmo-ai/upmo/internal/tasks/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func saveHistoryViaRunnerTx(ctx context.Context, store postgres.Store, access app.Access, taskID, runID, token string, history []agentloop.Message) error {
	return store.NarrowedRunnerTx(ctx, agentloop.AccessScope{
		UserID:     access.UserID,
		Allow:      access.Allow,
		SharedView: access.SharedView,
		SharedEdit: access.SharedEdit,
		ObjectID:   taskID,
	}, func(tx pgx.Tx) error {
		return store.SaveHistoryTx(ctx, tx, taskID, runID, token, history)
	})
}

func assertMessagesEqual(t *testing.T, got, want []agentloop.Message) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("message count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Role != w.Role || g.Text != w.Text || g.ToolName != w.ToolName || g.ToolID != w.ToolID ||
			g.IsError != w.IsError || g.Offload != w.Offload || g.HeldPlaceholder != w.HeldPlaceholder || g.Replay != w.Replay {
			t.Fatalf("msg[%d] = %+v, want %+v", i, g, w)
		}
		if len(g.ToolCalls) != len(w.ToolCalls) {
			t.Fatalf("msg[%d].ToolCalls len = %d, want %d", i, len(g.ToolCalls), len(w.ToolCalls))
		}
		for j := range w.ToolCalls {
			gc, wc := g.ToolCalls[j], w.ToolCalls[j]
			if gc.ID != wc.ID || gc.Name != wc.Name || gc.ThoughtSignature != wc.ThoughtSignature {
				t.Fatalf("msg[%d].ToolCalls[%d] = %+v, want %+v", i, j, gc, wc)
			}
			var gv, wv any
			if len(gc.Arguments) > 0 {
				if err := json.Unmarshal(gc.Arguments, &gv); err != nil {
					t.Fatalf("msg[%d].ToolCalls[%d].Arguments unmarshal got: %v", i, j, err)
				}
			}
			if len(wc.Arguments) > 0 {
				if err := json.Unmarshal(wc.Arguments, &wv); err != nil {
					t.Fatalf("msg[%d].ToolCalls[%d].Arguments unmarshal want: %v", i, j, err)
				}
			}
			gb, _ := json.Marshal(gv)
			wb, _ := json.Marshal(wv)
			if string(gb) != string(wb) {
				t.Fatalf("msg[%d].ToolCalls[%d].Arguments = %s, want %s", i, j, gb, wb)
			}
		}
	}
}

func TestRunnerHistory(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()

	task := reach(t, store, "Runner history task", domain.Working)
	markTaskRunnerOwned(t, owner, task.ID)
	run, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	otherRun, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	otherCreate := app.Access{UserID: otherUser, Allow: "task:create,task:view,task:edit"}
	otherTask, err := app.ApplyTransition(ctx, store, otherCreate, "", domain.Input{Kind: domain.Open, Goal: "Other history task"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.ApplyTransition(ctx, store, otherCreate, otherTask.ID, domain.Input{Kind: domain.Advance}); err != nil {
		t.Fatal(err)
	}
	foreignRun, err := app.Admit(ctx, store, otherCreate, otherTask.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	// Empty run_history returns nil, nil.
	empty, err := store.LoadHistory(ctx, ownerAccess, task.ID, run.ID)
	if err != nil || empty != nil {
		t.Fatalf("LoadHistory before save = %v, %v, want nil, nil", empty, err)
	}

	// LoadHistory as owner without task:edit succeeds.
	ownerViewOnly := app.Access{UserID: ownerUser, Allow: "task:view"}
	if empty2, err := store.LoadHistory(ctx, ownerViewOnly, task.ID, run.ID); err != nil || empty2 != nil {
		t.Fatalf("LoadHistory view-only owner = %v, %v, want nil, nil", empty2, err)
	}

	claim, err := store.ClaimRun(ctx, ownerAccess, task.ID, run.ID, 1)
	if err != nil {
		t.Fatal(err)
	}

	// Save 3 messages preserving Offload, HeldPlaceholder, Replay, IsError, and ToolCalls.
	three := []agentloop.Message{
		{
			Role: agentloop.RoleUser,
			Text: "run tool",
		},
		{
			Role: agentloop.RoleAssistant,
			Text: "calling",
			ToolCalls: []agentloop.ToolCall{
				{
					ID:               "call-1",
					Name:             "read_file",
					Arguments:        json.RawMessage(`{"path":"a.txt"}`),
					ThoughtSignature: "sig-1",
				},
			},
		},
		{
			Role:            agentloop.RoleTool,
			Text:            "simulated error output",
			ToolName:        "read_file",
			ToolID:          "call-1",
			IsError:         true,
			Offload:         true,
			HeldPlaceholder: true,
			Replay:          true,
		},
	}
	if err := saveHistoryViaRunnerTx(ctx, store, ownerAccess, task.ID, run.ID, claim.Token, three); err != nil {
		t.Fatalf("SaveHistoryTx(3) = %v", err)
	}

	// Fresh Store value loads the exact 3 messages.
	freshStore := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	got3, err := freshStore.LoadHistory(ctx, ownerAccess, task.ID, run.ID)
	if err != nil {
		t.Fatalf("LoadHistory(3) = %v", err)
	}
	assertMessagesEqual(t, got3, three)

	// Whole replacement: saving 1 message replaces the prior 3.
	one := []agentloop.Message{{Role: agentloop.RoleUser, Text: "only one"}}
	if err := saveHistoryViaRunnerTx(ctx, store, ownerViewOnly, task.ID, run.ID, claim.Token, one); err != nil {
		t.Fatalf("SaveHistoryTx(1) = %v", err)
	}
	got1, err := store.LoadHistory(ctx, ownerAccess, task.ID, run.ID)
	if err != nil {
		t.Fatalf("LoadHistory(1) = %v", err)
	}
	assertMessagesEqual(t, got1, one)

	// Wrong token and expired-lease token return ErrFenced and leave stored history unchanged.
	if err := saveHistoryViaRunnerTx(ctx, store, ownerAccess, task.ID, run.ID, uuid.NewString(), three); !errors.Is(err, app.ErrFenced) {
		t.Fatalf("SaveHistoryTx wrong token = %v, want ErrFenced", err)
	}
	gotAfterWrong, err := store.LoadHistory(ctx, ownerAccess, task.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertMessagesEqual(t, gotAfterWrong, one)

	if _, err := owner.Exec(ctx, `UPDATE tasks.tasks SET lease_until = now() - interval '1 second' WHERE id = $1::uuid`, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := saveHistoryViaRunnerTx(ctx, store, ownerAccess, task.ID, run.ID, claim.Token, three); !errors.Is(err, app.ErrFenced) {
		t.Fatalf("SaveHistoryTx expired lease = %v, want ErrFenced", err)
	}
	gotAfterExpired, err := store.LoadHistory(ctx, ownerAccess, task.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertMessagesEqual(t, gotAfterExpired, one)

	// Re-acquire live claim for size tests.
	claim2, err := store.ClaimRun(ctx, ownerAccess, task.ID, run.ID, 1)
	if err != nil {
		t.Fatal(err)
	}

	// History marshaling over 1048576 bytes returns errHistoryTooLarge and leaves stored history unchanged.
	tooBig := []agentloop.Message{{Role: agentloop.RoleUser, Text: strings.Repeat("x", 1048576)}}
	if err := saveHistoryViaRunnerTx(ctx, store, ownerAccess, task.ID, run.ID, claim2.Token, tooBig); err == nil || err.Error() != "tasks: history too large" {
		t.Fatalf("SaveHistoryTx(>1MiB) = %v, want tasks: history too large", err)
	}
	gotAfterTooBig, err := store.LoadHistory(ctx, ownerAccess, task.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertMessagesEqual(t, gotAfterTooBig, one)

	// Exactly 1048576 bytes of worst-case tiny elements saves and loads.
	tinyCount := 65500
	tinyMsgs := make([]agentloop.Message, tinyCount+1)
	for i := 0; i < tinyCount; i++ {
		tinyMsgs[i] = agentloop.Message{Role: agentloop.RoleUser}
	}
	prefixRaw, err := json.Marshal(tinyMsgs[:tinyCount])
	if err != nil {
		t.Fatal(err)
	}
	// Adding one message: `,{"role":"user","text":"<pad>"}` adds 26 + len(pad) bytes.
	padLen := 1048576 - len(prefixRaw) - len(`,{"role":"user","text":""}`)
	if padLen < 0 {
		t.Fatalf("unexpected negative padLen %d", padLen)
	}
	tinyMsgs[tinyCount] = agentloop.Message{Role: agentloop.RoleUser, Text: strings.Repeat("a", padLen)}
	exactRaw, err := json.Marshal(tinyMsgs)
	if err != nil {
		t.Fatal(err)
	}
	if len(exactRaw) != 1048576 {
		t.Fatalf("exactRaw len = %d, want 1048576", len(exactRaw))
	}
	if err := saveHistoryViaRunnerTx(ctx, store, ownerAccess, task.ID, run.ID, claim2.Token, tinyMsgs); err != nil {
		t.Fatalf("SaveHistoryTx(1048576 tiny) = %v", err)
	}
	gotTiny, err := store.LoadHistory(ctx, ownerAccess, task.ID, run.ID)
	if err != nil {
		t.Fatalf("LoadHistory(1048576 tiny) = %v", err)
	}
	if len(gotTiny) != len(tinyMsgs) {
		t.Fatalf("LoadHistory(1048576 tiny) len = %d, want %d", len(gotTiny), len(tinyMsgs))
	}

	// Raw INSERT of non-array jsonb through owner pool => 23514.
	if _, err := owner.Exec(ctx, `INSERT INTO tasks.run_history (run_id, history) VALUES ($1::uuid, '{}'::jsonb)`, otherRun.ID); sqlState(err) != "23514" {
		t.Fatalf("raw INSERT non-array jsonb = %v (code %s), want 23514", err, sqlState(err))
	}

	// Raw INSERT of 2097153 bytes jsonb::text through owner pool => 23514.
	// `["<2097149 bytes>"]` has jsonb::text length = 2 + 1 + 2097149 + 1 = 2097153.
	overDB := `["` + strings.Repeat("b", 2097149) + `"]`
	if _, err := owner.Exec(ctx, `INSERT INTO tasks.run_history (run_id, history) VALUES ($1::uuid, $2::jsonb)`, otherRun.ID, overDB); sqlState(err) != "23514" {
		t.Fatalf("raw INSERT 2097153 bytes = %v (code %s), want 23514", err, sqlState(err))
	}

	// upmo_app SELECT, INSERT, and UPDATE on tasks.run_history => 42501.
	for _, stmt := range []struct {
		name string
		sql  string
		args []any
	}{
		{"SELECT", `SELECT count(*) FROM tasks.run_history`, nil},
		{"INSERT", `INSERT INTO tasks.run_history (run_id, history) VALUES ($1::uuid, '[]'::jsonb)`, []any{otherRun.ID}},
		{"UPDATE", `UPDATE tasks.run_history SET history = '[]'::jsonb WHERE run_id = $1::uuid`, []any{run.ID}},
	} {
		asRole(t, owner, "upmo_app", ownerAccess, false, func(tx pgx.Tx) {
			if _, err := tx.Exec(ctx, stmt.sql, stmt.args...); sqlState(err) != "42501" {
				t.Fatalf("upmo_app %s run_history = %v (code %s), want 42501", stmt.name, err, sqlState(err))
			}
		})
	}

	// Runner as different owner reads 0 rows and cannot insert (42501).
	asRole(t, owner, "upmo_runner", otherCreate, false, func(tx pgx.Tx) {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tasks.run_history WHERE run_id = $1::uuid`, run.ID).Scan(&n); err != nil || n != 0 {
			t.Fatalf("other runner SELECT run_history = %d, %v, want 0", n, err)
		}
	})
	asRole(t, owner, "upmo_runner", otherCreate, false, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `INSERT INTO tasks.run_history (run_id, history) VALUES ($1::uuid, '[]'::jsonb)`, otherRun.ID); sqlState(err) != "42501" {
			t.Fatalf("other runner INSERT run_history = %v (code %s), want 42501", err, sqlState(err))
		}
	})

	// Run belonging to another task of another owner cannot be loaded or saved via taskID.
	if _, err := store.LoadHistory(ctx, ownerAccess, task.ID, foreignRun.ID); !errors.Is(err, app.ErrGone) {
		t.Fatalf("LoadHistory foreign run = %v, want ErrGone", err)
	}
	if err := saveHistoryViaRunnerTx(ctx, store, ownerAccess, task.ID, foreignRun.ID, claim2.Token, one); !errors.Is(err, app.ErrGone) {
		t.Fatalf("SaveHistoryTx foreign run = %v, want ErrGone", err)
	}
}

func TestRunnerEvidenceAccess(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()

	task := reach(t, store, "Evidence access task", domain.Working)
	markTaskRunnerOwned(t, owner, task.ID)
	run, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimRun(ctx, ownerAccess, task.ID, run.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	want := []agentloop.Message{{Role: agentloop.RoleUser, Text: "secret"}}
	if err := saveHistoryViaRunnerTx(ctx, store, ownerAccess, task.ID, run.ID, claim.Token, want); err != nil {
		t.Fatal(err)
	}

	sharedViewer := app.Access{
		UserID:     otherUser,
		Allow:      "task:view",
		SharedView: "{" + task.ID + "}",
	}
	sharedEditor := app.Access{
		UserID:     otherUser,
		Allow:      "task:view,task:edit",
		SharedView: "{" + task.ID + "}",
		SharedEdit: "{" + task.ID + "}",
	}
	stranger := app.Access{UserID: otherUser, Allow: "task:view,task:edit"}

	for _, tc := range []struct {
		name     string
		access   app.Access
		wantLoad error
		wantSave error
	}{
		{"shared viewer", sharedViewer, app.ErrAccess, app.ErrGone},
		{"shared editor", sharedEditor, app.ErrAccess, app.ErrAccess},
		{"stranger", stranger, app.ErrGone, app.ErrGone},
	} {
		got, err := store.LoadHistory(ctx, tc.access, task.ID, run.ID)
		if !errors.Is(err, tc.wantLoad) || got != nil {
			t.Fatalf("LoadHistory(%s) = %v, %v, want %v, nil", tc.name, got, err, tc.wantLoad)
		}
		if err := saveHistoryViaRunnerTx(ctx, store, tc.access, task.ID, run.ID, claim.Token, []agentloop.Message{{Role: agentloop.RoleUser, Text: "overwrite"}}); !errors.Is(err, tc.wantSave) {
			t.Fatalf("SaveHistoryTx(%s) = %v, want %v", tc.name, err, tc.wantSave)
		}
	}

	if _, err := store.LoadHistory(ctx, ownerAccess, uuid.NewString(), run.ID); !errors.Is(err, app.ErrGone) {
		t.Fatalf("LoadHistory missing task = %v, want ErrGone", err)
	}
	if err := saveHistoryViaRunnerTx(ctx, store, ownerAccess, uuid.NewString(), run.ID, claim.Token, want); !errors.Is(err, app.ErrGone) {
		t.Fatalf("SaveHistoryTx missing task = %v, want ErrGone", err)
	}

	// Stored history remains untouched after refused accesses.
	got, err := store.LoadHistory(ctx, ownerAccess, task.ID, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertMessagesEqual(t, got, want)

	// Outputs: owner without task:edit succeeds; shared viewer, shared editor, stranger, and missing task fail closed.
	ownerViewOnly := app.Access{UserID: ownerUser, Allow: "task:view"}
	ownerViewOutputs := store.Outputs(task.ID, run.ID, func() (app.Access, error) {
		return ownerViewOnly, nil
	})
	outID, err := ownerViewOutputs.SaveOutput(ctx, &agentloop.RunState{}, "owner-view", "owner output", false)
	if err != nil {
		t.Fatalf("Outputs.SaveOutput(owner without task:edit) = %v", err)
	}
	gotBody, gotUntrusted, err := ownerViewOutputs.RetrieveOutput(ctx, nil, outID)
	if err != nil || gotBody != "owner output" || gotUntrusted {
		t.Fatalf("Outputs.RetrieveOutput(owner without task:edit) = %q, %t, %v", gotBody, gotUntrusted, err)
	}

	for _, tc := range []struct {
		name   string
		access app.Access
		want   error
	}{
		{"shared viewer", sharedViewer, app.ErrAccess},
		{"shared editor", sharedEditor, app.ErrAccess},
		{"stranger", stranger, app.ErrGone},
	} {
		outs := store.Outputs(task.ID, run.ID, func() (app.Access, error) {
			return tc.access, nil
		})
		if savedID, err := outs.SaveOutput(ctx, nil, "forged", "nope", false); !errors.Is(err, tc.want) || savedID != "" {
			t.Fatalf("Outputs.SaveOutput(%s) = %q, %v, want %v", tc.name, savedID, err, tc.want)
		}
		if body, untrusted, err := outs.RetrieveOutput(ctx, nil, outID); !errors.Is(err, tc.want) || body != "" || untrusted {
			t.Fatalf("Outputs.RetrieveOutput(%s) = %q, %t, %v, want %v", tc.name, body, untrusted, err, tc.want)
		}
	}

	missingOuts := store.Outputs(uuid.NewString(), run.ID, func() (app.Access, error) {
		return ownerAccess, nil
	})
	if _, err := missingOuts.SaveOutput(ctx, nil, "missing", "nope", false); !errors.Is(err, app.ErrGone) {
		t.Fatalf("Outputs.SaveOutput(missing task) = %v, want ErrGone", err)
	}
	if _, _, err := missingOuts.RetrieveOutput(ctx, nil, outID); !errors.Is(err, app.ErrGone) {
		t.Fatalf("Outputs.RetrieveOutput(missing task) = %v, want ErrGone", err)
	}

	if gotCount := taskInt(t, owner, `SELECT count(*) FROM tasks.findings WHERE task_id = $1::uuid`, task.ID); gotCount != 1 {
		t.Fatalf("findings count after refused SaveOutput = %d, want 1", gotCount)
	}
}

func insertTurnsForTest(t *testing.T, ctx context.Context, store postgres.Store, taskID, runID string, seqStart, count int) {
	t.Helper()
	msgs := make([]agentloop.Message, count)
	for i := range msgs {
		msgs[i] = agentloop.Message{Role: agentloop.RoleAssistant, Text: "turn"}
	}
	err := store.NarrowedRunnerTx(ctx, agentloop.AccessScope{
		UserID:   ownerUser,
		Allow:    ownerAccess.Allow,
		ObjectID: taskID,
	}, func(tx pgx.Tx) error {
		return postgres.InsertTurnRows(ctx, tx, agentloop.TurnCommit{
			ObjectID: taskID,
			RunID:    runID,
			SeqStart: seqStart,
			Messages: msgs,
		}, "worker")
	})
	if err != nil {
		t.Fatalf("insertTurnsForTest: %v", err)
	}
}

func TestRunnerSettleMsgSeq(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()

	task := reach(t, store, "MsgSeq task", domain.Working)
	markTaskRunnerOwned(t, owner, task.ID)
	run, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	// Turn 1: ClaimRun returns MsgSeq 0; insert 3 turns (1..3); SettleTx(1, 3) sets msg_seq=3, turn_seq=1.
	claim1, err := store.ClaimRun(ctx, ownerAccess, task.ID, run.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if claim1.MsgSeq != 0 {
		t.Fatalf("claim1.MsgSeq = %d, want 0", claim1.MsgSeq)
	}
	insertTurnsForTest(t, ctx, store, task.ID, run.ID, 1, 3)
	if err := settleViaRunnerTx(ctx, store, task.ID, run.ID, claim1.Token, 100_000, 1, 3); err != nil {
		t.Fatalf("SettleTx(1, 3) = %v", err)
	}
	if got := taskInt(t, owner, `SELECT msg_seq FROM tasks.runs WHERE id = $1::uuid`, run.ID); got != 3 {
		t.Fatalf("msg_seq after turn 1 = %d, want 3", got)
	}
	if got := taskInt(t, owner, `SELECT turn_seq FROM tasks.runs WHERE id = $1::uuid`, run.ID); got != 1 {
		t.Fatalf("turn_seq after turn 1 = %d, want 1", got)
	}

	// Turn 2: ClaimRun returns MsgSeq 3; wrong seqStart / bad msgs / mismatched turn rows fail with nothing charged.
	claim2, err := store.ClaimRun(ctx, ownerAccess, task.ID, run.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if claim2.MsgSeq != 3 {
		t.Fatalf("claim2.MsgSeq = %d, want 3", claim2.MsgSeq)
	}

	// Negative msgs -> errBadMsgs and nothing charged.
	if err := settleViaRunnerTx(ctx, store, task.ID, run.ID, claim2.Token, 50_000, 4, -1); err == nil || err.Error() != "tasks: negative message count" {
		t.Fatalf("SettleTx(msgs=-1) = %v, want tasks: negative message count", err)
	}

	// Wrong seqStart (3 or 6 when msg_seq=3), even with msgs=0 -> ErrFenced and nothing charged.
	for _, badStart := range []int{3, 6} {
		if err := settleViaRunnerTx(ctx, store, task.ID, run.ID, claim2.Token, 50_000, badStart, 0); !errors.Is(err, app.ErrFenced) {
			t.Fatalf("SettleTx(seqStart=%d, msgs=0) = %v, want ErrFenced", badStart, err)
		}
		if err := settleViaRunnerTx(ctx, store, task.ID, run.ID, claim2.Token, 50_000, badStart, 2); !errors.Is(err, app.ErrFenced) {
			t.Fatalf("SettleTx(seqStart=%d, msgs=2) = %v, want ErrFenced", badStart, err)
		}
	}

	// msgs=3 with only 2 turn rows inserted -> ErrFenced and nothing charged.
	insertTurnsForTest(t, ctx, store, task.ID, run.ID, 4, 2)
	if err := settleViaRunnerTx(ctx, store, task.ID, run.ID, claim2.Token, 50_000, 4, 3); !errors.Is(err, app.ErrFenced) {
		t.Fatalf("SettleTx(msgs=3 with 2 rows) = %v, want ErrFenced", err)
	}
	if got := taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1::uuid`, task.ID); got != 100_000 {
		t.Fatalf("spent changed after fenced SettleTx = %d, want 100000", got)
	}
	if got := taskInt(t, owner, `SELECT msg_seq FROM tasks.runs WHERE id = $1::uuid`, run.ID); got != 3 {
		t.Fatalf("msg_seq changed after fenced SettleTx = %d, want 3", got)
	}

	// Matching seqStart=4, msgs=2 settles and advances msg_seq to 5.
	if err := settleViaRunnerTx(ctx, store, task.ID, run.ID, claim2.Token, 50_000, 4, 2); err != nil {
		t.Fatalf("SettleTx(4, 2) = %v", err)
	}
	if got := taskInt(t, owner, `SELECT msg_seq FROM tasks.runs WHERE id = $1::uuid`, run.ID); got != 5 {
		t.Fatalf("msg_seq after turn 2 = %d, want 5", got)
	}
	if got := taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1::uuid`, task.ID); got != 150_000 {
		t.Fatalf("spent after turn 2 = %d, want 150000", got)
	}

	// Turn rows starting at wrong seq (7 instead of 6) with msg_seq=5 => ErrFenced,
	// both when seqStart=6 (row seq mismatch) and when seqStart=7 (gap from msg_seq+1).
	claim3, err := store.ClaimRun(ctx, ownerAccess, task.ID, run.ID, 3)
	if err != nil {
		t.Fatal(err)
	}
	insertTurnsForTest(t, ctx, store, task.ID, run.ID, 7, 1)
	if err := settleViaRunnerTx(ctx, store, task.ID, run.ID, claim3.Token, 25_000, 6, 1); !errors.Is(err, app.ErrFenced) {
		t.Fatalf("SettleTx(wrong turn row seq) = %v, want ErrFenced", err)
	}
	if err := settleViaRunnerTx(ctx, store, task.ID, run.ID, claim3.Token, 25_000, 7, 1); !errors.Is(err, app.ErrFenced) {
		t.Fatalf("SettleTx(gap seqStart=7) = %v, want ErrFenced", err)
	}
	if got := taskInt(t, owner, `SELECT spent FROM tasks.tasks WHERE id = $1::uuid`, task.ID); got != 150_000 {
		t.Fatalf("spent changed on wrong turn row seq = %d, want 150000", got)
	}
}

func TestRunnerRunsGrants(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()

	task := reach(t, store, "Runs grants task", domain.Working)
	run, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	// upmo_app cannot UPDATE msg_seq or turn_seq, and cannot INSERT msg_seq.
	asRole(t, owner, "upmo_app", ownerAccess, false, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `UPDATE tasks.runs SET msg_seq = 1 WHERE id = $1::uuid`, run.ID); sqlState(err) != "42501" {
			t.Fatalf("upmo_app UPDATE msg_seq = %v (code %s), want 42501", err, sqlState(err))
		}
	})
	asRole(t, owner, "upmo_app", ownerAccess, false, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `UPDATE tasks.runs SET turn_seq = 1 WHERE id = $1::uuid`, run.ID); sqlState(err) != "42501" {
			t.Fatalf("upmo_app UPDATE turn_seq = %v (code %s), want 42501", err, sqlState(err))
		}
	})
	asRole(t, owner, "upmo_app", ownerAccess, false, func(tx pgx.Tx) {
		if _, err := tx.Exec(ctx, `INSERT INTO tasks.runs (task_id, state, msg_seq) VALUES ($1::uuid, 'queued', 1)`, task.ID); sqlState(err) != "42501" {
			t.Fatalf("upmo_app INSERT msg_seq = %v (code %s), want 42501", err, sqlState(err))
		}
	})

	// Production insert (task_id, state) through app.Admit still works.
	run2, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
	if err != nil || run2.ID == "" {
		t.Fatalf("app.Admit production insert = %+v, %v", run2, err)
	}
}

func TestRunnerOutputs(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()

	task := reach(t, store, "Outputs task", domain.Working)
	markTaskRunnerOwned(t, owner, task.ID)
	run1, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	run2, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	otherTask := reach(t, store, "Outputs other task", domain.Working)
	otherTaskRun, err := app.Admit(ctx, store, ownerAccess, otherTask.ID, "")
	if err != nil {
		t.Fatal(err)
	}

	// Access func resolves at call time.
	currentAccess := ownerAccess
	var accessErr error
	outs := store.Outputs(task.ID, run1.ID, func() (app.Access, error) {
		if accessErr != nil {
			return app.Access{}, accessErr
		}
		return currentAccess, nil
	})

	// SaveOutput then RetrieveOutput through a NEW Store value returns body and untrusted,
	// and private_read matches run.Exposure.PrivateRead.
	privRun := &agentloop.RunState{Exposure: agentloop.ExposureState{PrivateRead: true}}
	idPriv, err := outs.SaveOutput(ctx, privRun, "priv-title", "secret output body", true)
	if err != nil {
		t.Fatalf("SaveOutput(private, untrusted) = %v", err)
	}
	freshStore := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	freshOuts := freshStore.Outputs(task.ID, run1.ID, func() (app.Access, error) {
		return ownerAccess, nil
	})
	gotBody, gotUntrusted, err := freshOuts.RetrieveOutput(ctx, nil, idPriv)
	if err != nil || gotBody != "secret output body" || !gotUntrusted {
		t.Fatalf("RetrieveOutput(private) = %q, %t, %v, want secret output body, true, nil", gotBody, gotUntrusted, err)
	}
	if !taskBool(t, owner, `SELECT private_read FROM tasks.findings WHERE id = $1::uuid`, idPriv) {
		t.Fatalf("private_read = false, want true")
	}
	if gotKind := taskText(t, owner, `SELECT kind FROM tasks.findings WHERE id = $1::uuid`, idPriv); gotKind != "tool_output" {
		t.Fatalf("kind = %q, want tool_output", gotKind)
	}

	pubRun := &agentloop.RunState{Exposure: agentloop.ExposureState{PrivateRead: false}}
	idPub, err := outs.SaveOutput(ctx, pubRun, "pub-title", "public output body", false)
	if err != nil {
		t.Fatalf("SaveOutput(public, trusted) = %v", err)
	}
	gotBody, gotUntrusted, err = freshOuts.RetrieveOutput(ctx, nil, idPub)
	if err != nil || gotBody != "public output body" || gotUntrusted {
		t.Fatalf("RetrieveOutput(public) = %q, %t, %v, want public output body, false, nil", gotBody, gotUntrusted, err)
	}
	if taskBool(t, owner, `SELECT private_read FROM tasks.findings WHERE id = $1::uuid`, idPub) {
		t.Fatalf("private_read = true, want false")
	}

	// nil RunState guards PrivateRead to false.
	idNilRun, err := outs.SaveOutput(ctx, nil, "nil-run", "nil run body", false)
	if err != nil {
		t.Fatalf("SaveOutput(nil run) = %v", err)
	}
	if taskBool(t, owner, `SELECT private_read FROM tasks.findings WHERE id = $1::uuid`, idNilRun) {
		t.Fatalf("private_read with nil run = true, want false")
	}

	// 300000-byte ASCII body is stored as exactly 262144 bytes ending with the marker.
	asciiBig := strings.Repeat("x", 300000)
	idASCII, err := outs.SaveOutput(ctx, pubRun, "big-ascii", asciiBig, false)
	if err != nil {
		t.Fatalf("SaveOutput(300000 ASCII) = %v", err)
	}
	gotASCII, _, err := freshOuts.RetrieveOutput(ctx, nil, idASCII)
	if err != nil {
		t.Fatalf("RetrieveOutput(300000 ASCII) = %v", err)
	}
	asciiMarker := "\n[output truncated: 300000 bytes in all]"
	if len(gotASCII) != 262144 || !strings.HasSuffix(gotASCII, asciiMarker) || !utf8.ValidString(gotASCII) {
		t.Fatalf("300000 ASCII capped len=%d suffix=%t valid=%t", len(gotASCII), strings.HasSuffix(gotASCII, asciiMarker), utf8.ValidString(gotASCII))
	}

	// Multibyte body landing mid-rune backs up to a rune boundary.
	wideBig := "aa" + strings.Repeat("🚀", 80000) // 2 + 320000 = 320002 bytes; marker is 40 bytes -> keep 262104 backs to 262102.
	idWide, err := outs.SaveOutput(ctx, pubRun, "big-wide", wideBig, true)
	if err != nil {
		t.Fatalf("SaveOutput(wide) = %v", err)
	}
	gotWide, gotWideUntrusted, err := freshOuts.RetrieveOutput(ctx, nil, idWide)
	if err != nil {
		t.Fatalf("RetrieveOutput(wide) = %v", err)
	}
	wideMarker := "\n[output truncated: 320002 bytes in all]"
	wantWide := "aa" + strings.Repeat("🚀", 65525) + wideMarker
	if len(gotWide) != 262142 || gotWide != wantWide || !utf8.ValidString(gotWide) || !gotWideUntrusted {
		t.Fatalf("wide capped len=%d want=%d valid=%t untrusted=%t", len(gotWide), len(wantWide), utf8.ValidString(gotWide), gotWideUntrusted)
	}

	// kind = 'output' is retrievable; kind = 'finding' is not retrievable.
	var kindOutputID, kindFindingID string
	if err := owner.QueryRow(ctx, `
		INSERT INTO tasks.findings (task_id, run_id, kind, title, body, untrusted)
		VALUES ($1::uuid, $2::uuid, 'output', 'final', 'kind output body', false)
		RETURNING id::text`, task.ID, run1.ID).Scan(&kindOutputID); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `
		INSERT INTO tasks.findings (task_id, run_id, kind, title, body, untrusted)
		VALUES ($1::uuid, $2::uuid, 'finding', 'note', 'kind finding body', false)
		RETURNING id::text`, task.ID, run1.ID).Scan(&kindFindingID); err != nil {
		t.Fatal(err)
	}
	gotKindOut, _, err := outs.RetrieveOutput(ctx, nil, kindOutputID)
	if err != nil || gotKindOut != "kind output body" {
		t.Fatalf("RetrieveOutput(kind=output) = %q, %v", gotKindOut, err)
	}
	if _, _, err := outs.RetrieveOutput(ctx, nil, kindFindingID); err == nil || err.Error() != "no such output" {
		t.Fatalf("RetrieveOutput(kind=finding) = %v, want no such output", err)
	}

	// Scoping: wrong task of same owner, wrong run of same task, invalid uuid, unknown uuid -> no such output.
	sameOwnerOtherTaskOuts := store.Outputs(otherTask.ID, otherTaskRun.ID, func() (app.Access, error) {
		return ownerAccess, nil
	})
	if _, _, err := sameOwnerOtherTaskOuts.RetrieveOutput(ctx, nil, idPub); err == nil || err.Error() != "no such output" {
		t.Fatalf("RetrieveOutput(other task, same owner) = %v, want no such output", err)
	}
	sameOwnerOtherTaskSameRunOuts := store.Outputs(otherTask.ID, run1.ID, func() (app.Access, error) {
		return ownerAccess, nil
	})
	if _, _, err := sameOwnerOtherTaskSameRunOuts.RetrieveOutput(ctx, nil, idPub); err == nil || err.Error() != "no such output" {
		t.Fatalf("RetrieveOutput(other task, run1, same owner) = %v, want no such output", err)
	}
	sameTaskOtherRunOuts := store.Outputs(task.ID, run2.ID, func() (app.Access, error) {
		return ownerAccess, nil
	})
	if _, _, err := sameTaskOtherRunOuts.RetrieveOutput(ctx, nil, idPub); err == nil || err.Error() != "no such output" {
		t.Fatalf("RetrieveOutput(other run, same task) = %v, want no such output", err)
	}
	if _, _, err := outs.RetrieveOutput(ctx, nil, "not-a-uuid"); err == nil || err.Error() != "no such output" {
		t.Fatalf("RetrieveOutput(invalid uuid) = %v, want no such output", err)
	}
	if _, _, err := outs.RetrieveOutput(ctx, nil, uuid.NewString()); err == nil || err.Error() != "no such output" {
		t.Fatalf("RetrieveOutput(missing uuid) = %v, want no such output", err)
	}

	// SaveOutput refuses a run belonging to another task of the same owner.
	mismatchedOuts := store.Outputs(task.ID, otherTaskRun.ID, func() (app.Access, error) {
		return ownerAccess, nil
	})
	if _, err := mismatchedOuts.SaveOutput(ctx, pubRun, "mismatch", "nope", false); !errors.Is(err, app.ErrGone) {
		t.Fatalf("SaveOutput(mismatched run/task) = %v, want ErrGone", err)
	}

	// Access callback is evaluated per call.
	accessErr = errors.New("access revoked")
	if _, err := outs.SaveOutput(ctx, pubRun, "revoked", "nope", false); !errors.Is(err, accessErr) {
		t.Fatalf("SaveOutput(access error) = %v, want %v", err, accessErr)
	}
	if _, _, err := outs.RetrieveOutput(ctx, nil, idPub); !errors.Is(err, accessErr) {
		t.Fatalf("RetrieveOutput(access error) = %v, want %v", err, accessErr)
	}
	accessErr = nil

	// Cascade: deleting a run that has no events through the owner pool removes its findings and history.
	if _, err := app.EndRun(ctx, store, ownerAccess, run2.ID); err != nil {
		t.Fatal(err)
	}
	cascadeTask := reach(t, store, "Outputs cascade task", domain.Working)
	markTaskRunnerOwned(t, owner, cascadeTask.ID)
	if _, err := owner.Exec(ctx, `ALTER TABLE tasks.runs DISABLE TRIGGER runs_born`); err != nil {
		t.Fatal(err)
	}
	var bareRunID string
	err = owner.QueryRow(ctx, `INSERT INTO tasks.runs (task_id, state) VALUES ($1::uuid, 'admitted') RETURNING id::text`, cascadeTask.ID).Scan(&bareRunID)
	if enableErr := func() error {
		_, e := owner.Exec(ctx, `ALTER TABLE tasks.runs ENABLE TRIGGER runs_born`)
		return e
	}(); enableErr != nil {
		t.Fatal(enableErr)
	}
	if err != nil {
		t.Fatal(err)
	}

	claim, err := store.ClaimRun(ctx, ownerAccess, cascadeTask.ID, bareRunID, 1)
	if err != nil {
		t.Fatalf("ClaimRun(bareRun) = %v", err)
	}
	if err := saveHistoryViaRunnerTx(ctx, store, ownerAccess, cascadeTask.ID, bareRunID, claim.Token, []agentloop.Message{{Role: agentloop.RoleUser, Text: "cascade"}}); err != nil {
		t.Fatalf("SaveHistoryTx(bareRun) = %v", err)
	}
	bareOuts := store.Outputs(cascadeTask.ID, bareRunID, func() (app.Access, error) {
		return ownerAccess, nil
	})
	bareOutID, err := bareOuts.SaveOutput(ctx, pubRun, "cascade", "cascade output", false)
	if err != nil {
		t.Fatalf("SaveOutput(bareRun) = %v", err)
	}
	if got := taskInt(t, owner, `SELECT count(*) FROM tasks.run_history WHERE run_id = $1::uuid`, bareRunID); got != 1 {
		t.Fatalf("bareRun run_history count before delete = %d, want 1", got)
	}
	if got := taskInt(t, owner, `SELECT count(*) FROM tasks.findings WHERE id = $1::uuid`, bareOutID); got != 1 {
		t.Fatalf("bareRun findings count before delete = %d, want 1", got)
	}

	if _, err := owner.Exec(ctx, `DELETE FROM tasks.runs WHERE id = $1::uuid`, bareRunID); err != nil {
		t.Fatalf("DELETE bareRun = %v", err)
	}
	if got := taskInt(t, owner, `SELECT count(*) FROM tasks.run_history WHERE run_id = $1::uuid`, bareRunID); got != 0 {
		t.Fatalf("bareRun run_history count after delete = %d, want 0", got)
	}
	if got := taskInt(t, owner, `SELECT count(*) FROM tasks.findings WHERE id = $1::uuid`, bareOutID); got != 0 {
		t.Fatalf("bareRun findings count after delete = %d, want 0", got)
	}
}

func TestRunnerSettleTurnGap(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner), Runner: pgtest.Runner(t, owner)}
	ctx := context.Background()

	task := reach(t, store, "Turn gap task", domain.Working)
	markTaskRunnerOwned(t, owner, task.ID)
	run, err := app.Admit(ctx, store, ownerAccess, task.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimRun(ctx, ownerAccess, task.ID, run.ID, 1)
	if err != nil {
		t.Fatal(err)
	}

	// Rows 1 and 3 have the right first and last seq for msgs=3 but one row is missing.
	insertTurnsForTest(t, ctx, store, task.ID, run.ID, 1, 1)
	insertTurnsForTest(t, ctx, store, task.ID, run.ID, 3, 1)
	if err := settleViaRunnerTx(ctx, store, task.ID, run.ID, claim.Token, 10_000, 1, 3); !errors.Is(err, app.ErrFenced) {
		t.Fatalf("SettleTx(msgs=3 with a gap) = %v, want ErrFenced", err)
	}
	if got := taskInt(t, owner, `SELECT msg_seq FROM tasks.runs WHERE id = $1::uuid`, run.ID); got != 0 {
		t.Fatalf("msg_seq after fenced gap settle = %d, want 0", got)
	}
}
