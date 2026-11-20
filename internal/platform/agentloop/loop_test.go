package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/upmo-ai/upmo/internal/kernel"
	"github.com/upmo-ai/upmo/internal/platform/netguard"
	"github.com/upmo-ai/upmo/internal/platform/secretchain"
)

func int64Ptr(v int64) *int64 { return &v }

type fakeModel struct {
	fn func(ctx context.Context, req PromptRequest) (PromptResponse, error)
}

func (f fakeModel) Complete(ctx context.Context, req PromptRequest) (PromptResponse, error) {
	return f.fn(ctx, req)
}

type fakeSandbox struct {
	ensureCalls int
	execCalls   int
	readCalls   int
	writeCalls  int
}

func (s *fakeSandbox) EnsureSeat(_ context.Context, run *RunState) (string, error) {
	s.ensureCalls++
	return "seat-" + run.RunID, nil
}

func (s *fakeSandbox) Exec(_ context.Context, seatID string, cmd SandboxCommand) (SandboxOutput, error) {
	s.execCalls++
	return SandboxOutput{Stdout: "ran:" + strings.Join(cmd.Args, " ") + "@" + seatID, ExitCode: 0}, nil
}

func (s *fakeSandbox) ReadFile(_ context.Context, _ string, path string) ([]byte, error) {
	s.readCalls++
	return []byte("contents:" + path), nil
}

func (s *fakeSandbox) WriteFile(_ context.Context, _ string, _ string, _ []byte) error {
	s.writeCalls++
	return nil
}

type memOutputs struct {
	items map[string]struct {
		body      string
		untrusted bool
	}
	seq int
}

type failingOutputs struct{ memOutputs }

func (failingOutputs) SaveOutput(context.Context, *RunState, string, string, bool) (string, error) {
	return "", errors.New("save failed")
}

func (m *memOutputs) SaveOutput(_ context.Context, _ *RunState, title, body string, untrusted bool) (string, error) {
	if m.items == nil {
		m.items = make(map[string]struct {
			body      string
			untrusted bool
		})
	}
	m.seq++
	id := title + "-" + strconv.Itoa(m.seq)
	m.items[id] = struct {
		body      string
		untrusted bool
	}{body: body, untrusted: untrusted}
	return id, nil
}

func (m *memOutputs) RetrieveOutput(_ context.Context, _ *RunState, id string) (string, bool, error) {
	item, ok := m.items[id]
	if !ok {
		return "", false, errors.New("not found")
	}
	return item.body, item.untrusted, nil
}

type testActionLog struct {
	order   []string
	records map[string]ActionRecord
}

func newTestActionLog() *testActionLog {
	return &testActionLog{records: make(map[string]ActionRecord)}
}

func testActionKey(scopeID, callID string) string {
	return scopeID + ":" + callID
}

func (l *testActionLog) BeginDial(_ context.Context, rec ActionRecord) error {
	k := testActionKey(ActionScopeID(rec), rec.CallID)
	if prev, ok := l.records[k]; ok {
		if prev.State == ActionStateDialing || prev.State == ActionStateDone || prev.State == ActionStateSimulated {
			return ErrDuplicateAction
		}
	}
	rec.State = ActionStateDialing
	l.records[k] = rec
	l.order = append(l.order, "tx1:dialing:"+rec.CallID)
	return nil
}

func (l *testActionLog) FinishDial(_ context.Context, scopeID, callID string, state ActionState, responseDigest, errText string, completedAt time.Time) error {
	k := testActionKey(scopeID, callID)
	rec := l.records[k]
	rec.State = state
	rec.ResponseDigest = responseDigest
	rec.ErrorText = errText
	t := completedAt
	rec.CompletedAt = &t
	l.records[k] = rec
	l.order = append(l.order, "tx2:"+string(state)+":"+callID)
	return nil
}

func (l *testActionLog) RecordSimulated(_ context.Context, rec ActionRecord) error {
	k := testActionKey(ActionScopeID(rec), rec.CallID)
	rec.State = ActionStateSimulated
	rec.DryRun = true
	l.records[k] = rec
	l.order = append(l.order, "simulated:"+rec.CallID)
	return nil
}

type testTurnStore struct {
	order   *[]string
	commits []TurnCommit
}

func (s *testTurnStore) CommitTurnAndEnqueue(_ context.Context, commit TurnCommit) error {
	s.commits = append(s.commits, commit)
	if s.order != nil {
		*s.order = append(*s.order, "tx3:commit")
	}
	return nil
}

type testPolicy struct {
	verdict string
	reason  string
}

func (p testPolicy) Decide(_ context.Context, _, _ string, _ ToolOp, _ json.RawMessage) (PolicyDecision, error) {
	return PolicyDecision{Verdict: p.verdict, Resource: "acct-1", Reason: p.reason}, nil
}

type testAuditSink struct {
	events []TimelineEvent
}

func (a *testAuditSink) Record(_ context.Context, _ *RunState, ev TimelineEvent) error {
	a.events = append(a.events, ev)
	return nil
}

type fakeKnowledge struct {
	hits []KnowledgeHit
}

func (k fakeKnowledge) Search(_ context.Context, _ *RunState, _, _ string) ([]KnowledgeHit, error) {
	return k.hits, nil
}

type fakeMemory struct {
	text      string
	untrusted bool
	private   bool
}

func (m fakeMemory) SearchMemory(_ context.Context, _, _ string) (string, bool, bool, error) {
	return m.text, m.untrusted, m.private, nil
}

func TestPromptCachePrefixAndAppendOnlyTools(t *testing.T) {
	sys := AssembleSystemPrefix(
		"Always verify receipts.",
		"Bookkeeper",
		"Follow IFRS rules.",
		[]Lesson{
			{ID: "l1", Title: "VAT Rule", Cue: "eu vat", Body: "Check reverse charge.", AlwaysInline: true},
			{ID: "l2", Title: "Ignored", Cue: "non-inline", Body: "Should not inline.", AlwaysInline: false},
		},
		[]LessonCard{
			{ID: "l2", Title: "Payroll", Cue: "salary export"},
		},
	)
	for _, want := range []string{"Always verify receipts.", "# Agent: Bookkeeper", "Follow IFRS rules.", "## VAT Rule", "Check reverse charge.", "[Payroll -> salary export] (id: l2)"} {
		if !strings.Contains(sys, want) {
			t.Fatalf("system prefix missing %q:\n%s", want, sys)
		}
	}
	if strings.Contains(sys, "Should not inline.") {
		t.Fatalf("non-inline body leaked into system prefix:\n%s", sys)
	}

	catalog := NewToolCatalog(
		[]ToolDef{{Name: "zeta", Category: CategoryDirect, Op: OpRead}, {Name: "alpha", Category: CategoryDirect, Op: OpRead}},
		[]ToolDef{{Name: "send_invoice", Description: "send invoice over email", Category: CategoryConnector, Op: OpChange, External: true}},
	)
	run := &RunState{
		RunID:   "run-1",
		UserID:  "user-1",
		AgentID: "agent-1",
		Catalog: catalog,
	}
	req0 := BuildPromptRequest(run, sys, []Message{{Role: RoleUser, Text: "hi"}})
	if len(req0.Cache) != 2 || req0.Cache[0] != BreakTools || req0.Cache[1] != BreakSystem {
		t.Fatalf("cache breakpoints = %v", req0.Cache)
	}
	if req0.Tools[0].Name != "retrieve_output" || req0.Tools[1].Name != "find_tool" {
		t.Fatalf("core tools not first: %+v", req0.Tools)
	}
	if req0.Tools[2].Name != "alpha" || req0.Tools[3].Name != "zeta" {
		t.Fatalf("initial tools not sorted stably: %+v", req0.Tools)
	}

	// find_tool appends dynamically at the end without mutating earlier prefix.
	found := catalog.Find("invoice")
	if len(found) != 1 || found[0].Name != "send_invoice" {
		t.Fatalf("find_tool = %+v", found)
	}
	req1 := BuildPromptRequest(run, sys, []Message{{Role: RoleUser, Text: "hi"}})
	if len(req1.Tools) != 5 || req1.Tools[4].Name != "send_invoice" {
		t.Fatalf("appended tools = %+v", req1.Tools)
	}
	for i := 0; i < 4; i++ {
		if req1.Tools[i].Name != req0.Tools[i].Name {
			t.Fatalf("prefix mutated at index %d: %s vs %s", i, req1.Tools[i].Name, req0.Tools[i].Name)
		}
	}
}

func TestLazySandboxProvisionedOnlyWhenSandboxToolCalled(t *testing.T) {
	sb := &fakeSandbox{}
	exec := DefaultExecutor{
		Sandbox:   sb,
		Knowledge: fakeKnowledge{hits: []KnowledgeHit{{SourceID: "s1", Body: "curated doc", Untrusted: false}}},
		Outputs:   &memOutputs{},
	}
	catalog := NewToolCatalog([]ToolDef{
		{Name: "search_knowledge", Category: CategoryDirect, Op: OpRead},
		{Name: "shell", Category: CategorySandbox, Op: OpRead},
		{Name: "read_file", Category: CategorySandbox, Op: OpRead},
	}, nil)

	// Turn 1: direct tools only -> sandbox MUST NOT be provisioned.
	run := &RunState{RunID: "r1", ObjectID: "o1", UserID: "u1", Catalog: catalog}
	eng := Engine{
		Model: fakeModel{fn: func(_ context.Context, _ PromptRequest) (PromptResponse, error) {
			return PromptResponse{
				Model:     "m1",
				ToolCalls: []ToolCall{{ID: "c1", Name: "search_knowledge", Arguments: json.RawMessage(`{"folder_id":"f1","query":"tax"}`)}},
			}, nil
		}},
		Executor: exec,
	}
	out, err := eng.RunTurn(context.Background(), run, []Message{{Role: RoleUser, Text: "check tax"}}, "sys")
	if err != nil {
		t.Fatal(err)
	}
	if sb.ensureCalls != 0 || run.SeatID != "" {
		t.Fatalf("direct turn provisioned sandbox: ensureCalls=%d seat=%q", sb.ensureCalls, run.SeatID)
	}
	if out.Exposure.Tainted {
		t.Fatal("curated knowledge tainted the run")
	}

	// Turn 2: two sandbox tools -> EnsureSeat called once and reused.
	eng.Model = fakeModel{fn: func(_ context.Context, _ PromptRequest) (PromptResponse, error) {
		return PromptResponse{
			Model: "m1",
			ToolCalls: []ToolCall{
				{ID: "c2", Name: "shell", Arguments: json.RawMessage(`{"args":["go","test"]}`)},
				{ID: "c3", Name: "read_file", Arguments: json.RawMessage(`{"path":"go.mod"}`)},
			},
		}, nil
	}}
	_, err = eng.RunTurn(context.Background(), run, []Message{{Role: RoleUser, Text: "run tests"}}, "sys")
	if err != nil {
		t.Fatal(err)
	}
	if sb.ensureCalls != 1 || sb.execCalls != 1 || sb.readCalls != 1 || run.SeatID != "seat-r1" {
		t.Fatalf("sandbox calls ensure=%d exec=%d read=%d seat=%q", sb.ensureCalls, sb.execCalls, sb.readCalls, run.SeatID)
	}
}

