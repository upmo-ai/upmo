package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/platform/netguard"
)

func TestAdapterDecideYesNo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer s" {
			t.Fatalf("auth = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "yes"}}},
		})
	}))
	t.Cleanup(srv.Close)
	a := Adapter{Client: srv.Client()}
	got, _, class, err := a.Decide(context.Background(), app.Attempt{BaseURL: srv.URL, Model: "m", Secret: "s"}, domain.DecideRequest{Kind: domain.DecisionYesNo, Prompt: "keep?"})
	if err != nil || class != domain.ClassOK || !got.YesNo {
		t.Fatalf("got=%v class=%s err=%v", got, class, err)
	}
}

func TestAdapterMapsStatusToClass(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	a := Adapter{Client: srv.Client()}
	_, _, class, err := a.Chat(context.Background(), app.Attempt{BaseURL: srv.URL, Model: "m", Secret: "s"}, domain.Request{
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
	})
	if err == nil || class != domain.ClassCredential {
		t.Fatalf("class=%s err=%v", class, err)
	}
}

func TestAdapterEmbedOrdersByIndex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer s" {
			t.Fatalf("auth = %q", got)
		}
		var body struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Model != "m" || len(body.Input) != 2 || body.Input[0] != "one" {
			t.Fatalf("body = %+v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"index": 1, "embedding": []float32{0, 1}},
				{"index": 0, "embedding": []float32{1, 0}},
			},
			"usage": map[string]int{"prompt_tokens": 4},
		})
	}))
	t.Cleanup(srv.Close)
	a := Adapter{Client: srv.Client()}
	got, usage, class, err := a.Embed(context.Background(), app.Attempt{BaseURL: srv.URL, Model: "m", Secret: "s"}, []string{"one", "two"})
	if err != nil || class != domain.ClassOK {
		t.Fatalf("class=%s err=%v", class, err)
	}
	if len(got) != 2 || got[0][0] != 1 || got[1][1] != 1 || !usage.Arrived || usage.Input != 4 {
		t.Fatalf("got=%v usage=%+v", got, usage)
	}
}

func TestEncodeChatOrderIsToolsSystemMessages(t *testing.T) {
	req, err := domain.Build(domain.Input{
		Tools:    []domain.Tool{{Name: "search", Schema: json.RawMessage(`{"type":"object"}`)}},
		System:   "be brief",
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := encodeChat("m", req)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	toolsAt := strings.Index(text, `"tools"`)
	systemAt := strings.Index(text, `"system"`)
	messagesAt := strings.Index(text, `"messages"`)
	if toolsAt < 0 || systemAt < 0 || messagesAt < 0 || toolsAt >= systemAt || systemAt >= messagesAt {
		t.Fatalf("order = %s", text)
	}
	if !strings.Contains(text, `"cache_control"`) {
		t.Fatalf("missing cache breakpoint: %s", text)
	}
}

// provider is a fake provider on loopback that counts the requests it gets.
func provider(t *testing.T, status int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "hi"}}},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestProbeMapsStatusAndSendsTheKey(t *testing.T) {
	var auth atomic.Value
	cases := map[int]domain.FailureClass{
		200: domain.ClassOK,
		401: domain.ClassCredential,
		403: domain.ClassCredential,
		429: domain.ClassRateLimit,
		503: domain.ClassTransient,
		404: domain.ClassModelAccess,
		400: domain.ClassCaller,
	}
	for code, want := range cases {
		code := code
		stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth.Store(r.Header.Get("Authorization"))
			w.WriteHeader(code)
		}))
		a := Adapter{}
		class, err := a.Probe(context.Background(), app.Attempt{BaseURL: stub.URL, Secret: "sk-test", PlanKind: domain.PlanLocal})
		stub.Close()
		if class != want || (class == domain.ClassOK) != (err == nil) {
			t.Errorf("status %d: class %q, err %v; want %q", code, class, err, want)
		}
		if got := auth.Load(); got != "Bearer sk-test" {
			t.Errorf("status %d: Authorization = %v", code, got)
		}
	}
}

func TestProbeWithoutBaseURL(t *testing.T) {
	class, err := Adapter{}.Probe(context.Background(), app.Attempt{})
	if err == nil || class != domain.ClassCaller {
		t.Fatalf("class %q, err %v", class, err)
	}
}

