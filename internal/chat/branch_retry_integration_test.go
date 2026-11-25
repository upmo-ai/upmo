//go:build integration

package chat_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/chat"
	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/platform/agentloop"
	"github.com/upmo-ai/upmo/internal/platform/audit"
)

func TestBranchSharedViewOnlyChatPropagatesTaintAndSummaryRules(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	ctx := context.Background()
	for _, user := range []struct{ id, email, role string }{
		{ownerUser, "branch-owner@example.com", "owner"},
		{otherUser, "branch-viewer@example.com", "member"},
		{viewerUser, "branch-stranger@example.com", "member"},
	} {
		if _, err := r.owner.Exec(ctx, `
			INSERT INTO identity.users (id, email, email_hash, email_hash_key_id, name, role_id, joined_at)
			VALUES ($1::uuid, $2, sha256(convert_to($2, 'UTF8')), 'k1', $2, (SELECT id FROM identity.roles WHERE key = $3), now())
			ON CONFLICT (id) DO NOTHING`, user.id, user.email, user.role); err != nil {
			t.Fatal(err)
		}
	}

	chatID := r.open()
	const (
		pinAccount = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaa11"
		pinAgent   = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaa22"
		pinModel   = "gpt-x"
	)
	bodies := []string{"u1", "a1", "u2", "a2", "u3", "a3", "u4", "a4", "u5", "a5", "u6", "a6"}
	ids := make([]string, len(bodies)+1)
	var userLine string
	for i, body := range bodies {
		seq := i + 1
		var id string
		var err error
		if seq%2 == 1 {
			err = r.owner.QueryRow(ctx, `
				INSERT INTO chat.lines (chat_id, seq, role, body, author_id, route, pin_account_id, pin_model, agent_id)
				VALUES ($1::uuid, $2, 'user', $3, $4::uuid, 'chat', $5::uuid, $6, $7::uuid)
				RETURNING id::text`, chatID, seq, body, ownerUser, pinAccount, pinModel, pinAgent).Scan(&id)
			userLine = id
		} else {
			err = r.owner.QueryRow(ctx, `
				INSERT INTO chat.lines (chat_id, seq, role, body, model, status, reply_to)
				VALUES ($1::uuid, $2, 'assistant', $3, 'm1', 'done', $4::uuid)
				RETURNING id::text`, chatID, seq, body, userLine).Scan(&id)
		}
		if err != nil {
			t.Fatal(err)
		}
		ids[seq] = id
	}
	if _, err := r.owner.Exec(ctx, `
		UPDATE chat.chats
		SET title = 'Q4 Budget Review', tainted = true, private_read = true,
		    rolling_summary = 'sum1..4', summary_up_to_seq = 4, summary_up_to_line_id = $2::uuid
		WHERE id = $1::uuid`, chatID, ids[4]); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `
		INSERT INTO identity.shares (object_type, object_id, user_id, level, granted_by)
		VALUES ('chat', $1::uuid, $2::uuid, 'viewer', $3::uuid)`, chatID, otherUser, ownerUser); err != nil {
		t.Fatal(err)
	}

	viewer := chat.Access{
		UserID:     otherUser,
		Allow:      "chat:create,chat:view,chat:edit",
		SharedView: "{" + chatID + "}",
	}
	first := postBranch(t, r.httpMux(viewer), chatID, ids[6], false)
	if first.Code != http.StatusCreated {
		t.Fatalf("branch at seq 6 = %d %s", first.Code, first.Body.String())
	}
	summary := decodeSummary(t, first.Body.Bytes())
	if summary.Title != "Q4 Budget Review" || summary.Preview != "a3" {
		t.Fatalf("summary = %+v", summary)
	}
	var seq6At time.Time
	if err := r.owner.QueryRow(ctx, `SELECT at FROM chat.lines WHERE id = $1::uuid`, ids[6]).Scan(&seq6At); err != nil {
		t.Fatal(err)
	}
	if !summary.LastActivity.Equal(seq6At.UTC()) {
		t.Fatalf("last_activity = %s, want %s", summary.LastActivity, seq6At.UTC())
	}
	assertBranch(t, r, summary.ID, otherUser, true, true, "sum1..4", 4, []string{"u1", "a1", "u2", "a2", "u3", "a3"}, ids)

	second := postBranch(t, r.httpMux(viewer), chatID, ids[2], false)
	if second.Code != http.StatusCreated {
		t.Fatalf("branch at seq 2 = %d %s", second.Code, second.Body.String())
	}
	early := decodeSummary(t, second.Body.Bytes())
	if early.Title != "Q4 Budget Review" || early.Preview != "a1" {
		t.Fatalf("early summary = %+v", early)
	}
	assertBranch(t, r, early.ID, otherUser, true, true, "", 0, []string{"u1", "a1"}, ids)

	stranger := chat.Access{UserID: viewerUser, Allow: "chat:create,chat:view,chat:edit"}
	denied := postBranch(t, r.httpMux(stranger), chatID, ids[6], false)
	if denied.Code != http.StatusNotFound || !strings.Contains(denied.Body.String(), "no such chat") {
		t.Fatalf("user C branch = %d %s, want 404 no such chat", denied.Code, denied.Body.String())
	}

	again := postBranch(t, r.httpMux(viewer), chatID, ids[6], true)
	if again.Code != http.StatusCreated {
		t.Fatalf("retry branch at assistant seq 6 = %d %s", again.Code, again.Body.String())
	}
	retried := decodeSummary(t, again.Body.Bytes())
	var runState, runBody string
	var assistantCopies int
	if err := r.owner.QueryRow(ctx, `
		SELECT r.state, line.body
		FROM chat.runs AS r
		JOIN chat.lines AS line ON line.id = r.line_id
		WHERE r.chat_id = $1::uuid`, retried.ID).Scan(&runState, &runBody); err != nil {
		t.Fatal(err)
	}
	if runState != "running" || runBody != "u3" {
		t.Fatalf("reserved run = %s on %q, want running on u3", runState, runBody)
	}
	if err := r.owner.QueryRow(ctx, `SELECT count(*) FROM chat.lines WHERE chat_id = $1::uuid AND body = 'a3'`, retried.ID).Scan(&assistantCopies); err != nil {
		t.Fatal(err)
	}
	if assistantCopies != 0 {
		t.Fatalf("cloned a3 lines = %d, want 0", assistantCopies)
	}
	assertBranch(t, r, retried.ID, otherUser, true, true, "sum1..4", 4, []string{"u1", "a1", "u2", "a2", "u3"}, ids)
}