func TestSingleTeamRLSNarrowingIntersection(t *testing.T) {
	// Member of t1 and t2, running on object owned by t1 -> narrows to [t1], clears other shares.
	narrowed := NarrowRLS(AccessScope{
		UserID:           "u1",
		Allow:            "task:view,task:edit",
		UserTeams:        []string{"t1", "t2"},
		ObjectID:         "obj-1",
		ObjectVisibility: VisibilityTeam,
		VisibilityTeamID: "t1",
		SharedView:       "{obj-1,obj-other}",
		SharedEdit:       "{obj-other}",
	})
	if !slices.Equal(narrowed.Teams, []string{"t1"}) || narrowed.TeamsPGArray != "{t1}" {
		t.Fatalf("teams = %v %q", narrowed.Teams, narrowed.TeamsPGArray)
	}
	if narrowed.SharedView != "{obj-1}" || narrowed.SharedEdit != "" {
		t.Fatalf("shares not narrowed: view=%q edit=%q", narrowed.SharedView, narrowed.SharedEdit)
	}
	if narrowed.GatewayTeamID != "t1" {
		t.Fatalf("GatewayTeamID = %q", narrowed.GatewayTeamID)
	}

	// Shared editor NOT in t1 (only in t2) -> MUST NOT gain t1 membership!
	nonMember := NarrowRLS(AccessScope{
		UserID:           "u2",
		Allow:            "task:view,task:edit",
		UserTeams:        []string{"t2"},
		ObjectID:         "obj-1",
		ObjectVisibility: VisibilityTeam,
		VisibilityTeamID: "t1",
		SharedEdit:       "{obj-1,obj-other}",
	})
	if len(nonMember.Teams) != 0 || nonMember.TeamsPGArray != "{}" {
		t.Fatalf("non-member gained team membership: %v %q", nonMember.Teams, nonMember.TeamsPGArray)
	}
	if nonMember.SharedEdit != "{obj-1}" {
		t.Fatalf("shared_edit = %q", nonMember.SharedEdit)
	}
}

func TestNarrowRLSClearsOtherSharesForEveryVisibility(t *testing.T) {
	cases := []struct {
		name      string
		scope     AccessScope
		wantTeams []string
		wantView  string
		wantEdit  string
	}{
		{
			name: "private keeps only active object",
			scope: AccessScope{
				UserID:           "u1",
				Allow:            "task:view,task:edit",
				UserTeams:        []string{"t1", "t2"},
				ObjectID:         "obj-1",
				ObjectVisibility: VisibilityPrivate,
				SharedView:       "{obj-1,obj-other}",
				SharedEdit:       "{obj-1,obj-other}",
			},
			wantTeams: []string{"t1", "t2"},
			wantView:  "{obj-1}",
			wantEdit:  "{obj-1}",
		},
		{
			name: "everyone keeps only active object",
			scope: AccessScope{
				UserID:           "u1",
				Allow:            "task:view,task:edit",
				UserTeams:        []string{"t1", "t2"},
				ObjectID:         "obj-1",
				ObjectVisibility: VisibilityEveryone,
				SharedView:       "{obj-1,obj-other}",
				SharedEdit:       "{obj-other}",
			},
			wantTeams: []string{"t1", "t2"},
			wantView:  "{obj-1}",
			wantEdit:  "",
		},
		{
			name: "team narrows teams and shares",
			scope: AccessScope{
				UserID:           "u1",
				Allow:            "task:view,task:edit",
				UserTeams:        []string{"t1", "t2"},
				ObjectID:         "obj-1",
				ObjectVisibility: VisibilityTeam,
				VisibilityTeamID: "t1",
				SharedView:       "{obj-1,obj-other}",
				SharedEdit:       "{obj-other}",
			},
			wantTeams: []string{"t1"},
			wantView:  "{obj-1}",
			wantEdit:  "",
		},
		{
			name: "private without active object clears everything",
			scope: AccessScope{
				UserID:           "u1",
				Allow:            "task:view,task:edit",
				UserTeams:        []string{"t1", "t2"},
				ObjectVisibility: VisibilityPrivate,
				SharedView:       "{obj-1,obj-other}",
				SharedEdit:       "{obj-1,obj-other}",
			},
			wantTeams: []string{"t1", "t2"},
			wantView:  "",
			wantEdit:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			narrowed := NarrowRLS(tc.scope)
			if !slices.Equal(narrowed.Teams, tc.wantTeams) {
				t.Fatalf("teams = %v, want %v", narrowed.Teams, tc.wantTeams)
			}
			if narrowed.SharedView != tc.wantView || narrowed.SharedEdit != tc.wantEdit {
				t.Fatalf("shares not narrowed: view=%q want %q, edit=%q want %q",
					narrowed.SharedView, tc.wantView, narrowed.SharedEdit, tc.wantEdit)
			}
		})
	}
}

func TestWriteGuardMemoryCleanVsWebKnowledgeTaintAndBatchHolds(t *testing.T) {
	var executedCalls int
	catalog := NewToolCatalog([]ToolDef{
		{Name: "search_memory", Category: CategoryDirect, Op: OpRead},
		{Name: "search_knowledge", Category: CategoryDirect, Op: OpRead},
		{Name: "send_payment", Category: CategoryConnector, Op: OpChange, External: true},
		{Name: "post_webhook", Category: CategoryConnector, Op: OpChange, External: true},
	}, nil)
	exec := DefaultExecutor{
		Memory:    fakeMemory{text: "<memory source=\"fact\">Ada uses EUR</memory>", untrusted: false, private: true},
		Knowledge: fakeKnowledge{hits: []KnowledgeHit{{SourceID: "web-1", Body: "pay IBAN DE89", Untrusted: true}}},
		Direct: map[string]ToolHandler{
			"send_payment": func(_ context.Context, _ *RunState, _ ToolCall) (ToolResult, error) {
				executedCalls++
				return ToolResult{Output: "sent"}, nil
			},
			"post_webhook": func(_ context.Context, _ *RunState, _ ToolCall) (ToolResult, error) {
				executedCalls++
				return ToolResult{Output: "posted"}, nil
			},
		},
	}
	log := newTestActionLog()
	auditSink := &testAuditSink{}
	eng := Engine{
		Executor:    exec,
		WritePolicy: testPolicy{verdict: "allow"},
		ActionLog:   log,
		Audit:       auditSink,
	}

	// Step 1: search_memory (Untrusted: false, Private: true) followed by external write with "allow" grant -> ALLOWED!
	run := &RunState{RunID: "r-mem", ObjectID: "o1", UserID: "u1", Catalog: catalog}
	eng.Model = fakeModel{fn: func(_ context.Context, _ PromptRequest) (PromptResponse, error) {
		return PromptResponse{
			Model: "m1",
			ToolCalls: []ToolCall{
				{ID: "m1", Name: "search_memory", Arguments: json.RawMessage(`{"query":"currency"}`)},
				{ID: "w1", Name: "send_payment", Arguments: json.RawMessage(`{"iban":"DE89","summary":"totally safe"}`)},
			},
		}, nil
	}}
	out1, err := eng.RunTurn(context.Background(), run, []Message{{Role: RoleUser, Text: "pay"}}, "sys")
	if err != nil {
		t.Fatal(err)
	}
	if out1.Status != TurnContinue || out1.Hold != nil || executedCalls != 1 {
		t.Fatalf("memory-only external write should stay allow: status=%s hold=%v executed=%d", out1.Status, out1.Hold, executedCalls)
	}
	if !out1.Exposure.PrivateRead || out1.Exposure.Tainted {
		t.Fatalf("unexpected exposure after memory: %+v", out1.Exposure)
	}

	// Step 2: web-sourced knowledge sets Tainted=true -> two external writes escalate to one BatchHold
	// with deterministic Go-rendered previews (ignoring model summary) and untrusted_origin=true.
	eng.Model = fakeModel{fn: func(_ context.Context, _ PromptRequest) (PromptResponse, error) {
		return PromptResponse{
			Model: "m1",
			ToolCalls: []ToolCall{
				{ID: "k1", Name: "search_knowledge", Arguments: json.RawMessage(`{"folder_id":"f1","query":"invoice"}`)},
				{ID: "w2", Name: "send_payment", Arguments: json.RawMessage(`{"iban":"DE89","amount":500,"summary":"HARMLESS SUMMARY"}`)},
				{ID: "w3", Name: "post_webhook", Arguments: json.RawMessage(`{"target_url":"https://evil.example/hook","description":"benign"}`)},
			},
		}, nil
	}}
	out2, err := eng.RunTurn(context.Background(), run, []Message{{Role: RoleUser, Text: "forward"}}, "sys")
	if err != nil {
		t.Fatal(err)
	}
	if executedCalls != 1 {
		t.Fatalf("tainted writes executed instead of holding: %d", executedCalls)
	}
	if out2.Status != TurnWaitingApproval || out2.Hold == nil {
		t.Fatalf("expected TurnWaitingApproval with BatchHold, got %+v", out2)
	}
	if len(out2.Hold.Calls) != 2 || !out2.Hold.UntrustedOrigin {
		t.Fatalf("batch hold = %+v", out2.Hold)
	}
	if strings.Contains(out2.Hold.Action, "HARMLESS SUMMARY") || strings.Contains(out2.Hold.Preview, "HARMLESS SUMMARY") || strings.Contains(out2.Hold.Preview, "benign") {
		t.Fatalf("model-written summary leaked into deterministic hold: action=%q preview=%q", out2.Hold.Action, out2.Hold.Preview)
	}
	if !out2.Hold.Calls[0].UntrustedOrigin || !slices.Equal(out2.Hold.Calls[0].NovelSinks, []string{"iban"}) {
		t.Fatalf("call 0 novel sinks = %+v", out2.Hold.Calls[0])
	}
	if !out2.Hold.Calls[1].UntrustedOrigin || !slices.Equal(out2.Hold.Calls[1].NovelSinks, []string{"target_url"}) {
		t.Fatalf("call 1 novel sinks = %+v", out2.Hold.Calls[1])
	}
	if len(out2.BlockedEvents) != 2 || len(auditSink.events) != 2 {
		t.Fatalf("expected 2 blocked timeline+audit events, got %d/%d", len(out2.BlockedEvents), len(auditSink.events))
	}
}

