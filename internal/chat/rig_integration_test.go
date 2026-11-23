//go:build integration

package chat_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/upmo-ai/upmo/internal/agents"
	"github.com/upmo-ai/upmo/internal/chat"
	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/chat/domain"
	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/memory"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/jobs"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/tasks"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

const (
	ownerUser  = "018f3b2c-7a10-7c4a-8e11-111111111111"
	otherUser  = "018f3b2c-7a10-7c4a-8e11-222222222222"
	viewerUser = "018f3b2c-7a10-7c4a-8e11-333333333333"
	wait       = 5 * time.Second
)

var (
	ownerAccess = chat.Access{UserID: ownerUser, Allow: "chat:create,chat:view,chat:edit,agent:create,agent:view,agent:edit,agent:delete,agent:share"}
	otherAccess = chat.Access{UserID: otherUser, Allow: "chat:create,chat:view,chat:edit,agent:create,agent:view,agent:edit,agent:delete,agent:share"}
)

// rig is the chat store, the real reply worker and a stub gateway over a real
// database. Jobs are enqueued through the real River client, which is never
// started: a test runs the worker itself, so it decides when and how often.
type rig struct {
	t      *testing.T
	owner  *pgxpool.Pool
	pool   *pgxpool.Pool
	work   *jobs.Client
	store  chat.Store
	agents agents.Store
	stub   *stubGateway
	worker river.Worker[app.ReplyJobArgs]
	deps   app.ReplyDeps

	agentLookup *agentLookup
	mu          sync.Mutex
	people      map[string]chat.Access
	lookupErr   error
}

// setAccess changes what a person may do, as a role or a share changing would.
func (r *rig) setAccess(user string, access chat.Access) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.people[user] = access
}

// dropAccess takes a person's access away altogether.
func (r *rig) dropAccess(user string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.people, user)
}

// failLookups makes the lookup of a person's access fail, or stop failing.
func (r *rig) failLookups(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lookupErr = err
}

