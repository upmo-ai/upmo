//go:build integration

package tasks_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/httpserver"
	"github.com/upmo-ai/upmo/internal/platform/metrics"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/tasks"
	"github.com/upmo-ai/upmo/internal/tasks/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

const (
	ownerUser = "018f3b2c-7a10-7c4a-8e11-111111111111"
	otherUser = "018f3b2c-7a10-7c4a-8e11-222222222222"
	wait      = 5 * time.Second
)

var (
	ownerAccess = app.Access{UserID: ownerUser, Allow: "task:create,task:view,task:edit"}
	otherAccess = app.Access{UserID: otherUser, Allow: "task:view,task:edit"}
)

// countingRows is the real store with a count of the reads the hub makes.
type countingRows struct {
	postgres.Store
	mu    sync.Mutex
	tails map[string]int
}

func (c *countingRows) Tail(ctx context.Context, access app.Access, taskID, after string, limit int) (app.Tail, error) {
	c.mu.Lock()
	c.tails[taskID]++
	c.mu.Unlock()
	return c.Store.Tail(ctx, access, taskID, after, limit)
}

func (c *countingRows) reads(taskID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tails[taskID]
}

// rig is the stream route over the real store, hub, listener and database.
// Requests name the person in the X-User header: owner or other.
type rig struct {
	t       *testing.T
	owner   *pgxpool.Pool
	store   tasks.Store
	rows    *countingRows
	hub     *tasks.StreamHub
	srv     *httptest.Server
	metrics *metrics.Registry
	mu      sync.Mutex
	gone    map[string]bool
	people  map[string]app.Access
}