func TestDefaultClientDialsByPlan(t *testing.T) {
	srv, hits := provider(t, http.StatusOK)
	chat := func(plan string) (domain.FailureClass, error) {
		_, _, class, err := Adapter{}.Chat(context.Background(),
			app.Attempt{BaseURL: srv.URL, Model: "m", Secret: "s", PlanKind: plan},
			domain.Request{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}})
		return class, err
	}
	if class, err := chat(domain.PlanLocal); err != nil || class != domain.ClassOK {
		t.Fatalf("local plan: %q, %v", class, err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d", hits.Load())
	}
	for _, plan := range []string{domain.PlanMetered, domain.PlanSubscription, ""} {
		if class, err := chat(plan); err == nil || class == domain.ClassOK {
			t.Fatalf("plan %q reached a loopback provider: %q, %v", plan, class, err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("a refused call still reached the provider: hits = %d", hits.Load())
	}
}

func TestNameRepointedAfterSavingIsRefusedAtCallTime(t *testing.T) {
	srv, hits := provider(t, http.StatusOK)
	_, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	answers := map[string][]netip.Addr{}
	a := Adapter{Lookup: func(_ context.Context, _, host string) ([]netip.Addr, error) {
		if ips, ok := answers[host]; ok {
			return ips, nil
		}
		return nil, errors.New("no such host")
	}}
	attempt := func(plan string) app.Attempt {
		return app.Attempt{BaseURL: "http://provider.test:" + port, PlanKind: plan, Secret: "sk-test"}
	}
	probe := func(plan string) error {
		class, err := a.Probe(context.Background(), attempt(plan))
		if err == nil && class != domain.ClassOK {
			t.Fatalf("class %q without error", class)
		}
		return err
	}

	// A local provider on a name works while the name points at loopback. Then
	// the name is re-pointed at addresses no plan may reach.
	answers["provider.test"] = []netip.Addr{netip.MustParseAddr("127.0.0.1")}
	if err := probe(domain.PlanLocal); err != nil {
		t.Fatalf("local provider on a name: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d", hits.Load())
	}
	for name, ips := range map[string][]string{
		"metadata":              {"169.254.169.254"},
		"one metadata among ok": {"127.0.0.1", "169.254.169.254"},
		"link-local v6":         {"fe80::1"},
		"alibaba metadata":      {"100.100.100.200"},
		"unspecified":           {"0.0.0.0"},
	} {
		answers["provider.test"] = nil
		for _, ip := range ips {
			answers["provider.test"] = append(answers["provider.test"], netip.MustParseAddr(ip))
		}
		if err := probe(domain.PlanLocal); !errors.Is(err, netguard.ErrBlocked) {
			t.Errorf("%s: local plan error = %v, want ErrBlocked", name, err)
		}
	}
	// A metered account is refused a private answer that a local one may use.
	answers["provider.test"] = []netip.Addr{netip.MustParseAddr("10.1.2.3")}
	if err := probe(domain.PlanMetered); !errors.Is(err, netguard.ErrBlocked) {
		t.Errorf("metered plan on a private answer: %v", err)
	}
	answers["provider.test"] = []netip.Addr{netip.MustParseAddr("127.0.0.1")}
	if err := probe(domain.PlanSubscription); !errors.Is(err, netguard.ErrBlocked) {
		t.Errorf("subscription plan on a loopback answer: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("a refused call reached the provider: hits = %d", hits.Load())
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	target, hits := provider(t, http.StatusOK)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/models", http.StatusFound)
	}))
	t.Cleanup(front.Close)
	class, err := Adapter{}.Probe(context.Background(), app.Attempt{BaseURL: front.URL, Secret: "sk-test", PlanKind: domain.PlanLocal})
	if err == nil || class == domain.ClassOK {
		t.Fatalf("class %q, err %v", class, err)
	}
	if hits.Load() != 0 {
		t.Fatal("the redirect was followed with the key")
	}
}

// sseServer streams chunks as SSE data lines, then [DONE]. When cut is set
// the connection is aborted after the first chunk instead.
func sseServer(t *testing.T, cut bool, chunks ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i, chunk := range chunks {
			_, _ = io.WriteString(w, "data: "+chunk+"\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			if cut && i == 0 {
				panic(http.ErrAbortHandler)
			}
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

var streamDeltas = []string{
	`{"choices":[{"delta":{"content":"Hel"}}]}`,
	`{"choices":[{"delta":{"content":"lo"}}]}`,
}

func streamRequest(srv *httptest.Server) (app.Attempt, domain.Request) {
	return app.Attempt{BaseURL: srv.URL, Model: "m", Secret: "s"},
		domain.Request{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}
}

func TestChatStreamTextInOrderWithUsage(t *testing.T) {
	var got []string
	srv := sseServer(t, false, append(streamDeltas,
		`{"choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"prompt_tokens_details":{"cached_tokens":1}}}`)...)
	a := Adapter{Client: srv.Client()}
	attempt, req := streamRequest(srv)
	usage, class, err := a.ChatStream(context.Background(), attempt, req, func(text string) error {
		got = append(got, text)
		return nil
	})
	if err != nil || class != domain.ClassOK {
		t.Fatalf("class=%s err=%v", class, err)
	}
	if len(got) != 2 || got[0] != "Hel" || got[1] != "lo" {
		t.Fatalf("deltas = %v", got)
	}
	if !usage.Arrived || usage.Input != 3 || usage.Output != 2 || usage.CacheRead != 1 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestChatStreamSendsStreamFlagsAndCap(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	a := Adapter{Client: srv.Client()}
	attempt, req := streamRequest(srv)
	req.MaxOutputTokens = 7
	_, _, _ = a.ChatStream(context.Background(), attempt, req, func(string) error { return nil })
	if body["stream"] != true {
		t.Fatalf("stream = %v", body["stream"])
	}
	opts, _ := body["stream_options"].(map[string]any)
	if opts["include_usage"] != true {
		t.Fatalf("stream_options = %v", body["stream_options"])
	}
	if body["max_tokens"] != float64(7) {
		t.Fatalf("max_tokens = %v", body["max_tokens"])
	}
}

func TestChatStreamMapsStatusBeforeFirstChunk(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	a := Adapter{Client: srv.Client()}
	attempt, req := streamRequest(srv)
	_, class, err := a.ChatStream(context.Background(), attempt, req, func(string) error { return nil })
	if err == nil || class != domain.ClassCredential {
		t.Fatalf("class=%s err=%v", class, err)
	}
}

func TestChatStreamCutMidStreamKeepsPartialText(t *testing.T) {
	var got []string
	srv := sseServer(t, true, streamDeltas...)
	a := Adapter{Client: srv.Client()}
	attempt, req := streamRequest(srv)
	usage, class, err := a.ChatStream(context.Background(), attempt, req, func(text string) error {
		got = append(got, text)
		return nil
	})
	if err == nil || class != domain.ClassTransient {
		t.Fatalf("class=%s err=%v", class, err)
	}
	if len(got) != 1 || got[0] != "Hel" {
		t.Fatalf("deltas = %v", got)
	}
	if usage.Arrived {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestChatStreamStopsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+streamDeltas[0]+"\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	a := Adapter{Client: srv.Client()}
	attempt, req := streamRequest(srv)
	var got []string
	_, class, err := a.ChatStream(ctx, attempt, req, func(text string) error {
		got = append(got, text)
		cancel()
		return nil
	})
	if err == nil || class == domain.ClassOK {
		t.Fatalf("class=%s err=%v", class, err)
	}
	if len(got) != 1 {
		t.Fatalf("deltas = %v", got)
	}
}

func TestChatStreamCancelDuringTheReadIsCancelledAndEndsTheWalk(t *testing.T) {
	// The provider sends one delta and then stalls; the caller cancels while
	// the adapter is reading. The row and the failure say cancelled, the
	// partial text stays, and the second link is never tried.
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+streamDeltas[0]+"\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	one := domain.Account{ID: "1", Adapter: "openai", BaseURL: srv.URL, CredentialRef: "house", House: true}
	two := domain.Account{ID: "2", Adapter: "openai", BaseURL: srv.URL, CredentialRef: "house", House: true}
	route := domain.Route{Name: "draft", Capability: domain.CapChat, Links: []domain.Link{
		{Position: 1, Account: one, Model: "m1", RetryBudget: 1},
		{Position: 2, Account: two, Model: "m2", RetryBudget: 0},
	}}
	led := &classLedger{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &cancelOnDeltaSink{cancel: cancel}
	disp := app.Dispatch{
		Routes:  dispatchRoute{route},
		Secrets: mapSecrets{"house": "s"},
		Ledger:  led,
		Stream:  map[string]app.StreamChatter{"openai": Adapter{Client: srv.Client()}},
	}
	err := disp.ChatStreamCall(ctx, "draft", "", domain.Principal{}, domain.Input{
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
	}, 0, sink)
	if err != nil {
		t.Fatal(err)
	}
	if sink.fail == nil || sink.fail.Class != domain.ClassCancelled || sink.fail.Model != "m1" {
		t.Fatalf("fail = %+v", sink.fail)
	}
	if sink.text != "Hel" {
		t.Fatalf("text = %q", sink.text)
	}
	if len(led.classes) != 1 || led.classes[0] != domain.ClassCancelled {
		t.Fatalf("ledger = %v", led.classes)
	}
	if hits.Load() != 1 {
		t.Fatalf("provider hits = %d", hits.Load())
	}
}

// cancelOnDeltaSink keeps the text and the failure of one streamed call and
// cancels the call on the first delta.
type cancelOnDeltaSink struct {
	failSink
	text   string
	cancel context.CancelFunc
}

func (s *cancelOnDeltaSink) Delta(text string) error {
	s.text += text
	s.cancel()
	return nil
}

func TestReadStreamEndedWhileCancelledIsCancelled(t *testing.T) {
	// A body can end with EOF or a plain error once the context is done,
	// without the error naming the cancellation.
	for name, tail := range map[string]error{"eof": io.EOF, "reset": errors.New("connection reset")} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			body := io.MultiReader(strings.NewReader("data: "+streamDeltas[0]+"\n\n"), errReader{tail})
			var got []string
			_, class, err := readStream(ctx, body, func(text string) error {
				got = append(got, text)
				return nil
			})
			if err == nil || class != domain.ClassCancelled {
				t.Fatalf("class=%s err=%v", class, err)
			}
			if len(got) != 1 || got[0] != "Hel" {
				t.Fatalf("deltas = %v", got)
			}
		})
	}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func TestChatStreamJoinsTheDataLinesOfOneEvent(t *testing.T) {
	// One event whose JSON spans two data: lines, with a comment and other
	// fields around it: only the joined data lines are the chunk.
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": keep-alive\n\n")
		_, _ = io.WriteString(w, "event: message\nid: 7\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}],\n")
		_, _ = io.WriteString(w, "data: \"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	a := Adapter{Client: srv.Client()}
	attempt, req := streamRequest(srv)
	usage, class, err := a.ChatStream(context.Background(), attempt, req, func(text string) error {
		got = append(got, text)
		return nil
	})
	if err != nil || class != domain.ClassOK {
		t.Fatalf("class=%s err=%v", class, err)
	}
	if len(got) != 1 || got[0] != "Hel" {
		t.Fatalf("deltas = %v", got)
	}
	if !usage.Arrived || usage.Input != 3 || usage.Output != 2 {
		t.Fatalf("usage = %+v", usage)
	}
}

// dispatchRoute serves one chat route for a Dispatch test.
type dispatchRoute struct{ r domain.Route }

func (m dispatchRoute) ByName(context.Context, string) (domain.Route, error) { return m.r, nil }

type mapSecrets map[string]string

func (m mapSecrets) Get(ref string) (string, error) {
	s, ok := m[ref]
	if !ok {
		return "", errors.New("missing")
	}
	return s, nil
}

// classLedger keeps the class of every attempt row.
type classLedger struct{ classes []domain.FailureClass }

func (l *classLedger) Record(_ context.Context, row domain.Entry) error {
	l.classes = append(l.classes, row.Class)
	return nil
}

// failSink keeps the final failure of one streamed call.
type failSink struct{ fail *app.StreamFail }

func (s *failSink) Delta(string) error            { return nil }
func (s *failSink) Notice(app.StreamNotice) error { return nil }
func (s *failSink) Done(app.StreamDone)           {}
func (s *failSink) Fail(f app.StreamFail)         { s.fail = &f }

func TestChatStreamCancelledContextIsCancelledAndEndsTheWalk(t *testing.T) {
	// The context is cancelled before the call, so the provider is never
	// reached: the row says cancelled and the second link is never tried.
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
	}))
	t.Cleanup(srv.Close)
	one := domain.Account{ID: "1", Adapter: "openai", BaseURL: srv.URL, CredentialRef: "house", House: true}
	two := domain.Account{ID: "2", Adapter: "openai", BaseURL: srv.URL, CredentialRef: "house", House: true}
	route := domain.Route{Name: "draft", Capability: domain.CapChat, Links: []domain.Link{
		{Position: 1, Account: one, Model: "m1", RetryBudget: 0},
		{Position: 2, Account: two, Model: "m2", RetryBudget: 0},
	}}
	led := &classLedger{}
	sink := &failSink{}
	disp := app.Dispatch{
		Routes:  dispatchRoute{route},
		Secrets: mapSecrets{"house": "s"},
		Ledger:  led,
		Stream:  map[string]app.StreamChatter{"openai": Adapter{Client: srv.Client()}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := disp.ChatStreamCall(ctx, "draft", "", domain.Principal{}, domain.Input{
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
	}, 0, sink)
	if err != nil {
		t.Fatal(err)
	}
	if sink.fail == nil || sink.fail.Class != domain.ClassCancelled || sink.fail.Model != "m1" {
		t.Fatalf("fail = %+v", sink.fail)
	}
	if len(led.classes) != 1 || led.classes[0] != domain.ClassCancelled {
		t.Fatalf("ledger = %v", led.classes)
	}
	if hits.Load() != 0 {
		t.Fatalf("provider hits = %d", hits.Load())
	}
}

func TestChatStreamStopsWhenEmitFails(t *testing.T) {
	gone := errors.New("caller went away")
	srv := sseServer(t, false, streamDeltas...)
	a := Adapter{Client: srv.Client()}
	attempt, req := streamRequest(srv)
	_, class, err := a.ChatStream(context.Background(), attempt, req, func(string) error { return gone })
	if !errors.Is(err, gone) || class != domain.ClassCancelled {
		t.Fatalf("class=%s err=%v", class, err)
	}
}
