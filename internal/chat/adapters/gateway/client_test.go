package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/platform/svcsig"
)

func TestChatStreamParsesAttemptDeltaAndDoneFrames(t *testing.T) {
	secret := serviceKey(t, 'k')
	var gotRoute, gotRef, gotUser, gotPrompt string
	srv := httptest.NewServer(mustGateway(t, secret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gotRoute, _ = body["route"].(string)
		gotRef, _ = body["credential_ref"].(string)
		gotUser, _ = body["user_id"].(string)
		gotPrompt, _ = body["prompt"].(string)
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = fmt.Fprintf(w, "%s\n%s\n%s\n%s\n",
			`{"attempt":{"failed_model":"m1","failure_class":"transient","next_model":"m2"}}`,
			`{"delta":"one "}`,
			`{"delta":"two"}`,
			`{"done":{"model":"m2","input_tokens":1,"output_tokens":2,"cost_micro_usd":10000}}`)
	})))
	t.Cleanup(srv.Close)
	client, err := Open(srv.URL, secret)
	if err != nil {
		t.Fatal(err)
	}

	var order []string
	var text strings.Builder
	var attempts []Attempt
	done, err := client.ChatStream(t.Context(), Request{
		Route:         "draft",
		ChatID:        "chat-1",
		CredentialRef: "house",
		UserID:        "u",
		Prompt:        "hi",
	}, func(delta string) {
		text.WriteString(delta)
		order = append(order, "delta")
	}, func(a Attempt) {
		attempts = append(attempts, a)
		order = append(order, "attempt")
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotRoute != "draft" || gotRef != "house" || gotUser != "u" || gotPrompt != "hi" {
		t.Fatalf("body route=%s ref=%s user=%s prompt=%s", gotRoute, gotRef, gotUser, gotPrompt)
	}
	if len(order) != 3 || order[0] != "attempt" || order[1] != "delta" || order[2] != "delta" {
		t.Fatalf("order = %v", order)
	}
	if len(attempts) != 1 || attempts[0] != (Attempt{FailedModel: "m1", FailureClass: "transient", NextModel: "m2"}) {
		t.Fatalf("attempts = %+v", attempts)
	}
	if text.String() != "one two" {
		t.Fatalf("text = %q", text.String())
	}
	if done.Model != "m2" || done.InputTokens != 1 || done.OutputTokens != 2 || done.CostMicroUSD == nil || *done.CostMicroUSD != 10000 {
		t.Fatalf("done = %+v", done)
	}
}

func TestChatStreamSendsTheSystemTextAndAPinByAccountOrByProvider(t *testing.T) {
	secret := serviceKey(t, 'k')
	var bodies []map[string]any
	srv := httptest.NewServer(mustGateway(t, secret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
		_, _ = fmt.Fprintf(w, "%s\n", `{"done":{"model":"m","input_tokens":1,"output_tokens":1}}`)
	})))
	t.Cleanup(srv.Close)
	client, err := Open(srv.URL, secret)
	if err != nil {
		t.Fatal(err)
	}
	const account = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaa01"
	for _, req := range []Request{
		{Route: "r", ChatID: "chat-1", UserID: "u", Prompt: "hi", System: "# Agent: A\n\nBe brief.", PinProvider: "OpenAI", PinModel: "gpt-x"},
		{Route: "r", ChatID: "chat-2", UserID: "u", Prompt: "hi", PinAccount: account, PinModel: "gpt-y"},
		{Route: "r", ChatID: "chat-3", UserID: "u", Prompt: "hi"},
	} {
		if _, err := client.ChatStream(t.Context(), req, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if bodies[0]["system"] != "# Agent: A\n\nBe brief." || !reflect.DeepEqual(bodies[0]["pin"], map[string]any{"provider": "OpenAI", "model": "gpt-x"}) {
		t.Fatalf("provider pin body = %+v", bodies[0])
	}
	if _, has := bodies[1]["system"]; has || !reflect.DeepEqual(bodies[1]["pin"], map[string]any{"account_id": account, "model": "gpt-y"}) {
		t.Fatalf("account pin body = %+v", bodies[1])
	}
	if _, has := bodies[2]["pin"]; has {
		t.Fatalf("no pin body = %+v", bodies[2])
	}
}

func TestChatStreamHandsADeltaOverBeforeTheFinalFrame(t *testing.T) {
	secret := serviceKey(t, 'k')
	release := make(chan struct{})
	srv := httptest.NewServer(mustGateway(t, secret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = fmt.Fprintf(w, "%s\n", `{"delta":"one "}`)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// The final frame waits: the delta above must reach the caller now.
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = fmt.Fprintf(w, "%s\n%s\n", `{"delta":"two"}`, `{"done":{"model":"m2","input_tokens":1,"output_tokens":2}}`)
	})))
	t.Cleanup(srv.Close)
	client, err := Open(srv.URL, secret)
	if err != nil {
		t.Fatal(err)
	}

	first := make(chan string, 1)
	type result struct {
		done Done
		err  error
	}
	out := make(chan result, 1)
	go func() {
		var text strings.Builder
		done, err := client.ChatStream(t.Context(), Request{Route: "draft", ChatID: "chat-1", UserID: "u", Prompt: "hi"}, func(delta string) {
			text.WriteString(delta)
			select {
			case first <- delta:
			default:
			}
		}, nil)
		if err == nil && text.String() != "one two" {
			err = fmt.Errorf("text = %q", text.String())
		}
		out <- result{done, err}
	}()
	select {
	case delta := <-first:
		if delta != "one " {
			t.Fatalf("first delta = %q", delta)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no delta arrived")
	}
	// The final frame is still unwritten, so the reply cannot be finished.
	select {
	case res := <-out:
		t.Fatalf("the reply finished with the first delta: %+v %v", res.done, res.err)
	case <-time.After(60 * time.Millisecond):
	}
	close(release)
	res := <-out
	if res.err != nil {
		t.Fatal(res.err)
	}
	if res.done.Model != "m2" {
		t.Fatalf("done = %+v", res.done)
	}
}