func TestBranchKeepsAReplyUntilALaterDoneReplyInsideTheBranch(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	ctx := context.Background()
	chatID := r.open()
	var userLine string
	insert := func(seq int, role, body, status, replyTo string) string {
		t.Helper()
		var id string
		var err error
		if role == "user" {
			err = r.owner.QueryRow(ctx, `
				INSERT INTO chat.lines (chat_id, seq, role, body, author_id)
				VALUES ($1::uuid, $2, 'user', $3, $4::uuid)
				RETURNING id::text`, chatID, seq, body, ownerUser).Scan(&id)
			userLine = id
		} else {
			err = r.owner.QueryRow(ctx, `
				INSERT INTO chat.lines (chat_id, seq, role, body, model, status, reply_to)
				VALUES ($1::uuid, $2, 'assistant', $3, 'm1', $4, $5::uuid)
				RETURNING id::text`, chatID, seq, body, status, replyTo).Scan(&id)
		}
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	insert(1, "user", "u-keep", "", "")
	insert(2, "assistant", "failed-kept", "failed", userLine)
	stoppedKept := insert(3, "assistant", "stopped-kept", "stopped", userLine)
	insert(4, "user", "u-done", "", "")
	insert(5, "assistant", "failed-hidden", "failed", userLine)
	doneKept := insert(6, "assistant", "done-kept", "done", userLine)
	insert(7, "user", "u-newer", "", "")
	insert(8, "assistant", "older-done", "done", userLine)
	newerDone := insert(9, "assistant", "newer-done", "done", userLine)
	insert(10, "user", "u-after", "", "")
	failedInside := insert(11, "assistant", "failed-inside", "failed", userLine)
	insert(12, "assistant", "done-after", "done", userLine)

	type wantLine struct {
		role, body, status string
	}
	check := func(name, lineID string, want []wantLine) string {
		t.Helper()
		rec := postBranch(t, r.httpMux(ownerAccess), chatID, lineID, false)
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s branch = %d %s", name, rec.Code, rec.Body.String())
		}
		summary := decodeSummary(t, rec.Body.Bytes())
		got := clonedLines(t, r, summary.ID)
		if len(got) != len(want) {
			t.Fatalf("%s lines = %+v, want %d", name, got, len(want))
		}
		for i, line := range got {
			if line.seq != i+1 || line.role != want[i].role || line.body != want[i].body || line.status != want[i].status {
				t.Fatalf("%s line %d = %+v, want seq %d %+v", name, i, line, i+1, want[i])
			}
		}
		return summary.ID
	}

	branched := check("failed then stopped", stoppedKept, []wantLine{
		{"user", "u-keep", "done"},
		{"assistant", "failed-kept", "failed"},
		{"assistant", "stopped-kept", "stopped"},
	})
	var rows int
	var action, target string
	var raw []byte
	if err := r.owner.QueryRow(ctx, `
		SELECT count(*) FROM audit.pending WHERE action = $1 AND target_id = $2`,
		string(audit.ChatBranch), branched).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("chat.branch rows for %s = %d, %v, want 1", branched, rows, err)
	}
	if err := r.owner.QueryRow(ctx, `
		SELECT action, target_id, changes FROM audit.pending
		WHERE action = $1 AND target_id = $2`,
		string(audit.ChatBranch), branched).Scan(&action, &target, &raw); err != nil {
		t.Fatal(err)
	}
	var changes audit.Changes
	if err := json.Unmarshal(raw, &changes); err != nil {
		t.Fatal(err)
	}
	if action != string(audit.ChatBranch) || target != branched || strings.Join(changes.Fields, ",") != "retry,source_chat_id,up_to_line_id" {
		t.Fatalf("audit = %s %s %s, want chat.branch %s retry,source_chat_id,up_to_line_id", action, target, changes.Fields, branched)
	}
	source := changes.Values["source_chat_id"]
	upTo := changes.Values["up_to_line_id"]
	retry := changes.Values["retry"]
	gotSource, sourceOK := source.To.(string)
	gotUpTo, upToOK := upTo.To.(string)
	gotRetry, retryOK := retry.To.(bool)
	if source.From != nil || !sourceOK || gotSource != chatID ||
		upTo.From != nil || !upToOK || gotUpTo != stoppedKept ||
		retry.From != nil || !retryOK || gotRetry {
		t.Fatalf("changes = %s, want source_chat_id %s up_to_line_id %s retry false", raw, chatID, stoppedKept)
	}

	check("failed then done", doneKept, []wantLine{
		{"user", "u-keep", "done"},
		{"assistant", "failed-kept", "failed"},
		{"assistant", "stopped-kept", "stopped"},
		{"user", "u-done", "done"},
		{"assistant", "done-kept", "done"},
	})
	check("done then newer done", newerDone, []wantLine{
		{"user", "u-keep", "done"},
		{"assistant", "failed-kept", "failed"},
		{"assistant", "stopped-kept", "stopped"},
		{"user", "u-done", "done"},
		{"assistant", "done-kept", "done"},
		{"user", "u-newer", "done"},
		{"assistant", "newer-done", "done"},
	})
	check("done after the branch point", failedInside, []wantLine{
		{"user", "u-keep", "done"},
		{"assistant", "failed-kept", "failed"},
		{"assistant", "stopped-kept", "stopped"},
		{"user", "u-done", "done"},
		{"assistant", "done-kept", "done"},
		{"user", "u-newer", "done"},
		{"assistant", "newer-done", "done"},
		{"user", "u-after", "done"},
		{"assistant", "failed-inside", "failed"},
	})
}