func TestDryRunSimulatesWithoutExecuting(t *testing.T) {
	var executed int
	catalog := NewToolCatalog([]ToolDef{
		{Name: "send_payment", Category: CategoryConnector, Op: OpChange, External: true},
	}, nil)
	log := newTestActionLog()
	eng := Engine{
		Model: fakeModel{fn: func(_ context.Context, _ PromptRequest) (PromptResponse, error) {
			return PromptResponse{
				Model:     "m1",
				ToolCalls: []ToolCall{{ID: "dry-1", Name: "send_payment", Arguments: json.RawMessage(`{"recipient":"bob@example.com","amount":42}`)}},
			}, nil
		}},
		Executor: DefaultExecutor{
			Direct: map[string]ToolHandler{
				"send_payment": func(_ context.Context, _ *RunState, _ ToolCall) (ToolResult, error) {
					executed++
					return ToolResult{Output: "real-mutation"}, nil
				},
			},
		},
		ActionLog: log,
	}
	run := &RunState{
		RunID:    "r-dry",
		ObjectID: "o1",
		UserID:   "u1",
		DryRun:   true,
		Exposure: ExposureState{Tainted: true},
		Catalog:  catalog,
	}
	out, err := eng.RunTurn(context.Background(), run, []Message{{Role: RoleUser, Text: "dry run"}}, "sys")
	if err != nil {
		t.Fatal(err)
	}
	if executed != 0 {
		t.Fatalf("dry_run executed real mutation %d times", executed)
	}
	if len(out.SimulatedEvents) != 1 || !out.SimulatedEvents[0].DryRun || !out.SimulatedEvents[0].UntrustedOrigin {
		t.Fatalf("simulated events = %+v", out.SimulatedEvents)
	}
	if rec := log.records[testActionKey("r-dry", ScopedCallID(0, "dry-1"))]; rec.State != ActionStateSimulated || !rec.DryRun {
		t.Fatalf("action log record = %+v", rec)
	}
}

