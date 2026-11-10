//go:build integration

package chat_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river/rivertype"

	"github.com/upmo-ai/upmo/internal/chat"
	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/chat/domain"
)

func TestSayQueuesOneReplyOnItsOwnQueueUniqueByLine(t *testing.T) {
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
	if queued.Args.LineID != user.ID || queued.Args.ChatID != chatID || queued.Args.UserID != ownerUser || queued.Args.Route != "chat" {
		t.Fatalf("args = %+v, user line %s", queued.Args, user.ID)
	}

	// The same user line cannot be queued twice while a reply to it is open.
	tx, err := r.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	again := queued.Args
	again.ReplyID = "018f3b2c-7a10-7c4a-8e11-999999999999"
	res, err := r.work.InsertTx(context.Background(), tx, again, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.UniqueSkippedAsDuplicate {
		t.Fatal("a second reply job for the same line was queued")
	}
}

func TestSayQueuesNoReplyWhenRepliesAreOff(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	r.store.ReplyRoute = ""
	chatID := r.open()
	r.say(chatID, "hello", "")
	if got := r.jobs(); len(got) != 0 {
		t.Fatalf("jobs = %+v", got)
	}
	if err := app.Retry(context.Background(), r.store, ownerAccess, chatID, chatID, nil); !errors.Is(err, app.ErrReplies) {
		t.Fatalf("retry error = %v", err)
	}
}

func TestReplyStreamsDeltasThenStoresTheLineAndDropsTheDeltas(t *testing.T) {
	r := newRig(t, app.ReplyConfig{FlushEvery: 20 * time.Millisecond})
	release := make(chan struct{})
	r.stub.reply(func(_ int, s *frames) {
		s.delta("Hello ")
		s.hold(release)
		s.delta("world")
		s.done("m1", 3, 4, f64(0.0125))
	})
	chatID := r.open()
	r.say(chatID, "hi", "")
	queued := r.lastJob()
	done := r.runAsync(queued, 1)

	// While the model writes, the chat holds the reply as deltas: a start
	// marker and the text so far, under the id the line will get.
	eventually(t, "the first text delta", func() bool { return len(r.deltas(chatID)) == 2 })
	got := r.deltas(chatID)
	if got[0].Seq != 0 || got[0].Kind != "text" || got[0].Body != "" || got[1].Seq != 1 || got[1].Body != "Hello " {
		t.Fatalf("deltas = %+v", got)
	}
	for _, d := range got {
		if d.LineID != queued.Args.ReplyID {
			t.Fatalf("delta line = %s, want %s", d.LineID, queued.Args.ReplyID)
		}
	}
	if lines := r.lines(chatID); len(lines) != 1 {
		t.Fatalf("lines while streaming = %+v", lines)
	}

	close(release)
	if err := result(t, done); err != nil {
		t.Fatal(err)
	}
	lines := r.lines(chatID)
	if len(lines) != 2 {
		t.Fatalf("lines = %+v", lines)
	}
	reply := lines[1]
	if reply.ID != queued.Args.ReplyID || reply.Role != "assistant" || reply.Status != "done" || reply.Body != "Hello world" {
		t.Fatalf("reply = %+v", reply)
	}
	if reply.Model == nil || *reply.Model != "m1" || reply.Tokens != 7 || reply.Cost == nil || *reply.Cost != 12500 {
		t.Fatalf("reply cost = %+v", reply)
	}
	if reply.ReplyTo == nil || *reply.ReplyTo != lines[0].ID || reply.Attempts != nil {
		t.Fatalf("reply links = %+v", reply)
	}
	if left := r.deltas(chatID); len(left) != 0 {
		t.Fatalf("deltas after the line = %+v", left)
	}
}

func TestFinishRollsBackTheLineAndTheDeltaDeleteTogether(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	chatID := r.open()
	r.say(chatID, "hi", "")
	queued := r.lastJob()
	ctx := context.Background()
	if _, err := r.store.Begin(ctx, ownerAccess, queued.Args); err != nil {
		t.Fatal(err)
	}

	// A body over the limit fails the insert; the delete must not stand alone.
	err := r.store.Finish(ctx, ownerAccess, app.Reply{
		ChatID: chatID, LineID: queued.Args.ReplyID, ReplyTo: queued.Args.LineID,
		Status: domain.StatusDone, Body: strings.Repeat("x", domain.ReplyRunes+1), Model: "m1",
	})
	if err == nil {
		t.Fatal("an over-long reply was stored")
	}
	if got := r.deltas(chatID); len(got) != 1 {
		t.Fatalf("deltas after a failed finish = %+v", got)
	}
	if got := r.lines(chatID); len(got) != 1 {
		t.Fatalf("lines after a failed finish = %+v", got)
	}
}

func TestReplyWritesOneDeltaRowAndOneNotifyPerBatch(t *testing.T) {
	// The time flush is out of reach, so only the size flush runs.
	r := newRig(t, app.ReplyConfig{FlushEvery: time.Hour})
	release := make(chan struct{})
	piece := strings.Repeat("p", 100)
	r.stub.reply(func(_ int, s *frames) {
		for i := 0; i < 100; i++ {
			s.delta(piece)
		}
		s.hold(release)
		s.done("m1", 1, 1, nil)
	})
	chatID := r.open()
	r.say(chatID, "hi", "")
	queued := r.lastJob()

	listener, err := pgx.ConnectConfig(context.Background(), r.owner.Config().ConnConfig)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := listener.Exec(context.Background(), "LISTEN chat_stream"); err != nil {
		t.Fatal(err)
	}
	listening, stop := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	t.Cleanup(func() {
		stop()
		<-stopped
		_ = listener.Close(context.Background())
	})
	var mu sync.Mutex
	notes := 0
	go func() {
		defer close(stopped)
		for {
			if _, err := listener.WaitForNotification(listening); err != nil {
				return
			}
			mu.Lock()
			notes++
			mu.Unlock()
		}
	}()
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return notes
	}

	done := r.runAsync(queued, 1)
	// 10,000 bytes at 2 KB: four rows of 2,100 bytes, and 1,600 bytes pending.
	eventually(t, "four text rows and the marker", func() bool { return len(r.deltas(chatID)) == 5 })
	rows := r.deltas(chatID)
	for i, row := range rows {
		want := 2100
		if i == 0 {
			want = 0
		}
		if row.Seq != i || len(row.Body) != want {
			t.Fatalf("row %d = seq %d, %d bytes", i, row.Seq, len(row.Body))
		}
	}
	close(release)
	if err := result(t, done); err != nil {
		t.Fatal(err)
	}
	// One notification each for the marker, the four batches and the line.
	eventually(t, "six notifications", func() bool { return count() >= 6 })
	time.Sleep(200 * time.Millisecond)
	if got := count(); got != 6 {
		t.Fatalf("notifications = %d, want 6", got)
	}
	lines := r.lines(chatID)
	if got := lines[1].Body; len(got) != 10000 {
		t.Fatalf("reply = %d bytes", len(got))
	}
}

