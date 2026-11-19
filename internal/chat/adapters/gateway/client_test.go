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
			`{"done":{"model":"m2","input_tokens":1,"output_tokens":2,"cost_usd":0.01}}`)
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
	if done.Model != "m2" || done.InputTokens != 1 || done.OutputTokens != 2 || done.CostUSD == nil || *done.CostUSD != 0.01 {
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

func TestChatStreamStopsOnCancelledContext(t *testing.T) {
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

func mustGateway(t *testing.T, secret string, inner http.Handler) http.Handler {
	t.Helper()
	master, err := svcsig.ParseKey(secret)
	if err != nil {
		t.Fatal(err)
	}
	derived, err := svcsig.Derive(master, svcsig.AudienceGateway)
	if err != nil {
		t.Fatal(err)
	}
	h, err := svcsig.Handler([][]byte{derived}, svcsig.AudienceGateway, inner)
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
	return svcsig.KeyPrefix + base64.RawStdEncoding.EncodeToString(raw)
}