func newRig(t *testing.T, cfg tasks.StreamConfig) *rig {
	t.Helper()
	owner := pgtest.New(t)
	ctx := context.Background()
	// The store records audit events, so the audit schema comes up first.
	if err := audit.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, tasks.Schema, tasks.Migrations()); err != nil {
		t.Fatal(err)
	}
	store := tasks.NewStore(pgtest.App(t, owner), owner, nil)
	r := &rig{
		t:       t,
		owner:   owner,
		store:   store,
		rows:    &countingRows{Store: store, tails: map[string]int{}},
		metrics: metrics.NewRegistry(),
		gone:    map[string]bool{},
		people:  map[string]app.Access{"owner": ownerAccess, "other": otherAccess},
	}
	listener := postgres.Listener{Config: owner.Config().ConnConfig, MinBackoff: 50 * time.Millisecond}
	r.hub = app.NewStreamHub(r.rows, listener, nil, cfg)
	r.hub.Start(context.Background())

	mux := http.NewServeMux()
	tasks.Handler{
		View:    store,
		Stream:  r.hub,
		Metrics: r.metrics,
		Access: func(req *http.Request) (tasks.Access, bool) {
			access, ok := r.people[req.Header.Get("X-User")]
			return access, ok
		},
		Renew: func(_ context.Context, req *http.Request) (tasks.Access, bool, error) {
			user := req.Header.Get("X-User")
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.people[user], !r.gone[user], nil
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
	r.waitListening()
	return r
}

// quiet is a config with nothing but notifications driving the hub.
func quiet() tasks.StreamConfig {
	return tasks.StreamConfig{Replay: time.Hour, Refresh: time.Hour, Heartbeat: time.Hour}
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// listeners returns the process ids of the hub's listener connections.
func (r *rig) listeners() []int {
	rows, err := r.owner.Query(context.Background(),
		`SELECT pid FROM pg_stat_activity WHERE application_name = $1 AND datname = current_database() AND query = 'LISTEN task_stream' AND state = 'idle'`,
		postgres.ApplicationName)
	if err != nil {
		r.t.Fatal(err)
	}
	defer rows.Close()
	var pids []int
	for rows.Next() {
		var pid int
		if err := rows.Scan(&pid); err != nil {
			r.t.Fatal(err)
		}
		pids = append(pids, pid)
	}
	return pids
}

func (r *rig) waitListening() {
	r.t.Helper()
	eventually(r.t, "the listener to listen", func() bool { return len(r.listeners()) == 1 })
}

// dropListener ends the listener's connection and waits for a new one.
func (r *rig) dropListener() {
	r.t.Helper()
	old := r.listeners()
	if len(old) != 1 {
		r.t.Fatalf("listeners = %v", old)
	}
	if _, err := r.owner.Exec(context.Background(), `SELECT pg_terminate_backend($1)`, old[0]); err != nil {
		r.t.Fatal(err)
	}
	eventually(r.t, "the listener to reconnect", func() bool {
		now := r.listeners()
		return len(now) == 1 && now[0] != old[0]
	})
}

// notifyTriggers turns the notification triggers on or off, to write events
// the way a lost notification would leave them.
func (r *rig) notifyTriggers(on bool) {
	r.t.Helper()
	verb := "DISABLE"
	if on {
		verb = "ENABLE"
	}
	for _, stmt := range []string{
		"ALTER TABLE tasks.task_events " + verb + " TRIGGER task_events_stream_notify",
		"ALTER TABLE tasks.chat_events " + verb + " TRIGGER chat_events_stream_notify",
	} {
		if _, err := r.owner.Exec(context.Background(), stmt); err != nil {
			r.t.Fatal(err)
		}
	}
}

func (r *rig) revoke(user string) {
	r.mu.Lock()
	r.gone[user] = true
	r.mu.Unlock()
}

// task opens a task and walks it to status.
func (r *rig) task(goal string, status domain.Status) app.Task {
	r.t.Helper()
	ctx := context.Background()
	task, err := app.ApplyTransition(ctx, r.store, ownerAccess, "", domain.Input{Kind: domain.Open, Goal: goal})
	if err != nil {
		r.t.Fatal(err)
	}
	steps := map[domain.Status][]domain.Input{
		domain.Preparing: nil,
		domain.Planning:  {{Kind: domain.Plan}},
		domain.Working:   {{Kind: domain.Advance}},
		domain.Verifying: {{Kind: domain.Advance}, {Kind: domain.Advance, Turn: domain.Sentinel}},
		domain.Done:      {{Kind: domain.Advance}, {Kind: domain.Advance, Turn: domain.Sentinel}, {Kind: domain.Pass}},
		domain.Failed:    {{Kind: domain.Plan}, {Kind: domain.Fail}},
	}
	for _, input := range steps[status] {
		if input.Kind == domain.Pass {
			if _, err := app.RecordChecks(ctx, r.store, ownerAccess, task.ID, []domain.Check{{Name: "go test", Code: 0}}, nil); err != nil {
				r.t.Fatal(err)
			}
		}
		if task, err = app.ApplyTransition(ctx, r.store, ownerAccess, task.ID, input); err != nil {
			r.t.Fatalf("%s: %q: %v", status, input.Kind, err)
		}
	}
	if task.Status != status {
		r.t.Fatalf("reached %s, want %s", task.Status, status)
	}
	return task
}

func (r *rig) step(taskID string, input domain.Input) {
	r.t.Helper()
	if _, err := app.ApplyTransition(context.Background(), r.store, ownerAccess, taskID, input); err != nil {
		r.t.Fatalf("step %q: %v", input.Kind, err)
	}
}

func (r *rig) chat(taskID, body string) {
	r.t.Helper()
	r.step(taskID, domain.Input{Kind: domain.Chat, Body: body})
}

// ids returns the ids of the task's events and chat lines, in the order the
// detail read model gives them.
func (r *rig) ids(taskID string) []string {
	r.t.Helper()
	view, err := app.ViewTask(context.Background(), r.store, ownerAccess, taskID)
	if err != nil {
		r.t.Fatal(err)
	}
	ids := make([]string, 0, len(view.Events))
	for _, event := range view.Events {
		ids = append(ids, event.ID)
	}
	return ids
}

// detail returns the events array of GET /api/tasks/{id}.
func (r *rig) detail(taskID string) []map[string]any {
	r.t.Helper()
	req, err := http.NewRequest(http.MethodGet, r.srv.URL+"/api/tasks/"+taskID, nil)
	if err != nil {
		r.t.Fatal(err)
	}
	req.Header.Set("X-User", "owner")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		r.t.Fatal(err)
	}
	return body.Events
}

// frame is one event stream frame.
type frame struct {
	comment, event, id, data, retry string
}

func (f frame) String() string {
	return fmt.Sprintf("{comment:%q event:%q id:%q data:%s}", f.comment, f.event, f.id, f.data)
}

// sse is a client of one open stream.
type sse struct {
	t      *testing.T
	resp   *http.Response
	frames chan frame
	cancel context.CancelFunc
	eof    bool
}

// open opens a stream as user and expects it to start.
func (r *rig) open(user, path string, header http.Header) *sse {
	r.t.Helper()
	resp, cancel := r.get(user, path, header)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		cancel()
		r.t.Fatalf("GET %s = %d %s", path, resp.StatusCode, body)
	}
	frames := make(chan frame, 4096)
	go readFrames(resp.Body, frames)
	s := &sse{t: r.t, resp: resp, frames: frames, cancel: cancel}
	r.t.Cleanup(s.close)
	return s
}

