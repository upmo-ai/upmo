//go:build integration

package chat_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/chat"
	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/platform/httpserver"
	"github.com/upmo-ai/upmo/internal/platform/metrics"
)

// streamRig is the stream route over the rig's store, a hub and a listener.
// Requests name the person in the X-User header.
type streamRig struct {
	*rig
	hub     *chat.StreamHub
	srv     *httptest.Server
	metrics *metrics.Registry
	mu      sync.Mutex
	// gone lists the people whose access is lost for good.
	gone map[string]bool
}

func newStreamRig(t *testing.T, cfg chat.StreamConfig, reply app.ReplyConfig) *streamRig {
	t.Helper()
	r := &streamRig{rig: newRig(t, reply), metrics: metrics.NewRegistry(), gone: map[string]bool{}}
	r.hub = chat.NewStreamHub(r.pool, r.owner.Config().ConnConfig, nil, cfg)
	r.hub.Start(context.Background())

	mux := http.NewServeMux()
	chat.Handler{
		Book:    r.store,
		Stream:  r.hub,
		Metrics: r.metrics,
		Access: func(req *http.Request) (chat.Access, bool) {
			access, ok := r.people[names[req.Header.Get("X-User")]]
			return access, ok
		},
		Renew: func(_ context.Context, req *http.Request) (chat.Access, bool, error) {
			user := req.Header.Get("X-User")
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.people[names[user]], !r.gone[user], nil
		},
	}.Mount(mux, nil)
	// The server's own write timeout is short, so a stream that outlives it
	// shows that every write extends its deadline.
	r.srv = httptest.NewUnstartedServer(httpserver.Protect(mux))
	r.srv.Config.WriteTimeout = time.Second
	r.srv.Start()
	t.Cleanup(func() {
		r.hub.Close()
		r.srv.Close()
	})
	return r
}

// names are the people a request can name in X-User.
var names = map[string]string{"owner": ownerUser, "other": otherUser}

// quiet is a config with nothing but notifications driving the hub.
func quiet() chat.StreamConfig {
	return chat.StreamConfig{Replay: time.Hour, Refresh: time.Hour, Heartbeat: time.Hour}
}

type frame struct {
	comment, event, id, data, retry string
}

func (f frame) String() string {
	return fmt.Sprintf("{comment:%q event:%q id:%q data:%s}", f.comment, f.event, f.id, f.data)
}

type sse struct {
	t      *testing.T
	resp   *http.Response
	frames chan frame
	cancel context.CancelFunc
}

// stream opens a stream as user and expects it to start. The ok comment and the
// retry that open every stream are read.
func (r *streamRig) stream(user, chatID string, header http.Header, query string) *sse {
	r.t.Helper()
	resp, cancel := r.get(user, "/api/chats/"+chatID+"/stream"+query, header)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		cancel()
		r.t.Fatalf("GET stream = %d %s", resp.StatusCode, body)
	}
	frames := make(chan frame, 4096)
	go readFrames(resp.Body, frames)
	s := &sse{t: r.t, resp: resp, frames: frames, cancel: cancel}
	r.t.Cleanup(func() { s.cancel(); _ = s.resp.Body.Close() })
	if f := s.expect(); f.comment != "ok" {
		r.t.Fatalf("first frame = %s", f)
	}
	if f := s.expect(); f.retry != "3000" {
		r.t.Fatalf("second frame = %s", f)
	}
	return s
}

func (r *streamRig) get(user, path string, header http.Header) (*http.Response, context.CancelFunc) {
	r.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.srv.URL+path, nil)
	if err != nil {
		cancel()
		r.t.Fatal(err)
	}
	for name, values := range header {
		req.Header[name] = values
	}
	req.Header.Set("X-User", user)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		r.t.Fatal(err)
	}
	return resp, cancel
}

func readFrames(body io.Reader, out chan<- frame) {
	defer close(out)
	rd := bufio.NewReader(body)
	var f frame
	has := false
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if has {
				out <- f
				f, has = frame{}, false
			}
		case strings.HasPrefix(line, ":"):
			f.comment, has = strings.TrimSpace(line[1:]), true
		default:
			name, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch name {
			case "event":
				f.event = value
			case "id":
				f.id = value
			case "data":
				f.data = value
			case "retry":
				f.retry = value
			}
			has = true
		}
	}
}

// expect returns the next frame that is not a heartbeat.
func (s *sse) expect() frame {
	s.t.Helper()
	deadline := time.Now().Add(wait)
	for {
		select {
		case f, ok := <-s.frames:
			if !ok {
				s.t.Fatal("the stream ended")
			}
			if f.comment == "hb" {
				continue
			}
			return f
		case <-time.After(time.Until(deadline)):
			s.t.Fatalf("no frame in %s", wait)
		}
	}
}

// expectQuiet fails on any frame but a heartbeat within d.
func (s *sse) expectQuiet(d time.Duration) {
	s.t.Helper()
	deadline := time.After(d)
	for {
		select {
		case f, ok := <-s.frames:
			if !ok {
				s.t.Fatal("the stream ended")
			}
			if f.comment != "hb" {
				s.t.Fatalf("unexpected frame %s", f)
			}
		case <-deadline:
			return
		}
	}
}