func TestReplyFlushesAPendingBatchAfterTheInterval(t *testing.T) {
	r := newRig(t, app.ReplyConfig{FlushEvery: 50 * time.Millisecond, FlushBytes: 1 << 20})
	release := make(chan struct{})
	r.stub.reply(func(_ int, s *frames) {
		s.delta("abc")
		s.hold(release)
		s.delta("def")
		s.done("m1", 1, 1, nil)
	})
	chatID := r.open()
	r.say(chatID, "hi", "")
	done := r.runAsync(r.lastJob(), 1)

	eventually(t, "the first batch", func() bool {
		d := r.deltas(chatID)
		return len(d) == 2 && d[1].Body == "abc"
	})
	close(release)
	if err := result(t, done); err != nil {
		t.Fatal(err)
	}
	if got := r.lines(chatID)[1].Body; got != "abcdef" {
		t.Fatalf("reply = %q", got)
	}
}

func TestReplyKeepsThePartialTextWhenTheStreamFails(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	r.stub.reply(func(_ int, s *frames) {
		s.delta("part of ")
		s.delta("an answer")
		s.fail("rate_limit", "m1")
	})
	chatID := r.open()
	r.say(chatID, "hi", "")
	queued := r.lastJob()
	if err := r.run(queued, 1); err != nil {
		t.Fatal(err)
	}
	lines := r.lines(chatID)
	reply := lines[1]
	if reply.Status != "failed" || reply.Body != "part of an answer" || reply.ID != queued.Args.ReplyID {
		t.Fatalf("reply = %+v", reply)
	}
	if reply.Model == nil || *reply.Model != "m1" || reply.ReplyTo == nil || *reply.ReplyTo != lines[0].ID {
		t.Fatalf("reply = %+v", reply)
	}
	if len(reply.Attempts) != 1 || reply.Attempts[0] != (domain.Attempt{Model: "m1", Class: "rate_limit"}) {
		t.Fatalf("attempts = %+v", reply.Attempts)
	}
	if left := r.deltas(chatID); len(left) != 0 {
		t.Fatalf("deltas = %+v", left)
	}
}

func TestReplyOfAllFailedLinksListsEveryModelAndWritesNoticesAsStatusRows(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	release := make(chan struct{})
	r.stub.reply(func(_ int, s *frames) {
		s.attempt("m1", "rate_limit", "m2")
		s.attempt("m2", "transient", "")
		s.hold(release)
		s.fail("transient", "m2")
	})
	chatID := r.open()
	r.say(chatID, "hi", "")
	done := r.runAsync(r.lastJob(), 1)

	eventually(t, "both notices", func() bool { return len(r.deltas(chatID)) == 3 })
	rows := r.deltas(chatID)
	if rows[1].Kind != "status" || rows[1].Seq != 1 ||
		rows[1].Body != `{"failed_model":"m1","class":"rate_limit","next_model":"m2"}` ||
		rows[2].Kind != "status" || rows[2].Body != `{"failed_model":"m2","class":"transient","next_model":""}` {
		t.Fatalf("deltas = %+v", rows)
	}
	close(release)
	if err := result(t, done); err != nil {
		t.Fatal(err)
	}
	reply := r.lines(chatID)[1]
	if reply.Status != "failed" || reply.Body != "" {
		t.Fatalf("reply = %+v", reply)
	}
	want := []domain.Attempt{{Model: "m1", Class: "rate_limit"}, {Model: "m2", Class: "transient"}}
	if len(reply.Attempts) != 2 || reply.Attempts[0] != want[0] || reply.Attempts[1] != want[1] {
		t.Fatalf("attempts = %+v", reply.Attempts)
	}
}

