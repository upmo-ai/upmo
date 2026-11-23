//go:build integration

package chat_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/agents"
	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/chat/domain"
	"github.com/upmo-ai/upmo/internal/platform/agentloop"
)

var (
	token1 = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaa01"
	token2 = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaa02"
)

// holdRound returns a release channel for a stub round held mid-call, closed
// at most once and always on cleanup so a failed assertion cannot leave the
// round holding the package's httptest server open.
func holdRound(t *testing.T) (release chan struct{}, closeRelease func()) {
	t.Helper()
	release = make(chan struct{})
	var once sync.Once
	closeRelease = func() { once.Do(func() { close(release) }) }
	t.Cleanup(closeRelease)
	return release, closeRelease
}

func TestARoundWithToolCallsQueuesTheNextRoundOnce(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	chatID := r.open()
	r.say(chatID, "q", "")
	job1 := r.lastJob()
	r.stub.reply(func(call int, s *frames) {
		if call == 1 {
			s.doneCalls("m1", 2, 3, i64(1), stubTool{ID: "c1", Name: "find_tool", Args: `{"query":"x"}`})
			return
		}
		s.delta("ok")
		s.done("m1", 2, 3, i64(1))
	})
	if err := r.run(job1, 1); err != nil {
		t.Fatal(err)
	}
	jobs := r.jobs()
	if len(jobs) != 2 {
		t.Fatalf("jobs = %+v", jobs)
	}
	job2 := jobs[1]
	if job2.Args.Round != 2 || job2.Args.ReplyID != job1.Args.ReplyID || job2.Args.LineID != job1.Args.LineID {
		t.Fatalf("round 2 = %+v", job2.Args)
	}
	if err := r.run(job2, 1); err != nil {
		t.Fatal(err)
	}
	lines := r.lines(chatID)
	if len(lines) != 2 || lines[1].Status != "done" || lines[1].Body != "ok" {
		t.Fatalf("lines = %+v", lines)
	}
	if row := r.runRow(job1.Args.ReplyID); row.Round != 2 {
		t.Fatalf("run row = %+v", row)
	}
}

func TestASkippedNextRoundInsertFailsTheReply(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	ctx := context.Background()
	chatID := r.open()
	r.say(chatID, "q", "")
	job1 := r.lastJob()

	// Round 2 is already queued: the next-round insert is a duplicate.
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.work.InsertTx(ctx, tx, app.ReplyJobArgs{
		ChatID: job1.Args.ChatID, LineID: job1.Args.LineID, ReplyID: job1.Args.ReplyID,
		Round: 2, UserID: job1.Args.UserID,
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	r.stub.reply(func(call int, s *frames) {
		if call == 1 {
			s.doneCalls("m1", 1, 1, i64(1), stubTool{ID: "c1", Name: "find_tool", Args: `{"query":"x"}`})
			return
		}
		s.delta("ok")
		s.done("m1", 1, 1, i64(1))
	})
	if err := r.run(job1, 1); err != nil {
		t.Fatal(err)
	}
	reply := r.lines(chatID)[1]
	if reply.Status != "failed" || reasonOf(reply) != "the reply could not be written" {
		t.Fatalf("reply = %+v", reply)
	}
	if row := r.runRow(job1.Args.ReplyID); row.State != "failed" || row.Round != 0 {
		t.Fatalf("run row = %+v", row)
	}
	var turns int
	if err := r.owner.QueryRow(ctx, `SELECT count(*) FROM chat.turns WHERE chat_id = $1::uuid`, chatID).Scan(&turns); err != nil || turns != 0 {
		t.Fatalf("turns = %d %v", turns, err)
	}
	if got := len(r.jobs()); got != 2 {
		t.Fatalf("chat_reply rows = %d", got)
	}
}

func TestSayQueuesRoundOneUniqueByReplyAndRound(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	chatID := r.open()
	r.say(chatID, "hello", "")

	var queue string
	var attempts int
	if err := r.owner.QueryRow(context.Background(),
		`SELECT queue, max_attempts FROM platform.river_job WHERE kind = 'chat_reply'`).Scan(&queue, &attempts); err != nil {
		t.Fatal(err)
	}
	if queue != "chat_reply" || attempts != 2 {
		t.Fatalf("queue %q max attempts %d", queue, attempts)
	}
	queued := r.lastJob()
	user := r.lines(chatID)[0]
	if queued.Args.LineID != user.ID || queued.Args.ChatID != chatID || queued.Args.UserID != ownerUser || queued.Args.Route != "chat" || queued.Args.Round != 1 {
		t.Fatalf("args = %+v, user line %s", queued.Args, user.ID)
	}
	var runID string
	if err := r.owner.QueryRow(context.Background(), `SELECT id::text FROM chat.runs WHERE chat_id = $1::uuid`, chatID).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	if runID != queued.Args.ReplyID {
		t.Fatalf("reply id = %s, run id %s", queued.Args.ReplyID, runID)
	}

	// The same round of the same reply cannot be queued twice.
	tx, err := r.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	again := queued.Args
	res, err := r.work.InsertTx(context.Background(), tx, again, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.UniqueSkippedAsDuplicate {
		t.Fatal("a second round-1 job for the same reply was queued")
	}
	// A later round is a different unique key and is not skipped.
	round2 := queued.Args
	round2.Round = 2
	res, err = r.work.InsertTx(context.Background(), tx, round2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.UniqueSkippedAsDuplicate {
		t.Fatal("the round-2 job was skipped")
	}
}

func TestASecondSayWhileAReplyRunsIs409(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	release, closeRelease := holdRound(t)
	r.stub.reply(func(call int, s *frames) {
		s.delta("text")
		if call == 1 {
			s.hold(release)
		}
		s.done("m1", 1, 1, nil)
	})
	chatID := r.open()
	r.say(chatID, "first", "")
	first := r.lastJob()
	running := r.runAsync(first, 1)
	eventually(t, "the first reply to start", func() bool { return len(r.stub.seen()) == 1 })

	// A second Say on the same chat is refused while a reply runs.
	if err := app.Say(context.Background(), r.store, ownerAccess, chatID, "second", app.Picks{}); !errors.Is(err, app.ErrBusy) {
		t.Fatalf("second Say = %v, want ErrBusy", err)
	}
	if got := r.lines(chatID); len(got) != 1 {
		t.Fatalf("lines = %+v", got)
	}
	var runs int
	if err := r.owner.QueryRow(context.Background(), `SELECT count(*) FROM chat.runs WHERE chat_id = $1::uuid`, chatID).Scan(&runs); err != nil || runs != 1 {
		t.Fatalf("runs = %d %v", runs, err)
	}

	// Two Says race on a fresh chat: exactly one loses.
	fresh := r.open()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = app.Say(context.Background(), r.store, ownerAccess, fresh, "x", app.Picks{})
		}(i)
	}
	wg.Wait()
	busy := 0
	for _, e := range errs {
		if errors.Is(e, app.ErrBusy) {
			busy++
		} else if e != nil {
			t.Fatalf("Say error = %v", e)
		}
	}
	if busy != 1 {
		t.Fatalf("concurrent Says: %d busy, errors %+v", busy, errs)
	}

	// Another chat is not held up by it.
	other := r.open()
	r.say(other, "elsewhere", "")
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}
	if got := r.lines(other); len(got) != 2 || got[1].Status != "done" {
		t.Fatalf("other chat lines = %+v", got)
	}

	closeRelease()
	if err := result(t, running); err != nil {
		t.Fatal(err)
	}
	// After the first reply is done, the second Say is accepted.
	r.say(chatID, "second", "")
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}
	calls := r.stub.seen()
	sent := calls[len(calls)-1].Messages
	if len(sent) != 3 || sent[0].Text != "first" || sent[1].Role != "assistant" || sent[1].Text != "text" || sent[2].Text != "second" {
		t.Fatalf("history of the second reply = %+v", sent)
	}
}