func (s *sse) expectDelta(lineID string, seq int, body string) frame {
	s.t.Helper()
	f := s.expect()
	want := fmt.Sprintf(`{"line_id":%q,"seq":%d,"body":%q}`, lineID, seq, body)
	if f.event != "delta" || f.data != want {
		s.t.Fatalf("frame = %s, want delta %s", f, want)
	}
	return f
}

func (s *sse) expectLine(lineID string) (frame, map[string]any) {
	s.t.Helper()
	f := s.expect()
	var line map[string]any
	if f.event != "line" || json.Unmarshal([]byte(f.data), &line) != nil || line["id"] != lineID {
		s.t.Fatalf("frame = %s, want line %s", f, lineID)
	}
	return f, line
}

// put writes one delta of a reply the way the job does.
func (r *streamRig) put(chatID, replyID string, seq int, kind, body string) {
	r.t.Helper()
	if err := r.store.PutDelta(context.Background(), ownerAccess, chatID, replyID, seq, kind, body); err != nil {
		r.t.Fatal(err)
	}
}

const replyID = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaaa"

func TestStreamSendsDeltasAsTheyAreWrittenThenTheLine(t *testing.T) {
	r := newStreamRig(t, quiet(), app.ReplyConfig{FlushBytes: 5})
	chatID := r.open()
	r.say(chatID, "hi", "")
	queued := r.lastJob()
	release := make(chan struct{})
	r.stub.reply(func(_ int, s *frames) {
		s.delta("Hello ")
		s.hold(release)
		s.delta("world")
		s.done("m1", 1, 1, nil)
	})

	// With no cursor the client has the lines stored now, so the stream is
	// quiet until the reply starts.
	live := r.stream("owner", chatID, nil, "")
	live.expectQuiet(100 * time.Millisecond)
	done := r.runAsync(queued, 1)
	live.expectDelta(queued.Args.ReplyID, 0, "")
	live.expectDelta(queued.Args.ReplyID, 1, "Hello ")
	live.expectQuiet(100 * time.Millisecond)
	close(release)
	live.expectDelta(queued.Args.ReplyID, 2, "world")
	if err := result(t, done); err != nil {
		t.Fatal(err)
	}
	_, line := live.expectLine(queued.Args.ReplyID)
	if line["body"] != "Hello world" {
		t.Fatalf("line = %v", line)
	}
}

func TestStreamLineEventFollowsTheLastDeltaItsClientHolds(t *testing.T) {
	r := newStreamRig(t, quiet(), app.ReplyConfig{FlushBytes: 5})
	chatID := r.open()
	r.say(chatID, "hi", "")
	queued := r.lastJob()
	r.stub.reply(func(_ int, s *frames) { s.delta("Hello"); s.done("m1", 3, 4, i64(500000)) })

	live := r.stream("owner", chatID, nil, "")
	if err := r.run(queued, 1); err != nil {
		t.Fatal(err)
	}
	marker := live.expectDelta(queued.Args.ReplyID, 0, "")
	text := live.expectDelta(queued.Args.ReplyID, 1, "Hello")
	frame, line := live.expectLine(queued.Args.ReplyID)
	if frame.id <= text.id || text.id <= marker.id {
		t.Fatalf("ids %q %q %q do not rise", marker.id, text.id, frame.id)
	}
	if line["status"] != "done" || line["body"] != "Hello" || line["role"] != "assistant" ||
		line["reply_to"] != r.lines(chatID)[0].ID || line["tokens"] != float64(7) || line["cost_usd"] != 0.5 {
		t.Fatalf("line = %v", line)
	}

	// A client that came back holding the last delta receives the line and
	// nothing else: the deltas of a finished line are gone.
	again := r.stream("owner", chatID, http.Header{"Last-Event-Id": {text.id}}, "")
	again.expectLine(queued.Args.ReplyID)
	again.expectQuiet(150 * time.Millisecond)

	// With no cursor at all the client has the thread already: nothing.
	fresh := r.stream("owner", chatID, nil, "")
	fresh.expectQuiet(150 * time.Millisecond)
}

func TestStreamResumesMidLineByLastEventIDAndAfter(t *testing.T) {
	r := newStreamRig(t, quiet(), app.ReplyConfig{FlushBytes: 5})
	chatID := r.open()
	r.say(chatID, "hi", "")
	for i, body := range []string{"", "one ", "two ", "three"} {
		r.put(chatID, replyID, i, "text", body)
	}
	ids := make([]string, 0, 4)
	for _, d := range r.deltas(chatID) {
		ids = append(ids, d.ID)
	}

	s := r.stream("owner", chatID, http.Header{"Last-Event-Id": {ids[1]}}, "")
	two := s.expectDelta(replyID, 2, "two ")
	s.expectDelta(replyID, 3, "three")
	if two.id != ids[2] {
		t.Fatalf("event id = %q, want the row id %q", two.id, ids[2])
	}
	s.expectQuiet(100 * time.Millisecond)

	// The after query does the same, and the header wins over it.
	q := r.stream("owner", chatID, nil, "?after="+ids[2])
	q.expectDelta(replyID, 3, "three")
	both := r.stream("owner", chatID, http.Header{"Last-Event-Id": {ids[2]}}, "?after="+ids[0])
	both.expectDelta(replyID, 3, "three")

	// And a row written after the stream opened arrives live.
	r.put(chatID, replyID, 4, "text", "!")
	s.expectDelta(replyID, 4, "!")

	status, _ := r.get("owner", "/api/chats/"+chatID+"/stream?after=nope", nil)
	defer func() { _ = status.Body.Close() }()
	if status.StatusCode != http.StatusBadRequest {
		t.Fatalf("a bad cursor = %d", status.StatusCode)
	}
}