func TestRetryDoneReplyToEarlierUserLineIsConflictAndReservesNoRun(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	ctx := context.Background()
	chatID := r.open()
	var earlierUser, doneID string
	if err := r.owner.QueryRow(ctx, `
		INSERT INTO chat.lines (chat_id, seq, role, body, author_id)
		VALUES ($1::uuid, 1, 'user', 'earlier question', $2::uuid)
		RETURNING id::text`, chatID, ownerUser).Scan(&earlierUser); err != nil {
		t.Fatal(err)
	}
	if err := r.owner.QueryRow(ctx, `
		INSERT INTO chat.lines (chat_id, seq, role, body, model, status, reply_to)
		VALUES ($1::uuid, 2, 'assistant', 'earlier answer', 'm1', 'done', $2::uuid)
		RETURNING id::text`, chatID, earlierUser).Scan(&doneID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `
		INSERT INTO chat.lines (chat_id, seq, role, body, author_id)
		VALUES ($1::uuid, 3, 'user', 'later question', $2::uuid)`, chatID, ownerUser); err != nil {
		t.Fatal(err)
	}
	var userSeq, maxUserSeq int
	if err := r.owner.QueryRow(ctx, `
		SELECT line.seq, (SELECT max(seq) FROM chat.lines WHERE chat_id = line.chat_id AND role = 'user')
		FROM chat.lines AS reply
		JOIN chat.lines AS line ON line.id = reply.reply_to
		WHERE reply.id = $1::uuid`, doneID).Scan(&userSeq, &maxUserSeq); err != nil {
		t.Fatal(err)
	}
	if userSeq >= maxUserSeq {
		t.Fatalf("setup user seq %d is not earlier than max user seq %d", userSeq, maxUserSeq)
	}

	rec := httptest.NewRecorder()
	r.httpMux(ownerAccess).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/chats/"+chatID+"/lines/"+doneID+"/retry", nil))
	var runs int
	if err := r.owner.QueryRow(ctx, `SELECT count(*) FROM chat.runs WHERE chat_id = $1::uuid`, chatID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Error string `json:"error"`
	}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
	}
	if rec.Code != http.StatusConflict || body.Error != "that line cannot be retried" || runs != 0 {
		t.Fatalf("retry earlier done reply = %d %q runs=%d, want 409 \"that line cannot be retried\" runs=0", rec.Code, body.Error, runs)
	}
}