func TestThreeTransactionActionLogAndIdempotencyReplayGuard(t *testing.T) {
	var seenKeys []string
	var httpHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpHits.Add(1)
		seenKeys = append(seenKeys, r.Header.Get("Idempotency-Key"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	// Allow loopback in netguard solely for this httptest server.
	guard := &netguard.Guard{
		Allow: func(string) bool { return true },
		Lookup: func(_ context.Context, _, _ string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		},
	}
	httpClient := NewHTTPConnectorClient(guard, nil, 5*time.Second)

	log := newTestActionLog()
	turnStore := &testTurnStore{order: &log.order}
	catalog := NewToolCatalog([]ToolDef{
		{Name: "webhook_send", Category: CategoryConnector, Op: OpChange, External: true},
	}, nil)

	srvHost := strings.TrimPrefix(srv.URL, "http://")
	exec := DefaultExecutor{
		HTTP: httpClient,
	}
	exec.Direct = map[string]ToolHandler{
		"webhook_send": func(ctx context.Context, _ *RunState, call ToolCall) (ToolResult, error) {
			if call.IdempotencyKey == "" {
				t.Fatal("expected Engine.RunTurn to populate call.IdempotencyKey")
			}
			log.order = append(log.order, "http:"+call.ID)
			_, body, digest, err := exec.DoConnectorHTTP(ctx, call, http.MethodPost, srv.URL, srvHost, strings.NewReader(`{}`), "", nil)
			if err != nil {
				return ToolResult{IsError: true, Output: err.Error()}, err
			}
			return ToolResult{Output: string(body), ResponseDigest: digest}, nil
		},
	}

	eng := Engine{
		Model: fakeModel{fn: func(_ context.Context, _ PromptRequest) (PromptResponse, error) {
			return PromptResponse{
				Model:     "m1",
				ToolCalls: []ToolCall{{ID: "call-idem-1", Name: "webhook_send", Arguments: json.RawMessage(`{"to":"ops"}`)}},
			}, nil
		}},
		Executor:    exec,
		WritePolicy: testPolicy{verdict: "allow"},
		ActionLog:   log,
		TurnStore:   turnStore,
	}

	run := &RunState{RunID: "r-3tx", ObjectID: "o1", UserID: "u1", Catalog: catalog}
	out1, err := eng.RunTurn(context.Background(), run, []Message{{Role: RoleUser, Text: "send"}}, "sys")
	if err != nil {
		t.Fatal(err)
	}
	if len(out1.Appended) < 2 || out1.Appended[1].ToolID != "call-idem-1" {
		t.Fatalf("transcript ToolID should stay raw call.ID, got %+v", out1.Appended)
	}

	wantOrder := []string{"tx1:dialing:t0:call-idem-1", "http:call-idem-1", "tx2:done:t0:call-idem-1", "tx3:commit"}
	if !slices.Equal(log.order, wantOrder) {
		t.Fatalf("3-tx order = %v, want %v", log.order, wantOrder)
	}
	wantKey := DeriveIdempotencyKey("r-3tx", ScopedCallID(0, "call-idem-1"))
	if !slices.Equal(seenKeys, []string{wantKey}) || httpHits.Load() != 1 {
		t.Fatalf("http hits=%d keys=%v want=%q", httpHits.Load(), seenKeys, wantKey)
	}

	// Replay of Turn 1 (same Seq=0) after done -> refused!
	run.Seq = 0
	outReplay, err := eng.RunTurn(context.Background(), run, []Message{{Role: RoleUser, Text: "replay"}}, "sys")
	if err != nil {
		t.Fatal(err)
	}
	if httpHits.Load() != 1 || len(outReplay.BlockedEvents) != 1 || outReplay.BlockedEvents[0].Reason != "duplicate_call_id" {
		t.Fatalf("replay re-executed or missed duplicate block: hits=%d blocked=%+v", httpHits.Load(), outReplay.BlockedEvents)
	}

	// Simulate crash after tx1 (stuck in "dialing"): replay MUST refuse!
	log.records[testActionKey("", "call-crash-1")] = ActionRecord{CallID: "call-crash-1", State: ActionStateDialing}
	err = ExecuteAtMostOnce(context.Background(), log, ActionRecord{CallID: "call-crash-1"}, func(context.Context, string) (string, error) {
		httpHits.Add(1)
		return "", nil
	})
	if !errors.Is(err, ErrDuplicateAction) || httpHits.Load() != 1 {
		t.Fatalf("dialing crash replay: err=%v hits=%d", err, httpHits.Load())
	}
}

func TestMultiTurnSameCallIDSucceeds(t *testing.T) {
	if got := ScopedCallID(0, ""); got != "" {
		t.Fatalf("ScopedCallID(0, empty) = %q, want empty", got)
	}
	longID := strings.Repeat("x", 220)
	scopedLong := ScopedCallID(2, longID)
	if len(scopedLong) > 200 || !strings.HasPrefix(scopedLong, "t2:sha256:") {
		t.Fatalf("ScopedCallID long = %q (len=%d)", scopedLong, len(scopedLong))
	}

	var seenKeys []string
	var httpHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpHits.Add(1)
		seenKeys = append(seenKeys, r.Header.Get("Idempotency-Key"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	guard := &netguard.Guard{
		Allow: func(string) bool { return true },
		Lookup: func(_ context.Context, _, _ string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		},
	}
	exec := DefaultExecutor{HTTP: NewHTTPConnectorClient(guard, nil, 5*time.Second)}
	srvHost := strings.TrimPrefix(srv.URL, "http://")
	exec.Direct = map[string]ToolHandler{
		"webhook_send": func(ctx context.Context, _ *RunState, call ToolCall) (ToolResult, error) {
			_, body, digest, err := exec.DoConnectorHTTP(ctx, call, http.MethodPost, srv.URL, srvHost, strings.NewReader(`{}`), "", nil)
			if err != nil {
				return ToolResult{IsError: true, Output: err.Error()}, err
			}
			return ToolResult{Output: string(body), ResponseDigest: digest}, nil
		},
	}

	log := newTestActionLog()
	turnStore := &testTurnStore{order: &log.order}
	eng := Engine{
		Model: fakeModel{fn: func(_ context.Context, _ PromptRequest) (PromptResponse, error) {
			return PromptResponse{
				Model:     "m1",
				ToolCalls: []ToolCall{{ID: "call_0", Name: "webhook_send", Arguments: json.RawMessage(`{"to":"ops"}`)}},
			}, nil
		}},
		Executor:    exec,
		WritePolicy: testPolicy{verdict: "allow"},
		ActionLog:   log,
		TurnStore:   turnStore,
	}
	run := &RunState{
		RunID:    "r-multi",
		ObjectID: "o1",
		UserID:   "u1",
		Catalog:  NewToolCatalog([]ToolDef{{Name: "webhook_send", Category: CategoryConnector, Op: OpChange, External: true}}, nil),
	}

	// Turn 1 (Seq=0) with call_0.
	out1, err := eng.RunTurn(context.Background(), run, []Message{{Role: RoleUser, Text: "turn 1"}}, "sys")
	if err != nil {
		t.Fatal(err)
	}
	if run.Seq != 2 || len(out1.BlockedEvents) != 0 || out1.Appended[1].ToolID != "call_0" {
		t.Fatalf("turn 1 unexpected: seq=%d out=%+v", run.Seq, out1)
	}

	// Turn 2 (Seq=2) in the SAME run reusing provider call_0 succeeds with a distinct Idempotency-Key.
	out2, err := eng.RunTurn(context.Background(), run, []Message{{Role: RoleUser, Text: "turn 2"}}, "sys")
	if err != nil {
		t.Fatal(err)
	}
	if run.Seq != 4 || len(out2.BlockedEvents) != 0 || out2.Appended[1].ToolID != "call_0" {
		t.Fatalf("turn 2 unexpected: seq=%d out=%+v", run.Seq, out2)
	}
	wantKey1 := DeriveIdempotencyKey("r-multi", ScopedCallID(0, "call_0"))
	wantKey2 := DeriveIdempotencyKey("r-multi", ScopedCallID(2, "call_0"))
	if wantKey1 == wantKey2 {
		t.Fatalf("expected distinct idempotency keys, both %q", wantKey1)
	}
	if !slices.Equal(seenKeys, []string{wantKey1, wantKey2}) || httpHits.Load() != 2 {
		t.Fatalf("http hits=%d keys=%v want=[%q %q]", httpHits.Load(), seenKeys, wantKey1, wantKey2)
	}

	// Replaying Turn 1 (Seq=0, same call_0) is still blocked by ErrDuplicateAction.
	run.Seq = 0
	outReplay, err := eng.RunTurn(context.Background(), run, []Message{{Role: RoleUser, Text: "replay t1"}}, "sys")
	if err != nil {
		t.Fatal(err)
	}
	if httpHits.Load() != 2 || len(outReplay.BlockedEvents) != 1 || outReplay.BlockedEvents[0].Reason != "duplicate_call_id" {
		t.Fatalf("turn 1 replay not blocked: hits=%d blocked=%+v", httpHits.Load(), outReplay.BlockedEvents)
	}
}

func TestMicroUSDEscrowUnknownEstimateAndPauseOnCross(t *testing.T) {
	catalog := NewToolCatalog(nil, nil)
	eng := Engine{
		Model: fakeModel{fn: func(_ context.Context, _ PromptRequest) (PromptResponse, error) {
			return PromptResponse{
				Model: "m1",
				Text:  "done",
				Usage: kernel.TokenUsage{InputTokens: 1_000, OutputTokens: 500},
				// CostMicroUSD is nil -> must charge price-table estimate!
				Price: &kernel.TokenPrice{
					InputMicroUSDPerMTok:  int64Ptr(10_000_000), // $10/MTok -> 10,000 micro-USD
					OutputMicroUSDPerMTok: int64Ptr(30_000_000), // $30/MTok -> 15,000 micro-USD
				},
			}, nil
		}},
		Executor: DefaultExecutor{},
	}
	run := &RunState{
		RunID:          "r-budget",
		ObjectID:       "o1",
		UserID:         "u1",
		Budgeted:       true,
		BudgetMicroUSD: 20_000, // $0.02
		SpentMicroUSD:  0,
		Catalog:        catalog,
	}
	out, err := eng.RunTurn(context.Background(), run, []Message{{Role: RoleUser, Text: "go"}}, "sys")
	if err != nil {
		t.Fatal(err)
	}
	// Estimate is 10_000 + 15_000 = 25_000 micro-USD, crossing 20_000 budget!
	// Must preserve full actual spent (25_000) and set paused=true!
	if !out.EstimatedCost || out.CostMicroUSD != 25_000 || out.SpentMicroUSD != 25_000 || !out.Paused || out.Status != TurnBudgetExhausted {
		t.Fatalf("budget outcome = %+v", out)
	}
	if _, err := eng.RunTurn(context.Background(), run, []Message{{Role: RoleUser, Text: "again"}}, "sys"); !errors.Is(err, kernel.ErrExhausted) {
		t.Fatalf("paused run err = %v, want %v", err, kernel.ErrExhausted)
	}
}

func TestUnknownToolBlockedBeforeExecutor(t *testing.T) {
	var directCalled, sandboxCalled bool
	sb := &fakeSandbox{}
	exec := DefaultExecutor{
		Sandbox: sb,
		Direct: map[string]ToolHandler{
			"hidden_direct": func(_ context.Context, _ *RunState, _ ToolCall) (ToolResult, error) {
				directCalled = true
				return ToolResult{Output: "ran"}, nil
			},
		},
	}
	auditSink := &testAuditSink{}
	eng := Engine{
		Model: fakeModel{fn: func(_ context.Context, _ PromptRequest) (PromptResponse, error) {
			return PromptResponse{
				Model: "m1",
				ToolCalls: []ToolCall{
					{ID: "u1", Name: "hidden_direct", Arguments: json.RawMessage(`{"x":1}`)},
					{ID: "u2", Name: "shell", Arguments: json.RawMessage(`{"args":["id"]}`)},
				},
			}, nil
		}},
		Executor: exec,
		Audit:    auditSink,
	}
	run := &RunState{
		RunID:    "r-unknown",
		ObjectID: "o1",
		UserID:   "u1",
		Catalog:  NewToolCatalog(nil, nil), // neither hidden_direct nor shell is active
	}
	out, err := eng.RunTurn(context.Background(), run, []Message{{Role: RoleUser, Text: "run"}}, "sys")
	if err != nil {
		t.Fatal(err)
	}
	sandboxCalled = sb.ensureCalls > 0 || sb.execCalls > 0
	if directCalled || sandboxCalled {
		t.Fatalf("unknown tools executed: direct=%v sandbox=%v", directCalled, sandboxCalled)
	}
	if len(out.BlockedEvents) != 2 || len(auditSink.events) != 2 {
		t.Fatalf("blocked=%+v audit=%+v", out.BlockedEvents, auditSink.events)
	}
	for _, ev := range out.BlockedEvents {
		if ev.Kind != EventBlocked || ev.Reason != "unknown_tool" {
			t.Fatalf("unexpected blocked event = %+v", ev)
		}
	}
	if len(out.Appended) != 3 || out.Appended[1].Text != "unknown or inactive tool" || !out.Appended[1].IsError ||
		out.Appended[2].Text != "unknown or inactive tool" || !out.Appended[2].IsError {
		t.Fatalf("appended = %+v", out.Appended)
	}
}

func TestApprovedCallBoundToCanonicalArgsDigestAndPlaceholderRoleTool(t *testing.T) {
	var executed []string
	catalog := NewToolCatalog([]ToolDef{
		{Name: "send_payment", Category: CategoryConnector, Op: OpChange, External: true},
	}, nil)
	log := newTestActionLog()
	eng := Engine{
		Executor: DefaultExecutor{
			Direct: map[string]ToolHandler{
				"send_payment": func(_ context.Context, _ *RunState, call ToolCall) (ToolResult, error) {
					executed = append(executed, string(call.Arguments))
					return ToolResult{Output: "paid"}, nil
				},
			},
		},
		WritePolicy: testPolicy{verdict: "ask"},
		ActionLog:   log,
	}

	// Turn 1: held -> placeholder RoleTool message emitted & ArgsDigest computed.
	run := &RunState{RunID: "r-bind", ObjectID: "o1", UserID: "u1", Catalog: catalog}
	eng.Model = fakeModel{fn: func(_ context.Context, _ PromptRequest) (PromptResponse, error) {
		return PromptResponse{
			Model:     "m1",
			ToolCalls: []ToolCall{{ID: "call_0", Name: "send_payment", Arguments: json.RawMessage(`{"iban":"DE89","amount":500}`)}},
		}, nil
	}}
	out1, err := eng.RunTurn(context.Background(), run, []Message{{Role: RoleUser, Text: "pay"}}, "sys")
	if err != nil {
		t.Fatal(err)
	}
	if out1.Status != TurnWaitingApproval || out1.Hold == nil || len(out1.Hold.Calls) != 1 {
		t.Fatalf("expected hold, got %+v", out1)
	}
	digest := out1.Hold.Calls[0].ArgsDigest
	if digest == "" {
		t.Fatal("expected non-empty ArgsDigest on HeldCall")
	}
	if len(out1.Appended) != 2 || out1.Appended[1].Role != RoleTool || out1.Appended[1].ToolID != "call_0" || out1.Appended[1].Text != "awaiting approval" || !out1.Appended[1].HeldPlaceholder {
		t.Fatalf("expected awaiting approval RoleTool placeholder, got %+v", out1.Appended)
	}

	// Turn 2: same call_id with mutated arguments MUST be held again, NOT executed!
	run.ApprovedCalls = map[string]string{"call_0": digest}
	eng.Model = fakeModel{fn: func(_ context.Context, _ PromptRequest) (PromptResponse, error) {
		return PromptResponse{
			Model:     "m1",
			ToolCalls: []ToolCall{{ID: "call_0", Name: "send_payment", Arguments: json.RawMessage(`{"iban":"EVIL99","amount":500}`)}},
		}, nil
	}}
	out2, err := eng.RunTurn(context.Background(), run, []Message{{Role: RoleUser, Text: "resume"}}, "sys")
	if err != nil {
		t.Fatal(err)
	}
	if len(executed) != 0 || out2.Status != TurnWaitingApproval || out2.Hold == nil {
		t.Fatalf("mutated args should be held again, executed=%v out=%+v", executed, out2)
	}

	// Turn 3: same call_id with equivalent canonical arguments (different key order) executes!
	eng.Model = fakeModel{fn: func(_ context.Context, _ PromptRequest) (PromptResponse, error) {
		return PromptResponse{
			Model:     "m1",
			ToolCalls: []ToolCall{{ID: "call_0", Name: "send_payment", Arguments: json.RawMessage(`{"amount":500,"iban":"DE89"}`)}},
		}, nil
	}}
	out3, err := eng.RunTurn(context.Background(), run, []Message{{Role: RoleUser, Text: "resume"}}, "sys")
	if err != nil {
		t.Fatal(err)
	}
	if len(executed) != 1 || out3.Status != TurnContinue {
		t.Fatalf("canonical matching args should execute, executed=%v out=%+v", executed, out3)
	}

	// Turn 4: reusing call_0 with identical args in the same session MUST NOT bypass approval again!
	run.Seq += 2
	out4, err := eng.RunTurn(context.Background(), run, []Message{{Role: RoleUser, Text: "reuse approval"}}, "sys")
	if err != nil {
		t.Fatal(err)
	}
	if len(executed) != 1 || out4.Status != TurnWaitingApproval || out4.Hold == nil {
		t.Fatalf("consumed approval must not allow reuse on later turn: executed=%v out=%+v", executed, out4)
	}
}

func TestCallDigestNonObjectPayloadsDistinct(t *testing.T) {
	dObjA := CallDigest("tool", OpChange, json.RawMessage(`{"a":1,"b":2}`))
	dObjB := CallDigest("tool", OpChange, json.RawMessage(`{"b":2,"a":1}`))
	if dObjA != dObjB {
		t.Fatalf("canonical object digests differed: %s vs %s", dObjA, dObjB)
	}
	dEmptyObj := CallDigest("tool", OpChange, json.RawMessage(`{}`))
	dBlank := CallDigest("tool", OpChange, json.RawMessage(`   `))
	if dEmptyObj != dBlank {
		t.Fatalf("empty object vs blank differed: %s vs %s", dEmptyObj, dBlank)
	}
	dArr1 := CallDigest("tool", OpChange, json.RawMessage(`[1,2]`))
	dArr1Padded := CallDigest("tool", OpChange, json.RawMessage(`  [1,2]  `))
	if dArr1 != dArr1Padded {
		t.Fatalf("whitespace-trimmed non-object digests differed: %s vs %s", dArr1, dArr1Padded)
	}
	variants := []string{
		dEmptyObj,
		dArr1,
		CallDigest("tool", OpChange, json.RawMessage(`[3,4]`)),
		CallDigest("tool", OpChange, json.RawMessage(`"hello"`)),
		CallDigest("tool", OpChange, json.RawMessage(`invalid{json`)),
		CallDigest("tool", OpChange, json.RawMessage(`null`)),
		CallDigest("tool", OpChange, json.RawMessage(`{"a":1} {"b":2}`)),
	}
	seen := make(map[string]int, len(variants))
	for i, d := range variants {
		if prev, dup := seen[d]; dup {
			t.Fatalf("non-object variant %d collided with variant %d (%s)", i, prev, d)
		}
		seen[d] = i
	}
}

func TestWritePolicyStrictVerdictAndNonExternalMutating(t *testing.T) {
	var executed int
	catalog := NewToolCatalog([]ToolDef{
		{Name: "send_payment", Category: CategoryConnector, Op: OpChange, External: true},
		{Name: "save_note", Category: CategoryDirect, Op: OpChange, External: false},
	}, nil)
	exec := DefaultExecutor{
		Direct: map[string]ToolHandler{
			"send_payment": func(_ context.Context, _ *RunState, _ ToolCall) (ToolResult, error) {
				executed++
				return ToolResult{Output: "sent"}, nil
			},
			"save_note": func(_ context.Context, _ *RunState, _ ToolCall) (ToolResult, error) {
				executed++
				return ToolResult{Output: "saved"}, nil
			},
		},
	}

	// 1. Unrecognized verdict ("deny") fails closed to "block", even during DryRun!
	eng := Engine{
		Model: fakeModel{fn: func(_ context.Context, _ PromptRequest) (PromptResponse, error) {
			return PromptResponse{
				Model:     "m1",
				ToolCalls: []ToolCall{{ID: "b1", Name: "send_payment", Arguments: json.RawMessage(`{"amount":10}`)}},
			}, nil
		}},
		Executor:    exec,
		WritePolicy: testPolicy{verdict: "deny"},
	}
	run := &RunState{RunID: "r-pol", ObjectID: "o1", UserID: "u1", DryRun: true, Catalog: catalog}
	out, err := eng.RunTurn(context.Background(), run, []Message{{Role: RoleUser, Text: "go"}}, "sys")
	if err != nil {
		t.Fatal(err)
	}
	if executed != 0 || len(out.SimulatedEvents) != 0 || len(out.BlockedEvents) != 1 || out.BlockedEvents[0].Reason != "invalid_policy_verdict" {
		t.Fatalf("expected invalid verdict to fail closed to block even in dry_run: %+v", out)
	}

	// 2. Non-external OpChange honors DryRun (simulates without requiring ActionLog).
	eng.WritePolicy = testPolicy{verdict: "allow"}
	eng.Model = fakeModel{fn: func(_ context.Context, _ PromptRequest) (PromptResponse, error) {
		return PromptResponse{
			Model:     "m1",
			ToolCalls: []ToolCall{{ID: "n1", Name: "save_note", Arguments: json.RawMessage(`{"text":"hi"}`)}},
		}, nil
	}}
	outDry, err := eng.RunTurn(context.Background(), run, []Message{{Role: RoleUser, Text: "save"}}, "sys")
	if err != nil {
		t.Fatal(err)
	}
	if executed != 0 || len(outDry.SimulatedEvents) != 1 {
		t.Fatalf("non-external mutating dry_run = executed:%d sim:%+v", executed, outDry.SimulatedEvents)
	}

	// 3. Non-external OpChange with "allow" + DryRun=false runs even when tainted (no external writeguard escalation) and without ActionLog.
	run.DryRun = false
	run.Exposure.Tainted = true
	outExec, err := eng.RunTurn(context.Background(), run, []Message{{Role: RoleUser, Text: "save"}}, "sys")
	if err != nil {
		t.Fatal(err)
	}
	if executed != 1 || outExec.Status != TurnContinue {
		t.Fatalf("non-external allow with taint = executed:%d out:%+v", executed, outExec)
	}

	// 4. Non-external OpChange with "ask" holds cleanly.
	eng.WritePolicy = testPolicy{verdict: "ask"}
	outAsk, err := eng.RunTurn(context.Background(), run, []Message{{Role: RoleUser, Text: "save"}}, "sys")
	if err != nil {
		t.Fatal(err)
	}
	if executed != 1 || outAsk.Status != TurnWaitingApproval || outAsk.Hold == nil {
		t.Fatalf("non-external ask = executed:%d out:%+v", executed, outAsk)
	}
}

type errTimelineSink struct{ err error }

func (s errTimelineSink) Emit(context.Context, *RunState, TimelineEvent) error { return s.err }

type errAuditSink struct{ err error }

func (s errAuditSink) Record(context.Context, *RunState, TimelineEvent) error { return s.err }

func TestPreCallBudgetCheckAndEmitAndAuditPropagation(t *testing.T) {
	var modelCalls int
	catalog := NewToolCatalog([]ToolDef{{Name: "send_payment", Category: CategoryConnector, Op: OpChange, External: true}}, nil)
	eng := Engine{
		Model: fakeModel{fn: func(_ context.Context, _ PromptRequest) (PromptResponse, error) {
			modelCalls++
			return PromptResponse{
				Model:     "m1",
				ToolCalls: []ToolCall{{ID: "c1", Name: "send_payment", Arguments: json.RawMessage(`{}`)}},
			}, nil
		}},
		Executor:    DefaultExecutor{},
		WritePolicy: testPolicy{verdict: "block"},
	}

	// Budgeted with zero budget -> ErrExhausted before Model.Complete.
	if _, err := eng.RunTurn(context.Background(), &RunState{Budgeted: true, BudgetMicroUSD: 0, Catalog: catalog}, []Message{{Role: RoleUser, Text: "hi"}}, "sys"); !errors.Is(err, kernel.ErrExhausted) || modelCalls != 0 {
		t.Fatalf("zero budget = %v calls=%d", err, modelCalls)
	}
	// Budgeted with spent >= budget -> ErrExhausted before Model.Complete.
	if _, err := eng.RunTurn(context.Background(), &RunState{Budgeted: true, BudgetMicroUSD: 100, SpentMicroUSD: 100, Catalog: catalog}, []Message{{Role: RoleUser, Text: "hi"}}, "sys"); !errors.Is(err, kernel.ErrExhausted) || modelCalls != 0 {
		t.Fatalf("exhausted budget = %v calls=%d", err, modelCalls)
	}

	// Timeline error must propagate instead of being swallowed.
	wantTimelineErr := errors.New("timeline down")
	eng.Timeline = errTimelineSink{err: wantTimelineErr}
	if _, err := eng.RunTurn(context.Background(), &RunState{RunID: "r1", ObjectID: "o1", UserID: "u1", Catalog: catalog}, []Message{{Role: RoleUser, Text: "hi"}}, "sys"); !errors.Is(err, wantTimelineErr) {
		t.Fatalf("timeline err = %v, want %v", err, wantTimelineErr)
	}

	// Audit error must propagate instead of being swallowed.
	eng.Timeline = nil
	wantAuditErr := errors.New("audit down")
	eng.Audit = errAuditSink{err: wantAuditErr}
	if _, err := eng.RunTurn(context.Background(), &RunState{RunID: "r1", ObjectID: "o1", UserID: "u1", Catalog: catalog}, []Message{{Role: RoleUser, Text: "hi"}}, "sys"); !errors.Is(err, wantAuditErr) {
		t.Fatalf("audit err = %v, want %v", err, wantAuditErr)
	}
}

type fakeSecrets map[string]string

func (s fakeSecrets) Get(ref string) (string, error) {
	v, ok := s[ref]
	if !ok {
		return "", errors.New("missing secret")
	}
	return v, nil
}

var _ secretchain.Backend = fakeSecrets(nil)

func TestHTTPConnectorClientAllowedHostCheck(t *testing.T) {
	var hits atomic.Int32
	var gotAuth, gotIdem string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		gotAuth = r.Header.Get("Authorization")
		gotIdem = r.Header.Get("Idempotency-Key")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	guard := &netguard.Guard{
		Allow: func(string) bool { return true },
		Lookup: func(_ context.Context, _, _ string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		},
	}
	client := NewHTTPConnectorClient(guard, fakeSecrets{"cred-1": "tok-secret"}, 5*time.Second)

	// Missing allowedHost with credentialRef -> refused before dialing.
	if _, _, _, err := client.Do(context.Background(), http.MethodPost, srv.URL, "", nil, "cred-1", "k1", nil); err == nil || hits.Load() != 0 {
		t.Fatalf("empty allowedHost should fail without dialing: err=%v hits=%d", err, hits.Load())
	}
	// Mismatched allowedHost -> refused before dialing.
	if _, _, _, err := client.Do(context.Background(), http.MethodPost, srv.URL, "api.example.com", nil, "cred-1", "k1", nil); err == nil || hits.Load() != 0 {
		t.Fatalf("mismatched allowedHost should fail without dialing: err=%v hits=%d", err, hits.Load())
	}
	// Matching host -> succeeds and attaches Bearer + Idempotency-Key.
	srvHost := strings.TrimPrefix(srv.URL, "http://")
	if _, _, _, err := client.Do(context.Background(), http.MethodPost, srv.URL, srvHost, nil, "cred-1", "k1", nil); err != nil || hits.Load() != 1 {
		t.Fatalf("matching host failed: err=%v hits=%d", err, hits.Load())
	}
	if gotAuth != "Bearer tok-secret" || gotIdem != "k1" {
		t.Fatalf("headers auth=%q idem=%q", gotAuth, gotIdem)
	}
}

func TestAssembleSystemPrefixSortsIndexCardsDeterministically(t *testing.T) {
	cardsA := make([]LessonCard, 0, LessonIndexCap+4)
	for i := 0; i < LessonIndexCap+4; i++ {
		cardsA = append(cardsA, LessonCard{
			ID:    "id-" + strconv.Itoa(i),
			Title: "Rule-" + strconv.Itoa(100-i),
			Cue:   "cue-" + strconv.Itoa(i),
		})
	}
	cardsB := slices.Clone(cardsA)
	slices.Reverse(cardsB)

	gotA := AssembleSystemPrefix("stand", "Agent", "inst", nil, cardsA)
	gotB := AssembleSystemPrefix("stand", "Agent", "inst", nil, cardsB)
	if gotA != gotB {
		t.Fatalf("unstable system prefix across card ordering:\nA:\n%s\nB:\n%s", gotA, gotB)
	}
}

func TestFeature_UPM314_OffloadedOutputRetrievableWithoutBreakingTools(t *testing.T) {
	store := &memOutputs{}
	run := &RunState{RunID: "r", Catalog: NewToolCatalog(nil, nil)}
	before := BuildPromptRequest(run, "system", nil)
	body := strings.Repeat("x", 9000)
	text, offloaded, err := MaybeOffload(context.Background(), store, run, "shell", body, false, 0)
	if err != nil || !offloaded {
		t.Fatalf("offload = %v, %v", offloaded, err)
	}
	call := ToolCall{Arguments: json.RawMessage(fmt.Sprintf(`{"id":%q,"offset":100,"limit":100}`, strings.TrimPrefix(text, "retrieve_output:")))}
	result, err := (DefaultExecutor{Outputs: store}).execRetrieveOutput(context.Background(), run, call)
	if err != nil || !strings.HasPrefix(result.Output, strings.Repeat("x", 100)) || !strings.Contains(result.Output, "total_bytes=9000 next_offset=200") {
		t.Fatalf("paged output = %q, %v", result.Output, err)
	}
	after := BuildPromptRequest(run, "system", nil)
	if !reflect.DeepEqual(before.Tools, after.Tools) || !reflect.DeepEqual(before.Cache, after.Cache) {
		t.Fatal("offload changed tools or cache breakpoints")
	}
	for _, args := range []string{`{"id":"missing","offset":-1}`, `{"id":"missing","limit":"many"}`} {
		if _, err := (DefaultExecutor{Outputs: store}).execRetrieveOutput(context.Background(), run, ToolCall{Arguments: json.RawMessage(args)}); err == nil {
			t.Fatalf("invalid paging arguments accepted: %s", args)
		}
	}
	if _, err := (DefaultExecutor{Outputs: store}).execRetrieveOutput(context.Background(), run, ToolCall{Arguments: json.RawMessage(fmt.Sprintf(`{"id":%q,"offset":null,"limit":null}`, strings.TrimPrefix(text, "retrieve_output:")))}); err != nil {
		t.Fatalf("null paging defaults rejected: %v", err)
	}
}

func TestFeature_UPM314_RetrieveOutputPagesSurviveRunTurn(t *testing.T) {
	store := &memOutputs{}
	run := &RunState{RunID: "r", Catalog: NewToolCatalog([]ToolDef{{Name: "produce", Op: OpRead}}, nil)}
	var history []Message
	step := 0
	engine := Engine{Outputs: store, OffloadAtTokens: 100, Executor: DefaultExecutor{Outputs: store, Direct: map[string]ToolHandler{
		"produce": func(context.Context, *RunState, ToolCall) (ToolResult, error) {
			return ToolResult{Output: strings.Repeat("é", 5000)}, nil
		},
	}}, Model: fakeModel{fn: func(_ context.Context, req PromptRequest) (PromptResponse, error) {
		step++
		switch step {
		case 1:
			return PromptResponse{ToolCalls: []ToolCall{{ID: "produce", Name: "produce"}}}, nil
		default:
			var id string
			for _, msg := range req.Messages {
				if msg.Role == RoleTool && strings.HasPrefix(msg.Text, "retrieve_output:") {
					id = strings.Fields(strings.TrimPrefix(msg.Text, "retrieve_output:"))[0]
				}
			}
			args, _ := json.Marshal(map[string]any{"id": id, "offset": (step - 2) * 100, "limit": map[int]any{2: nil, 3: 100000000}[step]})
			return PromptResponse{ToolCalls: []ToolCall{{ID: fmt.Sprintf("read-%d", step), Name: toolRetrieveOutput, Arguments: args}}}, nil
		}
	}}}
	for step < 3 {
		out, err := engine.RunTurn(context.Background(), run, history, "system")
		if err != nil {
			t.Fatal(err)
		}
		history = out.History
	}
	if store.seq != 1 {
		t.Fatalf("retrieve pages were offloaded again: saves=%d", store.seq)
	}
	pages := 0
	for _, msg := range history {
		if msg.Role == RoleTool && msg.ToolName == toolRetrieveOutput && strings.Contains(msg.Text, "total_bytes=") {
			pages++
		}
	}
	if pages != 2 {
		t.Fatalf("got %d retrieved pages, want 2", pages)
	}
	for _, msg := range history {
		if msg.Role == RoleTool && msg.ToolName == toolRetrieveOutput {
			if msg.Offload {
				t.Fatalf("retrieve_output page was re-offloaded: %+v", msg)
			}
			if TokenEstimate(msg.Text) > engine.OffloadAtTokens {
				t.Fatalf("retrieve_output page estimate = %d, want <= %d", TokenEstimate(msg.Text), engine.OffloadAtTokens)
			}
		}
	}
}

func TestFeature_UPM314_RetrieveOutputPageIsNotReoffloadedAtThreshold(t *testing.T) {
	store := &memOutputs{}
	run := &RunState{RunID: "r", Catalog: NewToolCatalog([]ToolDef{{Name: "produce", Op: OpRead}}, nil)}
	engine := Engine{Outputs: store, OffloadAtTokens: 1, Executor: DefaultExecutor{Outputs: store, Direct: map[string]ToolHandler{
		"produce": func(context.Context, *RunState, ToolCall) (ToolResult, error) {
			return ToolResult{Output: strings.Repeat("x", 5000)}, nil
		},
	}}, Model: fakeModel{fn: func(_ context.Context, req PromptRequest) (PromptResponse, error) {
		if len(req.Messages) == 0 {
			return PromptResponse{ToolCalls: []ToolCall{{ID: "produce", Name: "produce"}}}, nil
		}
		id := strings.Fields(strings.TrimPrefix(req.Messages[1].Text, "retrieve_output:"))[0]
		args, _ := json.Marshal(map[string]any{"id": id, "offset": 0, "limit": 500})
		return PromptResponse{ToolCalls: []ToolCall{{ID: "page", Name: toolRetrieveOutput, Arguments: args}}}, nil
	}}}
	first, err := engine.RunTurn(context.Background(), run, nil, "system")
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.RunTurn(context.Background(), run, first.History, "system")
	if err != nil {
		t.Fatal(err)
	}
	if store.seq != 1 {
		t.Fatalf("retrieve page caused another save: saves=%d", store.seq)
	}
}

func TestFeature_UPM314_EnginePreservesExecutorOffloadThreshold(t *testing.T) {
	store := &memOutputs{}
	run := &RunState{RunID: "r", Catalog: NewToolCatalog([]ToolDef{{Name: "produce", Op: OpRead}}, nil)}
	engine := Engine{Outputs: store, Executor: DefaultExecutor{Outputs: store, OffloadAtTokens: 1, Direct: map[string]ToolHandler{
		"produce": func(context.Context, *RunState, ToolCall) (ToolResult, error) {
			return ToolResult{Output: strings.Repeat("x", 500)}, nil
		},
	}}, Model: fakeModel{fn: func(context.Context, PromptRequest) (PromptResponse, error) {
		return PromptResponse{ToolCalls: []ToolCall{{ID: "produce", Name: "produce"}}}, nil
	}}}
	if _, err := engine.RunTurn(context.Background(), run, nil, "system"); err != nil {
		t.Fatal(err)
	}
	if store.seq != 1 {
		t.Fatalf("executor threshold was overwritten: saves=%d", store.seq)
	}
}

func TestFeature_UPM314_RetrievePageIsBoundedAndUtf8Safe(t *testing.T) {
	store := &memOutputs{items: map[string]struct {
		body      string
		untrusted bool
	}{"id": {body: strings.Repeat("aé🙂", 3000)}}}
	executor := DefaultExecutor{Outputs: store}
	var joined strings.Builder
	first, err := executor.execRetrieveOutput(context.Background(), nil, ToolCall{Arguments: json.RawMessage(`{"id":"id","offset":1,"limit":1}`)})
	if err != nil || !strings.Contains(first.Output, "next_offset=3]") {
		t.Fatalf("small UTF-8 page did not advance by one character: output=%q err=%v", first.Output, err)
	}
	for offset := 0; offset < len(store.items["id"].body); {
		call := ToolCall{Arguments: json.RawMessage(fmt.Sprintf(`{"id":"id","offset":%d,"limit":100000000}`, offset))}
		result, err := executor.execRetrieveOutput(context.Background(), nil, call)
		if err != nil {
			t.Fatal(err)
		}
		marker := fmt.Sprintf("\n[total_bytes=%d next_offset=", len(store.items["id"].body))
		page, metadata, found := strings.Cut(result.Output, marker)
		if !found || !utf8.ValidString(page) || len(page) > DefaultOffloadTokens*4-128 {
			t.Fatalf("invalid or oversized page: bytes=%d output tail=%q", len(page), result.Output[len(result.Output)-40:])
		}
		joined.WriteString(page)
		next, err := strconv.Atoi(strings.TrimSuffix(metadata, "]"))
		if err != nil {
			t.Fatalf("invalid next offset metadata %q: %v", metadata, err)
		}
		if next <= offset {
			t.Fatalf("page did not advance offset %d -> %d", offset, next)
		}
		offset = next
	}
	if joined.String() != store.items["id"].body {
		t.Fatal("UTF-8 pages did not reassemble the original body")
	}
	inside := ToolCall{Arguments: json.RawMessage(`{"id":"id","offset":2,"limit":30}`)}
	result, err := executor.execRetrieveOutput(context.Background(), nil, inside)
	if err != nil || !utf8.ValidString(strings.Split(result.Output, "\n[total_bytes=")[0]) {
		t.Fatalf("offset inside multi-byte rune returned invalid UTF-8: %v", err)
	}
}

func TestFeature_UPM314_RetrievePageUsesRuneFallbackBelowByteFloor(t *testing.T) {
	store := &memOutputs{items: map[string]struct {
		body      string
		untrusted bool
	}{"id": {body: "aé🙂"}}}
	result, err := (DefaultExecutor{Outputs: store}).execRetrieveOutput(context.Background(), nil, ToolCall{Arguments: json.RawMessage(`{"id":"id","offset":2,"limit":4}`)})
	if err != nil || !strings.HasPrefix(result.Output, "🙂\n[") || !utf8.ValidString(result.Output) {
		t.Fatalf("one-character fallback page = %q, err=%v", result.Output, err)
	}
}

func TestFeature_UPM314_ToolSurfaceIsAppendOnly(t *testing.T) {
	store := &memOutputs{}
	catalog := NewToolCatalog([]ToolDef{{Name: "first", Op: OpRead}}, []ToolDef{{Name: "discovered", Description: "find me"}})
	run := &RunState{RunID: "r", Catalog: catalog}
	var requests []PromptRequest
	engine := Engine{ContextWindow: 1000, Model: fakeModel{fn: func(_ context.Context, req PromptRequest) (PromptResponse, error) {
		requests = append(requests, req)
		if len(requests) == 1 {
			return PromptResponse{ToolCalls: []ToolCall{{ID: "f", Name: toolFindTool, Arguments: json.RawMessage(`{"query":"find me"}`)}, {ID: "o", Name: "first"}}}, nil
		}
		return PromptResponse{Text: "done"}, nil
	}}, Executor: DefaultExecutor{Outputs: store, Direct: map[string]ToolHandler{
		"first": func(context.Context, *RunState, ToolCall) (ToolResult, error) {
			return ToolResult{Output: strings.Repeat("x", 9000)}, nil
		},
	}}, Outputs: store, OffloadAtTokens: 1}
	history := []Message{{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "old", Name: "older"}}}, {Role: RoleTool, ToolID: "old", ToolName: "older", Text: strings.Repeat("z", 6000)}, {Role: RoleAssistant, Text: "previous response"}}
	first, err := engine.RunTurn(context.Background(), run, history, "system")
	if err != nil {
		t.Fatal(err)
	}
	second, err := engine.RunTurn(context.Background(), run, first.History, "system")
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || len(requests[1].Tools) <= len(requests[0].Tools) || !reflect.DeepEqual(requests[1].Tools[:len(requests[0].Tools)], requests[0].Tools) || !reflect.DeepEqual(requests[0].Cache, requests[1].Cache) {
		t.Fatalf("tool surface/cache changed: first=%+v second=%+v", requests[0], requests[1])
	}
	if requests[1].Tools[len(requests[1].Tools)-1].Name != "discovered" || second.AssistantText != "done" {
		t.Fatalf("discovered tool was not appended or second turn not returned: %+v", requests[1].Tools)
	}
	hasOffload, hasTrim := false, false
	for _, msg := range requests[1].Messages {
		if msg.Role == RoleTool && strings.HasPrefix(msg.Text, "retrieve_output:") {
			hasOffload = true
			if strings.Contains(msg.Text, "older-") {
				hasTrim = true
			}
		}
	}
	if !hasOffload || !hasTrim {
		t.Fatalf("turn 2 prompt missing offload or trimmed history: %+v", requests[1].Messages)
	}
}

func TestFeature_UPM314_PreCallTrimKeepsRequestWithinWindow(t *testing.T) {
	store := &memOutputs{}
	messages := []Message{
		{Role: RoleUser, Text: "old"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "lookup"}}},
		{Role: RoleTool, ToolID: "c1", ToolName: "lookup", Text: strings.Repeat("a", 600)},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c2", Name: "lookup"}}},
		{Role: RoleTool, ToolID: "c2", ToolName: "lookup", Text: strings.Repeat("b", 300)},
		{Role: RoleAssistant, Text: "previous response"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "latest", Name: "lookup"}}},
		{Role: RoleTool, ToolID: "latest", ToolName: "lookup", Text: "current"},
	}
	var got PromptRequest
	engine := Engine{ContextWindow: 300, Outputs: store, Executor: DefaultExecutor{}, Model: fakeModel{fn: func(_ context.Context, req PromptRequest) (PromptResponse, error) {
		got = req
		return PromptResponse{Text: "ok"}, nil
	}}}
	_, err := engine.RunTurn(context.Background(), &RunState{Catalog: NewToolCatalog(nil, nil)}, messages, "s")
	if err != nil {
		t.Fatal(err)
	}
	if estimateMessages(got.Messages)+TokenEstimate(got.System)+toolTokens(got.Tools)+engine.ContextWindow/20 > engine.ContextWindow || !strings.HasPrefix(got.Messages[2].Text, "retrieve_output:") || got.Messages[4].Text != messages[4].Text || got.Messages[7].Text != "current" {
		t.Fatalf("request not trimmed to fit: %+v", got.Messages)
	}
	ref := strings.Fields(got.Messages[2].Text)[0]
	content, _, err := store.RetrieveOutput(context.Background(), nil, strings.TrimPrefix(ref, "retrieve_output:"))
	if err != nil || content != messages[2].Text {
		t.Fatalf("trimmed output not retrievable: err=%v", err)
	}
	if got.Messages[2].ToolID != "c1" || got.Messages[4].ToolID != "c2" {
		t.Fatalf("pre-call trim did not preserve oldest-first message order: %+v", got.Messages)
	}
}