func TestReplyRescuesACrashedRunFromItsDeltas(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	chatID := r.open()
	r.say(chatID, "hi", "")
	queued := r.lastJob()
	ctx := context.Background()

	// The first run got as far as two text deltas and a notice, then died.
	if turn, err := r.store.Begin(ctx, ownerAccess, queued.Args); err != nil || turn.State != app.ReplyGo {
		t.Fatalf("begin = %+v %v", turn, err)
	}
	for seq, d := range []struct{ kind, body string }{
		{"text", "half an "},
		{"text", "answer"},
		{"status", `{"failed_model":"m1","class":"timeout","next_model":"m2"}`},
	} {
		if err := r.store.PutDelta(ctx, ownerAccess, chatID, queued.Args.ReplyID, seq+1, d.kind, d.body); err != nil {
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
	want := []domain.Attempt{{Model: "m1", Class: "timeout"}, {Model: "m2", Class: "cancelled"}}
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

func TestOneReplyStreamsPerChatAtATime(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	release := make(chan struct{})
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
	other := r.open()
	r.say(other, "elsewhere", "")
	elsewhere := r.lastJob()

	running := r.runAsync(first, 1)
	eventually(t, "the first reply to start", func() bool { return len(r.stub.seen()) == 1 })

	// A newer line of the same chat waits; it does not call the model.
	r.say(chatID, "second", "")
	second := r.lastJob()
	var snooze *rivertype.JobSnoozeError
	if err := r.run(second, 1); !errors.As(err, &snooze) {
		t.Fatalf("second reply error = %v, want a snooze", err)
	}
	if calls := len(r.stub.seen()); calls != 1 {
		t.Fatalf("model calls = %d", calls)
	}

	// Another chat is not held up by it.
	if err := r.run(elsewhere, 1); err != nil {
		t.Fatal(err)
	}
	if got := r.lines(other); len(got) != 2 || got[1].Status != "done" {
		t.Fatalf("other chat lines = %+v", got)
	}

	close(release)
	if err := result(t, running); err != nil {
		t.Fatal(err)
	}
	if err := r.run(second, 1); err != nil {
		t.Fatal(err)
	}
	// The second reply was written after the first answer, which sits behind
	// the follow-up in the chat, and still sees the whole exchange in order.
	calls := r.stub.seen()
	sent := calls[len(calls)-1].Messages
	if len(sent) != 3 || sent[0].Text != "first" || sent[1].Role != "assistant" || sent[1].Text != "text" || sent[2].Text != "second" {
		t.Fatalf("history of the second reply = %+v", sent)
	}
	lines := r.lines(chatID)
	if len(lines) != 4 || lines[2].ReplyTo == nil || *lines[2].ReplyTo != lines[0].ID ||
		lines[3].ReplyTo == nil || *lines[3].ReplyTo != lines[1].ID {
		t.Fatalf("lines = %+v", lines)
	}
}

func TestAReplyThatWroteNothingForTooLongStopsHoldingTheChat(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	chatID := r.open()
	r.say(chatID, "first", "")
	first := r.lastJob()
	if _, err := r.store.Begin(context.Background(), ownerAccess, first.Args); err != nil {
		t.Fatal(err)
	}
	r.say(chatID, "second", "")
	second := r.lastJob()
	var snooze *rivertype.JobSnoozeError
	if err := r.run(second, 1); !errors.As(err, &snooze) {
		t.Fatalf("error = %v, want a snooze", err)
	}

	// The process behind the first reply died long ago.
	if _, err := r.owner.Exec(context.Background(),
		`UPDATE chat.deltas SET at = now() - interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(context.Background(), `
		ALTER TABLE chat.lines DISABLE TRIGGER chat_keep_line;
		UPDATE chat.lines SET at = now() - interval '1 hour';
		ALTER TABLE chat.lines ENABLE TRIGGER chat_keep_line`); err != nil {
		t.Fatal(err)
	}
	r.stub.reply(func(_ int, s *frames) { s.delta("ok"); s.done("m1", 1, 1, nil) })
	if err := r.run(second, 1); err != nil {
		t.Fatal(err)
	}
	if got := r.lines(chatID); got[len(got)-1].Status != "done" {
		t.Fatalf("lines = %+v", got)
	}
}

func TestReplyAsksTheRouteOfTheLineAndNoOtherKey(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	r.stub.reply(func(_ int, s *frames) { s.delta("ok"); s.done("m1", 1, 1, nil) })
	chatID := r.open()

	r.say(chatID, "one", "draft")
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}
	r.say(chatID, "two", "")
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}

	calls := r.stub.seen()
	if len(calls) != 2 || calls[0].Route != "draft" || calls[1].Route != "chat" {
		t.Fatalf("routes = %+v", calls)
	}
	for _, call := range calls {
		if call.UserID != ownerUser || call.CredentialRef != "" || call.MaxOutputTokens != 4096 {
			t.Fatalf("call = %+v", call)
		}
	}
}

func TestHistoryLeavesOutFailedAndReplacedLines(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	chatID := r.open()
	ctx := context.Background()

	// 1: the first try fails.
	r.stub.reply(func(_ int, s *frames) { s.delta("broken"); s.fail("transient", "m1") })
	r.say(chatID, "q1", "")
	first := r.lastJob()
	if err := r.run(first, 1); err != nil {
		t.Fatal(err)
	}
	r.completed(first)
	failed := r.lines(chatID)[1]

	// 2: a retry succeeds.
	r.stub.reply(func(_ int, s *frames) { s.delta("a1"); s.done("m1", 1, 1, nil) })
	if err := app.Retry(ctx, r.store, ownerAccess, chatID, failed.ID, &app.Picks{Route: "other"}); err != nil {
		t.Fatal(err)
	}
	retry := r.lastJob()
	if retry.Args.LineID != first.Args.LineID || retry.Args.ReplyID == first.Args.ReplyID || retry.Args.Route != "other" {
		t.Fatalf("retry args = %+v", retry.Args)
	}
	if err := r.run(retry, 1); err != nil {
		t.Fatal(err)
	}
	r.completed(retry)
	if calls := r.stub.seen(); calls[len(calls)-1].Route != "other" {
		t.Fatalf("retry route = %+v", calls[len(calls)-1])
	}

	// 3: the next question sees the question and its good answer only.
	r.say(chatID, "q2", "")
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}
	calls := r.stub.seen()
	sent := calls[len(calls)-1].Messages
	want := []struct{ role, text string }{{"user", "q1"}, {"assistant", "a1"}, {"user", "q2"}}
	if len(sent) != len(want) {
		t.Fatalf("history = %+v", sent)
	}
	for i := range want {
		if sent[i].Role != want[i].role || sent[i].Text != want[i].text {
			t.Fatalf("history[%d] = %+v", i, sent[i])
		}
	}

	// The failed line is still stored.
	if got := r.lines(chatID); len(got) != 5 || got[1].Status != "failed" {
		t.Fatalf("lines = %+v", got)
	}
}

func TestHistoryDropsADoneReplyAReplacementSuperseded(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	chatID := r.open()
	r.say(chatID, "q1", "")
	user := r.lines(chatID)[0]
	for _, body := range []string{"old answer", "new answer"} {
		if _, err := r.owner.Exec(context.Background(), `
			INSERT INTO chat.lines (chat_id, seq, role, body, model, reply_to)
			SELECT $1::uuid, max(seq) + 1, 'assistant', $2, 'm1', $3::uuid FROM chat.lines WHERE chat_id = $1::uuid`,
			chatID, body, user.ID); err != nil {
			t.Fatal(err)
		}
	}
	r.stub.reply(func(_ int, s *frames) { s.delta("ok"); s.done("m1", 1, 1, nil) })
	r.say(chatID, "q2", "")
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}
	sent := r.stub.seen()[0].Messages
	if len(sent) != 3 || sent[1].Text != "new answer" {
		t.Fatalf("history = %+v", sent)
	}
}

func TestHistoryTrimsFromTheOldestEndToTheBudget(t *testing.T) {
	r := newRig(t, app.ReplyConfig{HistoryChars: 8})
	r.stub.reply(func(_ int, s *frames) { s.delta("ok"); s.done("m1", 1, 1, nil) })
	chatID := r.open()
	for _, body := range []string{"aaaaaaaa", "bbbb", "cc"} {
		r.say(chatID, body, "")
		if err := r.run(r.lastJob(), 1); err != nil {
			t.Fatal(err)
		}
	}
	// The third reply takes the lines newest first while they fit in 8
	// characters: "cc", the answer "ok" before it, then "bbbb", and not the
	// answer before that.
	calls := r.stub.seen()
	sent := calls[2].Messages
	if len(sent) != 3 || sent[0].Text != "bbbb" || sent[1].Text != "ok" || sent[2].Text != "cc" {
		t.Fatalf("history = %+v", sent)
	}
}

func TestRetryHidesTheFailedLineOnceItSucceeds(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	ctx := context.Background()
	chatID := r.open()
	r.stub.reply(func(_ int, s *frames) { s.fail("credential", "m1") })
	r.say(chatID, "q", "")
	first := r.lastJob()
	if err := r.run(first, 1); err != nil {
		t.Fatal(err)
	}
	r.completed(first)

	read := func() []app.Line {
		got, err := app.Read(ctx, r.store, ownerAccess, chatID)
		if err != nil {
			t.Fatal(err)
		}
		return got.Lines
	}
	shown := read()
	if len(shown) != 2 || shown[1].Status != "failed" || shown[1].ReplyTo == nil || *shown[1].ReplyTo != shown[0].ID {
		t.Fatalf("thread before the retry = %+v", shown)
	}
	failed := shown[1]

	// A line that is not a failed reply, an unknown line and a stranger are refused.
	if err := app.Retry(ctx, r.store, ownerAccess, chatID, shown[0].ID, nil); !errors.Is(err, app.ErrRetry) {
		t.Fatalf("retry of a user line = %v", err)
	}
	if err := app.Retry(ctx, r.store, ownerAccess, chatID, "018f3b2c-7a10-7c4a-8e11-999999999999", nil); !errors.Is(err, app.ErrNoLine) {
		t.Fatalf("retry of no line = %v", err)
	}
	if err := app.Retry(ctx, r.store, otherAccess, chatID, failed.ID, nil); !errors.Is(err, app.ErrGone) {
		t.Fatalf("retry by a stranger = %v", err)
	}

	if err := app.Retry(ctx, r.store, ownerAccess, chatID, failed.ID, nil); err != nil {
		t.Fatal(err)
	}
	// One reply at a time per line: a second click is refused while the first waits.
	if err := app.Retry(ctx, r.store, ownerAccess, chatID, failed.ID, nil); !errors.Is(err, app.ErrBusy) {
		t.Fatalf("second retry = %v", err)
	}
	if got := read(); len(got) != 2 {
		t.Fatalf("thread while the retry waits = %+v", got)
	}

	r.stub.reply(func(_ int, s *frames) { s.delta("fixed"); s.done("m1", 1, 1, nil) })
	retry := r.lastJob()
	if err := r.run(retry, 1); err != nil {
		t.Fatal(err)
	}
	r.completed(retry)
	got := read()
	if len(got) != 2 || got[1].Body != "fixed" || got[1].Status != "done" {
		t.Fatalf("thread after the retry = %+v", got)
	}
	if stored := r.lines(chatID); len(stored) != 3 || stored[1].Status != "failed" {
		t.Fatalf("stored lines = %+v", stored)
	}
	// Answered now, the failed line cannot be retried again.
	if err := app.Retry(ctx, r.store, ownerAccess, chatID, failed.ID, nil); !errors.Is(err, app.ErrRetry) {
		t.Fatalf("retry after success = %v", err)
	}
}

func TestDeltasAreUnderRowSecurity(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	ctx := context.Background()
	chatID := r.open()
	if err := r.store.PutDelta(ctx, ownerAccess, chatID, "018f3b2c-7a10-7c4a-8e11-999999999999", 1, "text", "secret"); err != nil {
		t.Fatal(err)
	}

	as := func(access app.Access) pgx.Tx {
		tx, err := r.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tx.Rollback(ctx) })
		if _, err := tx.Exec(ctx, `
			SELECT set_config('upmo.user_id', $1, true), set_config('upmo.allow', $2, true),
			       set_config('upmo.teams', '{}', true), set_config('upmo.shared_view', $3, true),
			       set_config('upmo.shared_edit', '{}', true)`,
			access.UserID, access.Allow, nonEmpty(access.SharedView)); err != nil {
			t.Fatal(err)
		}
		return tx
	}
	count := func(tx pgx.Tx) int {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM chat.deltas`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Another person sees nothing and deletes nothing.
	stranger := as(otherAccess)
	if n := count(stranger); n != 0 {
		t.Fatalf("a stranger reads %d deltas", n)
	}
	if tag, err := stranger.Exec(ctx, `DELETE FROM chat.deltas`); err != nil || tag.RowsAffected() != 0 {
		t.Fatalf("a stranger deleted %d deltas: %v", tag.RowsAffected(), err)
	}
	if _, err := stranger.Exec(ctx, `
		INSERT INTO chat.deltas (chat_id, line_id, seq, body) VALUES ($1::uuid, $1::uuid, 9, 'x')`, chatID); err == nil {
		t.Fatal("a stranger wrote a delta")
	}

	// A person who may only view the chat reads its deltas and still cannot
	// write or delete them.
	viewer := as(chat.Access{UserID: viewerUser, Allow: "chat:view", SharedView: "{" + chatID + "}"})
	if n := count(viewer); n != 1 {
		t.Fatalf("a viewer reads %d deltas", n)
	}
	if tag, err := viewer.Exec(ctx, `DELETE FROM chat.deltas`); err != nil || tag.RowsAffected() != 0 {
		t.Fatalf("a viewer deleted %d deltas: %v", tag.RowsAffected(), err)
	}

	// The owner reads and deletes their own.
	owner := as(ownerAccess)
	if n := count(owner); n != 1 {
		t.Fatalf("the owner reads %d deltas", n)
	}
	if tag, err := owner.Exec(ctx, `DELETE FROM chat.deltas`); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("the owner deleted %d deltas: %v", tag.RowsAffected(), err)
	}
}

func TestSweepDeletesOnlyDeltasOlderThanTwoHours(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	ctx := context.Background()
	chatID := r.open()
	r.say(chatID, "hi", "")

	put := func(line string, seq int) {
		if err := r.store.PutDelta(ctx, ownerAccess, chatID, line, seq, "text", "x"); err != nil {
			t.Fatal(err)
		}
	}
	const stale, hourOld, fresh = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaaa", "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb", "018f3b2c-7a10-7c4a-8e11-cccccccccccc"
	put(stale, 1)
	put(hourOld, 1)
	put(fresh, 1)
	if _, err := r.owner.Exec(ctx, `UPDATE chat.deltas SET at = now() - interval '3 hours' WHERE line_id = $1::uuid`, stale); err != nil {
		t.Fatal(err)
	}
	// A rescued job may be an hour late and still needs its deltas.
	if _, err := r.owner.Exec(ctx, `UPDATE chat.deltas SET at = now() - interval '1 hour' WHERE line_id = $1::uuid`, hourOld); err != nil {
		t.Fatal(err)
	}

	swept, err := r.store.Sweep(ctx)
	if err != nil || swept != 1 {
		t.Fatalf("swept %d: %v", swept, err)
	}
	left := r.deltas(chatID)
	if len(left) != 2 {
		t.Fatalf("deltas = %+v", left)
	}
	for _, d := range left {
		if d.LineID == stale {
			t.Fatalf("the stale delta survived: %+v", left)
		}
	}
}

func TestSweepTakesNoAgeSoACallerCannotWidenIt(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	ctx := context.Background()
	chatID := r.open()
	r.say(chatID, "hi", "")
	if err := r.store.PutDelta(ctx, ownerAccess, chatID, replyID, 1, "text", "live"); err != nil {
		t.Fatal(err)
	}
	// The request role may call the function, with no argument and only so.
	for _, call := range []string{
		`SELECT chat.sweep_deltas(interval '-1 year')`,
		`SELECT chat.sweep_deltas(interval '0')`,
	} {
		if _, err := r.pool.Exec(ctx, call); err == nil {
			t.Fatalf("%s was accepted", call)
		}
	}
	if got := r.deltas(chatID); len(got) != 1 {
		t.Fatalf("a live delta was swept: %+v", got)
	}
}

func TestSweepWorksForAnOwnerThatIsNeitherSuperuserNorBypassRLS(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	ctx := context.Background()
	chatID := r.open()
	r.say(chatID, "hi", "")
	const role = "chat_sweep_owner"
	if _, err := r.owner.Exec(ctx, `DROP ROLE IF EXISTS `+role); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `CREATE ROLE `+role+` NOSUPERUSER NOBYPASSRLS NOLOGIN`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, stmt := range []string{`REASSIGN OWNED BY ` + role + ` TO CURRENT_USER`, `DROP OWNED BY ` + role, `DROP ROLE ` + role} {
			_, _ = r.owner.Exec(context.Background(), stmt)
		}
	})
	// The table and the function belong to a role with no privilege of its own.
	for _, stmt := range []string{
		`ALTER TABLE chat.deltas OWNER TO ` + role,
		`ALTER FUNCTION chat.sweep_deltas() OWNER TO ` + role,
		`GRANT USAGE ON SCHEMA chat TO ` + role,
		// What the policies of the table look at, which a real owner has.
		`GRANT SELECT ON chat.chats TO ` + role,
		`GRANT USAGE ON SCHEMA identity TO ` + role,
		`GRANT EXECUTE ON FUNCTION identity.may(text, text, uuid, text, uuid, uuid) TO ` + role,
	} {
		if _, err := r.owner.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	var super, bypass, forced bool
	if err := r.owner.QueryRow(ctx, `
		SELECT rolsuper, rolbypassrls, (SELECT relforcerowsecurity FROM pg_class WHERE oid = 'chat.deltas'::regclass)
		FROM pg_roles WHERE rolname = $1`, role).Scan(&super, &bypass, &forced); err != nil || super || bypass || !forced {
		t.Fatalf("owner super %v bypass %v forced %v: %v", super, bypass, forced, err)
	}

	for i, age := range []string{"3 hours", "1 hour"} {
		if _, err := r.owner.Exec(ctx, `
			INSERT INTO chat.deltas (chat_id, line_id, seq, body, at)
			SELECT $1::uuid, gen_random_uuid(), $2, 'x', now() - $3::interval`, chatID, i+1, age); err != nil {
			t.Fatal(err)
		}
	}
	// The request role cannot read or delete them itself.
	var visible int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM chat.deltas`).Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("the request role sees %d deltas: %v", visible, err)
	}
	swept, err := r.store.Sweep(ctx)
	if err != nil || swept != 1 {
		t.Fatalf("swept %d: %v", swept, err)
	}
	if got := r.deltas(chatID); len(got) != 1 {
		t.Fatalf("deltas = %+v", got)
	}
}

func nonEmpty(literal string) string {
	if literal == "" {
		return "{}"
	}
	return literal
}

// failedReply returns the one failed reply of a chat and fails if it is not.
func failedReply(t *testing.T, r *rig, chatID string) lineRow {
	t.Helper()
	lines := r.lines(chatID)
	if len(lines) != 2 || lines[1].Role != "assistant" || lines[1].Status != "failed" ||
		lines[1].ReplyTo == nil || *lines[1].ReplyTo != lines[0].ID {
		t.Fatalf("lines = %+v", lines)
	}
	return lines[1]
}

func reasonOf(l lineRow) string { return domain.Reason(l.Attempts[len(l.Attempts)-1].Class) }

func TestAnAuthorWhoLostAccessBeforeTheJobStartedStillGetsAFailedReply(t *testing.T) {
	for name, lose := range map[string]func(r *rig){
		"edit taken away": func(r *rig) { r.setAccess(ownerUser, chat.Access{UserID: ownerUser, Allow: "chat:view"}) },
		"no chat access":  func(r *rig) { r.dropAccess(ownerUser) },
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, app.ReplyConfig{})
			chatID := r.open()
			r.say(chatID, "hi", "")
			lose(r)
			if err := r.run(r.lastJob(), 1); err != nil {
				t.Fatal(err)
			}
			reply := failedReply(t, r, chatID)
			if reasonOf(reply) != "you no longer have access to this chat" || reply.Body != "" {
				t.Fatalf("reply = %+v", reply)
			}
			if calls := r.stub.seen(); len(calls) != 0 {
				t.Fatalf("the model was asked: %+v", calls)
			}
		})
	}
}

func TestAnErrorBeforeTheReplyBeginsFailsItOnTheLastAttempt(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	chatID := r.open()
	r.say(chatID, "hi", "")
	queued := r.lastJob()
	r.failLookups(errors.New("identity is down"))

	if err := r.run(queued, 1); err == nil {
		t.Fatal("the first attempt hid the error")
	}
	if got := r.lines(chatID); len(got) != 1 {
		t.Fatalf("a reply was stored on the first attempt: %+v", got)
	}
	if err := r.run(queued, 2); err != nil {
		t.Fatal(err)
	}
	reply := failedReply(t, r, chatID)
	if reasonOf(reply) != "the reply could not be written" || reply.ID != queued.Args.ReplyID {
		t.Fatalf("reply = %+v", reply)
	}
}

func TestFailReplyIsNarrow(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	ctx := context.Background()
	chatID := r.open()
	r.say(chatID, "hi", "")
	queued := r.lastJob()
	user := r.lines(chatID)[0]
	if err := r.store.PutDelta(ctx, ownerAccess, chatID, queued.Args.ReplyID, 1, "text", "partial"); err != nil {
		t.Fatal(err)
	}
	// An unrelated reply's delta must survive.
	if err := r.store.PutDelta(ctx, ownerAccess, chatID, replyID, 1, "text", "other"); err != nil {
		t.Fatal(err)
	}

	if _, err := r.store.FailReply(ctx, user.ID, queued.Args.ReplyID, "anything"); err == nil {
		t.Fatal("a free reason was accepted")
	}
	if stored, err := r.store.FailReply(ctx, "018f3b2c-7a10-7c4a-8e11-999999999999", queued.Args.ReplyID, "access"); err == nil || stored {
		t.Fatalf("a missing user line: stored %v %v", stored, err)
	}
	// Only a reply id that a job for that line holds may be failed.
	if stored, err := r.store.FailReply(ctx, user.ID, "018f3b2c-7a10-7c4a-8e11-777777777777", "access"); err == nil || stored {
		t.Fatalf("a reply id no job holds: stored %v %v", stored, err)
	}
	if got := r.lines(chatID); len(got) != 1 {
		t.Fatalf("a refused call stored a line: %+v", got)
	}
	if stored, err := r.store.FailReply(ctx, user.ID, queued.Args.ReplyID, "access"); err != nil || !stored {
		t.Fatalf("stored %v %v", stored, err)
	}
	reply := failedReply(t, r, chatID)
	if reply.Body != "partial" {
		t.Fatalf("reply = %+v", reply)
	}
	if left := r.deltas(chatID); len(left) != 1 || left[0].LineID != replyID {
		t.Fatalf("deltas = %+v", left)
	}
	// An answered line takes no second reply, and the same reply id no second line.
	if stored, err := r.store.FailReply(ctx, user.ID, queued.Args.ReplyID, "access"); err != nil || stored {
		t.Fatalf("again: stored %v %v", stored, err)
	}
	r.completed(queued)
	r.stub.reply(func(_ int, s *frames) { s.delta("ok"); s.done("m1", 1, 1, nil) })
	if err := app.Retry(ctx, r.store, ownerAccess, chatID, reply.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `DELETE FROM chat.deltas`); err != nil {
		t.Fatal(err)
	}
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}
	if stored, err := r.store.FailReply(ctx, user.ID, r.lastJob().Args.ReplyID, "unwritten"); err != nil || stored {
		t.Fatalf("a done line was failed: stored %v %v", stored, err)
	}
}

func TestEditAccessRevokedMidStreamStopsTheWritesAndFailsTheReply(t *testing.T) {
	r := newRig(t, app.ReplyConfig{FlushBytes: 4, AccessEvery: 40 * time.Millisecond})
	release := make(chan struct{})
	defer close(release)
	r.stub.reply(func(_ int, s *frames) {
		s.delta("seen")
		s.hold(release)
		s.delta("unseen")
		s.done("m1", 1, 1, nil)
	})
	chatID := r.open()
	r.say(chatID, "hi", "")
	done := r.runAsync(r.lastJob(), 1)
	eventually(t, "the first text", func() bool { return len(r.deltas(chatID)) == 2 })

	// The share is withdrawn while the model is still writing.
	r.setAccess(ownerUser, chat.Access{UserID: ownerUser, Allow: "chat:view"})
	if err := result(t, done); err != nil {
		t.Fatal(err)
	}
	reply := failedReply(t, r, chatID)
	if reply.Body != "seen" || reasonOf(reply) != "you no longer have access to this chat" {
		t.Fatalf("reply = %+v", reply)
	}
	if left := r.deltas(chatID); len(left) != 0 {
		t.Fatalf("deltas = %+v", left)
	}
}

func TestEditAccessRevokedJustBeforeTheLineIsStoredFailsTheReply(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	r.stub.reply(func(_ int, s *frames) {
		s.delta("whole answer")
		r.setAccess(ownerUser, chat.Access{UserID: ownerUser, Allow: "chat:view"})
		s.done("m1", 1, 1, nil)
	})
	chatID := r.open()
	r.say(chatID, "hi", "")
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}
	reply := failedReply(t, r, chatID)
	if reasonOf(reply) != "you no longer have access to this chat" {
		t.Fatalf("reply = %+v", reply)
	}
}

// failingRows is the real store with a delta write that fails from the nth on.
type failingRows struct {
	app.ReplyRows
	mu     sync.Mutex
	writes int
	from   int
}

func (f *failingRows) PutDelta(ctx context.Context, access app.Access, chatID, replyID string, seq int, kind, body string) error {
	f.mu.Lock()
	f.writes++
	n := f.writes
	f.mu.Unlock()
	if n >= f.from {
		return errors.New("the database went away")
	}
	return f.ReplyRows.PutDelta(ctx, access, chatID, replyID, seq, kind, body)
}

func TestAFailedDeltaWriteEndsInAFailedLineWithTheTextSoFar(t *testing.T) {
	r := newRig(t, app.ReplyConfig{FlushBytes: 3})
	// "abc" is written; "def" cannot be, and the text of that batch is kept.
	deps := r.deps
	deps.Rows = &failingRows{ReplyRows: r.store, from: 2}
	r.worker = app.NewReplyWorker(deps)
	r.stub.reply(func(_ int, s *frames) { s.delta("abc"); s.delta("def"); s.delta("ghi"); s.done("m1", 1, 1, nil) })
	chatID := r.open()
	r.say(chatID, "hi", "")
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}
	reply := failedReply(t, r, chatID)
	if reply.Body != "abcdef" || reasonOf(reply) != "the reply could not be written" {
		t.Fatalf("reply = %+v", reply)
	}
	if left := r.deltas(chatID); len(left) != 0 {
		t.Fatalf("deltas = %+v", left)
	}
}

func TestAJobTimeoutEndsInAFailedLineThatKeepsTheText(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	r.stub.reply(func(_ int, s *frames) {
		s.delta("so far")
		s.hold(nil)
	})
	chatID := r.open()
	r.say(chatID, "hi", "")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := r.runCtx(ctx, r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}
	reply := failedReply(t, r, chatID)
	if reply.Body != "so far" || reasonOf(reply) != "timed out" {
		t.Fatalf("reply = %+v", reply)
	}
}

func TestAGracefulShutdownEndsInAFailedLineThatKeepsTheText(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	r.stub.reply(func(_ int, s *frames) {
		s.delta("so far")
		s.hold(nil)
	})
	chatID := r.open()
	r.say(chatID, "hi", "")
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.runCtx(ctx, r.lastJob(), 1) }()
	eventually(t, "the model to be asked", func() bool { return len(r.stub.seen()) == 1 })
	time.Sleep(100 * time.Millisecond)
	stop()
	if err := result(t, done); err != nil {
		t.Fatal(err)
	}
	reply := failedReply(t, r, chatID)
	if reply.Body != "so far" || reasonOf(reply) != "stopped" {
		t.Fatalf("reply = %+v", reply)
	}
}

func TestAnAccessLookupThatFailsMidStreamCountsAsAccessLost(t *testing.T) {
	r := newRig(t, app.ReplyConfig{FlushBytes: 4, AccessEvery: 40 * time.Millisecond})
	release := make(chan struct{})
	defer close(release)
	r.stub.reply(func(_ int, s *frames) {
		s.delta("seen")
		s.hold(release)
		s.delta("unseen")
		s.done("m1", 1, 1, nil)
	})
	chatID := r.open()
	r.say(chatID, "hi", "")
	done := r.runAsync(r.lastJob(), 1)
	eventually(t, "the first text", func() bool { return len(r.deltas(chatID)) == 2 })
	r.failLookups(errors.New("identity is down"))
	if err := result(t, done); err != nil {
		t.Fatal(err)
	}
	reply := failedReply(t, r, chatID)
	if reply.Body != "seen" || reasonOf(reply) != "you no longer have access to this chat" {
		t.Fatalf("reply = %+v", reply)
	}
}

func TestAFailedReplyIsStoredOnlyUnderAccessThatIsStillThere(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	r.stub.reply(func(_ int, s *frames) {
		s.delta("partial")
		r.failLookups(errors.New("identity is down"))
		s.fail("transient", "m1")
	})
	chatID := r.open()
	r.say(chatID, "hi", "")
	if err := r.run(r.lastJob(), 1); err != nil {
		t.Fatal(err)
	}
	reply := failedReply(t, r, chatID)
	// The reply failed on its own; with the lookup down it is stored as an
	// access failure, through fail_reply, not under the old snapshot.
	if reasonOf(reply) != "you no longer have access to this chat" || reply.Body != "" {
		t.Fatalf("reply = %+v", reply)
	}
}

func TestAReplyIdWhoseDeltasBelongToAnotherChatIsNeitherAdoptedNorDeleted(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	ctx := context.Background()
	other := r.open()
	if err := r.store.PutDelta(ctx, ownerAccess, other, replyID, 5, "text", "b-secret"); err != nil {
		t.Fatal(err)
	}
	chatID := r.open()
	r.say(chatID, "hi", "")
	queued := r.lastJob()
	queued.Args.ReplyID = replyID
	// The job row is the one FailReply checks for; name the reply id there too.
	if _, err := r.owner.Exec(ctx, `UPDATE platform.river_job SET args = jsonb_set(args, '{reply_id}', to_jsonb($1::text)) WHERE id = $2`, replyID, queued.ID); err != nil {
		t.Fatal(err)
	}
	r.stub.reply(func(_ int, s *frames) { s.delta("mine"); s.done("m1", 1, 1, nil) })
	if err := r.run(queued, 1); err != nil {
		t.Fatal(err)
	}
	lines := r.lines(chatID)
	if len(lines) != 2 || lines[1].Status != "done" || lines[1].Body != "mine" {
		t.Fatalf("lines = %+v", lines)
	}
	if left := r.deltas(other); len(left) != 1 || left[0].Body != "b-secret" {
		t.Fatalf("the other chat's deltas = %+v", left)
	}

	// The same through fail_reply: it neither reads nor deletes them.
	const second = "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"
	if err := r.store.PutDelta(ctx, ownerAccess, other, second, 5, "text", "c-secret"); err != nil {
		t.Fatal(err)
	}
	third := r.open()
	r.say(third, "again", "")
	j := r.lastJob()
	if _, err := r.owner.Exec(ctx, `UPDATE platform.river_job SET args = jsonb_set(args, '{reply_id}', to_jsonb($1::text)) WHERE id = $2`, second, j.ID); err != nil {
		t.Fatal(err)
	}
	user := r.lines(third)[0]
	if stored, err := r.store.FailReply(ctx, user.ID, second, "access"); err != nil || !stored {
		t.Fatalf("stored %v %v", stored, err)
	}
	if got := r.lines(third)[1]; got.Body != "" {
		t.Fatalf("fail_reply adopted another chat's text: %+v", got)
	}
	if left := r.deltas(other); len(left) != 2 {
		t.Fatalf("the other chat's deltas = %+v", left)
	}
}

func TestAWorkerThatCannotStoreAFailedReplyReturnsAnError(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	chatID := r.open()
	r.say(chatID, "hi", "")
	queued := r.lastJob()
	// A job for a line that is not there: nothing can be stored for it.
	queued.Args.LineID = "018f3b2c-7a10-7c4a-8e11-999999999999"
	r.dropAccess(ownerUser)
	if err := r.run(queued, 1); err == nil {
		t.Fatal("the job reported success without storing a reply")
	}
}

func TestFailReplyRefusesAReplyIdNoJobHolds(t *testing.T) {
	r := newRig(t, app.ReplyConfig{})
	chatID := r.open()
	r.say(chatID, "hi", "")
	user := r.lines(chatID)[0]
	if stored, err := r.store.FailReply(context.Background(), user.ID, replyID, "access"); err == nil || stored {
		t.Fatalf("stored %v %v", stored, err)
	}
	if got := r.lines(chatID); len(got) != 1 {
		t.Fatalf("lines = %+v", got)
	}
}

func TestThePinGoesFromTheLineToTheGatewayAndARetryReusesItUnlessOverridden(t *testing.T) {
	const (
		accountA = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaa01"
		agent    = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaa03"
	)
	r := newRig(t, app.ReplyConfig{})
	chatID := r.open()
	ctx := context.Background()
	pin := func(call stubCall) string {
		if call.Pin == nil {
			return ""
		}
		return call.Pin.AccountID + "/" + call.Pin.Model
	}

	r.stub.reply(func(call int, s *frames) {
		if call == 1 {
			s.fail("transient", "m1")
			return
		}
		s.delta("ok")
		s.done("m1", 1, 1, nil)
	})
	if err := app.Say(ctx, r.store, ownerAccess, chatID, "q", app.Picks{Route: "fast", PinAccount: accountA, PinModel: "gpt-x", AgentID: agent}); err != nil {
		t.Fatal(err)
	}
	first := r.lastJob()
	if first.Args.PinAccount != accountA || first.Args.PinModel != "gpt-x" || first.Args.AgentID != agent {
		t.Fatalf("job args = %+v", first.Args)
	}
	if err := r.run(first, 1); err != nil {
		t.Fatal(err)
	}
	r.completed(first)
	if got := r.stub.seen(); len(got) != 1 || pin(got[0]) != accountA+"/gpt-x" || got[0].Route != "fast" {
		t.Fatalf("first call = %+v", got)
	}
	failed := r.lines(chatID)[1]

	// A retry with nothing given asks again with the stored picks.
	if err := app.Retry(ctx, r.store, ownerAccess, chatID, failed.ID, nil); err != nil {
		t.Fatal(err)
	}
	again := r.lastJob()
	if again.Args.PinAccount != accountA || again.Args.PinModel != "gpt-x" || again.Args.AgentID != agent || again.Args.Route != "fast" {
		t.Fatalf("reused args = %+v", again.Args)
	}
	if err := r.run(again, 1); err != nil {
		t.Fatal(err)
	}
	r.completed(again)
	if got := r.stub.seen(); len(got) != 2 || pin(got[1]) != accountA+"/gpt-x" || got[1].Route != "fast" {
		t.Fatalf("reused call = %+v", got)
	}
}

func TestARetryWithPicksOverridesTheStoredOnesAsAWhole(t *testing.T) {
	const (
		accountA = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaa01"
		accountB = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaa02"
	)
	r := newRig(t, app.ReplyConfig{})
	chatID := r.open()
	ctx := context.Background()
	r.stub.reply(func(_ int, s *frames) { s.fail("transient", "m1") })
	if err := app.Say(ctx, r.store, ownerAccess, chatID, "q", app.Picks{Route: "fast", PinAccount: accountA, PinModel: "gpt-x"}); err != nil {
		t.Fatal(err)
	}
	first := r.lastJob()
	if err := r.run(first, 1); err != nil {
		t.Fatal(err)
	}
	r.completed(first)
	failed := r.lines(chatID)[1]

	if err := app.Retry(ctx, r.store, ownerAccess, chatID, failed.ID, &app.Picks{PinAccount: accountB, PinModel: "other-m"}); err != nil {
		t.Fatal(err)
	}
	retry := r.lastJob()
	if retry.Args.PinAccount != accountB || retry.Args.PinModel != "other-m" || retry.Args.Route != "chat" {
		t.Fatalf("override args = %+v", retry.Args)
	}
	if err := r.run(retry, 1); err != nil {
		t.Fatal(err)
	}
	got := r.stub.seen()
	if last := got[len(got)-1]; last.Pin == nil || last.Pin.AccountID != accountB || last.Pin.Model != "other-m" || last.Route != "chat" {
		t.Fatalf("override call = %+v", last)
	}
	var stored string
	if err := r.owner.QueryRow(ctx, `SELECT pin_account_id::text || '/' || pin_model FROM chat.lines WHERE chat_id = $1::uuid AND seq = 1`, chatID).Scan(&stored); err != nil || stored != accountA+"/gpt-x" {
		t.Fatalf("the user line's stored pin = %q %v", stored, err)
	}
}