func TestRetryDoneReplyConsultsActionLogAndSupersedesOldDoneLine(t *testing.T) {
	r, _ := newToolsRig(t)
	seedToolsIdentity(t, r)
	ctx := context.Background()
	ownerFull := chat.Access{
		UserID: ownerUser,
		Allow:  "chat:create,chat:view,chat:edit,task:create,task:view,task:edit",
		Teams:  "{" + toolsTeamA + "}",
	}
	r.setAccess(ownerUser, ownerFull)
	chatID := r.open()
	if err := app.Say(ctx, r.store, ownerFull, chatID, "open a task", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	r.stub.reply(func(call int, s *frames) {
		if call == 1 {
			s.doneCalls("m1", 1, 1, nil, stubTool{ID: "open1", Name: "open_task", Args: `{"goal":"Bug"}`})
			return
		}
		s.delta("created")
		s.done("m1", 1, 1, nil)
	})
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}
	var holdID string
	if err := r.owner.QueryRow(ctx, `SELECT id::text FROM chat.holds WHERE chat_id = $1::uuid AND state = 'pending'`, chatID).Scan(&holdID); err != nil {
		t.Fatal(err)
	}
	if err := app.ApproveHold(ctx, r.store, ownerFull, chatID, holdID, false); err != nil {
		t.Fatal(err)
	}
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}
	stored := r.lines(chatID)
	if len(stored) != 2 || stored[0].Body != "open a task" || stored[1].Body != "created" || stored[1].Status != "done" {
		t.Fatalf("first reply = %+v", stored)
	}
	var tasks int
	if err := r.owner.QueryRow(ctx, `SELECT count(*) FROM tasks.tasks WHERE goal = 'Bug'`).Scan(&tasks); err != nil || tasks != 1 {
		t.Fatalf("tasks after first reply = %d (%v), want 1", tasks, err)
	}
	wantDigest := agentloop.CallDigest("open_task", agentloop.OpChange, json.RawMessage(`{"goal":"Bug"}`))
	var gotUserLine, gotDigest string
	var doneLogs int
	if err := r.owner.QueryRow(ctx, `
		SELECT count(*), min(user_line_id::text), min(args_digest)
		FROM chat.action_logs
		WHERE chat_id = $1::uuid AND tool_name = 'open_task' AND state = 'done'`, chatID).Scan(&doneLogs, &gotUserLine, &gotDigest); err != nil {
		t.Fatal(err)
	}
	if doneLogs != 1 || gotUserLine != stored[0].ID || gotDigest != wantDigest {
		t.Fatalf("done action log count=%d user_line=%s digest=%s, want 1 %s %s", doneLogs, gotUserLine, gotDigest, stored[0].ID, wantDigest)
	}
	var cachedRaw string
	if err := r.owner.QueryRow(ctx, `
		SELECT finding.body
		FROM chat.action_logs AS log
		JOIN chat.findings AS finding
		  ON finding.chat_id = log.chat_id
		 AND finding.line_id = log.scope_id
		 AND finding.source_ref = log.call_id
		 AND finding.kind = 'output'
		WHERE log.chat_id = $1::uuid AND log.user_line_id = $2::uuid AND log.state = 'done'`,
		chatID, stored[0].ID).Scan(&cachedRaw); err != nil {
		t.Fatal(err)
	}
	var cached struct {
		Output string
	}
	if err := json.Unmarshal([]byte(cachedRaw), &cached); err != nil || !strings.Contains(cached.Output, "Bug") {
		t.Fatalf("cached finding = %s (%v)", cachedRaw, err)
	}

	seen := len(r.stub.seen())
	r.stub.reply(func(call int, s *frames) {
		if call == seen+1 {
			s.doneCalls("m1", 1, 1, nil, stubTool{ID: "open1", Name: "open_task", Args: `{"goal":"Bug"}`})
			return
		}
		s.delta("again")
		s.done("m1", 1, 1, nil)
	})
	if err := app.Retry(ctx, r.store, ownerFull, chatID, stored[1].ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}
	if err := r.owner.QueryRow(ctx, `SELECT count(*) FROM tasks.tasks WHERE goal = 'Bug'`).Scan(&tasks); err != nil || tasks != 1 {
		t.Fatalf("tasks after retry = %d (%v), want 1", tasks, err)
	}
	if err := r.owner.QueryRow(ctx, `
		SELECT count(*) FROM chat.action_logs
		WHERE chat_id = $1::uuid AND tool_name = 'open_task' AND state = 'done'`, chatID).Scan(&doneLogs); err != nil || doneLogs != 1 {
		t.Fatalf("done action logs after retry = %d (%v), want 1", doneLogs, err)
	}
	var simulated int
	if err := r.owner.QueryRow(ctx, `
		SELECT count(*) FROM chat.turns
		WHERE chat_id = $1::uuid AND role = 'simulated' AND tool_name = 'open_task'`, chatID).Scan(&simulated); err != nil || simulated != 1 {
		t.Fatalf("simulated turns = %d (%v), want 1", simulated, err)
	}
	got, err := app.Read(ctx, r.store, ownerFull, chatID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Lines) != 2 || got.Lines[0].Body != "open a task" || got.Lines[1].Body != "again" || got.Lines[1].Status != "done" {
		t.Fatalf("thread after retry = %+v", got.Lines)
	}
	kept := r.lines(chatID)
	if len(kept) != 3 || kept[1].Body != "created" || kept[1].Status != "done" || kept[2].Body != "again" || kept[2].Status != "done" {
		t.Fatalf("stored lines after retry = %+v", kept)
	}
	last := r.stub.seen()
	saw := false
	for _, msg := range last[len(last)-1].Messages {
		if msg.ToolName == "open_task" && msg.Text == cached.Output {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("retry model call = %+v, want cached output %s", last[len(last)-1].Messages, cached.Output)
	}

	scope := agentloop.AccessScope{UserID: ownerUser, Allow: ownerFull.Allow, ObjectID: chatID}
	log := r.store.ActionLog(scope)
	dial := agentloop.ActionRecord{
		CallID: "dial-1", RunID: "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbb01", ObjectID: chatID,
		UserID: ownerUser, ToolName: "open_task", UserLineID: stored[0].ID, ArgsDigest: wantDigest,
		ExecutedAt: time.Now().UTC(),
	}
	if err := log.BeginDial(ctx, dial); err != nil {
		t.Fatal(err)
	}
	replay, ok := log.(interface {
		PriorMutating(context.Context, agentloop.ActionRecord) (agentloop.ToolResult, bool, error)
	})
	if !ok {
		t.Fatal("action log has no replay")
	}
	lookup := dial
	lookup.RunID = "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbb02"
	_, _, err = replay.PriorMutating(ctx, lookup)
	if !errors.Is(err, agentloop.ErrDuplicateAction) || !strings.Contains(err.Error(), "already in flight") {
		t.Fatalf("dialing replay = %v, want already in flight", err)
	}
}