func (r *rig) get(user, path string, header http.Header) (*http.Response, context.CancelFunc) {
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
	if user != "" {
		req.Header.Set("X-User", user)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		r.t.Fatal(err)
	}
	return resp, cancel
}

// refused returns the status and the body of a request that must not start a stream.
func (r *rig) refused(user, path string, header http.Header) (int, string) {
	r.t.Helper()
	resp, cancel := r.get(user, path, header)
	defer cancel()
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(body))
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

func (s *sse) close() {
	s.cancel()
	_ = s.resp.Body.Close()
}

// next returns the next frame, or false when none came in d or the stream
// ended. eof tells the two apart.
func (s *sse) next(d time.Duration) (frame, bool) {
	select {
	case f, ok := <-s.frames:
		if !ok {
			s.eof = true
		}
		return f, ok
	case <-time.After(d):
		return frame{}, false
	}
}

// expect returns the next frame that is not a heartbeat.
func (s *sse) expect() frame {
	s.t.Helper()
	deadline := time.Now().Add(wait)
	for {
		f, ok := s.next(time.Until(deadline))
		if !ok {
			s.t.Fatalf("no frame in %s (stream ended: %v)", wait, s.eof)
		}
		if f.comment == "hb" {
			continue
		}
		return f
	}
}

func (s *sse) expectLine(id string) frame {
	s.t.Helper()
	f := s.expect()
	if f.event != "line" || f.id != id {
		s.t.Fatalf("frame = %s, want line %s", f, id)
	}
	return f
}

func (s *sse) expectStatus(status, phase string) {
	s.t.Helper()
	f := s.expect()
	if f.event != "status" || f.id != "" || f.data != fmt.Sprintf(`{"status":%q,"phase":%q}`, status, phase) {
		s.t.Fatalf("frame = %s, want status %s %s", f, status, phase)
	}
}

func (s *sse) expectEnd(reason string) {
	s.t.Helper()
	f := s.expect()
	if f.event != "end" || f.data != fmt.Sprintf(`{"reason":%q}`, reason) {
		s.t.Fatalf("frame = %s, want end %s", f, reason)
	}
}

// expectHello reads the comment and the retry that open every stream.
func (s *sse) expectHello() {
	s.t.Helper()
	if f := s.expect(); f.comment != "ok" || f.event != "" {
		s.t.Fatalf("first frame = %s, want the ok comment", f)
	}
	if f := s.expect(); f.retry != "3000" || f.event != "" {
		s.t.Fatalf("second frame = %s, want retry 3000", f)
	}
}

func (s *sse) expectEOF() {
	s.t.Helper()
	deadline := time.Now().Add(wait)
	for !s.eof {
		f, ok := s.next(time.Until(deadline))
		if ok && f.comment != "hb" {
			s.t.Fatalf("frame %s before the stream closed", f)
		}
		if !ok && !s.eof {
			s.t.Fatal("the stream did not close")
		}
	}
}

// expectQuiet fails on any frame but a heartbeat within d.
func (s *sse) expectQuiet(d time.Duration) {
	s.t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		f, ok := s.next(time.Until(deadline))
		if ok && f.comment != "hb" {
			s.t.Fatalf("unexpected frame %s", f)
		}
		if s.eof {
			s.t.Fatal("the stream ended")
		}
	}
}

