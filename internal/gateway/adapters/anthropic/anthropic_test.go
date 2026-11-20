package anthropic

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

var (
	_ app.Decider           = Adapter{}
	_ app.Chatter           = Adapter{}
	_ app.StreamChatter     = Adapter{}
	_ app.ToolStreamChatter = Adapter{}
	_ app.Prober            = Adapter{}
)

func TestAdapterHasNoEmbed(t *testing.T) {
	// The gateway lists an embed adapter only for a type that has the method,
	// so an embed route that reaches this adapter finds none and moves on.
	if _, ok := any(Adapter{}).(app.Embedder); ok {
		t.Fatal("the anthropic adapter must not offer Embed")
	}
}

//go:embed testdata
var fixtures embed.FS

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := fixtures.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// seen is what the test server saw in one request.
type seen struct {
	Method, Path, Key, Version, Auth, Type string
	Body                                   map[string]any
	Raw                                    string
}

// recorder is a fake Messages API: it keeps every request and answers each
// with the reply function.
type recorder struct {
	srv   *httptest.Server
	mu    sync.Mutex
	calls []seen
}

func newRecorder(t *testing.T, reply func(w http.ResponseWriter, r *http.Request)) *recorder {
	t.Helper()
	rec := &recorder{}
	rec.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := seen{
			Method: r.Method, Path: r.URL.Path,
			Key: r.Header.Get("x-api-key"), Version: r.Header.Get("anthropic-version"),
			Auth: r.Header.Get("Authorization"), Type: r.Header.Get("Content-Type"),
		}
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			call.Raw = string(raw)
			_ = json.Unmarshal(raw, &call.Body)
		}
		rec.mu.Lock()
		rec.calls = append(rec.calls, call)
		rec.mu.Unlock()
		reply(w, r)
	}))
	t.Cleanup(rec.srv.Close)
	return rec
}

func (r *recorder) last(t *testing.T) seen {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		t.Fatal("the server saw no request")
	}
	return r.calls[len(r.calls)-1]
}

func jsonReply(body []byte) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}
}

func streamReply(body []byte) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(body)
	}
}

func statusReply(code int) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }
}

func attemptFor(rec *recorder) app.Attempt {
	return app.Attempt{Adapter: domain.AdapterAnthropic, BaseURL: rec.srv.URL, Model: "claude-3-7-sonnet", Secret: "s"}
}

func hi() domain.Request {
	return domain.Request{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}
}

func TestChatSendsHeadersAndTheMessagesShape(t *testing.T) {
	rec := newRecorder(t, jsonReply(fixture(t, "message.json")))
	a := Adapter{Client: rec.srv.Client()}
	_, class, err := a.Chat(context.Background(), attemptFor(rec), hi())
	if err != nil || class != domain.ClassOK {
		t.Fatalf("class=%s err=%v", class, err)
	}
	call := rec.last(t)
	if call.Method != http.MethodPost || call.Path != "/v1/messages" {
		t.Fatalf("call = %s %s", call.Method, call.Path)
	}
	if call.Key != "s" || call.Version != "2023-06-01" || call.Type != "application/json" {
		t.Fatalf("headers key=%q version=%q type=%q", call.Key, call.Version, call.Type)
	}
	if call.Auth != "" {
		t.Fatalf("a bearer token went to Anthropic: %q", call.Auth)
	}
	body := call.Body
	if body["model"] != "claude-3-7-sonnet" || body["max_tokens"] != float64(DefaultMaxTokens) {
		t.Fatalf("body = %s", call.Raw)
	}
	if _, streamed := body["stream"]; streamed {
		t.Fatalf("a plain chat set the stream flag: %s", call.Raw)
	}
	for _, key := range []string{"system", "tools"} {
		if _, has := body[key]; has {
			t.Fatalf("body has %q though the request set none: %s", key, call.Raw)
		}
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages = %v", body["messages"])
	}
	if msg, _ := msgs[0].(map[string]any); msg["role"] != "user" || msg["content"] != "hi" {
		t.Fatalf("message = %v", msgs[0])
	}
}

func TestChatCapComesFromTheRequest(t *testing.T) {
	rec := newRecorder(t, jsonReply(fixture(t, "message.json")))
	a := Adapter{Client: rec.srv.Client()}
	req := hi()
	req.MaxOutputTokens = 77
	if _, _, err := a.Chat(context.Background(), attemptFor(rec), req); err != nil {
		t.Fatal(err)
	}
	if got := rec.last(t).Body["max_tokens"]; got != float64(77) {
		t.Fatalf("max_tokens = %v", got)
	}
}

