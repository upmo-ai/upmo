package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
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
	_, class, err := a.Chat(context.Background(), app.Attempt{BaseURL: srv.URL, Model: "m", Secret: "s"}, domain.Request{
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

func TestEmbedUnsupportedCapability(t *testing.T) {
	var hits atomic.Int32
	rt := roundTripFunc(func(*http.Request) (*http.Response, error) {
		hits.Add(1)
		return nil, errors.New("unexpected HTTP request")
	})
	_, _, class, err := (Adapter{Client: &http.Client{Transport: rt}}).Embed(context.Background(), app.Attempt{
		BaseURL: "https://bedrock-mantle.us-east-1.api.aws/v1", Model: "openai.gpt-oss-20b", Secret: "sk-test",
	}, []string{"one"})
	if !errors.Is(err, domain.ErrCapability) || class != domain.ClassModelAccess || hits.Load() != 0 {
		t.Fatalf("class=%s err=%v HTTP hits=%d, want model-access capability refusal and no request", class, err, hits.Load())
	}
}

func TestPatternVendorURLsTrimTrailingSlashAndKeepDocumentedPath(t *testing.T) {
	for _, tc := range []struct {
		key, base string
		want      []string
	}{
		{
			key:  "azure",
			base: "https://sample.openai.azure.com/openai/v1",
			want: []string{
				"https://sample.openai.azure.com/openai/v1/chat/completions",
				"https://sample.openai.azure.com/openai/v1/chat/completions",
				"https://sample.openai.azure.com/openai/v1/models",
				"https://sample.openai.azure.com/openai/v1/embeddings",
			},
		},
		{
			key:  "bedrock-mantle",
			base: "https://bedrock-mantle.us-east-1.api.aws/v1",
			want: []string{
				"https://bedrock-mantle.us-east-1.api.aws/v1/chat/completions",
				"https://bedrock-mantle.us-east-1.api.aws/v1/chat/completions",
				"https://bedrock-mantle.us-east-1.api.aws/v1/models",
			},
		},
	} {
		for _, suffix := range []string{"", "/"} {
			t.Run(fmt.Sprintf("%s/trailing-slash=%t", tc.key, suffix != ""), func(t *testing.T) {
				stream, err := os.ReadFile(filepath.Join(contractDir, tc.key+".sse"))
				if err != nil {
					t.Fatal(err)
				}
				cs := newContractServer(t, stream)
				to, err := url.Parse(cs.srv.URL)
				if err != nil {
					t.Fatal(err)
				}
				rt := &redirect{to: to}
				a := Adapter{Client: &http.Client{Transport: rt}}
				attempt := app.Attempt{BaseURL: tc.base + suffix, Model: "m", Secret: "sk-test", PlanKind: domain.PlanMetered}
				request := domain.Request{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}
				ctx := context.Background()
				if _, class, err := a.ChatStream(ctx, attempt, request, func(string) error { return nil }); err != nil || class != domain.ClassOK {
					t.Fatalf("stream: class=%s err=%v", class, err)
				}
				assertContractAuth(t, cs.last(t), tc.key)
				if _, class, err := a.Chat(ctx, attempt, request); err != nil || class != domain.ClassOK {
					t.Fatalf("chat: class=%s err=%v", class, err)
				}
				assertContractAuth(t, cs.last(t), tc.key)
				if _, class, err := a.Probe(ctx, attempt); err != nil || class != domain.ClassOK {
					t.Fatalf("probe: class=%s err=%v", class, err)
				}
				assertContractAuth(t, cs.last(t), tc.key)
				if tc.key == "azure" {
					if _, _, class, err := a.Embed(ctx, attempt, []string{"one"}); err != nil || class != domain.ClassOK {
						t.Fatalf("embed: class=%s err=%v", class, err)
					}
					assertContractAuth(t, cs.last(t), tc.key)
				}
				if !reflect.DeepEqual(rt.asked, tc.want) {
					t.Errorf("final URLs = %v, want %v", rt.asked, tc.want)
				}
			})
		}
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
		_, class, err := a.Probe(context.Background(), app.Attempt{BaseURL: stub.URL, Secret: "sk-test", PlanKind: domain.PlanLocal})
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
	_, class, err := Adapter{}.Probe(context.Background(), app.Attempt{})
	if err == nil || class != domain.ClassCaller {
		t.Fatalf("class %q, err %v", class, err)
	}
}

func TestDefaultClientDialsByPlan(t *testing.T) {
	srv, hits := provider(t, http.StatusOK)
	chat := func(plan string) (domain.FailureClass, error) {
		_, class, err := Adapter{}.Chat(context.Background(),
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
		_, class, err := a.Probe(context.Background(), attempt(plan))
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
	_, class, err := Adapter{}.Probe(context.Background(), app.Attempt{BaseURL: front.URL, Secret: "sk-test", PlanKind: domain.PlanLocal})
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
	if !usage.Arrived || usage.Input != 2 || usage.Output != 2 || usage.CacheRead != 1 {
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

func TestChatStreamStopsOnCanceledContext(t *testing.T) {
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

func TestChatStreamCancelDuringTheReadIsCanceledAndEndsTheWalk(t *testing.T) {
	// The provider sends one delta and then stalls; the caller cancels while
	// the adapter is reading. The row and the failure say canceled, the
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
	if sink.fail == nil || sink.fail.Class != domain.ClassCanceled || sink.fail.Model != "m1" {
		t.Fatalf("fail = %+v", sink.fail)
	}
	if sink.text != "Hel" {
		t.Fatalf("text = %q", sink.text)
	}
	if len(led.classes) != 1 || led.classes[0] != domain.ClassCanceled {
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

func TestReadStreamEndedWhileCanceledIsCanceled(t *testing.T) {
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
			if err == nil || class != domain.ClassCanceled {
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

func TestChatStreamCanceledContextIsCanceledAndEndsTheWalk(t *testing.T) {
	// The context is canceled before the call, so the provider is never
	// reached: the row says canceled and the second link is never tried.
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
	if sink.fail == nil || sink.fail.Class != domain.ClassCanceled || sink.fail.Model != "m1" {
		t.Fatalf("fail = %+v", sink.fail)
	}
	if len(led.classes) != 1 || led.classes[0] != domain.ClassCanceled {
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
	if !errors.Is(err, gone) || class != domain.ClassCanceled {
		t.Fatalf("class=%s err=%v", class, err)
	}
}

func TestChatToolCallsAndSerialization(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"finish_reason": "tool_calls",
				"message": map[string]any{
					"content": nil,
					"tool_calls": []map[string]any{
						{"id": "c1", "type": "function", "function": map[string]string{"name": "search", "arguments": `{"q":"go"}`}},
						{"id": "c2", "type": "function", "function": map[string]string{"name": "fetch", "arguments": `{"id":"1"}`}},
					},
				},
			}},
			"usage": map[string]any{
				"prompt_tokens":             12,
				"completion_tokens":         8,
				"prompt_tokens_details":     map[string]int{"cached_tokens": 4},
				"completion_tokens_details": map[string]int{"reasoning_tokens": 3},
			},
		})
	}))
	t.Cleanup(srv.Close)

	req, err := domain.Build(domain.Input{
		Tools:           []domain.Tool{{Name: "search"}, {Name: "fetch"}},
		ToolChoice:      domain.ToolChoice{Mode: domain.ToolChoiceAny},
		MaxOutputTokens: 200,
		Messages: []domain.Message{
			{Role: domain.RoleUser, Text: "go"},
			{Role: domain.RoleAssistant, ToolCalls: []domain.ToolCall{{ID: "prev", Name: "search", Arguments: json.RawMessage(`{"q":"a"}`)}}},
			{Role: domain.RoleTool, ToolID: "prev", ToolName: "search", Text: "timed out", IsError: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, class, err := Adapter{Client: srv.Client()}.Chat(context.Background(), app.Attempt{BaseURL: srv.URL, Model: "gpt-4o", Secret: "s"}, req)
	if err != nil || class != domain.ClassOK {
		t.Fatalf("class=%s err=%v", class, err)
	}
	if res.StopReason != "tool_calls" || len(res.ToolCalls) != 2 || res.ToolCalls[0].Name != "search" || res.ToolCalls[1].ID != "c2" {
		t.Fatalf("res = %+v", res)
	}
	if !res.Usage.Arrived || res.Usage.Input != 8 || res.Usage.Output != 8 || res.Usage.CacheRead != 4 || res.Usage.Reasoning != 3 {
		t.Fatalf("usage = %+v", res.Usage)
	}
	if gotBody["tool_choice"] != "required" || gotBody["max_tokens"] != float64(200) {
		t.Fatalf("body = %+v", gotBody)
	}
	msgs, _ := gotBody["messages"].([]any)
	assistant, _ := msgs[1].(map[string]any)
	if _, hasContent := assistant["content"]; hasContent {
		t.Fatalf("assistant tool-call turn unexpectedly set content: %v", assistant)
	}
	toolMsg, _ := msgs[2].(map[string]any)
	if toolMsg["role"] != "tool" || toolMsg["tool_call_id"] != "prev" || !strings.HasPrefix(toolMsg["content"].(string), "Error: ") {
		t.Fatalf("tool msg = %v", toolMsg)
	}
	if got := encodeToolChoice(domain.ToolChoice{Mode: domain.ToolChoiceTool, Name: "search"}); got == nil {
		t.Fatal("expected tool choice object")
	}
}

func TestChatStreamToolsAndGeminiCompat(t *testing.T) {
	v, _ := domain.VendorFor("gemini")
	chunks := []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_g1","type":"function","function":{"name":"search","arguments":""}}],"role":"assistant"}}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":4}}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"gemini\"}"},"extra_content":{"google":{"thought_signature":"sig-abc"}}},{"index":1,"id":"call_g2","type":"function","function":{"name":"fetch","arguments":"{\"id\":\"42\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":9,"completion_tokens_details":{"reasoning_tokens":3}}}`,
	}
	srv := sseServer(t, false, chunks...)
	defer srv.Close()

	var askedURL string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		askedURL = r.URL.String()
		moved := r.Clone(r.Context())
		moved.URL.Scheme = "http"
		moved.URL.Host = strings.TrimPrefix(srv.URL, "http://")
		moved.Host = ""
		return http.DefaultTransport.RoundTrip(moved)
	})
	a := Adapter{Client: &http.Client{Transport: rt}}
	attempt := app.Attempt{BaseURL: v.BaseURL, Model: "gemini-2.5-flash", Secret: "k", PlanKind: domain.PlanMetered}
	req, err := domain.Build(domain.Input{
		Tools:    []domain.Tool{{Name: "search"}, {Name: "fetch"}},
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var deltas []app.ToolCallDelta
	res, class, err := a.ChatStreamTools(context.Background(), attempt, req, app.StreamEvents{
		ToolCall: func(d app.ToolCallDelta) error {
			deltas = append(deltas, d)
			return nil
		},
	})
	if err != nil || class != domain.ClassOK {
		t.Fatalf("class=%s err=%v", class, err)
	}
	if askedURL != "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions" {
		t.Fatalf("gemini URL = %q", askedURL)
	}
	if len(res.ToolCalls) != 2 || res.StopReason != "tool_calls" {
		t.Fatalf("res = %+v", res)
	}
	if res.ToolCalls[0].ID != "call_g1" || res.ToolCalls[0].Name != "search" || string(res.ToolCalls[0].Arguments) != `{"q":"gemini"}` || res.ToolCalls[0].ThoughtSignature != "sig-abc" {
		t.Fatalf("tool_calls[0] = %+v", res.ToolCalls[0])
	}
	if res.ToolCalls[1].ID != "call_g2" || res.ToolCalls[1].Name != "fetch" || string(res.ToolCalls[1].Arguments) != `{"id":"42"}` {
		t.Fatalf("tool_calls[1] = %+v", res.ToolCalls[1])
	}
	if res.Usage != (domain.Usage{Arrived: true, Input: 10, Output: 9, Reasoning: 3}) {
		t.Fatalf("usage = %+v", res.Usage)
	}
	if len(deltas) != 4 {
		t.Fatalf("deltas = %+v", deltas)
	}

	// Verify thought_signature replays cleanly on the next turn.
	next := buildChat("gemini-2.5-flash", domain.Request{
		Messages: []domain.Message{
			{Role: domain.RoleUser, Text: "hi"},
			{Role: domain.RoleAssistant, ToolCalls: res.ToolCalls},
			{Role: domain.RoleTool, ToolID: "call_g1", Text: "err", IsError: true},
		},
	})
	if next.Messages[1].ToolCalls[0].ExtraContent == nil || next.Messages[1].ToolCalls[0].ExtraContent.Google.ThoughtSignature != "sig-abc" {
		t.Fatalf("replayed thought_signature = %+v", next.Messages[1].ToolCalls[0].ExtraContent)
	}
	if *next.Messages[2].Content != "Error: err" {
		t.Fatalf("tool error content = %q", *next.Messages[2].Content)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAdapterDecideReturnsUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "yes"}}},
			"usage":   map[string]int{"prompt_tokens": 7, "completion_tokens": 1},
		})
	}))
	t.Cleanup(srv.Close)
	got, usage, class, err := Adapter{Client: srv.Client()}.Decide(
		context.Background(),
		app.Attempt{BaseURL: srv.URL, Model: "m", Secret: "s"},
		domain.DecideRequest{Kind: domain.DecisionYesNo, Prompt: "keep?"},
	)
	if err != nil || class != domain.ClassOK || !got.YesNo {
		t.Fatalf("got=%+v class=%s err=%v", got, class, err)
	}
	if !usage.Arrived || usage.Input != 7 || usage.Output != 1 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestProbeParsesRecordedFixturesAndRejectsBadLists(t *testing.T) {
	readFixture := func(name string) []byte {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join("testdata", "recorded", name)) //nolint:gosec // static testdata path
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	recordedCases := []struct {
		name, file, key, base, wantURL string
		want                           []domain.SeenModel
	}{
		{
			name: "plain openai-compat",
			file: "openai_models.json",
			want: []domain.SeenModel{
				{ID: "gpt-4o"},
				{ID: "gpt-4o-mini"},
			},
		},
		{
			name:    "Azure deployment list",
			file:    "azure_models.json",
			key:     "azure",
			base:    "https://sample.openai.azure.com/openai/v1",
			wantURL: "https://sample.openai.azure.com/openai/v1/models",
			want:    []domain.SeenModel{{ID: "my-gpt-4o-deployment"}},
		},
		{
			name:    "Bedrock Mantle model list",
			file:    "bedrock_mantle_models.json",
			key:     "bedrock-mantle",
			base:    "https://bedrock-mantle.us-east-1.api.aws/v1",
			wantURL: "https://bedrock-mantle.us-east-1.api.aws/v1/models",
			want:    []domain.SeenModel{{ID: "openai.gpt-oss-20b"}},
		},
		{
			name: "gemini strips models prefix",
			file: "gemini_models.json",
			want: []domain.SeenModel{
				{ID: "gemini-2.5-flash"},
				{ID: "gemini-2.5-pro"},
			},
		},
		{
			name: "openrouter keeps context pricing and modalities",
			file: "openrouter_models.json",
			want: []domain.SeenModel{
				{
					ID:            "anthropic/claude-sonnet-4",
					ContextLength: 200000,
					Pricing: map[string]string{
						"prompt":            "0.000003",
						"completion":        "0.000015",
						"request":           "0",
						"image":             "0.0048",
						"input_cache_read":  "0.0000003",
						"input_cache_write": "0.00000375",
					},
					Modalities: &domain.ModelModalities{
						Input:  []string{"text", "image"},
						Output: []string{"text"},
					},
				},
				{
					ID:            "openai/gpt-4o-mini",
					ContextLength: 128000,
					Pricing: map[string]string{
						"prompt":     "0.00000015",
						"completion": "0.0000006",
					},
					Modalities: &domain.ModelModalities{
						Input:  []string{"text", "image"},
						Output: []string{"text"},
					},
				},
			},
		},
	}
	for _, tc := range recordedCases {
		t.Run(tc.name, func(t *testing.T) {
			raw := readFixture(tc.file)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.key != "" {
					assertContractAuth(t, contractCall{Auth: r.Header.Get("Authorization"), APIKey: r.Header.Get("api-key")}, tc.key)
				}
				_, _ = w.Write(raw)
			}))
			t.Cleanup(srv.Close)
			to, err := url.Parse(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			rt := &redirect{to: to}
			base := tc.base
			if base == "" {
				base = srv.URL
			}
			got, class, err := (Adapter{Client: &http.Client{Transport: rt}}).Probe(context.Background(), app.Attempt{BaseURL: base, Secret: "sk-test"})
			if err != nil || class != domain.ClassOK {
				t.Fatalf("class=%s err=%v", class, err)
			}
			if tc.wantURL != "" && rt.lastAsked() != tc.wantURL {
				t.Errorf("models URL = %q, want %q", rt.lastAsked(), tc.wantURL)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got = %+v, want %+v", got, tc.want)
			}
		})
	}

	tooManyItems := make([]map[string]string, domain.MaxSeenModels+1)
	for i := range tooManyItems {
		tooManyItems[i] = map[string]string{"id": fmt.Sprintf("m-%d", i)}
	}
	tooMany, _ := json.Marshal(map[string]any{"object": "list", "data": tooManyItems})

	badCases := []struct {
		name string
		body []byte
	}{
		{"empty body", nil},
		{"malformed json", []byte("{not-json")},
		{"missing data", []byte(`{"object":"list","models":[]}`)},
		{"null data", []byte(`{"object":"list","data":null}`)},
		{"array top level", []byte(`[{"id":"gpt-4o"}]`)},
		{"unexpected list object", []byte(`{"object":"error","data":[]}`)},
		{"unexpected entry object", []byte(`{"object":"list","data":[{"id":"gpt-4o","object":"file"}]}`)},
		{"empty id", []byte(`{"data":[{"id":""}]}`)},
		{"empty after prefix strip", []byte(`{"data":[{"id":"models/"}]}`)},
		{"trailing garbage", []byte(`{"data":[]}extra`)},
		{"non-string pricing", []byte(`{"data":[{"id":"m","pricing":{"prompt":1.5}}]}`)},
		{"negative context length", []byte(`{"data":[{"id":"m","context_length":-1}]}`)},
		{"oversized body", append([]byte(`{"data":[{"id":"gpt-x"}]`), append(bytes.Repeat([]byte{' '}, 4<<20), '}')...)},
		{"too many entries", tooMany},
	}
	for _, tc := range badCases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(tc.body)
			}))
			t.Cleanup(srv.Close)
			got, class, err := (Adapter{Client: srv.Client()}).Probe(context.Background(), app.Attempt{BaseURL: srv.URL, Secret: "sk-test"})
			if err != nil || class != domain.ClassOK || got != nil {
				t.Fatalf("got=%+v class=%s err=%v, want nil models with ClassOK", got, class, err)
			}
		})
	}
}

func TestAzureAndMantleDocumentedChatToolAndErrorFixtures(t *testing.T) {
	for _, vendor := range []struct {
		name, chat, tool, failure string
		wantID, wantArgs          string
		base, path                string
	}{
		{"azure", "azure_chat.json", "azure_tool.json", "azure_error.json", "call_azure_1", `{"q":"azure"}`, "https://sample.openai.azure.com/openai/v1", "/openai/v1/chat/completions"},
		{"bedrock-mantle", "bedrock_mantle_chat.json", "bedrock_mantle_tool.json", "bedrock_mantle_error.json", "call_mantle_1", `{"q":"mantle"}`, "https://bedrock-mantle.us-east-1.api.aws/v1", "/v1/chat/completions"},
	} {
		vendor := vendor
		t.Run(vendor.name, func(t *testing.T) {
			read := func(name string) []byte {
				t.Helper()
				raw, err := os.ReadFile(filepath.Join("testdata", "recorded", name)) //nolint:gosec // static testdata path
				if err != nil {
					t.Fatal(err)
				}
				return raw
			}
			chat, tool := read(vendor.chat), read(vendor.tool)
			checkRequest := func(r *http.Request) {
				t.Helper()
				if r.URL.Path != vendor.path {
					t.Errorf("chat path = %q, want %q", r.URL.Path, vendor.path)
				}
				assertContractAuth(t, contractCall{Auth: r.Header.Get("Authorization"), APIKey: r.Header.Get("api-key")}, vendor.name)
			}
			var chatHits atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				checkRequest(r)
				if chatHits.Add(1) == 2 {
					_, _ = w.Write(tool)
					return
				}
				_, _ = w.Write(chat)
			}))
			t.Cleanup(srv.Close)
			request, err := domain.Build(domain.Input{Tools: []domain.Tool{{Name: "search"}}, Messages: []domain.Message{{Role: domain.RoleUser, Text: "find"}}})
			if err != nil {
				t.Fatal(err)
			}
			to, err := url.Parse(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			rt := &redirect{to: to}
			adapter := Adapter{Client: &http.Client{Transport: rt}}
			attempt := app.Attempt{BaseURL: vendor.base, Model: "m", Secret: "sk-test"}
			got, class, err := adapter.Chat(context.Background(), attempt, request)
			if err != nil || class != domain.ClassOK || got.Text != "Fixture answer" || !got.Usage.Arrived {
				t.Fatalf("chat fixture=%+v class=%s err=%v", got, class, err)
			}
			got, class, err = adapter.Chat(context.Background(), attempt, request)
			if err != nil || class != domain.ClassOK || len(got.ToolCalls) != 1 || got.ToolCalls[0].ID != vendor.wantID || got.ToolCalls[0].Name != "search" || string(got.ToolCalls[0].Arguments) != vendor.wantArgs {
				t.Fatalf("tool response=%+v class=%s err=%v", got, class, err)
			}
			decision, _, class, err := adapter.Decide(context.Background(), attempt, domain.DecideRequest{Kind: domain.DecisionChoice, Prompt: "choose", Options: []string{"Fixture answer", "other"}})
			if err != nil || class != domain.ClassOK || decision.Choice != "Fixture answer" {
				t.Fatalf("decide fixture=%+v class=%s err=%v", decision, class, err)
			}
			wantURL := vendor.base + "/chat/completions"
			if want := []string{wantURL, wantURL, wantURL}; !reflect.DeepEqual(rt.asked, want) {
				t.Errorf("chat, tool and decide URLs = %v, want %v", rt.asked, want)
			}

			failure := read(vendor.failure)
			bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				checkRequest(r)
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write(failure)
			}))
			defer bad.Close()
			to, err = url.Parse(bad.URL)
			if err != nil {
				t.Fatal(err)
			}
			rt = &redirect{to: to}
			_, class, err = (Adapter{Client: &http.Client{Transport: rt}}).Chat(context.Background(), attempt, request)
			if err == nil || class != domain.ClassRateLimit {
				t.Fatalf("error fixture class=%s err=%v", class, err)
			}
			if rt.lastAsked() != wantURL {
				t.Errorf("error URL = %q, want %q", rt.lastAsked(), wantURL)
			}
		})
	}
}

func TestProbePerplexityModelsFixture(t *testing.T) {
	v, ok := domain.VendorFor("perplexity")
	if !ok {
		t.Fatal("missing perplexity vendor")
	}
	raw, err := os.ReadFile(filepath.Join("testdata", "perplexity_models.json")) //nolint:gosec // static testdata path
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(raw)
	}))
	t.Cleanup(srv.Close)

	var askedURL string
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		askedURL = r.URL.String()
		moved := r.Clone(r.Context())
		moved.URL.Scheme = "http"
		moved.URL.Host = strings.TrimPrefix(srv.URL, "http://")
		moved.Host = ""
		return http.DefaultTransport.RoundTrip(moved)
	})
	a := Adapter{Client: &http.Client{Transport: rt}}
	got, class, err := a.Probe(context.Background(), app.Attempt{BaseURL: v.BaseURL, Secret: "sk-test", PlanKind: domain.PlanMetered})
	if err != nil || class != domain.ClassOK {
		t.Fatalf("class=%s err=%v", class, err)
	}
	if askedURL != "https://api.perplexity.ai/v1/models" {
		t.Fatalf("perplexity models URL = %q, want https://api.perplexity.ai/v1/models", askedURL)
	}
	want := []domain.SeenModel{
		{ID: "anthropic/claude-opus-5"},
		{ID: "anthropic/claude-opus-5-5"},
		{ID: "anthropic/claude-sonnet-5"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got = %+v, want %+v", got, want)
	}
}

func TestOpenAIUsage_CachedTokensAndDeepSeek(t *testing.T) {
	// 1. Non-streamed OpenAI cached_tokens subtracted from prompt_tokens.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
			"usage": map[string]any{
				"prompt_tokens":         1000,
				"completion_tokens":     250,
				"prompt_tokens_details": map[string]int{"cached_tokens": 800},
				"cost_in_usd_ticks":     15000,
			},
		})
	}))
	t.Cleanup(srv.Close)
	res, class, err := Adapter{Client: srv.Client()}.Chat(context.Background(), app.Attempt{BaseURL: srv.URL, Model: "grok-3", Secret: "s"}, domain.Request{
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
	})
	if err != nil || class != domain.ClassOK {
		t.Fatalf("class=%s err=%v", class, err)
	}
	if res.Usage.Input != 200 || res.Usage.CacheRead != 800 || res.Usage.Output != 250 {
		t.Fatalf("usage = %+v, want input=200 cache_read=800 output=250", res.Usage)
	}
	if res.Usage.ReportedCostMicroUSD == nil || *res.Usage.ReportedCostMicroUSD != 2 {
		t.Fatalf("xai ReportedCostMicroUSD = %v, want 2", res.Usage.ReportedCostMicroUSD)
	}

	// 2. DeepSeek prompt_cache_hit_tokens (streamed + clamped when > prompt_tokens).
	dsSrv := sseServer(t, false,
		`{"choices":[{"delta":{"content":"hi"}}]}`,
		`{"choices":[],"usage":{"prompt_tokens":500,"completion_tokens":40,"prompt_cache_hit_tokens":350}}`,
	)
	usage, class, err := Adapter{Client: dsSrv.Client()}.ChatStream(context.Background(), app.Attempt{BaseURL: dsSrv.URL, Model: "deepseek-chat", Secret: "s"}, domain.Request{
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
	}, func(string) error { return nil })
	if err != nil || class != domain.ClassOK {
		t.Fatalf("deepseek stream class=%s err=%v", class, err)
	}
	if usage.Input != 150 || usage.CacheRead != 350 || usage.Output != 40 {
		t.Fatalf("deepseek usage = %+v, want input=150 cache_read=350 output=40", usage)
	}

	// 3. Perplexity usage.cost.total_cost and OpenRouter scalar usage.cost, plus invalid costs ignored.
	for _, tc := range []struct {
		name string
		body string
		want *int64
	}{
		{
			name: "perplexity total_cost",
			body: `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"cost":{"input_tokens_cost":0.001,"output_tokens_cost":0.002,"total_cost":0.003}}}`,
			want: microPtr(3000),
		},
		{
			name: "openrouter string cost",
			body: `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"cost":"0.0000015"}}`,
			want: microPtr(2),
		},
		{
			name: "openrouter byok includes upstream cost",
			body: `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"cost":0.00025,"is_byok":true,"cost_details":{"upstream_inference_cost":0.00075}}}`,
			want: microPtr(1000),
		},
		{
			name: "openrouter byok missing upstream cost",
			body: `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"cost":0.00025,"is_byok":true}}`,
			want: nil,
		},
		{
			name: "openrouter byok null upstream cost",
			body: `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"cost":0.00025,"is_byok":true,"cost_details":{"upstream_inference_cost":null}}}`,
			want: nil,
		},
		{
			name: "openrouter byok invalid upstream cost",
			body: `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"cost":0.00025,"is_byok":true,"cost_details":{"upstream_inference_cost":-1}}}`,
			want: nil,
		},
		{
			name: "openrouter byok sum overflows cap",
			body: `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"cost":10000,"is_byok":true,"cost_details":{"upstream_inference_cost":1}}}`,
			want: nil,
		},
		{
			name: "openrouter non-byok null upstream cost unchanged",
			body: `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"cost":0.00025,"is_byok":false,"cost_details":{"upstream_inference_cost":null}}}`,
			want: microPtr(250),
		},
		{
			name: "clamped cached_tokens",
			body: `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":99},"cost":-1}}`,
			want: nil,
		},
		{
			name: "absurd cost",
			body: `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"cost":10001}}`,
			want: nil,
		},
		{
			name: "nan cost",
			body: `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"cost":"NaN"}}`,
			want: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			r, class, err := Adapter{Client: s.Client()}.Chat(context.Background(), app.Attempt{BaseURL: s.URL, Model: "m", Secret: "s"}, domain.Request{
				Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
			})
			s.Close()
			if err != nil || class != domain.ClassOK {
				t.Fatalf("class=%s err=%v", class, err)
			}
			if r.Usage.Input < 0 {
				t.Fatalf("negative input tokens: %d", r.Usage.Input)
			}
			if (r.Usage.ReportedCostMicroUSD == nil) != (tc.want == nil) || (tc.want != nil && *r.Usage.ReportedCostMicroUSD != *tc.want) {
				t.Fatalf("ReportedCostMicroUSD = %v, want %v", r.Usage.ReportedCostMicroUSD, tc.want)
			}
		})
	}
}