func TestStreamResumesAfterAnIdAndLastEventIDWins(t *testing.T) {
	r := newRig(t, quiet())
	task := r.task("Resume", domain.Working)
	r.chat(task.ID, "first line")
	r.step(task.ID, domain.Input{Kind: domain.Advance, Turn: domain.Sentinel})
	r.chat(task.ID, "second line")
	ids := r.ids(task.ID)
	if len(ids) != 5 {
		t.Fatalf("events = %d, want 5", len(ids))
	}
	path := "/api/tasks/" + task.ID + "/stream"

	// ?after= replays what is newer, in the order and shape of the detail,
	// and the comment and retry come before any event.
	s := r.open("owner", path+"?after="+ids[1], nil)
	if got := s.resp.Header.Get("Content-Type"); got != "text/event-stream; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := s.resp.Header.Get("Cache-Control"); got != "no-cache, no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	if got := s.resp.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Errorf("X-Accel-Buffering = %q", got)
	}
	if got := s.resp.Header.Get("Content-Encoding"); got != "" {
		t.Errorf("Content-Encoding = %q", got)
	}
	s.expectHello()
	s.expectStatus("Working", "Verifying")
	want := r.detail(task.ID)[2:]
	for i, id := range ids[2:] {
		f := s.expectLine(id)
		var got map[string]any
		if err := json.Unmarshal([]byte(f.data), &got); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want[i]) {
			t.Errorf("line %d = %v, want %v", i, got, want[i])
		}
	}
	s.expectStatus("Working", "Verifying")
	s.expectQuiet(300 * time.Millisecond)

	// The header wins over the query.
	header := http.Header{"Last-Event-Id": {ids[2]}}
	s = r.open("owner", path+"?after="+ids[0], header)
	s.expectHello()
	s.expectStatus("Working", "Verifying")
	s.expectLine(ids[3])
	s.expectLine(ids[4])

	// With neither, nothing is replayed: the client has the task from GET.
	s = r.open("owner", path, nil)
	s.expectHello()
	s.expectStatus("Working", "Verifying")
	s.expectQuiet(300 * time.Millisecond)
}

func TestStreamStoreTailExcludesTheCursorEvent(t *testing.T) {
	r := newRig(t, quiet())
	task := r.task("Cursor", domain.Planning)
	r.chat(task.ID, "first line")
	r.step(task.ID, domain.Input{Kind: domain.Advance})
	r.chat(task.ID, "second line")
	ids := r.ids(task.ID)
	if len(ids) != 5 {
		t.Fatalf("events = %d, want 5", len(ids))
	}
	ctx := context.Background()

	// Tail from a task event cursor returns what is strictly newer, on the
	// store's own: the hub re-filters in deliver, so a cursor event handed
	// back here would replay a line the client already has.
	tail, err := r.store.Tail(ctx, ownerAccess, task.ID, ids[1], len(ids))
	if err != nil {
		t.Fatal(err)
	}
	if got := tailIDs(tail); !reflect.DeepEqual(got, ids[2:]) {
		t.Fatalf("tail after %s = %v, want %v", ids[1], got, ids[2:])
	}

	// The chat query takes the cursor too, so a chat event is excluded the
	// same way.
	tail, err = r.store.Tail(ctx, ownerAccess, task.ID, ids[2], len(ids))
	if err != nil {
		t.Fatal(err)
	}
	if got := tailIDs(tail); !reflect.DeepEqual(got, ids[3:]) {
		t.Fatalf("tail after %s = %v, want %v", ids[2], got, ids[3:])
	}
}

// tailIDs lists the ids of a tail's events.
func tailIDs(tail app.Tail) []string {
	ids := make([]string, 0, len(tail.Events))
	for _, event := range tail.Events {
		ids = append(ids, event.ID)
	}
	return ids
}

func TestStreamReplayMergesTaskAndChatEventsWithTheirPhase(t *testing.T) {
	r := newRig(t, quiet())
	task := r.task("Merge", domain.Planning)
	r.chat(task.ID, "while planning")
	r.step(task.ID, domain.Input{Kind: domain.Advance})
	r.chat(task.ID, "while working")
	ids := r.ids(task.ID)
	s := r.open("owner", "/api/tasks/"+task.ID+"/stream?after="+ids[0], nil)
	s.expectHello()
	s.expectStatus("Working", "Working")
	var phases []string
	for _, id := range ids[1:] {
		f := s.expectLine(id)
		var body struct{ Type, Phase string }
		if err := json.Unmarshal([]byte(f.data), &body); err != nil {
			t.Fatal(err)
		}
		phases = append(phases, body.Type+":"+body.Phase)
	}
	want := []string{"plan:Planning", "message:Planning", "advance:Working", "message:Working"}
	if !reflect.DeepEqual(phases, want) {
		t.Fatalf("lines = %v, want %v", phases, want)
	}
}

func TestStreamDeliversALiveEventByNotification(t *testing.T) {
	r := newRig(t, quiet())
	task := r.task("Live", domain.Working)
	s := r.open("owner", "/api/tasks/"+task.ID+"/stream", nil)
	s.expectHello()
	s.expectStatus("Working", "Working")
	s.expectQuiet(200 * time.Millisecond)

	r.chat(task.ID, "hello")
	ids := r.ids(task.ID)
	f := s.expectLine(ids[len(ids)-1])
	if !strings.Contains(f.data, `"message":"hello"`) {
		t.Fatalf("data = %s", f.data)
	}
	s.expectStatus("Working", "Working")
}