func TestChatStreamErrorFrameIsAFailure(t *testing.T) {
	secret := serviceKey(t, 'k')
	srv := httptest.NewServer(mustGateway(t, secret, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = fmt.Fprintf(w, "%s\n%s\n", `{"delta":"one "}`, `{"error":{"failure_class":"transient","model":"m1","message":"boom"}}`)
	})))
	t.Cleanup(srv.Close)
	client, err := Open(srv.URL, secret)
	if err != nil {
		t.Fatal(err)
	}

	var text strings.Builder
	done, err := client.ChatStream(t.Context(), Request{Route: "draft", ChatID: "chat-1", UserID: "u", Prompt: "hi"}, func(delta string) {
		text.WriteString(delta)
	}, nil)
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("err = %v", err)
	}
	if failure.Class != "transient" || failure.Model != "m1" {
		t.Fatalf("failure = %+v", failure)
	}
	if text.String() != "one " || done.Model != "" {
		t.Fatalf("text = %q done = %+v", text.String(), done)
	}
}

func TestChatStreamCutWithoutAFinalFrame(t *testing.T) {
	secret := serviceKey(t, 'k')
	srv := httptest.NewServer(mustGateway(t, secret, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = fmt.Fprintf(w, "%s\n", `{"delta":"x"}`)
	})))
	t.Cleanup(srv.Close)
	client, err := Open(srv.URL, secret)
	if err != nil {
		t.Fatal(err)
	}

	deltas := 0
	_, err = client.ChatStream(t.Context(), Request{Route: "draft", ChatID: "chat-1", UserID: "u", Prompt: "hi"}, func(string) {
		deltas++
	}, nil)
	if !errors.Is(err, ErrCut) || deltas != 1 {
		t.Fatalf("err = %v deltas = %d", err, deltas)
	}
}

func TestChatStreamRefusalBeforeTheStreamIsAPlainError(t *testing.T) {
	secret := serviceKey(t, 'k')
	srv := httptest.NewServer(mustGateway(t, secret, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	})))
	t.Cleanup(srv.Close)
	client, err := Open(srv.URL, secret)
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.ChatStream(t.Context(), Request{Route: "draft", ChatID: "chat-1", UserID: "u", Prompt: "hi"}, nil, nil)
	var failure *Failure
	if err == nil || errors.Is(err, ErrCut) || errors.As(err, &failure) {
		t.Fatalf("err = %v", err)
	}
}

func TestChatStreamStopsOnCanceledContext(t *testing.T) {
	secret := serviceKey(t, 'k')
	srv := httptest.NewServer(mustGateway(t, secret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = fmt.Fprintf(w, "%s\n", `{"delta":"one "}`)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Hold the stream open: whatever follows must never reach the caller.
		<-r.Context().Done()
	})))
	t.Cleanup(srv.Close)
	client, err := Open(srv.URL, secret)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	deltas := 0
	_, err = client.ChatStream(ctx, Request{Route: "draft", ChatID: "chat-1", UserID: "u", Prompt: "hi"}, func(string) {
		deltas++
		cancel()
	}, nil)
	if err == nil || deltas != 1 {
		t.Fatalf("err = %v deltas = %d", err, deltas)
	}
}