func TestAStuckRunStopsBlockingAfterLiveFor(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	ctx := context.Background()
	chatID := r.open()
	r.say(chatID, "first", "")
	first := r.lastJob()
	if _, err := r.owner.Exec(ctx, `UPDATE chat.runs SET created_at = now() - interval '11 minutes 1 second' WHERE id = $1::uuid`, first.Args.ReplyID); err != nil {
		t.Fatal(err)
	}
	r.putDelta(chatID, first.Args.ReplyID, 1, "text", "partial")

	r.stub.reply(func(_ int, s *frames) { s.delta("ok"); s.done("m1", 1, 1, nil) })
	r.say(chatID, "second", "")
	lines := r.lines(chatID)
	if len(lines) != 3 || lines[2].Status != "failed" || lines[2].Body != "partial" {
		t.Fatalf("lines = %+v", lines)
	}
	if want := []domain.Attempt{{Model: "", Class: "timeout"}}; len(lines[2].Attempts) != 1 || lines[2].Attempts[0] != want[0] {
		t.Fatalf("attempts = %+v", lines[2].Attempts)
	}
	var running int
	if err := r.owner.QueryRow(ctx, `SELECT count(*) FROM chat.runs WHERE chat_id = $1::uuid AND state = 'running'`, chatID).Scan(&running); err != nil || running != 1 {
		t.Fatalf("running runs = %d %v", running, err)
	}

	// A run that is younger than LiveFor still blocks.
	fresh := r.open()
	r.say(fresh, "first", "")
	young := r.lastJob()
	if _, err := r.owner.Exec(ctx, `UPDATE chat.runs SET created_at = now() - interval '10 seconds' WHERE id = $1::uuid`, young.Args.ReplyID); err != nil {
		t.Fatal(err)
	}
	if err := app.Say(ctx, r.store, ownerAccess, fresh, "second", app.Picks{}); !errors.Is(err, app.ErrBusy) {
		t.Fatalf("Say on a young run = %v, want ErrBusy", err)
	}
}

func TestRoundsJoinTheirTextIntoOneLine(t *testing.T) {
	r := newRig(t, app.ReplyConfig{FlushBytes: 1})
	release, closeRelease := holdRound(t)
	chatID := r.open()
	r.say(chatID, "q", "")
	job1 := r.lastJob()
	r.stub.reply(func(call int, s *frames) {
		if call == 1 {
			s.delta("Let me check.")
			s.doneCalls("m1", 7, 2, i64(4), stubTool{ID: "c1", Name: "find_tool", Args: `{"query":"x"}`})
			return
		}
		s.delta("Done.")
		s.hold(release)
		s.done("m2", 10, 5, i64(3))
	})
	if err := r.run(job1, 1); err != nil {
		t.Fatal(err)
	}
	job2 := r.jobs()[1]
	done := r.runAsync(job2, 1)
	eventually(t, "the second round's text", func() bool { return len(r.deltas(chatID)) == 3 })
	deltas := r.deltas(chatID)
	if len(deltas) != 3 || deltas[0].Seq != 0 || deltas[0].Body != "" || deltas[1].Seq != 1 || deltas[2].Seq != 2 {
		t.Fatalf("deltas = %+v", deltas)
	}
	closeRelease()
	if err := result(t, done); err != nil {
		t.Fatal(err)
	}
	reply := r.lines(chatID)[1]
	if reply.Body != "Let me check.\n\nDone." || reply.Tokens != 24 || reply.Cost == nil || *reply.Cost != 7 || reply.Model == nil || *reply.Model != "m2" {
		t.Fatalf("reply = %+v", reply)
	}
}