func TestStreamRepairsAMissedNotification(t *testing.T) {
	t.Run("on the replay timer", func(t *testing.T) {
		cfg := quiet()
		cfg.Replay = 300 * time.Millisecond
		r := newRig(t, cfg)
		task := r.task("Missed", domain.Working)
		s := r.open("owner", "/api/tasks/"+task.ID+"/stream", nil)
		s.expectHello()
		s.expectStatus("Working", "Working")

		r.notifyTriggers(false)
		r.chat(task.ID, "no notification")
		ids := r.ids(task.ID)
		s.expectLine(ids[len(ids)-1])
		s.expectStatus("Working", "Working")
	})

	t.Run("after the listener reconnects", func(t *testing.T) {
		// The timer is an hour away, so only the reconnect can repair it.
		r := newRig(t, quiet())
		task := r.task("Missed after reconnect", domain.Working)
		s := r.open("owner", "/api/tasks/"+task.ID+"/stream", nil)
		s.expectHello()
		s.expectStatus("Working", "Working")

		r.notifyTriggers(false)
		r.chat(task.ID, "no notification either")
		ids := r.ids(task.ID)
		s.expectQuiet(500 * time.Millisecond)
		r.notifyTriggers(true)
		r.dropListener()
		s.expectLine(ids[len(ids)-1])
		s.expectStatus("Working", "Working")
	})
}

func TestStreamEndsWhenAccessIsLost(t *testing.T) {
	t.Run("the session or role goes", func(t *testing.T) {
		cfg := quiet()
		cfg.Refresh = 300 * time.Millisecond
		r := newRig(t, cfg)
		task := r.task("Session", domain.Working)
		s := r.open("owner", "/api/tasks/"+task.ID+"/stream", nil)
		s.expectHello()
		s.expectStatus("Working", "Working")
		s.expectQuiet(500 * time.Millisecond)
		r.revoke("owner")
		s.expectEnd("access")
		s.expectEOF()
		eventually(t, "the stream to be released", func() bool { return r.hub.Open() == 0 })
	})

	t.Run("the task is unshared and the refresh finds out", func(t *testing.T) {
		cfg := quiet()
		cfg.Refresh = 300 * time.Millisecond
		r := newRig(t, cfg)
		task := r.task("Unshare", domain.Working)
		if err := r.store.SaveVisibility(context.Background(), ownerAccess, task.ID, "everyone", "", nil, nil); err != nil {
			t.Fatal(err)
		}
		s := r.open("other", "/api/tasks/"+task.ID+"/stream", nil)
		s.expectHello()
		s.expectStatus("Working", "Working")
		if err := r.store.SaveVisibility(context.Background(), ownerAccess, task.ID, "private", "", nil, nil); err != nil {
			t.Fatal(err)
		}
		s.expectEnd("access")
		s.expectEOF()
	})

	t.Run("a read that fails for the person is a revocation", func(t *testing.T) {
		// The refresh is an hour away. The next event is read with the
		// person's access, which no longer sees the task, and that sends the
		// stream to an access check at once.
		r := newRig(t, quiet())
		task := r.task("Detected", domain.Working)
		if err := r.store.SaveVisibility(context.Background(), ownerAccess, task.ID, "everyone", "", nil, nil); err != nil {
			t.Fatal(err)
		}
		s := r.open("other", "/api/tasks/"+task.ID+"/stream", nil)
		s.expectHello()
		s.expectStatus("Working", "Working")
		if err := r.store.SaveVisibility(context.Background(), ownerAccess, task.ID, "private", "", nil, nil); err != nil {
			t.Fatal(err)
		}
		r.chat(task.ID, "private now")
		s.expectEnd("access")
		s.expectEOF()
	})
}