func TestFeature_UPM314_PostTurnTrimAt80Percent(t *testing.T) {
	belowMessages := []Message{{Role: RoleUser, Text: "old"}, {Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c", Name: "lookup"}}}, {Role: RoleTool, ToolID: "c", ToolName: "lookup", Text: strings.Repeat("x", 200)}, {Role: RoleUser, Text: "latest"}}
	store := &memOutputs{}
	below, err := TrimMessages(context.Background(), store, nil, belowMessages, 200, 160)
	if err != nil || !reflect.DeepEqual(belowMessages, below) {
		t.Fatalf("below threshold changed: %v", err)
	}
	aboveMessages := []Message{{Role: RoleUser, Text: "old"}, {Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c", Name: "lookup"}}}, {Role: RoleTool, ToolID: "c", ToolName: "lookup", Text: strings.Repeat("x", 800)}, {Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "latest", Name: "lookup"}}}, {Role: RoleTool, ToolID: "latest", ToolName: "lookup", Text: "latest"}}
	above, err := TrimMessages(context.Background(), store, nil, aboveMessages, 200, 160)
	if err != nil || !strings.HasPrefix(above[2].Text, "retrieve_output:") {
		t.Fatalf("above threshold not trimmed: %v", err)
	}
}

func TestFeature_UPM314_PostTurnThresholdIncludesOverhead(t *testing.T) {
	if got := PostTurnThreshold(1000, 20, 80, 100); got != 600 {
		t.Fatalf("threshold = %d, want 600", got)
	}
	if got := PostTurnThreshold(100, 100, 100, 10); got >= 0 {
		t.Fatalf("oversized overhead threshold = %d, want negative so eligible messages still trim", got)
	}
	if got := PromptMessageBudget(100, strings.Repeat("s", 400), nil); got >= 0 {
		t.Fatalf("oversized prompt budget = %d, want negative", got)
	}
	if got := PostTurnBudget(1000, "", nil); got != 750 {
		t.Fatalf("post-turn budget = %d, want 750", got)
	}
}

func TestFeature_UPM314_PostTurnBudgetKeepsNextRunStable(t *testing.T) {
	store := &memOutputs{}
	input := []Message{{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "old", Name: "lookup"}}}, {Role: RoleTool, ToolID: "old", ToolName: "lookup", Text: strings.Repeat("x", 380)}, {Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "later", Name: "lookup"}}}, {Role: RoleTool, ToolID: "later", ToolName: "lookup", Text: "later result"}}
	tools := NewToolCatalog(nil, nil).PromptTools()
	budget := PostTurnBudget(240, "s", tools)
	trimmed, err := TrimMessages(context.Background(), store, nil, input, 240, budget)
	if err != nil {
		t.Fatal(err)
	}
	if estimateMessages(trimmed) > budget {
		t.Fatalf("trimmed history estimate = %d, budget = %d", estimateMessages(trimmed), budget)
	}
	var requests []PromptRequest
	engine := Engine{ContextWindow: 240, Outputs: store, Executor: DefaultExecutor{}, Model: fakeModel{fn: func(_ context.Context, req PromptRequest) (PromptResponse, error) {
		requests = append(requests, req)
		return PromptResponse{Text: "ok"}, nil
	}}}
	first, err := engine.RunTurn(context.Background(), &RunState{Catalog: NewToolCatalog(nil, nil)}, trimmed, "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 {
		t.Fatalf("first model requests = %d, want 1", len(requests))
	}
	secondInput := append(cloneMessages(first.History), Message{Role: RoleUser, Text: strings.Repeat("growth ", 30)})
	second, err := engine.RunTurn(context.Background(), &RunState{Catalog: NewToolCatalog(nil, nil)}, secondInput, "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("model requests = %d, want 2", len(requests))
	}
	if store.seq != 1 || !reflect.DeepEqual(requests[1].Messages[:len(first.History)], first.History) || !reflect.DeepEqual(second.History[:len(first.History)], first.History) {
		t.Fatalf("request prefix changed or history re-saved: saves=%d", store.seq)
	}
}