func newRig(t *testing.T, cfg app.ReplyConfig) *rig {
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
	if err := migrate.Up(ctx, owner, chat.Schema, chat.Migrations()); err != nil {
		t.Fatal(err)
	}
	// The agents migration copies from the gateway, memory and chat schemas.
	if err := migrate.Up(ctx, owner, memory.Schema, memory.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, gateway.Schema, gateway.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, agents.Schema, agents.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := jobs.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	pool := pgtest.App(t, owner)
	stub := newStubGateway(t)
	turns, err := chat.OpenTurns(stub.URL, serviceKey('k'))
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{
		t: t, owner: owner, pool: pool, stub: stub,
		people: map[string]chat.Access{ownerUser: ownerAccess, otherUser: otherAccess},
	}
	deps := app.ReplyDeps{
		Turns: turns,
		Access: func(_ context.Context, userID string) (chat.Access, bool, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.lookupErr != nil {
				return chat.Access{}, false, r.lookupErr
			}
			access, ok := r.people[userID]
			return access, ok, nil
		},
		Config: cfg,
	}
	// River only takes a job whose kind has a worker. The scout and the
	// extract that Say also queues get ones that do nothing, since the River
	// client is never started here.
	workers := river.NewWorkers()
	river.AddWorker(workers, river.WorkFunc(func(context.Context, *river.Job[tasks.ScoutJobArgs]) error { return nil }))
	river.AddWorker(workers, river.WorkFunc(func(context.Context, *river.Job[memory.ExtractJobArgs]) error { return nil }))
	r.work, err = jobs.Open(pool, jobs.WithWorkers(workers))
	if err != nil {
		t.Fatal(err)
	}
	r.store = chat.NewStore(pool, r.work)
	r.store.ReplyRoute = "chat"
	r.agents = agents.NewStore(pool)
	deps.Rows = r.store
	al := &agentLookup{store: r.agents}
	deps.Agents = al
	deps.Lessons = lessonLookup{store: r.agents}
	r.agentLookup = al
	if err := chat.AddReplyWorker(workers, deps); err != nil {
		t.Fatal(err)
	}
	r.deps = deps
	r.worker, err = app.NewReplyWorker(deps)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// agentLookup is how production reads an agent for a reply: through the agents
// context, as the person who wrote the line.
type agentLookup struct {
	store agents.Store
	block bool
}

func (l *agentLookup) Agent(ctx context.Context, access chat.Access, id string) (chat.Agent, error) {
	if l.block {
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
		return chat.Agent{}, errors.New("agents: blocked")
	}
	a, inline, err := l.store.AgentForUse(ctx, agentsAccess(access), id)
	if errors.Is(err, agents.ErrAgentNotFound) {
		return chat.Agent{}, chat.ErrAgentNotFound
	}
	if err != nil {
		return chat.Agent{}, err
	}
	cards, err := agents.Index(ctx, l.store, agentsAccess(access), id)
	if err != nil {
		return chat.Agent{}, err
	}
	lessonCards := make([]chat.LessonCard, 0, len(cards))
	for _, c := range cards {
		lessonCards = append(lessonCards, chat.LessonCard{ID: c.ID, Title: c.Title, Cue: c.Cue})
	}
	return chat.Agent{
		ID:            a.ID,
		Name:          a.Name,
		Description:   a.Description,
		PromptOverlay: agents.AppendInlineLessons(a.Instructions, inline),
		Route:         a.DefaultRoute,
		ModelProvider: a.DefaultModelProvider,
		ModelID:       a.DefaultModelID,
		Enabled:       a.Enabled,
		Cards:         lessonCards,
	}, nil
}

// lessonLookup opens a house rule through the agents facade, like brain.
type lessonLookup struct{ store agents.Store }

func (l lessonLookup) OpenLesson(ctx context.Context, access chat.Access, agentID, lessonID string) (chat.Lesson, error) {
	got, err := agents.Open(ctx, l.store, agentsAccess(access), agentID, lessonID)
	if err != nil {
		return chat.Lesson{}, err
	}
	return chat.Lesson{ID: got.ID, Title: got.Title, Cue: got.Cue, Body: got.Body, AlwaysInline: got.AlwaysInline}, nil
}

func agentsAccess(access chat.Access) agents.Access {
	teams := access.Teams
	if teams == "" {
		teams = "{}"
	}
	return agents.Access{UserID: access.UserID, Allow: access.Allow, Teams: teams}
}

// open starts a chat for the owner and returns its id.
func (r *rig) open() string {
	r.t.Helper()
	summary, err := app.Open(context.Background(), r.store, ownerAccess)
	if err != nil {
		r.t.Fatal(err)
	}
	return summary.ID
}

func (r *rig) say(chatID, body, route string) {
	r.t.Helper()
	if err := app.Say(context.Background(), r.store, ownerAccess, chatID, body, app.Picks{Route: route}); err != nil {
		r.t.Fatal(err)
	}
}

// job is a reply job as River stores it.
type job struct {
	ID   int64
	Args app.ReplyJobArgs
}

// jobs returns the reply jobs enqueued so far, oldest first.
func (r *rig) jobs() []job {
	r.t.Helper()
	rows, err := r.owner.Query(context.Background(),
		`SELECT id, args FROM platform.river_job WHERE kind = 'chat_reply' ORDER BY id`)
	if err != nil {
		r.t.Fatal(err)
	}
	defer rows.Close()
	var out []job
	for rows.Next() {
		var j job
		var raw []byte
		if err := rows.Scan(&j.ID, &raw); err != nil {
			r.t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &j.Args); err != nil {
			r.t.Fatal(err)
		}
		out = append(out, j)
	}
	return out
}

// lastJob is the newest reply job.
func (r *rig) lastJob() job {
	r.t.Helper()
	all := r.jobs()
	if len(all) == 0 {
		r.t.Fatal("no reply job was enqueued")
	}
	return all[len(all)-1]
}

// run works a job as River would on its attempt-th try.
func (r *rig) run(j job, attempt int) error {
	return r.runCtx(context.Background(), j, attempt)
}

func (r *rig) runCtx(ctx context.Context, j job, attempt int) error {
	return r.worker.Work(ctx, &river.Job[app.ReplyJobArgs]{
		JobRow: &rivertype.JobRow{ID: j.ID, Attempt: attempt, MaxAttempts: 2},
		Args:   j.Args,
	})
}

// completed marks a job as River would once it ran, which ends its hold on
// the uniqueness of its line.
func (r *rig) completed(j job) {
	r.t.Helper()
	if _, err := r.owner.Exec(context.Background(),
		`UPDATE platform.river_job SET state = 'completed', finalized_at = now() WHERE id = $1`, j.ID); err != nil {
		r.t.Fatal(err)
	}
}

type lineRow struct {
	ID       string
	Seq      int
	Role     string
	Body     string
	Status   string
	Model    *string
	Tokens   int
	Cost     *int64
	ReplyTo  *string
	Attempts []domain.Attempt
}

// lines returns every stored line of a chat, failed and replaced ones too.
func (r *rig) lines(chatID string) []lineRow {
	r.t.Helper()
	rows, err := r.owner.Query(context.Background(), `
		SELECT id::text, seq, role, body, status, model, tokens, cost_micro_usd, reply_to::text, attempts
		FROM chat.lines WHERE chat_id = $1::uuid ORDER BY seq`, chatID)
	if err != nil {
		r.t.Fatal(err)
	}
	defer rows.Close()
	var out []lineRow
	for rows.Next() {
		var l lineRow
		var attempts []byte
		if err := rows.Scan(&l.ID, &l.Seq, &l.Role, &l.Body, &l.Status, &l.Model, &l.Tokens, &l.Cost, &l.ReplyTo, &attempts); err != nil {
			r.t.Fatal(err)
		}
		if attempts != nil {
			if err := json.Unmarshal(attempts, &l.Attempts); err != nil {
				r.t.Fatal(err)
			}
		}
		out = append(out, l)
	}
	return out
}

type deltaRow struct {
	ID     string
	LineID string
	Seq    int
	Kind   string
	Body   string
}

// deltas returns the delta rows of a chat in write order.
func (r *rig) deltas(chatID string) []deltaRow {
	r.t.Helper()
	rows, err := r.owner.Query(context.Background(), `
		SELECT id::text, line_id::text, seq, kind, body
		FROM chat.deltas WHERE chat_id = $1::uuid ORDER BY id`, chatID)
	if err != nil {
		r.t.Fatal(err)
	}
	defer rows.Close()
	var out []deltaRow
	for rows.Next() {
		var d deltaRow
		if err := rows.Scan(&d.ID, &d.LineID, &d.Seq, &d.Kind, &d.Body); err != nil {
			r.t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
}

// settle fails the running run of a chat as the owner, as a process death
// would leave it, so the next Say or Retry can reserve a fresh one.
func (r *rig) settle(chatID string) {
	r.t.Helper()
	if _, err := r.owner.Exec(context.Background(), `
		UPDATE chat.runs SET state = 'failed', claim = NULL, history = NULL, finished_at = now()
		WHERE chat_id = $1::uuid AND state = 'running'`, chatID); err != nil {
		r.t.Fatal(err)
	}
}

// putDelta writes one delta as the owner, past the claim fence, like a crash
// that already wrote.
func (r *rig) putDelta(chatID, lineID string, seq int, kind, body string) {
	r.t.Helper()
	if _, err := r.owner.Exec(context.Background(), `
		INSERT INTO chat.deltas (chat_id, line_id, seq, kind, body)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5)`, chatID, lineID, seq, kind, body); err != nil {
		r.t.Fatal(err)
	}
}

// runRow is the run row a test reads by reply id.
type runRow struct {
	State      string
	Round      int
	ClaimRound int
	Tokens     int
	Cost       *int64
	CostKnown  bool
	HistoryNil bool
}

func (r *rig) runRow(replyID string) runRow {
	r.t.Helper()
	var row runRow
	if err := r.owner.QueryRow(context.Background(), `
		SELECT state, round, claim_round, tokens, cost_micro_usd, cost_known, history IS NULL
		FROM chat.runs WHERE id = $1::uuid`, replyID).Scan(
		&row.State, &row.Round, &row.ClaimRound, &row.Tokens, &row.Cost, &row.CostKnown, &row.HistoryNil); err != nil {
		r.t.Fatal(err)
	}
	return row
}

// runAsync works a job in the background and returns the channel of its result.
func (r *rig) runAsync(j job, attempt int) <-chan error {
	out := make(chan error, 1)
	go func() { out <- r.run(j, attempt) }()
	return out
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

func result(t *testing.T, c <-chan error) error {
	t.Helper()
	select {
	case err := <-c:
		return err
	case <-time.After(wait):
		t.Fatal("the reply job did not return")
		return nil
	}
}

// stubGateway is the gateway's chat stream as brain sees it: signed requests
// in, NDJSON frames out. Each request runs script with the number of the call.
type stubGateway struct {
	*httptest.Server
	mu     sync.Mutex
	calls  []stubCall
	script func(call int, s *frames)
}

type stubCall struct {
	Route           string `json:"route"`
	CredentialRef   string `json:"credential_ref"`
	UserID          string `json:"user_id"`
	MaxOutputTokens int    `json:"max_output_tokens"`
	CoreTools       bool   `json:"core_tools"`
	System          string `json:"system"`
	RunID           string `json:"run_id"`
	TeamID          string `json:"team_id"`
	Tools           []struct {
		Name string `json:"name"`
	} `json:"tools"`
	Pin *struct {
		AccountID string `json:"account_id"`
		Provider  string `json:"provider"`
		Model     string `json:"model"`
	} `json:"pin"`
	Messages []struct {
		Role      string `json:"role"`
		Text      string `json:"text"`
		ToolName  string `json:"tool_name"`
		ToolID    string `json:"tool_id"`
		ToolCalls []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"tool_calls"`
	} `json:"messages"`
}

func newStubGateway(t *testing.T) *stubGateway {
	t.Helper()
	g := &stubGateway{}
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var call stubCall
		if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		g.mu.Lock()
		g.calls = append(g.calls, call)
		n := len(g.calls)
		script := g.script
		g.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-ndjson")
		if script != nil {
			script(n, &frames{w: w, f: w.(http.Flusher), ctx: r.Context()})
		}
	})
	master, err := svcsig.ParseKey(serviceKey('k'))
	if err != nil {
		t.Fatal(err)
	}
	derived, err := svcsig.Derive(master, svcsig.AudienceGateway)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := svcsig.Handler([][]byte{derived}, svcsig.AudienceGateway, inner)
	if err != nil {
		t.Fatal(err)
	}
	g.Server = httptest.NewServer(signed)
	t.Cleanup(g.Server.Close)
	return g
}

func (g *stubGateway) reply(script func(call int, s *frames)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.script = script
}

func (g *stubGateway) seen() []stubCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]stubCall(nil), g.calls...)
}

// frames writes the frames of a chat stream, each flushed as written.
type frames struct {
	w   http.ResponseWriter
	f   http.Flusher
	ctx context.Context
}

func (s *frames) line(v any) {
	raw, _ := json.Marshal(v)
	_, _ = s.w.Write(append(raw, '\n'))
	s.f.Flush()
}

func (s *frames) delta(text string) { s.line(map[string]any{"delta": text}) }

func (s *frames) attempt(failed, class, next string) {
	s.line(map[string]any{"attempt": map[string]string{"failed_model": failed, "failure_class": class, "next_model": next}})
}

func (s *frames) done(model string, in, out int, cost *int64) {
	s.line(map[string]any{"done": map[string]any{"model": model, "input_tokens": in, "output_tokens": out, "cost_micro_usd": cost}})
}

// stubTool is one tool call the model asks for in a done frame.
type stubTool struct {
	ID   string
	Name string
	Args string
}

// doneCalls writes a done frame that asks for tools, then continues to the
// next round.
func (s *frames) doneCalls(model string, in, out int, cost *int64, calls ...stubTool) {
	callsJSON := make([]map[string]any, 0, len(calls))
	for _, c := range calls {
		callsJSON = append(callsJSON, map[string]any{"id": c.ID, "name": c.Name, "arguments": json.RawMessage(c.Args)})
	}
	s.line(map[string]any{"done": map[string]any{"model": model, "input_tokens": in, "output_tokens": out, "cost_micro_usd": cost, "tool_calls": callsJSON}})
}

func (s *frames) fail(class, model string) {
	s.line(map[string]any{"error": map[string]string{"failure_class": class, "model": model, "message": "logs only"}})
}

// hold waits until release is closed, or the call is over.
func (s *frames) hold(release <-chan struct{}) {
	select {
	case <-release:
	case <-s.ctx.Done():
	}
}

func i64(v int64) *int64 { return &v }

func serviceKey(b byte) string {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = b
	}
	return svcsig.KeyPrefix + base64.RawStdEncoding.EncodeToString(raw)
}
