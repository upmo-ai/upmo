//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/upmo-ai/upmo/internal/chat/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

const (
	pickAccount  = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaa01"
	pickAccount2 = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaa02"
	pickAgent    = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaa03"
	pickReply1   = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaa11"
	pickReply2   = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaa12"
)

// pickJobs is a queue that keeps the reply jobs it is given, in order.
type pickJobs struct{ replies []app.ReplyJobArgs }

func (q *pickJobs) InsertTx(_ context.Context, _ pgx.Tx, args river.JobArgs, _ *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	if reply, ok := args.(app.ReplyJobArgs); ok {
		q.replies = append(q.replies, reply)
	}
	return &rivertype.JobInsertResult{}, nil
}

func (q *pickJobs) last(t *testing.T) app.ReplyJobArgs {
	t.Helper()
	if len(q.replies) == 0 {
		t.Fatal("no reply job was enqueued")
	}
	return q.replies[len(q.replies)-1]
}

func failReply(t *testing.T, owner *pgxpool.Pool, chatID, replyID, userLine string) {
	t.Helper()
	ctx := context.Background()
	if _, err := owner.Exec(ctx, `
		INSERT INTO chat.lines (id, chat_id, seq, role, body, model, status, reply_to, attempts)
		SELECT $1::uuid, $2::uuid, COALESCE(MAX(seq), 0) + 1, 'assistant', '', 'm1', 'failed', $3::uuid, '[]'::jsonb
		FROM chat.lines WHERE chat_id = $2::uuid`, replyID, chatID, userLine); err != nil {
		t.Fatal(err)
	}
	settle(t, owner, chatID)
}