func TestFeature_UPM314_EngineOffloadThresholdOverridesDefaultExecutor(t *testing.T) {
	for _, tc := range []struct {
		name     string
		executor func(OutputStore, map[string]ToolHandler) Executor
	}{
		{name: "value", executor: func(store OutputStore, direct map[string]ToolHandler) Executor {
			return DefaultExecutor{Outputs: store, OffloadAtTokens: 1, Direct: direct}
		}},
		{name: "pointer", executor: func(store OutputStore, direct map[string]ToolHandler) Executor {
			return &DefaultExecutor{Outputs: store, OffloadAtTokens: 1, Direct: direct}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &memOutputs{items: map[string]struct {
				body      string
				untrusted bool
			}{"saved": {body: strings.Repeat("x", 2000)}}}
			engine := Engine{Outputs: store, OffloadAtTokens: 100, Executor: tc.executor(store, nil), Model: fakeModel{fn: func(_ context.Context, req PromptRequest) (PromptResponse, error) {
				if len(req.Messages) == 0 {
					args, _ := json.Marshal(map[string]string{"id": "saved"})
					return PromptResponse{ToolCalls: []ToolCall{{ID: "page", Name: toolRetrieveOutput, Arguments: args}}}, nil
				}
				return PromptResponse{Text: "done"}, nil
			}}}
			out, err := engine.RunTurn(context.Background(), &RunState{RunID: "r", Catalog: NewToolCatalog(nil, nil)}, nil, "system")
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Appended) < 2 || strings.Contains(out.Appended[1].Text, "next_offset=4") {
				t.Fatalf("Engine threshold did not control retrieve_output page size: appended=%+v", out.Appended)
			}
		})
	}
}