type branchSummary struct {
	ID           string
	Title        string
	Preview      string
	LastActivity time.Time
}

func postBranch(t *testing.T, mux *http.ServeMux, chatID, lineID string, retry bool) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{"up_to_line_id": lineID, "retry": retry})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/chats/"+chatID+"/branch", bytes.NewReader(body)))
	return rec
}

func decodeSummary(t *testing.T, raw []byte) branchSummary {
	t.Helper()
	var body struct {
		ID           string `json:"id"`
		Title        string `json:"title"`
		Preview      string `json:"preview"`
		LastActivity string `json:"last_activity"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	at, err := time.Parse(time.RFC3339Nano, body.LastActivity)
	if err != nil {
		t.Fatal(err)
	}
	return branchSummary{ID: body.ID, Title: body.Title, Preview: body.Preview, LastActivity: at}
}

type clonedLine struct {
	seq    int
	role   string
	body   string
	status string
}

func clonedLines(t *testing.T, r *rig, chatID string) []clonedLine {
	t.Helper()
	rows, err := r.owner.Query(context.Background(), `
		SELECT seq, role, body, status FROM chat.lines WHERE chat_id = $1::uuid ORDER BY seq`, chatID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var lines []clonedLine
	for rows.Next() {
		var line clonedLine
		if err := rows.Scan(&line.seq, &line.role, &line.body, &line.status); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}

func assertBranch(t *testing.T, r *rig, chatID, owner string, tainted, privateRead bool, summary string, summarySeq int, bodies []string, sourceIDs []string) {
	t.Helper()
	ctx := context.Background()
	var gotOwner, visibility, rolling string
	var gotTainted, gotPrivate bool
	var gotSeq int
	var summaryLine *string
	if err := r.owner.QueryRow(ctx, `
		SELECT owner_user_id::text, visibility, tainted, private_read, rolling_summary, summary_up_to_seq, summary_up_to_line_id::text
		FROM chat.chats WHERE id = $1::uuid`, chatID).Scan(&gotOwner, &visibility, &gotTainted, &gotPrivate, &rolling, &gotSeq, &summaryLine); err != nil {
		t.Fatal(err)
	}
	if gotOwner != owner || visibility != "private" || gotTainted != tainted || gotPrivate != privateRead || rolling != summary || gotSeq != summarySeq {
		t.Fatalf("chat %s owner=%s visibility=%s tainted=%v private=%v summary=%q seq=%d", chatID, gotOwner, visibility, gotTainted, gotPrivate, rolling, gotSeq)
	}
	if summarySeq == 0 {
		if summaryLine != nil {
			t.Fatalf("summary line = %v, want null", summaryLine)
		}
	} else if summaryLine == nil {
		t.Fatal("summary line is null")
	}
	rows, err := r.owner.Query(ctx, `
		SELECT id::text, seq, role, body, reply_to::text, author_id::text, pin_account_id::text, pin_model, agent_id::text
		FROM chat.lines WHERE chat_id = $1::uuid ORDER BY seq`, chatID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type cloned struct {
		seq   int
		role  string
		body  string
		reply *string
		auth  *string
		pin   *string
		model *string
		agent *string
	}
	var lines []cloned
	old := map[string]bool{}
	for _, id := range sourceIDs {
		if id != "" {
			old[id] = true
		}
	}
	byBody := map[string]string{}
	for rows.Next() {
		var line cloned
		var id string
		if err := rows.Scan(&id, &line.seq, &line.role, &line.body, &line.reply, &line.auth, &line.pin, &line.model, &line.agent); err != nil {
			t.Fatal(err)
		}
		byBody[line.body] = id
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(lines) != len(bodies) {
		t.Fatalf("lines = %d, want %d", len(lines), len(bodies))
	}
	for i, line := range lines {
		if line.seq != i+1 || line.body != bodies[i] {
			t.Fatalf("line %d = %+v, want seq %d body %s", i, line, i+1, bodies[i])
		}
		if line.pin != nil || line.model != nil || line.agent != nil {
			t.Fatalf("line %s kept a private pick: pin=%v model=%v agent=%v", line.body, line.pin, line.model, line.agent)
		}
		if line.role == "user" {
			if line.auth == nil || *line.auth != owner || line.reply != nil {
				t.Fatalf("user line %s author=%v reply=%v", line.body, line.auth, line.reply)
			}
			continue
		}
		parent := byBody[bodies[i-1]]
		if line.auth != nil || line.reply == nil || *line.reply != parent || old[*line.reply] {
			t.Fatalf("assistant %s reply_to=%v author=%v, want %s", line.body, line.reply, line.auth, parent)
		}
	}
	if summarySeq > 0 {
		want := byBody["a2"]
		if summaryLine == nil || *summaryLine != want {
			t.Fatalf("summary line = %v, want %s", summaryLine, want)
		}
	}
}