func TestServiceTierDecoding(t *testing.T) {
	// 1. Non-streaming complete decodes and normalizes service_tier, including
	// when usage is absent.
	for _, tc := range []struct {
		name        string
		body        string
		wantTier    string
		wantArrived bool
	}{
		{
			name:        "omitted",
			body:        `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
			wantTier:    "",
			wantArrived: true,
		},
		{
			name:        "default normalizes to standard",
			body:        `{"service_tier":"default","choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
			wantTier:    domain.ServiceTierStandard,
			wantArrived: true,
		},
		{
			name:        "priority",
			body:        `{"service_tier":"priority","choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
			wantTier:    domain.ServiceTierPriority,
			wantArrived: true,
		},
		{
			name:        "flex",
			body:        `{"service_tier":"flex","choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
			wantTier:    domain.ServiceTierFlex,
			wantArrived: true,
		},
		{
			name:        "auto normalizes to other",
			body:        `{"service_tier":"auto","choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
			wantTier:    domain.ServiceTierOther,
			wantArrived: true,
		},
		{
			name:        "scale normalizes to other",
			body:        `{"service_tier":"scale","choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
			wantTier:    domain.ServiceTierOther,
			wantArrived: true,
		},
		{
			name:        "priority preserved without usage block",
			body:        `{"service_tier":"priority","choices":[{"message":{"content":"ok"}}]}`,
			wantTier:    domain.ServiceTierPriority,
			wantArrived: false,
		},
	} {
		t.Run("complete/"+tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			res, class, err := Adapter{Client: s.Client()}.Chat(context.Background(), app.Attempt{BaseURL: s.URL, Model: "gpt-4o", Secret: "s"}, domain.Request{
				Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
			})
			s.Close()
			if err != nil || class != domain.ClassOK {
				t.Fatalf("class=%s err=%v", class, err)
			}
			if res.Usage.ServiceTier != tc.wantTier || res.Usage.Arrived != tc.wantArrived {
				t.Fatalf("usage = %+v, want service_tier=%q arrived=%v", res.Usage, tc.wantTier, tc.wantArrived)
			}
		})
	}

	// 2. Streaming: tier on earlier chunk survives final usage-only chunk omitting service_tier.
	t.Run("stream/preserved across usage-only final chunk", func(t *testing.T) {
		srv := sseServer(t, false,
			`{"service_tier":"priority","choices":[{"delta":{"content":"hi"}}]}`,
			`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20}}`,
		)
		res, class, err := Adapter{Client: srv.Client()}.ChatStreamTools(context.Background(), app.Attempt{BaseURL: srv.URL, Model: "gpt-4o", Secret: "s"}, domain.Request{
			Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
		}, app.StreamEvents{})
		if err != nil || class != domain.ClassOK {
			t.Fatalf("class=%s err=%v", class, err)
		}
		if !res.Usage.Arrived || res.Usage.Input != 100 || res.Usage.Output != 20 || res.Usage.ServiceTier != domain.ServiceTierPriority {
			t.Fatalf("usage = %+v, want priority with 100/20", res.Usage)
		}
	})

	// 3. Streaming: tier appearing only on final usage chunk, and multiple usage chunks.
	t.Run("stream/tier only on final usage chunk", func(t *testing.T) {
		srv := sseServer(t, false,
			`{"choices":[{"delta":{"content":"hi"}}],"usage":{"prompt_tokens":10,"completion_tokens":1}}`,
			`{"service_tier":"flex","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20}}`,
		)
		res, class, err := Adapter{Client: srv.Client()}.ChatStreamTools(context.Background(), app.Attempt{BaseURL: srv.URL, Model: "gpt-4o", Secret: "s"}, domain.Request{
			Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
		}, app.StreamEvents{})
		if err != nil || class != domain.ClassOK {
			t.Fatalf("class=%s err=%v", class, err)
		}
		if res.Usage.ServiceTier != domain.ServiceTierFlex || res.Usage.Input != 100 || res.Usage.Output != 20 {
			t.Fatalf("usage = %+v, want flex with 100/20", res.Usage)
		}
	})

	// 4. Streaming: interrupted by EOF without [DONE] still preserves service_tier.
	t.Run("stream/eof before done preserves tier", func(t *testing.T) {
		srv := sseServer(t, true,
			`{"service_tier":"priority","choices":[{"delta":{"content":"partial"}}]}`,
		)
		res, class, err := Adapter{Client: srv.Client()}.ChatStreamTools(context.Background(), app.Attempt{BaseURL: srv.URL, Model: "gpt-4o", Secret: "s"}, domain.Request{
			Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
		}, app.StreamEvents{})
		if err == nil || class != domain.ClassTransient {
			t.Fatalf("class=%s err=%v, want transient EOF error", class, err)
		}
		if res.Usage.ServiceTier != domain.ServiceTierPriority || res.Text != "partial" {
			t.Fatalf("res = %+v, want service_tier=priority text=partial", res)
		}
	})

	// 5. Streaming: malformed later chunk preserves tier seen in earlier chunk.
	t.Run("stream/malformed later chunk preserves tier", func(t *testing.T) {
		srv := sseServer(t, false,
			`{"service_tier":"flex","choices":[{"delta":{"content":"hi"}}]}`,
			`{not-json`,
		)
		res, class, err := Adapter{Client: srv.Client()}.ChatStreamTools(context.Background(), app.Attempt{BaseURL: srv.URL, Model: "gpt-4o", Secret: "s"}, domain.Request{
			Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
		}, app.StreamEvents{})
		if err == nil || class != domain.ClassCaller {
			t.Fatalf("class=%s err=%v, want caller malformed body error", class, err)
		}
		if res.Usage.ServiceTier != domain.ServiceTierFlex {
			t.Fatalf("usage = %+v, want service_tier=flex", res.Usage)
		}
	})

	// 6. Streaming: text callback cancellation on the same chunk still preserves tier
	// because service_tier is recorded before callbacks run.
	t.Run("stream/callback cancel on same chunk preserves tier", func(t *testing.T) {
		srv := sseServer(t, false,
			`{"service_tier":"priority","choices":[{"delta":{"content":"hi"}}]}`,
		)
		cancelErr := errors.New("caller gone")
		res, class, err := Adapter{Client: srv.Client()}.ChatStreamTools(context.Background(), app.Attempt{BaseURL: srv.URL, Model: "gpt-4o", Secret: "s"}, domain.Request{
			Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
		}, app.StreamEvents{
			Text: func(string) error { return cancelErr },
		})
		if !errors.Is(err, cancelErr) || class != domain.ClassCanceled {
			t.Fatalf("class=%s err=%v, want canceled", class, err)
		}
		if res.Usage.ServiceTier != domain.ServiceTierPriority {
			t.Fatalf("usage = %+v, want service_tier=priority", res.Usage)
		}
	})

	// 7. Streaming: tool callback cancellation on the same chunk preserves tier.
	t.Run("stream/tool callback cancel on same chunk preserves tier", func(t *testing.T) {
		srv := sseServer(t, false,
			`{"service_tier":"batch","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}}]}`,
		)
		cancelErr := errors.New("tool caller gone")
		res, class, err := Adapter{Client: srv.Client()}.ChatStreamTools(context.Background(), app.Attempt{BaseURL: srv.URL, Model: "gpt-4o", Secret: "s"}, domain.Request{
			Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
		}, app.StreamEvents{
			ToolCall: func(app.ToolCallDelta) error { return cancelErr },
		})
		if !errors.Is(err, cancelErr) || class != domain.ClassCanceled {
			t.Fatalf("class=%s err=%v, want canceled", class, err)
		}
		if res.Usage.ServiceTier != domain.ServiceTierBatch {
			t.Fatalf("usage = %+v, want service_tier=batch", res.Usage)
		}
	})
}