func TestStreamEndsFinishedWhenTheTaskIsDoneOrFailed(t *testing.T) {
	r := newRig(t, quiet())

	for _, tc := range []struct {
		name   string
		status domain.Status
		shown  string
		phase  string
		kind   domain.Input
	}{
		{"done", domain.Done, "Done", "Verifying", domain.Input{Kind: domain.Pass}},
		{"failed", domain.Failed, "Failed", "Planning", domain.Input{Kind: domain.Fail}},
	} {
		t.Run(tc.name+" while connected", func(t *testing.T) {
			var task app.Task
			if tc.status == domain.Done {
				task = r.task("Finish", domain.Verifying)
			} else {
				task = r.task("Finish", domain.Planning)
			}
			s := r.open("owner", "/api/tasks/"+task.ID+"/stream", nil)
			s.expectHello()
			s.expectStatus("Working", tc.phase)
			if tc.status == domain.Done {
				if _, err := app.RecordChecks(context.Background(), r.store, ownerAccess, task.ID, []domain.Check{{Name: "go test", Code: 0}}, nil); err != nil {
					t.Fatal(err)
				}
			}
			r.step(task.ID, tc.kind)
			ids := r.ids(task.ID)
			s.expectLine(ids[len(ids)-1])
			s.expectStatus(tc.shown, tc.phase)
			s.expectEnd("finished")
			s.expectEOF()
		})

		t.Run(tc.name+" before connecting", func(t *testing.T) {
			task := r.task("Finished already", tc.status)
			ids := r.ids(task.ID)
			s := r.open("owner", "/api/tasks/"+task.ID+"/stream?after="+ids[len(ids)-2], nil)
			s.expectHello()
			s.expectStatus(tc.shown, tc.phase)
			s.expectLine(ids[len(ids)-1])
			s.expectStatus(tc.shown, tc.phase)
			s.expectEnd("finished")
			s.expectEOF()

			s = r.open("owner", "/api/tasks/"+task.ID+"/stream", nil)
			s.expectHello()
			s.expectStatus(tc.shown, tc.phase)
			s.expectEnd("finished")
			s.expectEOF()
		})
	}
}

func TestStreamDropsASlowSubscriberWhoCanResume(t *testing.T) {
	cfg := quiet()
	cfg.Buffer = 4
	cfg.Stall = 200 * time.Millisecond
	cfg.Replay = 100 * time.Millisecond
	r := newRig(t, cfg)
	task := r.task("Slow", domain.Working)
	ids := r.ids(task.ID)

	// A subscriber that never takes a message.
	slow, err := r.hub.Subscribe(tasks.StreamSpec{TaskID: task.ID, Person: ownerUser, Access: ownerAccess, After: ids[len(ids)-1]})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 8 {
		r.chat(task.ID, fmt.Sprintf("line %d", i))
	}
	select {
	case <-slow.Closed():
	case <-time.After(wait):
		t.Fatal("the slow subscriber was not closed")
	}
	if got := slow.Reason(); got == app.EndAccess || got == app.EndFinished {
		t.Fatalf("reason = %q, want a close without an end event", got)
	}
	var got []string
	for done := false; !done; {
		select {
		case m := <-slow.Out():
			if m.Kind == app.StreamEnd {
				t.Fatal("the slow subscriber was sent an end")
			}
			if m.Kind == app.StreamLine {
				got = append(got, m.Event.ID)
			}
		default:
			done = true
		}
	}
	all := r.ids(task.ID)
	if len(got) == 0 || len(got) >= 8 {
		t.Fatalf("slow subscriber got %d of 8 lines", len(got))
	}
	if !reflect.DeepEqual(got, all[len(ids):len(ids)+len(got)]) {
		t.Fatalf("slow subscriber got %v, want a prefix of %v", got, all[len(ids):])
	}
	eventually(t, "the slow stream to be released", func() bool { return r.hub.Open() == 0 })

	// It reconnects with Last-Event-ID and gets the rest.
	s := r.open("owner", "/api/tasks/"+task.ID+"/stream", http.Header{"Last-Event-Id": {got[len(got)-1]}})
	s.expectHello()
	s.expectStatus("Working", "Working")
	for _, id := range all[len(ids)+len(got):] {
		s.expectLine(id)
	}
}