func TestSystemIsBlocksAndCacheControlSitsOnTheMarkedBlocks(t *testing.T) {
	rec := newRecorder(t, jsonReply(fixture(t, "message.json")))
	a := Adapter{Client: rec.srv.Client()}
	req, err := domain.Build(domain.Input{
		Tools: []domain.Tool{
			{Name: "search", Description: "find", Schema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}}}`)},
			{Name: "fetch"},
		},
		System:   "be brief",
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Chat(context.Background(), attemptFor(rec), req); err != nil {
		t.Fatal(err)
	}
	body := rec.last(t).Body

	system, _ := body["system"].([]any)
	if len(system) != 1 {
		t.Fatalf("system = %v", body["system"])
	}
	block, _ := system[0].(map[string]any)
	cc, _ := block["cache_control"].(map[string]any)
	if block["type"] != "text" || block["text"] != "be brief" || cc["type"] != "ephemeral" {
		t.Fatalf("system block = %v", block)
	}

	tools, _ := body["tools"].([]any)
	if len(tools) != 4 {
		t.Fatalf("tools = %v", body["tools"])
	}
	search, _ := tools[2].(map[string]any)
	last, _ := tools[3].(map[string]any)
	if search["name"] != "search" || search["description"] != "find" || last["name"] != "fetch" {
		t.Fatalf("tools = %v", tools)
	}
	if _, marked := search["cache_control"]; marked {
		t.Fatalf("a non-final tool is marked: %v", search)
	}
	if cc, _ := last["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
		t.Fatalf("the last tool is not marked: %v", last)
	}
	if schema, _ := search["input_schema"].(map[string]any); schema["type"] != "object" {
		t.Fatalf("input_schema = %v", search["input_schema"])
	}
	// A tool with no schema still carries an object schema, which the API requires.
	if schema, _ := last["input_schema"].(map[string]any); schema["type"] != "object" {
		t.Fatalf("input_schema of a bare tool = %v", last["input_schema"])
	}
}

func TestNoBreakpointMeansNoCacheControl(t *testing.T) {
	req := domain.Request{
		Tools:    []domain.Tool{{Name: "search"}},
		System:   "be brief",
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
	}
	raw, err := json.Marshal(buildPayload("m", req, false))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "cache_control") {
		t.Fatalf("body = %s", raw)
	}
}

func TestOnlyTheSystemBreakpointMarksOnlySystem(t *testing.T) {
	req := domain.Request{
		Tools:    []domain.Tool{{Name: "search"}},
		System:   "be brief",
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
		Cache:    []domain.Breakpoint{domain.BreakSystem},
	}
	body := buildPayload("m", req, false)
	if body.System[0].CacheControl == nil || body.Tools[0].CacheControl != nil {
		t.Fatalf("system=%+v tool=%+v", body.System[0], body.Tools[0])
	}
	req.Cache = []domain.Breakpoint{domain.BreakTools}
	body = buildPayload("m", req, false)
	if body.System[0].CacheControl != nil || body.Tools[0].CacheControl == nil {
		t.Fatalf("system=%+v tool=%+v", body.System[0], body.Tools[0])
	}
}

func TestBlankSystemIsLeftOut(t *testing.T) {
	body := buildPayload("m", domain.Request{System: "  \n", Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}, false)
	if len(body.System) != 0 {
		t.Fatalf("system = %+v", body.System)
	}
}

func TestPayloadOrderIsToolsSystemMessages(t *testing.T) {
	req, err := domain.Build(domain.Input{
		Tools:    []domain.Tool{{Name: "search", Schema: json.RawMessage(`{"type":"object"}`)}},
		System:   "be brief",
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(buildPayload("m", req, true))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	toolsAt := strings.Index(text, `"tools"`)
	systemAt := strings.Index(text, `"system"`)
	messagesAt := strings.Index(text, `"messages"`)
	maxAt := strings.Index(text, `"max_tokens"`)
	if toolsAt < 0 || systemAt < 0 || messagesAt < 0 || toolsAt >= systemAt || systemAt >= messagesAt || messagesAt >= maxAt {
		t.Fatalf("order = %s", text)
	}
}

func TestRolesMapOntoUserAndAssistant(t *testing.T) {
	body := buildPayload("m", domain.Request{Messages: []domain.Message{
		{Role: domain.RoleUser, Text: "q"},
		{Role: domain.RoleAssistant, Text: "a"},
		{Role: domain.RoleTool, Text: "result", ToolName: "search", ToolID: "t1"},
	}}, false)
	if len(body.Messages) != 3 {
		t.Fatalf("messages = %+v", body.Messages)
	}
	if body.Messages[0].Role != "user" || body.Messages[0].Content != "q" {
		t.Fatalf("msg[0] = %+v", body.Messages[0])
	}
	if body.Messages[1].Role != "assistant" || body.Messages[1].Content != "a" {
		t.Fatalf("msg[1] = %+v", body.Messages[1])
	}
	blocks, ok := body.Messages[2].Content.([]toolResultBlock)
	if body.Messages[2].Role != "user" || !ok || len(blocks) != 1 || blocks[0].ToolUseID != "t1" || blocks[0].Content != "result" || blocks[0].IsError {
		t.Fatalf("msg[2] = %+v", body.Messages[2])
	}
}

func TestChatReadsTextBlocksToolCallsAndUsageWithCacheTokens(t *testing.T) {
	rec := newRecorder(t, jsonReply(fixture(t, "message.json")))
	a := Adapter{Client: rec.srv.Client()}
	res, class, err := a.Chat(context.Background(), attemptFor(rec), hi())
	if err != nil || class != domain.ClassOK {
		t.Fatalf("class=%s err=%v", class, err)
	}
	if res.Text != "Hello, world" {
		t.Fatalf("text = %q", res.Text)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].ID != "toolu_01" || res.ToolCalls[0].Name != "search" || string(res.ToolCalls[0].Arguments) != `{"q": "x"}` {
		t.Fatalf("tool_calls = %+v", res.ToolCalls)
	}
	want := domain.Usage{Arrived: true, Input: 10, Output: 5, CacheRead: 3, CacheWrite: 2}
	if res.Usage != want {
		t.Fatalf("usage = %+v, want %+v", res.Usage, want)
	}
}

func TestChatWithoutUsageBlockLeavesItNotArrived(t *testing.T) {
	rec := newRecorder(t, jsonReply([]byte(`{"content":[{"type":"text","text":"hi"}]}`)))
	a := Adapter{Client: rec.srv.Client()}
	res, class, err := a.Chat(context.Background(), attemptFor(rec), hi())
	if err != nil || class != domain.ClassOK || res.Text != "hi" || res.Usage.Arrived {
		t.Fatalf("res=%+v class=%s err=%v", res, class, err)
	}
}

func TestChatAcceptsAnAnswerWithNoTextBlocks(t *testing.T) {
	for name, body := range map[string]string{
		"empty content": `{"content":[],"usage":{"input_tokens":3,"output_tokens":0}}`,
		"tool use only": `{"content":[{"type":"tool_use","id":"t","name":"n","input":{}}],"usage":{"input_tokens":3,"output_tokens":9}}`,
	} {
		rec := newRecorder(t, jsonReply([]byte(body)))
		a := Adapter{Client: rec.srv.Client()}
		res, class, err := a.Chat(context.Background(), attemptFor(rec), hi())
		if err != nil || class != domain.ClassOK || res.Text != "" || !res.Usage.Arrived || res.Usage.Input != 3 {
			t.Errorf("%s: res=%+v class=%s err=%v", name, res, class, err)
		}
	}
}

func TestChatRefusesAnAnswerItCannotRead(t *testing.T) {
	for name, body := range map[string]string{
		"not json":      `<html>`,
		"no content":    `{"content":[]}`,
		"missing field": `{}`,
	} {
		rec := newRecorder(t, jsonReply([]byte(body)))
		a := Adapter{Client: rec.srv.Client()}
		_, class, err := a.Chat(context.Background(), attemptFor(rec), hi())
		if err == nil || class != domain.ClassCaller {
			t.Errorf("%s: class=%s err=%v", name, class, err)
		}
	}
}

// statusCases is how each status the API answers with is classed.
var statusCases = map[int]domain.FailureClass{
	400: domain.ClassCaller,
	401: domain.ClassCredential,
	403: domain.ClassCredential,
	404: domain.ClassModelAccess,
	413: domain.ClassCaller,
	429: domain.ClassRateLimit,
	500: domain.ClassTransient,
	502: domain.ClassTransient,
	503: domain.ClassTransient,
	529: domain.ClassOverloaded,
}

func TestChatMapsStatusToClass(t *testing.T) {
	for code, want := range statusCases {
		rec := newRecorder(t, statusReply(code))
		a := Adapter{Client: rec.srv.Client()}
		_, class, err := a.Chat(context.Background(), attemptFor(rec), hi())
		if err == nil || class != want {
			t.Errorf("status %d: class=%s err=%v, want %s", code, class, err, want)
		}
	}
}

func TestChatStreamMapsStatusBeforeTheFirstEvent(t *testing.T) {
	for code, want := range statusCases {
		rec := newRecorder(t, statusReply(code))
		a := Adapter{Client: rec.srv.Client()}
		_, class, err := a.ChatStream(context.Background(), attemptFor(rec), hi(), func(string) error { return nil })
		if err == nil || class != want {
			t.Errorf("status %d: class=%s err=%v, want %s", code, class, err, want)
		}
	}
}

func TestProbeAsksForTheModelListWithTheKey(t *testing.T) {
	rec := newRecorder(t, jsonReply([]byte(`{"data":[],"has_more":false}`)))
	a := Adapter{Client: rec.srv.Client()}
	models, class, err := a.Probe(context.Background(), attemptFor(rec))
	if err != nil || class != domain.ClassOK || models == nil || len(models) != 0 {
		t.Fatalf("models=%+v class=%s err=%v", models, class, err)
	}
	call := rec.last(t)
	if call.Method != http.MethodGet || call.Path != "/v1/models" {
		t.Fatalf("call = %s %s", call.Method, call.Path)
	}
	if call.Key != "s" || call.Version != "2023-06-01" || call.Auth != "" || call.Type != "" || call.Raw != "" {
		t.Fatalf("call = %+v", call)
	}
}

func TestProbeParsesRecordedModelsAndRejectsBadLists(t *testing.T) {
	rec := newRecorder(t, jsonReply(fixture(t, "models.json")))
	a := Adapter{Client: rec.srv.Client()}
	models, class, err := a.Probe(context.Background(), attemptFor(rec))
	if err != nil || class != domain.ClassOK {
		t.Fatalf("class=%s err=%v", class, err)
	}
	if len(models) != 2 {
		t.Fatalf("models len = %d, want 2: %+v", len(models), models)
	}
	if models[0].ID != "claude-sonnet-4-20250514" || models[0].DisplayName != "Claude Sonnet 4" || models[0].Created != "2025-05-14T00:00:00Z" {
		t.Fatalf("first model = %+v", models[0])
	}
	if models[1].ID != "claude-3-5-haiku-20241022" || models[1].DisplayName != "Claude 3.5 Haiku" || models[1].Created != "2024-10-22T00:00:00Z" {
		t.Fatalf("second model = %+v", models[1])
	}

	tooMany, _ := json.Marshal(map[string]any{
		"data": func() []map[string]string {
			items := make([]map[string]string, domain.MaxSeenModels+1)
			for i := range items {
				items[i] = map[string]string{"type": "model", "id": "m-" + string(rune('a'+i%26)) + "-" + strings.Repeat("x", i/26)}
			}
			return items
		}(),
	})
	badCases := []struct {
		name string
		body []byte
	}{
		{"empty body", nil},
		{"malformed json", []byte("{bad")},
		{"missing data field", []byte(`{"models":[]}`)},
		{"null data", []byte(`{"data":null}`)},
		{"array top level", []byte(`[{"id":"m"}]`)},
		{"trailing garbage", []byte(`{"data":[]}garbage`)},
		{"unexpected type", []byte(`{"data":[{"type":"other","id":"m"}]}`)},
		{"empty id", []byte(`{"data":[{"type":"model","id":""}]}`)},
		{"bad created_at", []byte(`{"data":[{"type":"model","id":"m","created_at":"not-a-date"}]}`)},
		{"oversized body", append([]byte(`{"data":[{"type":"model","id":"claude-x"}]`), append(bytes.Repeat([]byte{' '}, 4<<20), '}')...)},
		{"too many entries", tooMany},
	}
	for _, tc := range badCases {
		t.Run(tc.name, func(t *testing.T) {
			badRec := newRecorder(t, jsonReply(tc.body))
			got, c, e := Adapter{Client: badRec.srv.Client()}.Probe(context.Background(), attemptFor(badRec))
			if e != nil || c != domain.ClassOK || got != nil {
				t.Fatalf("got=%+v class=%s err=%v, want nil models with ClassOK", got, c, e)
			}
		})
	}
}

func TestProbeMapsStatusToClass(t *testing.T) {
	cases := map[int]domain.FailureClass{200: domain.ClassOK}
	for code, class := range statusCases {
		cases[code] = class
	}
	for code, want := range cases {
		rec := newRecorder(t, statusReply(code))
		_, class, err := Adapter{Client: rec.srv.Client()}.Probe(context.Background(), attemptFor(rec))
		if class != want || (class == domain.ClassOK) != (err == nil) {
			t.Errorf("status %d: class=%s err=%v, want %s", code, class, err, want)
		}
	}
}

func TestEveryCallRefusesAnEmptyBaseURL(t *testing.T) {
	a := Adapter{}
	ctx := context.Background()
	if _, class, err := a.Probe(ctx, app.Attempt{}); err == nil || class != domain.ClassCaller {
		t.Errorf("probe: class=%s err=%v", class, err)
	}
	if _, class, err := a.Chat(ctx, app.Attempt{}, hi()); err == nil || class != domain.ClassCaller {
		t.Errorf("chat: class=%s err=%v", class, err)
	}
	if _, class, err := a.ChatStream(ctx, app.Attempt{}, hi(), func(string) error { return nil }); err == nil || class != domain.ClassCaller {
		t.Errorf("stream: class=%s err=%v", class, err)
	}
}

func TestChatStreamSendsTheStreamFlagAndCap(t *testing.T) {
	rec := newRecorder(t, streamReply(fixture(t, "stream_text.sse")))
	a := Adapter{Client: rec.srv.Client()}
	req := hi()
	req.MaxOutputTokens = 9
	if _, class, err := a.ChatStream(context.Background(), attemptFor(rec), req, func(string) error { return nil }); err != nil || class != domain.ClassOK {
		t.Fatalf("class=%s err=%v", class, err)
	}
	call := rec.last(t)
	if call.Path != "/v1/messages" || call.Key != "s" || call.Version != "2023-06-01" {
		t.Fatalf("call = %+v", call)
	}
	if call.Body["stream"] != true || call.Body["max_tokens"] != float64(9) {
		t.Fatalf("body = %s", call.Raw)
	}
}

func TestChatStreamTextInOrderWithUsageIncludingCacheTokens(t *testing.T) {
	rec := newRecorder(t, streamReply(fixture(t, "stream_text.sse")))
	a := Adapter{Client: rec.srv.Client()}
	var got []string
	usage, class, err := a.ChatStream(context.Background(), attemptFor(rec), hi(), func(text string) error {
		got = append(got, text)
		return nil
	})
	if err != nil || class != domain.ClassOK {
		t.Fatalf("class=%s err=%v", class, err)
	}
	if strings.Join(got, "|") != "Hel|lo" {
		t.Fatalf("deltas = %v", got)
	}
	// Input and the cache counts come from message_start, the output count
	// from message_delta, which carries the total.
	want := domain.Usage{Arrived: true, Input: 25, Output: 15, CacheRead: 100, CacheWrite: 7}
	if usage != want {
		t.Fatalf("usage = %+v, want %+v", usage, want)
	}
}

func TestChatStreamDeltaWithOnlyOutputKeepsTheStartCounts(t *testing.T) {
	// message_delta carries only the output count; the input and cache counts
	// of message_start must survive it.
	body := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":25,\"cache_creation_input_tokens\":7,\"cache_read_input_tokens\":100,\"output_tokens\":1}}}\n\n" +
		"event: message_delta\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":15}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	usage, class, err := readStream(context.Background(), strings.NewReader(body), func(string) error { return nil })
	if err != nil || class != domain.ClassOK {
		t.Fatalf("class=%s err=%v", class, err)
	}
	want := domain.Usage{Arrived: true, Input: 25, Output: 15, CacheRead: 100, CacheWrite: 7}
	if usage != want {
		t.Fatalf("usage = %+v, want %+v", usage, want)
	}
}

func TestChatStreamDeltaWithoutCacheBreakdownPreservesOneHourWrite(t *testing.T) {
	body := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":25,\"cache_creation_input_tokens\":18,\"cache_creation\":{\"ephemeral_5m_input_tokens\":7,\"ephemeral_1h_input_tokens\":11}}}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{},\"usage\":{\"cache_creation_input_tokens\":18}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	usage, _, err := readStream(context.Background(), strings.NewReader(body), func(string) error { return nil })
	if err != nil || usage.CacheWrite != 18 || usage.CacheWrite1h != 11 {
		t.Fatalf("usage=%+v err=%v", usage, err)
	}
}

func TestChatStreamCarriesCacheCreationBreakdownThroughAdapter(t *testing.T) {
	body := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":25,\"cache_creation_input_tokens\":18,\"cache_creation\":{\"ephemeral_5m_input_tokens\":7,\"ephemeral_1h_input_tokens\":11}}}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	rec := newRecorder(t, streamReply([]byte(body)))
	a := Adapter{Client: rec.srv.Client()}
	usage, class, err := a.ChatStream(context.Background(), attemptFor(rec), hi(), func(string) error { return nil })
	if err != nil || class != domain.ClassOK || usage.CacheWrite != 18 || usage.CacheWrite1h != 11 {
		t.Fatalf("usage=%+v class=%s err=%v", usage, class, err)
	}
}

func TestChatStreamToolsCarriesCacheCreationBreakdownThroughAdapter(t *testing.T) {
	body := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":25,\"cache_creation_input_tokens\":18,\"cache_creation\":{\"ephemeral_5m_input_tokens\":7,\"ephemeral_1h_input_tokens\":11}}}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	rec := newRecorder(t, streamReply([]byte(body)))
	a := Adapter{Client: rec.srv.Client()}
	result, class, err := a.ChatStreamTools(context.Background(), attemptFor(rec), hi(), app.StreamEvents{})
	if err != nil || class != domain.ClassOK || result.Usage.CacheWrite != 18 || result.Usage.CacheWrite1h != 11 {
		t.Fatalf("usage=%+v class=%s err=%v", result.Usage, class, err)
	}
}

func TestChatStreamCutKeepsOneHourCacheBreakdown(t *testing.T) {
	body := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":25,\"cache_creation\":{\"ephemeral_5m_input_tokens\":7,\"ephemeral_1h_input_tokens\":11}}}}\n\n"
	usage, class, err := readStream(context.Background(), strings.NewReader(body), func(string) error { return nil })
	if err == nil || class != domain.ClassTransient || usage.CacheWrite != 18 || usage.CacheWrite1h != 11 {
		t.Fatalf("usage=%+v class=%s err=%v", usage, class, err)
	}
}

func TestChatStreamSkipsPingsNonTextBlocksAndUnknownEvents(t *testing.T) {
	rec := newRecorder(t, streamReply(fixture(t, "stream_blocks.sse")))
	a := Adapter{Client: rec.srv.Client()}
	var got []string
	usage, class, err := a.ChatStream(context.Background(), attemptFor(rec), hi(), func(text string) error {
		got = append(got, text)
		return nil
	})
	if err != nil || class != domain.ClassOK {
		t.Fatalf("class=%s err=%v", class, err)
	}
	if strings.Join(got, "|") != "Sure" {
		t.Fatalf("deltas = %v", got)
	}
	// Null cache counts leave the cache fields at zero.
	want := domain.Usage{Arrived: true, Input: 40, Output: 30}
	if usage != want {
		t.Fatalf("usage = %+v, want %+v", usage, want)
	}
}

func TestChatStreamErrorEventEndsTheStreamWithItsClass(t *testing.T) {
	rec := newRecorder(t, streamReply(fixture(t, "stream_error.sse")))
	a := Adapter{Client: rec.srv.Client()}
	var got []string
	usage, class, err := a.ChatStream(context.Background(), attemptFor(rec), hi(), func(text string) error {
		got = append(got, text)
		return nil
	})
	if err == nil || class != domain.ClassOverloaded {
		t.Fatalf("class=%s err=%v", class, err)
	}
	if strings.Contains(err.Error(), "Overloaded") {
		t.Fatalf("the provider's text is in the error: %v", err)
	}
	if strings.Join(got, "|") != "Hel" {
		t.Fatalf("deltas = %v", got)
	}
	if want := (domain.Usage{Arrived: true, Input: 25, Output: 1}); usage != want {
		t.Fatalf("usage = %+v, want %+v", usage, want)
	}
}

func TestStreamErrorTypesMapToClasses(t *testing.T) {
	cases := map[string]domain.FailureClass{
		"overloaded_error":      domain.ClassOverloaded,
		"rate_limit_error":      domain.ClassRateLimit,
		"authentication_error":  domain.ClassCredential,
		"permission_error":      domain.ClassCredential,
		"not_found_error":       domain.ClassModelAccess,
		"invalid_request_error": domain.ClassCaller,
		"request_too_large":     domain.ClassCaller,
		"api_error":             domain.ClassTransient,
		"timeout_error":         domain.ClassTransient,
		"something_new":         domain.ClassTransient,
	}
	for kind, want := range cases {
		body := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"" + kind + "\",\"message\":\"x\"}}\n\n"
		_, class, err := readStream(context.Background(), strings.NewReader(body), func(string) error { return nil })
		if err == nil || class != want {
			t.Errorf("%s: class=%s err=%v, want %s", kind, class, err, want)
		}
	}
}

func TestChatStreamCutBeforeMessageStopKeepsPartialTextAndIsTransient(t *testing.T) {
	// The text arrived, then the connection closed with no message_stop.
	body := string(fixture(t, "stream_text.sse"))
	cut := body[:strings.Index(body, "event: content_block_stop")]
	var got []string
	usage, class, err := readStream(context.Background(), strings.NewReader(cut), func(text string) error {
		got = append(got, text)
		return nil
	})
	if err == nil || class != domain.ClassTransient {
		t.Fatalf("class=%s err=%v", class, err)
	}
	if strings.Join(got, "|") != "Hel|lo" {
		t.Fatalf("deltas = %v", got)
	}
	if !usage.Arrived || usage.Input != 25 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestChatStreamAbortedConnectionIsTransient(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hel\"}}\n\n")
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	}))
	t.Cleanup(srv.Close)
	a := Adapter{Client: srv.Client()}
	_, class, err := a.ChatStream(context.Background(), app.Attempt{BaseURL: srv.URL, Model: "m", Secret: "s"}, hi(), func(text string) error {
		got = append(got, text)
		return nil
	})
	if err == nil || class != domain.ClassTransient {
		t.Fatalf("class=%s err=%v", class, err)
	}
	if len(got) != 1 || got[0] != "Hel" {
		t.Fatalf("deltas = %v", got)
	}
}

func TestChatStreamStopsWhenEmitFails(t *testing.T) {
	gone := errors.New("caller went away")
	rec := newRecorder(t, streamReply(fixture(t, "stream_text.sse")))
	a := Adapter{Client: rec.srv.Client()}
	_, class, err := a.ChatStream(context.Background(), attemptFor(rec), hi(), func(string) error { return gone })
	if !errors.Is(err, gone) || class != domain.ClassCancelled {
		t.Fatalf("class=%s err=%v", class, err)
	}
}

func TestChatStreamStopsOnCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hel\"}}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	a := Adapter{Client: srv.Client()}
	var got []string
	_, class, err := a.ChatStream(ctx, app.Attempt{BaseURL: srv.URL, Model: "m", Secret: "s"}, hi(), func(text string) error {
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

func TestReadStreamEndedWhileCancelledIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body := strings.NewReader("event: ping\ndata: {\"type\":\"ping\"}\n\n")
	_, class, err := readStream(ctx, body, func(string) error { return nil })
	if err == nil || class != domain.ClassCancelled {
		t.Fatalf("class=%s err=%v", class, err)
	}
}

func TestReadStreamRefusesAPayloadItCannotRead(t *testing.T) {
	_, class, err := readStream(context.Background(), strings.NewReader("data: {not json\n\n"), func(string) error { return nil })
	if err == nil || class != domain.ClassCaller {
		t.Fatalf("class=%s err=%v", class, err)
	}
}

func TestReadStreamDispatchesAPendingEventAtTheEndOfTheBody(t *testing.T) {
	// No blank line follows the last event.
	var got []string
	body := "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"a\"}}\n\n" +
		"data: {\"type\":\"message_stop\"}"
	_, class, err := readStream(context.Background(), strings.NewReader(body), func(text string) error {
		got = append(got, text)
		return nil
	})
	if err != nil || class != domain.ClassOK || strings.Join(got, "|") != "a" {
		t.Fatalf("class=%s err=%v deltas=%v", class, err, got)
	}
}

func decideServer(t *testing.T, answer string) (*recorder, Adapter) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": answer}}})
	if err != nil {
		t.Fatal(err)
	}
	rec := newRecorder(t, jsonReply(body))
	return rec, Adapter{Client: rec.srv.Client()}
}

func TestDecideYesNo(t *testing.T) {
	rec, a := decideServer(t, "Yes")
	got, _, class, err := a.Decide(context.Background(), attemptFor(rec), domain.DecideRequest{Kind: domain.DecisionYesNo, Prompt: "keep?"})
	if err != nil || class != domain.ClassOK || !got.YesNo {
		t.Fatalf("got=%v class=%s err=%v", got, class, err)
	}
	msgs, _ := rec.last(t).Body["messages"].([]any)
	if msg, _ := msgs[0].(map[string]any); !strings.HasPrefix(msg["content"].(string), "keep?") || !strings.Contains(msg["content"].(string), "yes or no") {
		t.Fatalf("prompt = %v", msgs[0])
	}
}

func TestDecideChoice(t *testing.T) {
	rec, a := decideServer(t, "beta")
	got, _, class, err := a.Decide(context.Background(), attemptFor(rec), domain.DecideRequest{
		Kind: domain.DecisionChoice, Prompt: "which?", Options: []string{"alpha", "beta"},
	})
	if err != nil || class != domain.ClassOK || got.Choice != "beta" {
		t.Fatalf("got=%v class=%s err=%v", got, class, err)
	}
}

func TestDecideAnAnswerOutsideTheOptionsIsACallerFault(t *testing.T) {
	rec, a := decideServer(t, "gamma")
	_, _, class, err := a.Decide(context.Background(), attemptFor(rec), domain.DecideRequest{
		Kind: domain.DecisionChoice, Prompt: "which?", Options: []string{"alpha", "beta"},
	})
	if err == nil || class != domain.ClassCaller {
		t.Fatalf("class=%s err=%v", class, err)
	}
	rec, a = decideServer(t, "maybe")
	_, _, class, err = a.Decide(context.Background(), attemptFor(rec), domain.DecideRequest{Kind: domain.DecisionYesNo, Prompt: "keep?"})
	if err == nil || class != domain.ClassCaller {
		t.Fatalf("yesno: class=%s err=%v", class, err)
	}
}

func TestDecideMapsStatusToClass(t *testing.T) {
	rec := newRecorder(t, statusReply(http.StatusTooManyRequests))
	_, _, class, err := Adapter{Client: rec.srv.Client()}.Decide(context.Background(), attemptFor(rec),
		domain.DecideRequest{Kind: domain.DecisionYesNo, Prompt: "keep?"})
	if err == nil || class != domain.ClassRateLimit {
		t.Fatalf("class=%s err=%v", class, err)
	}
}

func TestDefaultClientDialsByPlan(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"content": []map[string]string{{"type": "text", "text": "hi"}}})
	}))
	t.Cleanup(srv.Close)
	chat := func(plan string) (domain.FailureClass, error) {
		_, class, err := Adapter{}.Chat(context.Background(),
			app.Attempt{BaseURL: srv.URL, Model: "m", Secret: "s", PlanKind: plan}, hi())
		return class, err
	}
	if class, err := chat(domain.PlanLocal); err != nil || class != domain.ClassOK {
		t.Fatalf("local plan: %q, %v", class, err)
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

func TestRedirectIsNotFollowedSoTheKeyStaysPut(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(target.Close)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/models", http.StatusFound)
	}))
	t.Cleanup(front.Close)
	_, class, err := Adapter{}.Probe(context.Background(), app.Attempt{BaseURL: front.URL, Secret: "s", PlanKind: domain.PlanLocal})
	if err == nil || class == domain.ClassOK {
		t.Fatalf("class=%q err=%v", class, err)
	}
	if hits.Load() != 0 {
		t.Fatal("the redirect was followed with the key")
	}
}

func TestBuildPayloadSerializesAssistantToolCallsMergedToolResultsAndToolChoice(t *testing.T) {
	req, err := domain.Build(domain.Input{
		Tools: []domain.Tool{{Name: "search"}, {Name: "fetch"}},
		Messages: []domain.Message{
			{Role: domain.RoleUser, Text: "find both"},
			{
				Role: domain.RoleAssistant,
				Text: "calling both",
				ToolCalls: []domain.ToolCall{
					{ID: "t1", Name: "search", Arguments: json.RawMessage(`{"q":"a"}`)},
					{ID: "t2", Name: "fetch", Arguments: json.RawMessage(`{"id":"b"}`)},
				},
			},
			{Role: domain.RoleTool, ToolID: "t1", ToolName: "search", Text: "ok"},
			{Role: domain.RoleTool, ToolID: "t2", ToolName: "fetch", Text: "boom", IsError: true},
		},
		ToolChoice: domain.ToolChoice{Mode: domain.ToolChoiceTool, Name: "search"},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(buildPayload("claude-test", req, false))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		ToolChoice struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"tool_choice"`
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.ToolChoice.Type != "tool" || parsed.ToolChoice.Name != "search" {
		t.Fatalf("tool_choice = %+v", parsed.ToolChoice)
	}
	if len(parsed.Messages) != 3 {
		t.Fatalf("expected consecutive tool results merged into 3 messages, got %d: %s", len(parsed.Messages), raw)
	}
	assistantBlocks, _ := parsed.Messages[1].Content.([]any)
	if len(assistantBlocks) != 3 {
		t.Fatalf("assistant blocks = %v", assistantBlocks)
	}
	toolBlocks, _ := parsed.Messages[2].Content.([]any)
	if parsed.Messages[2].Role != "user" || len(toolBlocks) != 2 {
		t.Fatalf("merged tool_result message = %+v", parsed.Messages[2])
	}
	second, _ := toolBlocks[1].(map[string]any)
	if second["tool_use_id"] != "t2" || second["content"] != "boom" || second["is_error"] != true {
		t.Fatalf("second tool_result = %v", second)
	}
	for mode, want := range map[string]string{
		domain.ToolChoiceAuto: "auto",
		domain.ToolChoiceAny:  "any",
		domain.ToolChoiceNone: "none",
	} {
		got := encodeToolChoice(domain.ToolChoice{Mode: mode})
		if got == nil || got.Type != want {
			t.Errorf("mode %q = %+v, want %q", mode, got, want)
		}
	}
}

func TestChatStreamToolsReadsToolUseDeltasAndMultiToolCalls(t *testing.T) {
	rec := newRecorder(t, streamReply(fixture(t, "stream_blocks.sse")))
	a := Adapter{Client: rec.srv.Client()}
	var text strings.Builder
	var deltas []app.ToolCallDelta
	res, class, err := a.ChatStreamTools(context.Background(), attemptFor(rec), hi(), app.StreamEvents{
		Text: func(s string) error {
			text.WriteString(s)
			return nil
		},
		ToolCall: func(d app.ToolCallDelta) error {
			deltas = append(deltas, d)
			return nil
		},
	})
	if err != nil || class != domain.ClassOK {
		t.Fatalf("class=%s err=%v", class, err)
	}
	if text.String() != "Sure" || res.StopReason != "tool_use" {
		t.Fatalf("text=%q stop=%q", text.String(), res.StopReason)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].ID != "toolu_01" || res.ToolCalls[0].Name != "search" || string(res.ToolCalls[0].Arguments) != `{"q": "x"}` {
		t.Fatalf("tool_calls = %+v", res.ToolCalls)
	}
	if len(deltas) != 2 || deltas[0].Index != 0 || deltas[0].ID != "toolu_01" || deltas[1].Index != 0 || deltas[1].ArgumentsDelta != `{"q": "x"}` {
		t.Fatalf("deltas = %+v", deltas)
	}
}

func TestDecideReturnsUsage(t *testing.T) {
	body := `{"content":[{"type":"text","text":"yes"}],"usage":{"input_tokens":12,"output_tokens":1}}`
	rec := newRecorder(t, jsonReply([]byte(body)))
	a := Adapter{Client: rec.srv.Client()}
	got, usage, class, err := a.Decide(context.Background(), attemptFor(rec), domain.DecideRequest{Kind: domain.DecisionYesNo, Prompt: "keep?"})
	if err != nil || class != domain.ClassOK || !got.YesNo {
		t.Fatalf("got=%+v class=%s err=%v", got, class, err)
	}
	if !usage.Arrived || usage.Input != 12 || usage.Output != 1 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestAnthropicUsage_DoesNotSubtractCacheFromInput(t *testing.T) {
	body := `{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":20}}`
	rec := newRecorder(t, jsonReply([]byte(body)))
	a := Adapter{Client: rec.srv.Client()}
	res, class, err := a.Chat(context.Background(), attemptFor(rec), hi())
	if err != nil || class != domain.ClassOK {
		t.Fatalf("class=%s err=%v", class, err)
	}
	if res.Usage.Input != 10 || res.Usage.CacheRead != 100 || res.Usage.CacheWrite != 20 || res.Usage.Output != 5 {
		t.Fatalf("anthropic usage = %+v", res.Usage)
	}
}

func TestUsageBlockCacheCreationBreakdown(t *testing.T) {
	var block usageBlock
	if err := json.Unmarshal([]byte(`{"input_tokens":10,"cache_creation_input_tokens":99,"cache_creation":{"ephemeral_5m_input_tokens":7,"ephemeral_1h_input_tokens":11}}`), &block); err != nil {
		t.Fatal(err)
	}
	var usage domain.Usage
	block.into(&usage)
	if usage.CacheWrite != 18 || usage.CacheWrite1h != 11 || usage.Tokens() != 28 {
		t.Fatalf("usage = %+v, want total cache write 18, 1h 11, tokens 28", usage)
	}
}

func TestChatCompleteReturnsCacheCreationBreakdown(t *testing.T) {
	body := `{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":10,"output_tokens":5,"cache_creation_input_tokens":18,"cache_creation":{"ephemeral_5m_input_tokens":7,"ephemeral_1h_input_tokens":11}}}`
	rec := newRecorder(t, jsonReply([]byte(body)))
	a := Adapter{Client: rec.srv.Client()}
	res, class, err := a.Chat(context.Background(), attemptFor(rec), hi())
	if err != nil || class != domain.ClassOK || res.Usage.CacheWrite != 18 || res.Usage.CacheWrite1h != 11 {
		t.Fatalf("usage=%+v class=%s err=%v", res.Usage, class, err)
	}
}

func TestUsageBlockEmptyBreakdownPreservesExistingCacheWritesAndClampsNegative(t *testing.T) {
	var empty domain.Usage
	var block usageBlock
	if err := json.Unmarshal([]byte(`{"cache_creation_input_tokens":18,"cache_creation":{}}`), &block); err != nil {
		t.Fatal(err)
	}
	block.into(&empty)
	if empty.CacheWrite != 18 {
		t.Fatalf("empty breakdown fallback count = %d, want 18", empty.CacheWrite)
	}
	usage := domain.Usage{CacheWrite: 18, CacheWrite1h: 11}
	if err := json.Unmarshal([]byte(`{"cache_creation_input_tokens":18,"cache_creation":{}}`), &block); err != nil {
		t.Fatal(err)
	}
	block.into(&usage)
	if usage.CacheWrite != 18 || usage.CacheWrite1h != 11 {
		t.Fatalf("empty breakdown erased counts: %+v", usage)
	}
	var negative domain.Usage
	if err := json.Unmarshal([]byte(`{"cache_creation_input_tokens":-4}`), &block); err != nil {
		t.Fatal(err)
	}
	block.into(&negative)
	if negative.CacheWrite != 0 || negative.CacheWrite1h != 0 {
		t.Fatalf("negative cache-creation fallback = %+v, want clamped zeros", negative)
	}
}