func TestUnflushedTextOfEachRoundIsKept(t *testing.T) {
	r := newRig(t, app.ReplyConfig{FlushEvery: time.Hour, FlushBytes: 1 << 20})
	chatID := r.open()
	r.say(chatID, "q", "")
	job1 := r.lastJob()
	r.stub.reply(func(call int, s *frames) {
		if call == 1 {
			s.delta("Let me check.")
			s.doneCalls("m1", 7, 2, i64(4), stubTool{ID: "c1", Name: "find_tool", Args: `{"query":"x"}`})
			return
		}
		s.delta("Done.")
		s.done("m2", 10, 5, i64(3))
	})
	if err := r.run(job1, 1); err != nil {
		t.Fatal(err)
	}
	deltas := r.deltas(chatID)
	if len(deltas) != 2 || deltas[1].Seq != 1 || deltas[1].Body != "Let me check." {
		t.Fatalf("deltas after round 1 = %+v", deltas)
	}
	job2 := r.jobs()[1]
	if err := r.run(job2, 1); err != nil {
		t.Fatal(err)
	}
	if reply := r.lines(chatID)[1]; reply.Body != "Let me check.\n\nDone." {
		t.Fatalf("reply = %+v", reply)
	}
}

func TestTheBodyCapCoversAllRounds(t *testing.T) {
	r := newRig(t, app.ReplyConfig{FlushBytes: 1})
	chatID := r.open()
	r.say(chatID, "q", "")
	job1 := r.lastJob()
	release, closeRelease := holdRound(t)
	r.stub.reply(func(call int, s *frames) {
		if call == 1 {
			for i := 0; i < 20; i++ {
				s.delta(strings.Repeat("a", 1000))
			}
			s.doneCalls("m1", 1, 1, i64(1), stubTool{ID: "c1", Name: "find_tool", Args: `{"query":"x"}`})
			return
		}
		for i := 0; i < 20; i++ {
			s.delta(strings.Repeat("é", 1000))
		}
		s.hold(release)
		s.done("m2", 1, 1, i64(1))
	})
	if err := r.run(job1, 1); err != nil {
		t.Fatal(err)
	}
	job2 := r.jobs()[1]
	done := r.runAsync(job2, 1)
	eventually(t, "the second round to reach its hold", func() bool {
		var sum int
		if err := r.owner.QueryRow(context.Background(), `
			SELECT COALESCE(sum(char_length(body)), 0) FROM chat.deltas
			WHERE line_id = $1::uuid AND kind = 'text'`, job1.Args.ReplyID).Scan(&sum); err != nil {
			return false
		}
		return sum == 32000
	})
	closeRelease()
	if err := result(t, done); err != nil {
		t.Fatal(err)
	}
	if reply := r.lines(chatID)[1]; reply.Body != strings.Repeat("a", 20000)+"\n\n"+strings.Repeat("é", 11998) {
		t.Fatalf("reply body = %d runes", len([]rune(reply.Body)))
	}
}

func TestAnUnknownRoundCostMakesTheReplyCostNull(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	chatID := r.open()
	r.say(chatID, "q", "")
	job1 := r.lastJob()
	r.stub.reply(func(call int, s *frames) {
		if call == 1 {
			s.doneCalls("m1", 1, 1, nil, stubTool{ID: "c1", Name: "find_tool", Args: `{"query":"x"}`})
			return
		}
		s.delta("ok")
		s.done("m2", 1, 1, i64(5))
	})
	if err := r.run(job1, 1); err != nil {
		t.Fatal(err)
	}
	if err := r.run(r.jobs()[1], 1); err != nil {
		t.Fatal(err)
	}
	reply := r.lines(chatID)[1]
	if reply.Cost != nil || reply.Tokens != 4 {
		t.Fatalf("reply = %+v", reply)
	}
	if row := r.runRow(job1.Args.ReplyID); row.CostKnown {
		t.Fatalf("run row = %+v", row)
	}
}

func TestKnownFreeRoundsCostZero(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	chatID := r.open()
	r.say(chatID, "q", "")
	job1 := r.lastJob()
	r.stub.reply(func(call int, s *frames) {
		if call == 1 {
			s.doneCalls("m1", 1, 1, i64(0), stubTool{ID: "c1", Name: "find_tool", Args: `{"query":"x"}`})
			return
		}
		s.delta("ok")
		s.done("m1", 1, 1, i64(0))
	})
	if err := r.run(job1, 1); err != nil {
		t.Fatal(err)
	}
	if err := r.run(r.jobs()[1], 1); err != nil {
		t.Fatal(err)
	}
	reply := r.lines(chatID)[1]
	if reply.Cost == nil || *reply.Cost != 0 {
		t.Fatalf("reply cost = %+v", reply.Cost)
	}
	rows, err := r.owner.Query(context.Background(), `SELECT cost_micro_usd FROM chat.turns WHERE chat_id = $1::uuid AND role = 'assistant' ORDER BY seq`, chatID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cost *int64
		if err := rows.Scan(&cost); err != nil {
			t.Fatal(err)
		}
		if cost == nil || *cost != 0 {
			t.Fatalf("assistant turn cost = %v", cost)
		}
	}
}