func TestStreamReplaysAttemptNoticesToAClientThatReconnects(t *testing.T) {
	r := newStreamRig(t, quiet(), app.ReplyConfig{FlushBytes: 5})
	chatID := r.open()
	r.say(chatID, "hi", "")
	r.put(chatID, replyID, 0, "text", "")
	r.put(chatID, replyID, 1, "status", `{"failed_model":"m1","class":"rate_limit","next_model":"m2"}`)
	r.put(chatID, replyID, 2, "text", "going")

	// A new stream with no cursor replays the reply under way from its start,
	// notice included, with the reason as a phrase and never the class.
	s := r.stream("owner", chatID, nil, "")
	s.expectDelta(replyID, 0, "")
	f := s.expect()
	want := fmt.Sprintf(`{"line_id":%q,"failed_model":"m1","reason":"rate limited","next_model":"m2"}`, replyID)
	if f.event != "status" || f.id == "" || f.data != want {
		t.Fatalf("frame = %s, want status %s", f, want)
	}
	s.expectDelta(replyID, 2, "going")
}

func TestStreamRefusesAChatThePersonCannotSee(t *testing.T) {
	r := newStreamRig(t, quiet(), app.ReplyConfig{FlushBytes: 5})
	chatID := r.open()
	for _, user := range []string{"other", "nobody"} {
		resp, cancel := r.get(user, "/api/chats/"+chatID+"/stream", nil)
		_ = resp.Body.Close()
		cancel()
		want := http.StatusNotFound
		if user == "nobody" {
			want = http.StatusUnauthorized
		}
		if resp.StatusCode != want {
			t.Fatalf("%s: status %d, want %d", user, resp.StatusCode, want)
		}
	}
	resp, cancel := r.get("owner", "/api/chats/not-a-chat/stream", nil)
	defer cancel()
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a bad chat id = %d", resp.StatusCode)
	}
}

func TestStreamEndsWhenAccessIsLost(t *testing.T) {
	r := newStreamRig(t, chat.StreamConfig{Replay: time.Hour, Refresh: 50 * time.Millisecond, Heartbeat: time.Hour}, app.ReplyConfig{})
	chatID := r.open()
	s := r.stream("owner", chatID, nil, "")
	s.expectQuiet(150 * time.Millisecond)
	r.mu.Lock()
	r.gone["owner"] = true
	r.mu.Unlock()
	f := s.expect()
	if f.event != "end" || f.data != `{"reason":"access"}` {
		t.Fatalf("frame = %s", f)
	}
}

func TestStreamWithNoCursorSeesAReplyUnderWayFromItsStartEvenAfterANewerUserLine(t *testing.T) {
	r := newStreamRig(t, quiet(), app.ReplyConfig{})
	chatID := r.open()
	r.say(chatID, "first", "")
	r.put(chatID, replyID, 0, "text", "")
	r.put(chatID, replyID, 1, "status", `{"failed_model":"m1","class":"timeout","next_model":"m2"}`)
	r.put(chatID, replyID, 2, "text", "going")
	// A follow-up is sent after the reply began: its line sorts after the deltas.
	r.say(chatID, "second", "")
	second := r.lines(chatID)[1]

	s := r.stream("owner", chatID, nil, "")
	s.expectDelta(replyID, 0, "")
	if f := s.expect(); f.event != "status" {
		t.Fatalf("frame = %s, want the notice", f)
	}
	s.expectDelta(replyID, 2, "going")
	s.expectLine(second.ID)
	s.expectQuiet(100 * time.Millisecond)
}

func TestStreamMetricsGauge(t *testing.T) {
	r := newStreamRig(t, quiet(), app.ReplyConfig{})
	chatID := r.open()
	s := r.stream("owner", chatID, nil, "")

	scrape := func() string {
		rec := httptest.NewRecorder()
		r.metrics.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		return rec.Body.String()
	}

	if body := scrape(); !strings.Contains(body, `upmo_sse_streams_active{kind="chat"} 1`) {
		t.Fatalf("expected active chat stream = 1:\n%s", body)
	}
	s.cancel()
	_ = s.resp.Body.Close()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if strings.Contains(scrape(), `upmo_sse_streams_active{kind="chat"} 0`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected active chat stream = 0 after close:\n%s", scrape())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