func TestFeature_UPM314_TrimUsesOldestResultFirst(t *testing.T) {
	input := []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "one", Name: "lookup"}}}, {Role: RoleTool, ToolID: "one", ToolName: "lookup", Text: strings.Repeat("a", 400)},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "two", Name: "lookup"}}}, {Role: RoleTool, ToolID: "two", ToolName: "lookup", Text: strings.Repeat("b", 400)},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "latest", Name: "lookup"}}}, {Role: RoleTool, ToolID: "latest", ToolName: "lookup", Text: "latest"},
	}
	got, err := TrimMessages(context.Background(), &memOutputs{}, nil, input, 100, 180)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got[1].Text, "retrieve_output:") || got[3].Text != input[3].Text || got[5].Text != "latest" {
		t.Fatalf("trim order or latest turn changed: %+v", got)
	}
}

func TestFeature_UPM314_TrimStableAcrossReturnedHistory(t *testing.T) {
	store := &memOutputs{}
	input := []Message{{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "old", Name: "lookup"}}}, {Role: RoleTool, ToolID: "old", ToolName: "lookup", Text: strings.Repeat("x", 800)}, {Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "new", Name: "lookup"}}}, {Role: RoleTool, ToolID: "new", ToolName: "lookup", Text: "latest"}}
	first, err := TrimMessages(context.Background(), store, nil, input, 100, 50)
	if err != nil {
		t.Fatal(err)
	}
	second, err := TrimMessages(context.Background(), store, nil, first, 100, 50)
	if err != nil || !reflect.DeepEqual(first, second) || store.seq != 1 {
		t.Fatalf("trim churn: saves=%d err=%v", store.seq, err)
	}
}