func TestOpenRejectsRelativeURL(t *testing.T) {
	if _, err := Open("gateway:8080", serviceKey(t, 'k')); err == nil {
		t.Fatal("relative url")
	}
}

func TestOpenRejectsBadKey(t *testing.T) {
	if _, err := Open("http://gateway:8080", "not-a-key"); err == nil {
		t.Fatal("bad key")
	}
}

func TestChatStreamRejectsMissingRouteBeforeTheRequest(t *testing.T) {
	client, err := Open("http://gateway:8080", serviceKey(t, 'k'))
	if err != nil {
		t.Fatal(err)
	}
	// The input is otherwise complete, and the host does not resolve: only
	// the client-side route check can answer this fast.
	_, err = client.ChatStream(t.Context(), Request{UserID: "u", Prompt: "hi"}, nil, nil)
	if err == nil || err.Error() != "chat: gateway route" {
		t.Fatalf("err = %v", err)
	}
}

func TestChatStreamRejectsEmptyInput(t *testing.T) {
	client, err := Open("http://gateway:8080", serviceKey(t, 'k'))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ChatStream(t.Context(), Request{Route: "draft"}, nil, nil); err == nil {
		t.Fatal("empty input")
	}
}

func TestChatStreamSendsToolsAndToolHistory(t *testing.T) {
	secret := serviceKey(t, 'k')
	var body map[string]any
	srv := httptest.NewServer(mustGateway(t, secret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = fmt.Fprintf(w, "%s\n", `{"done":{"model":"m","input_tokens":1,"output_tokens":1}}`)
	})))
	t.Cleanup(srv.Close)
	client, err := Open(srv.URL, secret)
	if err != nil {
		t.Fatal(err)
	}
	const (
		agentID = "018f3b2c-7a10-7c4a-8e11-0000000000a1"
		runID   = "018f3b2c-7a10-7c4a-8e11-0000000000b2"
	)
	_, err = client.ChatStream(t.Context(), Request{
		Route:      "draft",
		ChatID:     "chat-1",
		UserID:     "u",
		Tools:      []Tool{{Name: "lookup", Description: "find", Schema: json.RawMessage(`{"type":"object"}`)}},
		AllowTools: []string{"lookup"},
		CoreTools:  true,
		ToolChoice: ToolChoice{Mode: "tool", Name: "lookup"},
		RunID:      runID,
		AgentID:    agentID,
		Messages: []Message{
			{Role: "user", Text: "q"},
			{Role: "assistant", ToolCalls: []ToolCall{{ID: "c0", Name: "lookup", Arguments: json.RawMessage(`{"q":1}`), ThoughtSignature: "sig"}}},
			{Role: "tool", Text: "boom", ToolName: "lookup", ToolID: "c0", IsError: true, Offload: true},
		},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"tools":       []any{map[string]any{"name": "lookup", "description": "find", "schema": map[string]any{"type": "object"}}},
		"allow_tools": []any{"lookup"},
		"core_tools":  true,
		"tool_choice": map[string]any{"mode": "tool", "name": "lookup"},
		"run_id":      runID,
		"agent_id":    agentID,
		"messages": []any{
			map[string]any{"role": "user", "text": "q"},
			map[string]any{"role": "assistant", "text": "", "tool_calls": []any{
				map[string]any{"id": "c0", "name": "lookup", "arguments": map[string]any{"q": float64(1)}, "thought_signature": "sig"},
			}},
			map[string]any{"role": "tool", "text": "boom", "tool_name": "lookup", "tool_id": "c0", "is_error": true, "offload": true},
		},
	}
	for _, key := range []string{"tools", "allow_tools", "core_tools", "tool_choice", "run_id", "agent_id", "messages"} {
		if !reflect.DeepEqual(body[key], want[key]) {
			t.Fatalf("%s = %#v, want %#v", key, body[key], want[key])
		}
	}
}