func TestSayStoresThePicksOnTheUserLineAndTheReplyJobCarriesThem(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	queue := &pickJobs{}
	store := postgres.Store{Pool: pgtest.App(t, owner), Enqueuer: queue, ReplyRoute: "chat"}
	opened, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	picks := app.Picks{Route: "fast", PinAccount: pickAccount, PinModel: "gpt-x", AgentID: pickAgent}
	if err := app.Say(ctx, store, ownerAccess, opened.ID, "with picks", picks); err != nil {
		t.Fatal(err)
	}
	job := queue.last(t)
	if job.Route != "fast" || job.PinAccount != pickAccount || job.PinModel != "gpt-x" || job.AgentID != pickAgent {
		t.Fatalf("job = %+v", job)
	}
	settle(t, owner, opened.ID)
	if err := app.Say(ctx, store, ownerAccess, opened.ID, "without", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	job = queue.last(t)
	if job.Route != "chat" || job.PinAccount != "" || job.PinModel != "" || job.AgentID != "" {
		t.Fatalf("job without picks = %+v", job)
	}

	got, err := app.Read(ctx, store, ownerAccess, opened.ID)
	if err != nil || len(got.Lines) != 2 {
		t.Fatalf("lines = %+v %v", got.Lines, err)
	}
	with, without := got.Lines[0], got.Lines[1]
	if with.Route == nil || *with.Route != "fast" || with.PinAccount == nil || *with.PinAccount != pickAccount ||
		with.PinModel == nil || *with.PinModel != "gpt-x" || with.AgentID == nil || *with.AgentID != pickAgent {
		t.Fatalf("line with picks = %+v", with)
	}
	if without.Route != nil || without.PinAccount != nil || without.PinModel != nil || without.AgentID != nil {
		t.Fatalf("line without picks = %+v", without)
	}
}

func TestRetryWithNothingGivenReusesTheStoredPicksAndGivenPicksOverride(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	queue := &pickJobs{}
	store := postgres.Store{Pool: pgtest.App(t, owner), Enqueuer: queue, ReplyRoute: "chat"}
	opened, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	picks := app.Picks{Route: "fast", PinAccount: pickAccount, PinModel: "gpt-x", AgentID: pickAgent}
	if err := app.Say(ctx, store, ownerAccess, opened.ID, "with picks", picks); err != nil {
		t.Fatal(err)
	}
	chat, err := app.Read(ctx, store, ownerAccess, opened.ID)
	if err != nil {
		t.Fatal(err)
	}
	user := chat.Lines[0].ID
	failReply(t, owner, opened.ID, pickReply1, user)

	// Nothing given: the picks stored on the user line are asked for again.
	if err := app.Retry(ctx, store, ownerAccess, opened.ID, pickReply1, nil); err != nil {
		t.Fatal(err)
	}
	job := queue.last(t)
	if job.LineID != user || job.Route != "fast" || job.PinAccount != pickAccount || job.PinModel != "gpt-x" || job.AgentID != pickAgent {
		t.Fatalf("retry with nothing given = %+v", job)
	}

	// Picks given replace the stored ones as a whole.
	settle(t, owner, opened.ID)
	override := app.Picks{Route: "slow", PinAccount: pickAccount2, PinModel: "other"}
	if err := app.Retry(ctx, store, ownerAccess, opened.ID, pickReply1, &override); err != nil {
		t.Fatal(err)
	}
	job = queue.last(t)
	if job.Route != "slow" || job.PinAccount != pickAccount2 || job.PinModel != "other" || job.AgentID != "" {
		t.Fatalf("retry with picks = %+v", job)
	}
	settle(t, owner, opened.ID)
	if err := app.Retry(ctx, store, ownerAccess, opened.ID, pickReply1, &app.Picks{Route: "slow"}); err != nil {
		t.Fatal(err)
	}
	job = queue.last(t)
	if job.Route != "slow" || job.PinAccount != "" || job.PinModel != "" || job.AgentID != "" {
		t.Fatalf("retry with only a route = %+v", job)
	}

	// An explicit empty override is Auto on every axis, not a reuse.
	settle(t, owner, opened.ID)
	if err := app.Retry(ctx, store, ownerAccess, opened.ID, pickReply1, &app.Picks{}); err != nil {
		t.Fatal(err)
	}
	job = queue.last(t)
	if job.Route != "chat" || job.PinAccount != "" || job.PinModel != "" || job.AgentID != "" {
		t.Fatalf("retry with an explicit empty override = %+v", job)
	}

	// The user line keeps what it was sent with.
	chat, err = app.Read(ctx, store, ownerAccess, opened.ID)
	if err != nil {
		t.Fatal(err)
	}
	line := chat.Lines[0]
	if line.Route == nil || *line.Route != "fast" || line.PinModel == nil || *line.PinModel != "gpt-x" {
		t.Fatalf("stored picks changed: %+v", line)
	}
}

func TestRetryOfALineSentWithoutPicksUsesTheInstallRoute(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	queue := &pickJobs{}
	store := postgres.Store{Pool: pgtest.App(t, owner), Enqueuer: queue, ReplyRoute: "chat"}
	opened, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Say(ctx, store, ownerAccess, opened.ID, "plain", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	chat, err := app.Read(ctx, store, ownerAccess, opened.ID)
	if err != nil {
		t.Fatal(err)
	}
	failReply(t, owner, opened.ID, pickReply2, chat.Lines[0].ID)
	if err := app.Retry(ctx, store, ownerAccess, opened.ID, pickReply2, nil); err != nil {
		t.Fatal(err)
	}
	job := queue.last(t)
	if job.Route != "chat" || job.PinAccount != "" || job.PinModel != "" || job.AgentID != "" {
		t.Fatalf("job = %+v", job)
	}
}

func TestOnlyTheAuthorOfALineGetsItsAccountAndAgentBack(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	queue := &pickJobs{}
	store := postgres.Store{Pool: pgtest.App(t, owner), Enqueuer: queue, ReplyRoute: "chat"}
	opened, err := app.Open(ctx, store, ownerAccess)
	if err != nil {
		t.Fatal(err)
	}
	editor := app.Access{UserID: otherUser, Allow: "chat:view,chat:edit", SharedView: "{" + opened.ID + "}", SharedEdit: "{" + opened.ID + "}"}
	theirs := app.Picks{PinAccount: pickAccount2, PinModel: "theirs-m", AgentID: pickAgent}
	if err := app.Say(ctx, store, editor, opened.ID, "theirs", theirs); err != nil {
		t.Fatal(err)
	}
	settle(t, owner, opened.ID)
	picks := app.Picks{Route: "fast", PinAccount: pickAccount, PinModel: "gpt-x", AgentID: pickAgent}
	if err := app.Say(ctx, store, ownerAccess, opened.ID, "mine", picks); err != nil {
		t.Fatal(err)
	}

	check := func(who string, line app.Line, wantAccount, wantAgent bool, model string) {
		t.Helper()
		if (line.PinAccount != nil) != wantAccount || (line.AgentID != nil) != wantAgent ||
			line.PinModel == nil || *line.PinModel != model {
			t.Fatalf("%s sees account %v agent %v model %v on the line of %q", who, line.PinAccount, line.AgentID, line.PinModel, model)
		}
	}
	read := func(access app.Access) app.Chat {
		t.Helper()
		chat, err := app.Read(ctx, store, access, opened.ID)
		if err != nil {
			t.Fatal(err)
		}
		return chat
	}
	streamed := func(access app.Access) []app.Line {
		t.Helper()
		tail, err := store.Tail(ctx, access, opened.ID, "", 100)
		if err != nil {
			t.Fatal(err)
		}
		var lines []app.Line
		for _, e := range tail.Events {
			if e.Kind == app.EventLine {
				lines = append(lines, e.Line)
			}
		}
		return lines
	}

	for name, lines := range map[string][]app.Line{
		"owner read": read(ownerAccess).Lines, "owner stream": streamed(ownerAccess),
	} {
		check(name, lines[0], false, false, "theirs-m")
		check(name, lines[1], true, true, "gpt-x")
	}
	for name, lines := range map[string][]app.Line{
		"editor read": read(editor).Lines, "editor stream": streamed(editor),
	} {
		check(name, lines[0], true, true, "theirs-m")
		check(name, lines[1], false, false, "gpt-x")
		if lines[1].Route == nil || *lines[1].Route != "fast" {
			t.Fatalf("%s lost the route: %+v", name, lines[1])
		}
	}

	// The editor retrying the owner's line reuses its route and nothing of the
	// owner's account or agent.
	chat := read(ownerAccess)
	failReply(t, owner, opened.ID, pickReply1, chat.Lines[1].ID)
	if err := app.Retry(ctx, store, editor, opened.ID, pickReply1, nil); err != nil {
		t.Fatal(err)
	}
	job := queue.last(t)
	if job.Route != "fast" || job.PinAccount != "" || job.PinModel != "" || job.AgentID != "" {
		t.Fatalf("editor retry of the owner's line = %+v", job)
	}
}