func TestATakeoverFencesTheOldClaim(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	ctx := context.Background()
	chatID := r.open()
	r.say(chatID, "q", "")
	job1 := r.lastJob()

	c1, err := r.store.Claim(ctx, ownerAccess, job1.Args, token1)
	if err != nil || c1.State != app.ClaimGo {
		t.Fatalf("claim 1 = %+v %v", c1, err)
	}
	c2, err := r.store.Claim(ctx, ownerAccess, job1.Args, token2)
	if err != nil || c2.State != app.ClaimRescue {
		t.Fatalf("claim 2 = %+v %v", c2, err)
	}
	if got := r.lines(chatID); len(got) != 1 {
		t.Fatalf("a takeover stored a line: %+v", got)
	}

	t.Run("delta", func(t *testing.T) {
		if err := r.store.PutDelta(ctx, ownerAccess, c1, 1, "text", "x"); !errors.Is(err, app.ErrFenced) {
			t.Fatalf("PutDelta(c1) = %v, want ErrFenced", err)
		}
		if got := r.deltas(chatID); len(got) != 1 || got[0].Body != "" {
			t.Fatalf("deltas = %+v", got)
		}
		if err := r.store.PutDelta(ctx, ownerAccess, c2, 1, "text", "x"); err != nil {
			t.Fatalf("PutDelta(c2) = %v", err)
		}
	})
	t.Run("commit", func(t *testing.T) {
		rc := app.RoundCommit{Turn: agentloop.TurnCommit{
			RunID: job1.Args.ReplyID, ObjectID: chatID, SeqStart: 1,
			Messages: []agentloop.Message{{Role: agentloop.RoleAssistant, Text: "x"}},
			History:  []agentloop.Message{{Role: agentloop.RoleUser}, {Role: agentloop.RoleAssistant}},
			Status:   agentloop.TurnDone, Model: "m1",
		}, End: app.RoundDone}
		if err := r.store.CommitRound(ctx, ownerAccess, c1, rc); !errors.Is(err, app.ErrFenced) {
			t.Fatalf("CommitRound(c1) = %v, want ErrFenced", err)
		}
		var turns int
		if err := r.owner.QueryRow(ctx, `SELECT count(*) FROM chat.turns WHERE chat_id = $1::uuid`, chatID).Scan(&turns); err != nil || turns != 0 {
			t.Fatalf("turns = %d %v", turns, err)
		}
		if got := r.lines(chatID); len(got) != 1 {
			t.Fatalf("lines = %+v", got)
		}
	})
	t.Run("finish", func(t *testing.T) {
		if err := r.store.Finish(ctx, ownerAccess, c1, app.ReplyEnd{Model: "m1"}); !errors.Is(err, app.ErrFenced) {
			t.Fatalf("Finish(c1) = %v, want ErrFenced", err)
		}
		if got := r.lines(chatID); len(got) != 1 {
			t.Fatalf("lines = %+v", got)
		}
	})
	t.Run("fail_reply", func(t *testing.T) {
		if stored, err := r.store.FailReply(ctx, job1.Args, token1, "access"); err != nil || stored {
			t.Fatalf("FailReply = %v %v", stored, err)
		}
		if got := r.lines(chatID); len(got) != 1 {
			t.Fatalf("lines = %+v", got)
		}
	})
}

func TestAnAuthorWhoLostAccessBetweenRoundsGetsAFailedReply(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	chatID := r.open()
	r.say(chatID, "q", "")
	job1 := r.lastJob()
	r.stub.reply(func(call int, s *frames) {
		if call == 1 {
			s.delta("a")
			s.doneCalls("m1", 1, 2, i64(4), stubTool{ID: "c1", Name: "find_tool", Args: `{"query":"x"}`})
			return
		}
		s.delta("b")
		s.done("m1", 1, 1, i64(1))
	})
	if err := r.run(job1, 1); err != nil {
		t.Fatal(err)
	}
	job2 := r.jobs()[1]
	r.dropAccess(ownerUser)
	if err := r.run(job2, 1); err != nil {
		t.Fatal(err)
	}
	reply := r.lines(chatID)[1]
	if reply.Status != "failed" || reply.Body != "a" || reasonOf(reply) != "you no longer have access to this chat" {
		t.Fatalf("reply = %+v", reply)
	}
}

func TestATakenOverWorkerWritesNothingMore(t *testing.T) {
	r := newRig(t, app.ReplyConfig{FlushBytes: 1})
	release, closeRelease := holdRound(t)
	chatID := r.open()
	r.say(chatID, "q", "")
	job1 := r.lastJob()
	r.stub.reply(func(_ int, s *frames) {
		s.delta("first")
		s.hold(release)
		s.delta("second")
		s.done("m1", 1, 1, nil)
	})
	done := r.runAsync(job1, 1)
	eventually(t, "the first delta", func() bool { return len(r.deltas(chatID)) == 2 })

	if err := r.run(job1, 2); err != nil {
		t.Fatal(err)
	}
	lines := r.lines(chatID)
	if len(lines) != 2 || lines[1].Body != "first" || reasonOf(lines[1]) != "stopped" {
		t.Fatalf("lines = %+v", lines)
	}

	closeRelease()
	if err := result(t, done); err != nil {
		t.Fatal(err)
	}
	if got := r.lines(chatID); len(got) != 2 {
		t.Fatalf("lines = %+v", got)
	}
	var turns int
	if err := r.owner.QueryRow(context.Background(), `SELECT count(*) FROM chat.turns WHERE chat_id = $1::uuid`, chatID).Scan(&turns); err != nil || turns != 0 {
		t.Fatalf("turns = %d %v", turns, err)
	}
	if got := r.deltas(chatID); len(got) != 0 {
		t.Fatalf("deltas = %+v", got)
	}
}