func TestChatStreamTakesToolCallFramesAndDoneToolCalls(t *testing.T) {
	secret := serviceKey(t, 'k')
	srv := httptest.NewServer(mustGateway(t, secret, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = fmt.Fprintf(w, "%s\n%s\n%s\n%s\n%s\n%s\n",
			`{"delta":"a "}`,
			`{"tool_call":{"index":0,"id":"t1","name":"lookup"}}`,
			`{"tool_call":{"index":0,"arguments_delta":"{\"q\":1}"}}`,
			`{"tool_call":{"index":1,"id":"t2","name":"search"}}`,
			`{"tool_call":{"index":1,"arguments_delta":"{\"q\":2}"}}`,
			`{"done":{"model":"m","input_tokens":1,"output_tokens":2,"tool_calls":[{"id":"t1","name":"lookup","arguments":{"q":1}},{"id":"t2","name":"search","arguments":{"q":2},"thought_signature":"s2"}],"stop_reason":"tool_calls"}}`)
	})))
	t.Cleanup(srv.Close)
	client, err := Open(srv.URL, secret)
	if err != nil {
		t.Fatal(err)
	}

	var deltas []string
	done, err := client.ChatStream(t.Context(), Request{Route: "draft", ChatID: "chat-1", UserID: "u", Prompt: "hi"}, func(delta string) {
		deltas = append(deltas, delta)
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(deltas, []string{"a "}) {
		t.Fatalf("deltas = %v", deltas)
	}
	want := []ToolCall{
		{ID: "t1", Name: "lookup", Arguments: json.RawMessage(`{"q":1}`)},
		{ID: "t2", Name: "search", Arguments: json.RawMessage(`{"q":2}`), ThoughtSignature: "s2"},
	}
	if !reflect.DeepEqual(done.ToolCalls, want) || done.StopReason != "tool_calls" {
		t.Fatalf("done = %+v", done)
	}
}

func TestChatStreamDecodesCostMicroUSD(t *testing.T) {
	for _, tc := range []struct {
		name string
		done string
		cost *int64
		err  string
	}{
		{name: "integer", done: `{"done":{"model":"m","input_tokens":1,"output_tokens":1,"cost_micro_usd":17}}`, cost: i64p(17)},
		{name: "null", done: `{"done":{"model":"m","input_tokens":1,"output_tokens":1,"cost_micro_usd":null}}`, cost: nil},
		{name: "absent", done: `{"done":{"model":"m","input_tokens":1,"output_tokens":1}}`, cost: nil},
		{name: "zero", done: `{"done":{"model":"m","input_tokens":1,"output_tokens":1,"cost_micro_usd":0}}`, cost: i64p(0)},
		{name: "float", done: `{"done":{"model":"m","input_tokens":1,"output_tokens":1,"cost_micro_usd":17.5}}`, err: "chat: gateway frame"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			secret := serviceKey(t, 'k')
			srv := httptest.NewServer(mustGateway(t, secret, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/x-ndjson")
				_, _ = fmt.Fprintf(w, "%s\n", tc.done)
			})))
			t.Cleanup(srv.Close)
			client, err := Open(srv.URL, secret)
			if err != nil {
				t.Fatal(err)
			}
			done, err := client.ChatStream(t.Context(), Request{Route: "draft", ChatID: "chat-1", UserID: "u", Prompt: "hi"}, nil, nil)
			if tc.err != "" {
				if err == nil || err.Error() != tc.err {
					t.Fatalf("err = %v, want %s", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(done.CostMicroUSD, tc.cost) {
				t.Fatalf("cost = %v, want %v", done.CostMicroUSD, tc.cost)
			}
		})
	}
}

func TestChatStreamDecodesDoneCountersAndWindow(t *testing.T) {
	secret := serviceKey(t, 'k')
	srv := httptest.NewServer(mustGateway(t, secret, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = fmt.Fprintf(w, "%s\n", `{"done":{"model":"m","input_tokens":100,"output_tokens":50,"cache_read":25,"cache_write":10,"reasoning":5,"context_window":200000}}`)
	})))
	t.Cleanup(srv.Close)
	client, err := Open(srv.URL, secret)
	if err != nil {
		t.Fatal(err)
	}
	done, err := client.ChatStream(t.Context(), Request{Route: "draft", ChatID: "chat-1", UserID: "u", Prompt: "hi"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := Done{Model: "m", InputTokens: 100, OutputTokens: 50, CacheRead: 25, CacheWrite: 10, Reasoning: 5, ContextWindow: 200000}
	if !reflect.DeepEqual(done, want) {
		t.Fatalf("done = %+v, want %+v", done, want)
	}
}

func TestChatStreamLimitReachedBeforeTheStreamIsAFailure(t *testing.T) {
	secret := serviceKey(t, 'k')
	srv := httptest.NewServer(mustGateway(t, secret, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = fmt.Fprint(w, "gateway: person monthly spend limit reached\n")
	})))
	t.Cleanup(srv.Close)
	client, err := Open(srv.URL, secret)
	if err != nil {
		t.Fatal(err)
	}
	deltas := 0
	done, err := client.ChatStream(t.Context(), Request{Route: "draft", ChatID: "chat-1", UserID: "u", Prompt: "hi"}, func(string) {
		deltas++
	}, nil)
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("err = %v", err)
	}
	if failure.Class != ClassLimitReached || failure.Message != "gateway: person monthly spend limit reached" {
		t.Fatalf("failure = %+v", failure)
	}
	if deltas != 0 || done.Model != "" {
		t.Fatalf("deltas = %d done = %+v", deltas, done)
	}

	bad := httptest.NewServer(mustGateway(t, secret, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	})))
	t.Cleanup(bad.Close)
	refused, err := Open(bad.URL, secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := refused.ChatStream(t.Context(), Request{Route: "draft", ChatID: "chat-1", UserID: "u", Prompt: "hi"}, nil, nil); err == nil || errors.As(err, &failure) {
		t.Fatalf("400 err = %v", err)
	}
}