func TestFeature_UPM314_ReturnedTrimmedHistoryIsStableAcrossTurns(t *testing.T) {
	store := &memOutputs{}
	input := []Message{{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "old", Name: "lookup"}}}, {Role: RoleTool, ToolID: "old", ToolName: "lookup", Text: strings.Repeat("x", 800)}, {Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "latest", Name: "lookup"}}}, {Role: RoleTool, ToolID: "latest", ToolName: "lookup", Text: "current"}}
	var requests []PromptRequest
	engine := Engine{ContextWindow: 200, Outputs: store, Executor: DefaultExecutor{}, Model: fakeModel{fn: func(_ context.Context, req PromptRequest) (PromptResponse, error) {
		requests = append(requests, req)
		return PromptResponse{Text: "ok"}, nil
	}}}
	run := &RunState{Catalog: NewToolCatalog(nil, nil)}
	first, err := engine.RunTurn(context.Background(), run, input, "s")
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.RunTurn(context.Background(), run, first.History, "s")
	if err != nil {
		t.Fatal(err)
	}
	if store.seq != 1 || len(requests) != 2 || !reflect.DeepEqual(requests[0].Messages, requests[1].Messages[:len(requests[0].Messages)]) {
		t.Fatalf("trimmed history churned: saves=%d requests=%+v", store.seq, requests)
	}
}

func TestFeature_UPM314_ContextExceededWhenSystemAndToolsDoNotFit(t *testing.T) {
	store := &memOutputs{}
	engine := Engine{ContextWindow: 1, Outputs: store, Executor: DefaultExecutor{}, Model: fakeModel{fn: func(context.Context, PromptRequest) (PromptResponse, error) {
		t.Fatal("model called with oversized prompt")
		return PromptResponse{}, nil
	}}}
	messages := []Message{{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "old", Name: "lookup"}}}, {Role: RoleTool, ToolID: "old", ToolName: "lookup", Text: strings.Repeat("x", 800)}, {Role: RoleAssistant, Text: "latest"}}
	_, err := engine.RunTurn(context.Background(), &RunState{Catalog: NewToolCatalog(nil, nil)}, messages, "system")
	if !errors.Is(err, ErrContextExceeded) || store.seq != 1 {
		t.Fatalf("context error = %v, trim saves = %d; want context error after trimming", err, store.seq)
	}
}

func TestFeature_UPM314_ContextExceededAfterTrimAndWithMargin(t *testing.T) {
	for _, tc := range []struct {
		name      string
		checkSave bool
		messages  []Message
		window    int
	}{
		{name: "still too large after trimming", checkSave: true, messages: []Message{{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "older", Name: "lookup"}}}, {Role: RoleTool, ToolID: "older", ToolName: "lookup", Text: strings.Repeat("x", 1000)}, {Role: RoleUser, Text: strings.Repeat("u", 2000)}, {Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "latest", Name: "lookup"}}}, {Role: RoleTool, ToolID: "latest", ToolName: "lookup", Text: "latest"}}, window: 200},
		{name: "fits only without safety margin", messages: []Message{{Role: RoleUser, Text: "fits"}}, window: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := NewToolCatalog(nil, nil)
			window := tc.window
			if window == 0 {
				window = TokenEstimate("s") + 1
				window += toolTokens(catalog.PromptTools())
			}
			engine := Engine{ContextWindow: window, Outputs: &memOutputs{}, Executor: DefaultExecutor{}, Model: fakeModel{fn: func(context.Context, PromptRequest) (PromptResponse, error) {
				t.Fatal("model called with context exceeding the window")
				return PromptResponse{}, nil
			}}}
			_, err := engine.RunTurn(context.Background(), &RunState{Catalog: catalog}, tc.messages, "s")
			if tc.checkSave && engine.Outputs.(*memOutputs).seq != 1 {
				t.Fatalf("trim saves = %d, want 1", engine.Outputs.(*memOutputs).seq)
			}
			if !errors.Is(err, ErrContextExceeded) {
				t.Fatalf("context error = %v", err)
			}
		})
	}
}

func TestFeature_UPM314_PreCallTrimSaveFailureFailsTurn(t *testing.T) {
	input := []Message{{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "old", Name: "lookup"}}}, {Role: RoleTool, ToolID: "old", ToolName: "lookup", Text: strings.Repeat("x", 800)}, {Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "latest", Name: "lookup"}}}, {Role: RoleTool, ToolID: "latest", ToolName: "lookup", Text: "current"}}
	engine := Engine{ContextWindow: 200, Outputs: &failingOutputs{}, Executor: DefaultExecutor{}, Model: fakeModel{fn: func(context.Context, PromptRequest) (PromptResponse, error) { return PromptResponse{}, nil }}}
	if _, err := engine.RunTurn(context.Background(), &RunState{Catalog: NewToolCatalog(nil, nil)}, input, "s"); err == nil || err.Error() != "save failed" {
		t.Fatalf("save failure = %v", err)
	}
}

func TestTrimMessagesRules(t *testing.T) {
	input := []Message{{Role: RoleUser, Text: "user"}, {Role: RoleAssistant, Text: "assistant", ToolCalls: []ToolCall{{ID: "old", Name: "lookup"}}}, {Role: RoleTool, ToolID: "old", ToolName: "lookup", Text: strings.Repeat("x", 500)}, {Role: RoleUser, Text: "latest"}, {Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "new", Name: "lookup"}}}, {Role: RoleTool, ToolID: "new", ToolName: "lookup", Text: strings.Repeat("y", 500)}}
	store := &memOutputs{}
	got, err := TrimMessages(context.Background(), store, nil, input, 100, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(input) || got[0].Text != "user" || got[1].Text != "assistant" || got[2].ToolID != "old" || !strings.HasPrefix(got[2].Text, "retrieve_output:") || got[5].Text != input[5].Text || got[4].ToolCalls[0].ID != "new" {
		t.Fatalf("trim violated message invariants: %+v", got)
	}
	offloaded := cloneMessages(input)
	offloaded[2].Offload = true
	offloaded[2].Text = "retrieve_output:already-stored\n" + strings.Repeat("z", 300)
	stored, err := TrimMessages(context.Background(), store, nil, offloaded, 100, 1)
	if err != nil || !reflect.DeepEqual(stored, offloaded) {
		t.Fatalf("already offloaded result was not skipped: %v", err)
	}
	again, err := TrimMessages(context.Background(), store, nil, got, 100, 50)
	if err != nil || !reflect.DeepEqual(got, again) {
		t.Fatalf("trim not idempotent: %v", err)
	}
	zero, err := TrimMessages(context.Background(), store, nil, input, 0, 1)
	if err != nil || !reflect.DeepEqual(input, zero) {
		t.Fatalf("zero window changed messages: %v", err)
	}
	first, err := TrimMessages(context.Background(), nil, nil, input, 100, 50)
	if err != nil {
		t.Fatal(err)
	}
	second, err := TrimMessages(context.Background(), nil, nil, input, 100, 50)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("trim not deterministic: %v", err)
	}
}

func TestFeature_UPM314_TrimSkipsUntrustedMarkerTextAndSmallResults(t *testing.T) {
	input := []Message{{Role: RoleAssistant, Text: "untouched", ToolCalls: []ToolCall{{ID: "old", Name: "lookup"}}}, {Role: RoleTool, ToolID: "old", ToolName: "lookup", Text: strings.Repeat("x", 600) + " awaiting approval retrieve_output:fake"}, {Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "small", Name: "lookup"}}}, {Role: RoleTool, ToolID: "small", ToolName: "lookup", Text: "tiny"}, {Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "latest", Name: "lookup"}}}, {Role: RoleTool, ToolID: "latest", ToolName: "lookup", Text: "current"}}
	store := &memOutputs{}
	got, err := TrimMessages(context.Background(), store, nil, input, 100, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(input) || got[0].Text != input[0].Text || !strings.HasPrefix(got[1].Text, "retrieve_output:") || got[3].Text != "tiny" || got[5].Text != "current" || store.seq != 1 {
		t.Fatalf("trim changed protected messages or saved a small result: messages=%+v saves=%d", got, store.seq)
	}
}

func TestFeature_UPM314_TrimHeldCallPlaceholder(t *testing.T) {
	input := []Message{{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "held", Name: "change"}}}, {Role: RoleTool, ToolID: "held", Text: strings.Repeat("awaiting approval ", 80), HeldPlaceholder: true}, {Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "latest", Name: "lookup"}}}, {Role: RoleTool, ToolID: "latest", Text: "current"}}
	store := &memOutputs{}
	got, err := TrimMessages(context.Background(), store, nil, input, 100, 20)
	if err != nil || got[1].Text != input[1].Text || store.seq != 0 {
		t.Fatalf("held placeholder changed: saves=%d err=%v", store.seq, err)
	}
}

func TestFeature_UPM314_TrimDoesNotSaveUnshrinkableResult(t *testing.T) {
	store := &memOutputs{}
	input := []Message{{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "old", Name: "lookup"}}}, {Role: RoleTool, ToolID: "old", ToolName: "lookup", Text: "short"}, {Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "latest", Name: "lookup"}}}, {Role: RoleTool, ToolID: "latest", Text: "current"}}
	got, err := TrimMessages(context.Background(), store, nil, input, 100, 1)
	if err != nil || store.seq != 0 || !reflect.DeepEqual(got, input) {
		t.Fatalf("small result caused orphan save: saves=%d err=%v messages=%+v", store.seq, err, got)
	}
}

func TestFeature_UPM314_TrimUsesRealisticOutputIDLength(t *testing.T) {
	store := &memOutputs{}
	input := []Message{{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "old", Name: "lookup"}}}, {Role: RoleTool, ToolID: "old", ToolName: "lookup", Text: strings.Repeat("x", 150)}, {Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "latest", Name: "lookup"}}}, {Role: RoleTool, ToolID: "latest", ToolName: "lookup", Text: "current"}}
	got, err := TrimMessages(context.Background(), store, nil, input, 100, 1)
	if err != nil || store.seq != 0 || got[1].Text != input[1].Text || got[3].Text != "current" {
		t.Fatalf("older result was saved despite realistic reference length: saves=%d err=%v messages=%+v", store.seq, err, got)
	}
}