func TestStreamCapsStreamsPerPersonAndPerProcess(t *testing.T) {
	cfg := quiet()
	cfg.PerProcess = 10
	r := newRig(t, cfg)
	task := r.task("Caps", domain.Working)
	if err := r.store.SaveVisibility(context.Background(), ownerAccess, task.ID, "everyone", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	path := "/api/tasks/" + task.ID + "/stream"
	var open []*sse
	for range 8 {
		open = append(open, r.open("owner", path, nil))
	}
	status, body := r.refused("owner", path, nil)
	if status != http.StatusTooManyRequests || body != `{"error":"too many open streams"}` {
		t.Fatalf("the ninth stream = %d %s", status, body)
	}
	// Another person is not held back by it, up to the process cap.
	r.open("other", path, nil)
	r.open("other", path, nil)
	if status, _ := r.refused("other", path, nil); status != http.StatusServiceUnavailable {
		t.Fatalf("a stream over the process cap = %d, want 503", status)
	}
	// A closed stream frees its place.
	open[0].close()
	eventually(t, "the closed stream to free its place", func() bool { return r.hub.Open() == 9 })
	r.open("owner", path, nil)
}

func TestStreamRefusesBeforeItStarts(t *testing.T) {
	r := newRig(t, quiet())
	task := r.task("Refusals", domain.Working)
	path := "/api/tasks/" + task.ID + "/stream"
	ids := r.ids(task.ID)

	tests := []struct {
		name   string
		user   string
		path   string
		header http.Header
		status int
		body   string
	}{
		{"invisible task", "other", path, nil, 404, `{"error":"no such task"}`},
		{"bearer token", "owner", path, http.Header{"Authorization": {"Bearer upmo_pat_secret"}}, 403, `{"error":"needs a signed-in person"}`},
		{"no session", "", path, nil, 401, `{"error":"unauthorized"}`},
		{"bad id", "owner", "/api/tasks/not-an-id/stream", nil, 400, `{"error":"the task input was refused"}`},
		{"bad cursor", "owner", path + "?after=nope", nil, 400, `{"error":"the event id is not valid"}`},
		{"bad header", "owner", path, http.Header{"Last-Event-Id": {"nope"}}, 400, `{"error":"the event id is not valid"}`},
		{"unknown task", "owner", "/api/tasks/" + ids[0] + "/stream", nil, 404, `{"error":"no such task"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, body := r.refused(tc.user, tc.path, tc.header)
			if status != tc.status || body != tc.body {
				t.Fatalf("= %d %s, want %d %s", status, body, tc.status, tc.body)
			}
		})
	}
	if got := r.hub.Open(); got != 0 {
		t.Fatalf("%d streams are open after the refusals", got)
	}
}

func TestStreamOutlivesTheServerWriteTimeout(t *testing.T) {
	cfg := quiet()
	cfg.Heartbeat = 200 * time.Millisecond
	r := newRig(t, cfg) // the rig's server cuts a response off after 1 s
	task := r.task("Long", domain.Working)
	s := r.open("owner", "/api/tasks/"+task.ID+"/stream", nil)
	s.expectHello()
	s.expectStatus("Working", "Working")

	start := time.Now()
	beats := 0
	for time.Since(start) < 3*time.Second {
		f, ok := s.next(time.Second)
		if !ok {
			t.Fatalf("the stream stopped after %s with %d heartbeats", time.Since(start), beats)
		}
		if f.comment != "hb" {
			t.Fatalf("frame = %s", f)
		}
		beats++
	}
	if beats < 10 {
		t.Fatalf("heartbeats = %d in 3 s, want one every 200 ms", beats)
	}
	// An event still gets through after the write timeout has long passed.
	r.chat(task.ID, "late")
	ids := r.ids(task.ID)
	s.expectLine(ids[len(ids)-1])
}

func TestStreamShutdownDoesNotWaitForOpenStreams(t *testing.T) {
	r := newRig(t, quiet())
	r.srv.Config.RegisterOnShutdown(r.hub.Close)
	task := r.task("Shutdown", domain.Working)
	s := r.open("owner", "/api/tasks/"+task.ID+"/stream", nil)
	s.expectHello()
	s.expectStatus("Working", "Working")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	if err := r.srv.Config.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v after %s", err, time.Since(start))
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("shutdown took %s with a stream open", took)
	}
	s.expectEOF() // closed without an end event, so the browser reconnects
	if _, err := r.hub.Subscribe(tasks.StreamSpec{TaskID: task.ID, Person: ownerUser, Access: ownerAccess}); err == nil {
		t.Fatal("a stream opened on a closed hub")
	}
}

func TestStreamReadsOncePerTaskAndNotification(t *testing.T) {
	r := newRig(t, quiet())
	task := r.task("Fan out", domain.Working)
	other := r.task("Not watched", domain.Working)
	path := "/api/tasks/" + task.ID + "/stream"
	var streams []*sse
	for range 6 {
		s := r.open("owner", path, nil)
		s.expectHello()
		s.expectStatus("Working", "Working")
		streams = append(streams, s)
	}
	// The reads made while the streams opened are done once the count holds.
	last := -1
	eventually(t, "the opening reads to finish", func() bool {
		now := r.rows.reads(task.ID)
		settled := now == last
		last = now
		time.Sleep(100 * time.Millisecond)
		return settled && now > 0
	})
	before := r.rows.reads(task.ID)

	r.chat(task.ID, "to everyone")
	r.chat(other.ID, "to no one")
	ids := r.ids(task.ID)
	for _, s := range streams {
		s.expectLine(ids[len(ids)-1])
		s.expectStatus("Working", "Working")
	}
	time.Sleep(300 * time.Millisecond)
	if got := r.rows.reads(task.ID) - before; got != 1 {
		t.Fatalf("the task was read %d times for one notification and %d streams, want 1", got, len(streams))
	}
	if got := r.rows.reads(other.ID); got != 0 {
		t.Fatalf("a task with no stream was read %d times", got)
	}
}

func TestStreamTriggersNotifyOnCommitAndNotOnRollback(t *testing.T) {
	r := newRig(t, quiet())
	task := r.task("Triggers", domain.Working)
	ctx := context.Background()

	conn, err := pgx.ConnectConfig(ctx, r.owner.Config().ConnConfig.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, "LISTEN task_stream"); err != nil {
		t.Fatal(err)
	}
	notification := func(d time.Duration) (map[string]string, bool) {
		wctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		note, err := conn.WaitForNotification(wctx)
		if err != nil {
			return nil, false
		}
		var payload map[string]string
		if err := json.Unmarshal([]byte(note.Payload), &payload); err != nil {
			t.Fatal(err)
		}
		return payload, true
	}
	insertChat := func(commit bool) string {
		tx, err := r.owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var id string
		err = tx.QueryRow(ctx, `
			INSERT INTO tasks.chat_events (task_id, seq, body)
			VALUES ($1::uuid, (SELECT COALESCE(MAX(seq), 0) + 1 FROM tasks.chat_events WHERE task_id = $1::uuid), 'x')
			RETURNING id::text`, task.ID).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		if !commit {
			if err := tx.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			return id
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return id
	}

	// Drain what opening the task sent before the listener existed: nothing.
	insertChat(false)
	if payload, ok := notification(500 * time.Millisecond); ok {
		t.Fatalf("a rolled back event sent %v", payload)
	}
	id := insertChat(true)
	payload, ok := notification(wait)
	if !ok || payload["task"] != task.ID || payload["id"] != id || len(payload) != 2 {
		t.Fatalf("a chat event sent %v, want task %s id %s only", payload, task.ID, id)
	}

	r.step(task.ID, domain.Input{Kind: domain.Advance, Turn: domain.Sentinel})
	var eventID string
	if err := r.owner.QueryRow(ctx, `SELECT id::text FROM tasks.task_events WHERE task_id = $1::uuid ORDER BY seq DESC LIMIT 1`, task.ID).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	payload, ok = notification(wait)
	if !ok || payload["task"] != task.ID || payload["id"] != eventID {
		t.Fatalf("a task event sent %v, want task %s id %s", payload, task.ID, eventID)
	}
}

func TestStreamMigrationGoesDownAndUpAgain(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := migrate.Up(ctx, owner, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, tasks.Schema, tasks.Migrations()); err != nil {
		t.Fatal(err)
	}
	triggers := func() int {
		var n int
		if err := owner.QueryRow(ctx, `SELECT count(*) FROM pg_trigger WHERE tgname IN ('task_events_stream_notify', 'chat_events_stream_notify')`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got := triggers(); got != 2 {
		t.Fatalf("triggers after up = %d", got)
	}
	if err := migrate.Down(ctx, owner, tasks.Schema, tasks.Migrations()); err != nil {
		t.Fatal(err)
	}
	if got := triggers(); got != 0 {
		t.Fatalf("triggers after down = %d", got)
	}
	if err := migrate.Up(ctx, owner, tasks.Schema, tasks.Migrations()); err != nil {
		t.Fatal(err)
	}
	if got := triggers(); got != 2 {
		t.Fatalf("triggers after up again = %d", got)
	}
}

func TestStreamMetricsGauge(t *testing.T) {
	r := newRig(t, quiet())
	task := r.task("metrics", domain.Working)
	s := r.open("owner", "/api/tasks/"+task.ID+"/stream", nil)
	s.expectHello()

	scrape := func() string {
		rec := httptest.NewRecorder()
		r.metrics.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		return rec.Body.String()
	}

	if body := scrape(); !strings.Contains(body, `upmo_sse_streams_active{kind="tasks"} 1`) {
		t.Fatalf("expected active tasks stream = 1:\n%s", body)
	}
	s.close()

	eventually(t, "active tasks stream returns to 0", func() bool {
		return strings.Contains(scrape(), `upmo_sse_streams_active{kind="tasks"} 0`)
	})
}