func TestChatStreamLimitReachedFrameKeepsEarlierDeltas(t *testing.T) {
	secret := serviceKey(t, 'k')
	srv := httptest.NewServer(mustGateway(t, secret, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = fmt.Fprintf(w, "%s\n%s\n",
			`{"delta":"x"}`,
			`{"error":{"failure_class":"limit_reached","model":"","message":"gateway: person monthly spend limit reached"}}`)
	})))
	t.Cleanup(srv.Close)
	client, err := Open(srv.URL, secret)
	if err != nil {
		t.Fatal(err)
	}
	var deltas []string
	_, err = client.ChatStream(t.Context(), Request{Route: "draft", ChatID: "chat-1", UserID: "u", Prompt: "hi"}, func(delta string) {
		deltas = append(deltas, delta)
	}, nil)
	var failure *Failure
	if !errors.As(err, &failure) {
		t.Fatalf("err = %v", err)
	}
	if failure.Class != "limit_reached" || failure.Model != "" {
		t.Fatalf("failure = %+v", failure)
	}
	if !reflect.DeepEqual(deltas, []string{"x"}) {
		t.Fatalf("deltas = %v", deltas)
	}
}

func TestChatStreamRefusesNonUUIDRunOrAgent(t *testing.T) {
	secret := serviceKey(t, 'k')
	hits := 0
	srv := httptest.NewServer(mustGateway(t, secret, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = fmt.Fprintf(w, "%s\n", `{"done":{"model":"m","input_tokens":1,"output_tokens":1}}`)
	})))
	t.Cleanup(srv.Close)
	client, err := Open(srv.URL, secret)
	if err != nil {
		t.Fatal(err)
	}
	for name, req := range map[string]Request{
		"run":       {Route: "draft", ChatID: "chat-1", UserID: "u", Prompt: "hi", RunID: "r1"},
		"agent":     {Route: "draft", ChatID: "chat-1", UserID: "u", Prompt: "hi", AgentID: "a1"},
		"urn run":   {Route: "draft", ChatID: "chat-1", UserID: "u", Prompt: "hi", RunID: "urn:uuid:018f3b2c-7a10-7c4a-8e11-0000000000b2"},
		"urn agent": {Route: "draft", ChatID: "chat-1", UserID: "u", Prompt: "hi", AgentID: "urn:uuid:018f3b2c-7a10-7c4a-8e11-0000000000b2"},
	} {
		if _, err := client.ChatStream(t.Context(), req, nil, nil); err == nil {
			t.Fatalf("%s id was sent", name)
		}
	}
	if hits != 0 {
		t.Fatalf("stub hits = %d, want 0", hits)
	}
}

func mustGateway(t *testing.T, secret string, inner http.Handler) http.Handler {
	t.Helper()
	derived, err := svcsig.ParseAudienceKey(secret, svcsig.AudienceGateway)
	if err != nil {
		t.Fatal(err)
	}
	h, err := svcsig.Handler([][]byte{derived}, svcsig.AudienceGateway, 1<<20, inner)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func serviceKey(t *testing.T, b byte) string {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = b
	}
	return svcsig.KeyPrefix + "gateway_" + base64.RawStdEncoding.EncodeToString(raw)
}