func TestARescuedRoundFailsWithItsTextAndUnknownCost(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	release, closeRelease := holdRound(t)
	chatID := r.open()
	r.say(chatID, "q", "")
	job1 := r.lastJob()
	r.stub.reply(func(call int, s *frames) {
		if call == 1 {
			s.delta("a")
			s.doneCalls("m1", 3, 1, i64(4), stubTool{ID: "c1", Name: "find_tool", Args: `{"query":"x"}`})
			return
		}
		s.delta("b")
		s.hold(release)
		s.done("m1", 1, 1, i64(1))
	})
	if err := r.run(job1, 1); err != nil {
		t.Fatal(err)
	}
	job2 := r.jobs()[1]
	done := r.runAsync(job2, 1)
	eventually(t, "the second round's text", func() bool { return len(r.deltas(chatID)) == 3 })

	if err := r.run(job2, 2); err != nil {
		t.Fatal(err)
	}
	reply := r.lines(chatID)[1]
	if reply.Status != "failed" || reply.Body != "a\n\nb" || reply.Tokens != 4 || reply.Cost != nil {
		t.Fatalf("reply = %+v", reply)
	}
	want := []domain.Attempt{{Model: "m1", Class: "canceled"}}
	if len(reply.Attempts) != 1 || reply.Attempts[0] != want[0] {
		t.Fatalf("attempts = %+v", reply.Attempts)
	}
	closeRelease()
	if err := result(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestReplyRescuesACrashedRunFromItsDeltas(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	chatID := r.open()
	r.say(chatID, "hi", "")
	queued := r.lastJob()
	ctx := context.Background()

	c1, err := r.store.Claim(ctx, ownerAccess, queued.Args, token1)
	if err != nil || c1.State != app.ClaimGo {
		t.Fatalf("claim = %+v %v", c1, err)
	}
	for seq, d := range []struct{ kind, body string }{
		{"text", "half an "},
		{"text", "answer"},
		{"status", `{"failed_model":"m1","class":"timeout","next_model":"m2"}`},
	} {
		if err := r.store.PutDelta(ctx, ownerAccess, c1, seq+1, d.kind, d.body); err != nil {
			t.Fatal(err)
		}
	}

	if err := r.run(queued, 2); err != nil {
		t.Fatal(err)
	}
	if calls := r.stub.seen(); len(calls) != 0 {
		t.Fatalf("the rerun called the model: %+v", calls)
	}
	reply := r.lines(chatID)[1]
	if reply.Status != "failed" || reply.Body != "half an answer" || reply.ID != queued.Args.ReplyID {
		t.Fatalf("reply = %+v", reply)
	}
	want := []domain.Attempt{{Model: "m1", Class: "timeout"}, {Model: "m2", Class: "canceled"}}
	if len(reply.Attempts) != 2 || reply.Attempts[0] != want[0] || reply.Attempts[1] != want[1] {
		t.Fatalf("attempts = %+v", reply.Attempts)
	}
	if reply.Model == nil || *reply.Model != "m2" {
		t.Fatalf("model = %v", reply.Model)
	}
	if left := r.deltas(chatID); len(left) != 0 {
		t.Fatalf("deltas = %+v", left)
	}

	// A third run, after the line is stored, does nothing.
	if err := r.run(queued, 2); err != nil {
		t.Fatal(err)
	}
	if got := r.lines(chatID); len(got) != 2 {
		t.Fatalf("lines = %+v", got)
	}
}

func TestRerunOfACommittedRoundIsANoOp(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	chatID := r.open()
	r.say(chatID, "q", "")
	job1 := r.lastJob()
	r.stub.reply(func(call int, s *frames) {
		if call == 1 {
			s.doneCalls("m1", 1, 1, i64(1), stubTool{ID: "c1", Name: "find_tool", Args: `{"query":"x"}`})
			return
		}
		s.delta("ok")
		s.done("m1", 1, 1, i64(1))
	})
	if err := r.run(job1, 1); err != nil {
		t.Fatal(err)
	}
	// Rerun round 1: it is committed, so it is a no-op.
	if err := r.run(job1, 2); err != nil {
		t.Fatal(err)
	}
	if calls := r.stub.seen(); len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	job2 := r.jobs()[1]
	if err := r.run(job2, 1); err != nil {
		t.Fatal(err)
	}
	if got := r.lines(chatID)[1]; got.Status != "done" || got.Body != "ok" {
		t.Fatalf("reply = %+v", got)
	}
	var turns int
	if err := r.owner.QueryRow(context.Background(), `SELECT count(*) FROM chat.turns WHERE chat_id = $1::uuid`, chatID).Scan(&turns); err != nil || turns != 3 {
		t.Fatalf("turns = %d %v", turns, err)
	}
}

func TestARoundPastTheReplyDeadlineFailsAsTimedOut(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	chatID := r.open()
	r.say(chatID, "q", "")
	job1 := r.lastJob()
	if _, err := r.owner.Exec(context.Background(), `UPDATE chat.runs SET created_at = now() - interval '10 minutes 1 second' WHERE id = $1::uuid`, job1.Args.ReplyID); err != nil {
		t.Fatal(err)
	}
	r.stub.reply(func(_ int, s *frames) { s.delta("ok"); s.done("m1", 1, 1, nil) })
	if err := r.run(job1, 1); err != nil {
		t.Fatal(err)
	}
	if calls := r.stub.seen(); len(calls) != 0 {
		t.Fatalf("calls = %+v", calls)
	}
	reply := r.lines(chatID)[1]
	if reply.Status != "failed" || reasonOf(reply) != "timed out" {
		t.Fatalf("reply = %+v", reply)
	}
}

func TestARoundCutByTheReplyDeadlineIsTimedOut(t *testing.T) {
	r := newRig(t, app.ReplyConfig{Timeout: time.Second})
	chatID := r.open()
	r.say(chatID, "q", "")
	job1 := r.lastJob()
	r.stub.reply(func(call int, s *frames) {
		if call == 1 {
			s.doneCalls("m1", 1, 1, i64(1), stubTool{ID: "c1", Name: "find_tool", Args: `{"query":"x"}`})
			return
		}
		s.hold(nil)
	})
	if err := r.run(job1, 1); err != nil {
		t.Fatal(err)
	}
	if err := r.run(r.jobs()[1], 1); err != nil {
		t.Fatal(err)
	}
	reply := r.lines(chatID)[1]
	if reply.Status != "failed" || reasonOf(reply) != "timed out" {
		t.Fatalf("reply = %+v", reply)
	}
}

func TestOnlyEightToolCallsRunInARound(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	chatID := r.open()
	r.say(chatID, "q", "")
	var tools []stubTool
	for i := 1; i <= 9; i++ {
		tools = append(tools, stubTool{ID: fmt.Sprintf("c%d", i), Name: "find_tool", Args: `{"query":"x"}`})
	}
	r.stub.reply(func(call int, s *frames) {
		if call == 1 {
			s.doneCalls("m1", 1, 1, i64(1), tools...)
			return
		}
		s.delta("ok")
		s.done("m1", 1, 1, i64(1))
	})
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}
	if err := r.run(r.jobs()[1], 1); err != nil {
		t.Fatal(err)
	}
	calls := r.stub.seen()
	second := calls[1]
	var toolMsgs []string
	for _, m := range second.Messages {
		if m.Role == "tool" {
			toolMsgs = append(toolMsgs, m.Text)
		}
	}
	if len(toolMsgs) != 9 {
		t.Fatalf("tool messages = %d", len(toolMsgs))
	}
	if toolMsgs[8] != "skipped: more than 8 tool calls in one round" {
		t.Fatalf("9th tool message = %q", toolMsgs[8])
	}
}

func TestTheFifteenthRoundEndsTheReply(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	chatID := r.open()
	r.say(chatID, "q", "")
	r.stub.reply(func(call int, s *frames) {
		s.doneCalls("m1", 1, 0, i64(1), stubTool{ID: fmt.Sprintf("c%d", call), Name: "find_tool", Args: `{"query":"x"}`})
	})
	job := r.lastJob()
	for round := 1; round <= 15; round++ {
		if err := r.run(job, 1); err != nil {
			t.Fatal(err)
		}
		if round < 15 {
			job = r.jobs()[round]
		}
	}
	if got := len(r.jobs()); got != 15 {
		t.Fatalf("jobs = %d", got)
	}
	reply := r.lines(chatID)[1]
	if reply.Status != "failed" || reasonOf(reply) != "the reply took too many steps" || reply.Cost == nil || *reply.Cost != 15 {
		t.Fatalf("reply = %+v", reply)
	}
}

func TestTurnSeqContinuesAcrossReplies(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	chatID := r.open()
	script := func(call int, s *frames) {
		if call%2 == 1 {
			s.doneCalls("m1", 1, 1, i64(1), stubTool{ID: fmt.Sprintf("c%d", call), Name: "find_tool", Args: `{"query":"x"}`})
			return
		}
		s.delta("ok")
		s.done("m1", 1, 1, i64(1))
	}
	r.stub.reply(script)
	r.say(chatID, "q1", "")
	job := r.lastJob()
	r.run(job, 1)
	job = r.jobs()[1]
	r.run(job, 1)
	r.say(chatID, "q2", "")
	job = r.lastJob()
	r.run(job, 1)
	job = r.jobs()[len(r.jobs())-1]
	r.run(job, 1)

	rows, err := r.owner.Query(context.Background(), `SELECT seq FROM chat.turns WHERE chat_id = $1::uuid ORDER BY seq`, chatID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var seqs []int
	for rows.Next() {
		var seq int
		if err := rows.Scan(&seq); err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, seq)
	}
	want := []int{1, 2, 3, 4, 5, 6}
	if len(seqs) != len(want) {
		t.Fatalf("turn seqs = %v", seqs)
	}
	for i := range want {
		if seqs[i] != want[i] {
			t.Fatalf("turn seqs = %v", seqs)
		}
	}
	lines := r.lines(chatID)
	if len(lines) != 4 || lines[1].Status != "done" || lines[3].Status != "done" {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestAnAgentReplyCanOpenAHouseRule(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	ctx := context.Background()
	chatID := r.open()
	agent := r.agent(ownerAccess, agents.AgentFields{Name: "Research", Enabled: true})
	lessonID, err := agents.SaveLesson(ctx, r.agents, agentsAccess(ownerAccess), agent.ID, agents.LessonFields{
		Title: "Tone", Cue: "when writing", Body: "Be plain.",
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Say(ctx, r.store, ownerAccess, chatID, "q", app.Picks{AgentID: agent.ID}); err != nil {
		t.Fatal(err)
	}
	job1 := r.lastJob()
	r.stub.reply(func(call int, s *frames) {
		if call == 1 {
			s.doneCalls("m1", 1, 1, nil, stubTool{ID: "c1", Name: "open_lesson", Args: fmt.Sprintf(`{"lesson_id":%q}`, lessonID)})
			return
		}
		s.delta("ok")
		s.done("m1", 1, 1, nil)
	})
	if err := r.run(job1, 1); err != nil {
		t.Fatal(err)
	}
	calls := r.stub.seen()
	if !strings.HasSuffix(calls[0].System, "## House rules index\n\n- [Tone -> when writing] (id: "+lessonID+")") {
		t.Fatalf("system = %q", calls[0].System)
	}
	hasOpen := false
	for _, tool := range calls[0].Tools {
		if tool.Name == "open_lesson" {
			hasOpen = true
		}
	}
	if !hasOpen {
		t.Fatalf("tools = %+v", calls[0].Tools)
	}
	if err := r.run(r.jobs()[1], 1); err != nil {
		t.Fatal(err)
	}
	calls = r.stub.seen()
	last := calls[len(calls)-1].Messages[len(calls[len(calls)-1].Messages)-1]
	if last.Role != "tool" || !strings.Contains(last.Text, "Be plain.") {
		t.Fatalf("last message = %+v", last)
	}
}

func TestAReplyWithoutAnAgentHasNoLessonTool(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	chatID := r.open()
	r.say(chatID, "q", "")
	r.stub.reply(func(_ int, s *frames) { s.delta("ok"); s.done("m1", 1, 1, nil) })
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}
	call := r.stub.seen()[0]
	var names []string
	for _, tool := range call.Tools {
		names = append(names, tool.Name)
	}
	want := []string{"retrieve_output", "find_tool"}
	if len(names) != len(want) || names[0] != want[0] || names[1] != want[1] {
		t.Fatalf("tools = %+v", names)
	}
	if call.System != "" {
		t.Fatalf("system = %q", call.System)
	}
}

func TestAnOffloadedOutputIsRetrievableInALaterRound(t *testing.T) {
	r := newRig(t, app.ReplyConfig{OffloadAtTokens: 50})
	ctx := context.Background()
	chatID := r.open()
	agent := r.agent(ownerAccess, agents.AgentFields{Name: "Research", Enabled: true})
	lessonID, err := agents.SaveLesson(ctx, r.agents, agentsAccess(ownerAccess), agent.ID, agents.LessonFields{
		Title: "Tone", Cue: "when writing", Body: strings.Repeat("x", 4000),
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Say(ctx, r.store, ownerAccess, chatID, "q", app.Picks{AgentID: agent.ID}); err != nil {
		t.Fatal(err)
	}
	job1 := r.lastJob()
	r.stub.reply(func(call int, s *frames) {
		if call == 1 {
			s.doneCalls("m1", 1, 1, nil, stubTool{ID: "c1", Name: "open_lesson", Args: fmt.Sprintf(`{"lesson_id":%q}`, lessonID)})
			return
		}
		if call == 2 {
			msgs := r.stub.seen()[1].Messages
			id := strings.TrimPrefix(msgs[len(msgs)-1].Text, "retrieve_output:")
			s.doneCalls("m1", 1, 1, nil, stubTool{ID: "c2", Name: "retrieve_output", Args: fmt.Sprintf(`{"id":%q}`, id)})
			return
		}
		s.delta("ok")
		s.done("m1", 1, 1, nil)
	})
	if err := r.run(job1, 1); err != nil {
		t.Fatal(err)
	}
	if err := r.run(r.jobs()[1], 1); err != nil {
		t.Fatal(err)
	}
	calls := r.stub.seen()
	second := calls[1].Messages
	last := second[len(second)-1]
	if last.Role != "tool" || !strings.HasPrefix(last.Text, "retrieve_output:") {
		t.Fatalf("round 2 last message = %+v", last)
	}
	if err := r.run(r.jobs()[2], 1); err != nil {
		t.Fatal(err)
	}
	expected, _ := json.Marshal(agentloop.Lesson{ID: lessonID, Title: "Tone", Cue: "when writing", Body: strings.Repeat("x", 4000)})
	wantText := string(expected)[:72] + fmt.Sprintf("\n[total_bytes=%d next_offset=72]", len(expected))
	calls = r.stub.seen()
	third := calls[2].Messages
	last = third[len(third)-1]
	if last.Role != "tool" || last.Text != wantText {
		t.Fatalf("round 3 tool text = %q, want %q", last.Text, wantText)
	}
}

func TestASpendLimitInRoundTwoFailsWithItsReason(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	chatID := r.open()
	r.say(chatID, "q", "")
	job1 := r.lastJob()
	r.stub.reply(func(call int, s *frames) {
		if call == 1 {
			s.delta("ok?")
			s.doneCalls("m1", 1, 1, i64(1), stubTool{ID: "c1", Name: "find_tool", Args: `{"query":"x"}`})
			return
		}
		s.fail("limit_reached", "m1")
	})
	if err := r.run(job1, 1); err != nil {
		t.Fatal(err)
	}
	if err := r.run(r.jobs()[1], 1); err != nil {
		t.Fatal(err)
	}
	reply := r.lines(chatID)[1]
	if reply.Status != "failed" || reply.Body != "ok?" || reasonOf(reply) != "spend limit reached" {
		t.Fatalf("reply = %+v", reply)
	}
	want := []domain.Attempt{{Model: "m1", Class: "limit_reached"}}
	if len(reply.Attempts) != 1 || reply.Attempts[0] != want[0] {
		t.Fatalf("attempts = %+v", reply.Attempts)
	}
}

func TestAFailoverInALaterRoundIsKeptInTheAttempts(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	chatID := r.open()
	r.say(chatID, "q", "")
	job1 := r.lastJob()
	r.stub.reply(func(call int, s *frames) {
		if call == 1 {
			s.delta("ok?")
			s.doneCalls("m1", 1, 1, i64(1), stubTool{ID: "c1", Name: "find_tool", Args: `{"query":"x"}`})
			return
		}
		s.attempt("m1", "timeout", "m2")
		s.fail("limit_reached", "m2")
	})
	if err := r.run(job1, 1); err != nil {
		t.Fatal(err)
	}
	if err := r.run(r.jobs()[1], 1); err != nil {
		t.Fatal(err)
	}
	reply := r.lines(chatID)[1]
	if reply.Status != "failed" || reasonOf(reply) != "spend limit reached" || reply.Model == nil || *reply.Model != "m2" {
		t.Fatalf("reply = %+v", reply)
	}
	want := []domain.Attempt{{Model: "m1", Class: "timeout"}, {Model: "m2", Class: "limit_reached"}}
	if len(reply.Attempts) != 2 || reply.Attempts[0] != want[0] || reply.Attempts[1] != want[1] {
		t.Fatalf("attempts = %+v", reply.Attempts)
	}
}

func TestATimeoutAfterAnEarlierFailoverNamesTheCurrentModel(t *testing.T) {
	r := newRig(t, app.ReplyConfig{Timeout: time.Second})
	chatID := r.open()
	r.say(chatID, "q", "")
	job1 := r.lastJob()
	r.stub.reply(func(call int, s *frames) {
		if call == 1 {
			s.attempt("m1", "timeout", "m2")
			s.doneCalls("m2", 1, 1, i64(1), stubTool{ID: "c1", Name: "find_tool", Args: `{"query":"x"}`})
			return
		}
		s.hold(nil)
	})
	if err := r.run(job1, 1); err != nil {
		t.Fatal(err)
	}
	if err := r.run(r.jobs()[1], 1); err != nil {
		t.Fatal(err)
	}
	reply := r.lines(chatID)[1]
	if reply.Status != "failed" || reasonOf(reply) != "timed out" || reply.Model == nil || *reply.Model != "m2" {
		t.Fatalf("reply = %+v", reply)
	}
	want := []domain.Attempt{{Model: "m1", Class: "timeout"}, {Model: "m2", Class: "timeout"}}
	if len(reply.Attempts) != 2 || reply.Attempts[0] != want[0] || reply.Attempts[1] != want[1] {
		t.Fatalf("attempts = %+v", reply.Attempts)
	}
}

func TestABlockedAgentReadInALaterRoundIsTimedOut(t *testing.T) {
	r := newRig(t, app.ReplyConfig{Timeout: time.Second})
	ctx := context.Background()
	chatID := r.open()
	agent := r.agent(ownerAccess, agents.AgentFields{Name: "Research", Enabled: true})
	r.stub.reply(func(call int, s *frames) {
		if call == 1 {
			s.doneCalls("m1", 1, 1, i64(1), stubTool{ID: "c1", Name: "find_tool", Args: `{"query":"x"}`})
			return
		}
		s.delta("ok")
		s.done("m1", 1, 1, i64(1))
	})
	if err := app.Say(ctx, r.store, ownerAccess, chatID, "q", app.Picks{AgentID: agent.ID}); err != nil {
		t.Fatal(err)
	}
	job1 := r.lastJob()
	if err := r.run(job1, 1); err != nil {
		t.Fatal(err)
	}
	r.agentLookup.block = true
	job2 := r.jobs()[1]
	start := time.Now()
	if err := r.run(job2, 1); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("round 2 outlived the reply deadline")
	}
	reply := r.lines(chatID)[1]
	if reply.Status != "failed" || reasonOf(reply) != "timed out" {
		t.Fatalf("reply = %+v", reply)
	}
	want := domain.Attempt{Model: "m1", Class: "timeout"}
	if len(reply.Attempts) != 1 || reply.Attempts[0] != want {
		t.Fatalf("attempts = %+v", reply.Attempts)
	}
	if calls := r.stub.seen(); len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	if row := r.runRow(job1.Args.ReplyID); row.State != "failed" {
		t.Fatalf("run row = %+v", row)
	}
}

func TestAnEmptyFinalAnswerFails(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	chatID := r.open()
	r.say(chatID, "q", "")
	job1 := r.lastJob()
	r.stub.reply(func(call int, s *frames) {
		if call == 1 {
			s.doneCalls("m1", 1, 1, i64(1), stubTool{ID: "c1", Name: "find_tool", Args: `{"query":"x"}`})
			return
		}
		s.done("m1", 1, 1, i64(1))
	})
	if err := r.run(job1, 1); err != nil {
		t.Fatal(err)
	}
	if err := r.run(r.jobs()[1], 1); err != nil {
		t.Fatal(err)
	}
	reply := r.lines(chatID)[1]
	if reply.Status != "failed" || reasonOf(reply) != "the model gave no answer" {
		t.Fatalf("reply = %+v", reply)
	}
}

func TestARoundSendsTheReplyAsItsRunAndATeamChatsTeam(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	ctx := context.Background()
	chatID := r.open()
	r.say(chatID, "q", "")
	job1 := r.lastJob()
	r.stub.reply(func(_ int, s *frames) { s.delta("ok"); s.done("m1", 1, 1, nil) })
	if err := r.run(job1, 1); err != nil {
		t.Fatal(err)
	}
	call := r.stub.seen()[0]
	if call.RunID != job1.Args.ReplyID || call.TeamID != "" {
		t.Fatalf("call = %+v", call)
	}

	const teamID = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaa99"
	owner := ownerAccess
	owner.Teams = "{" + teamID + "}"
	r.setAccess(ownerUser, owner)
	if _, err := r.owner.Exec(ctx, `UPDATE chat.chats SET visibility = 'team', visibility_team_id = $2::uuid WHERE id = $1::uuid`, chatID, teamID); err != nil {
		t.Fatal(err)
	}
	if err := app.Say(ctx, r.store, owner, chatID, "q2", app.Picks{}); err != nil {
		t.Fatal(err)
	}
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}
	calls := r.stub.seen()
	last := calls[len(calls)-1]
	if last.TeamID != teamID {
		t.Fatalf("team call = %+v", last)
	}
}
