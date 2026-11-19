//go:build integration

// These tests run the chat client against the real gateway handler and fake
// adapters. They live in pkg/brain, the composition root, because the chat
// context must not import the gateway process, tests included.
package brain

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	chatgateway "github.com/upmo-ai/upmo/internal/chat/adapters/gateway"
	"github.com/upmo-ai/upmo/internal/gateway/adapters/fake"
	httpapi "github.com/upmo-ai/upmo/internal/gateway/adapters/http"
	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/platform/httpserver"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
)

const (
	rigUserID = "018f3b2c-7a10-7c4a-8e11-111111111111"
	rigChatID = "018f3b2c-7a10-7c4a-8e11-222222222222"
)

// mustGateway wraps inner with the gateway service signature check.
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

type rigRoutes struct{ r domain.Route }

func (m rigRoutes) ByName(_ context.Context, name string) (domain.Route, error) {
	if name != m.r.Name {
		return domain.Route{}, fmt.Errorf("gateway: no route %q", name)
	}
	return m.r, nil
}

type rigSecrets struct{ v map[string]string }

func (m rigSecrets) Get(ref string) (string, error) {
	s, ok := m.v[ref]
	if !ok {
		return "", errors.New("missing")
	}
	return s, nil
}

type rigLedger struct {
	mu   sync.Mutex
	rows []domain.Entry
}

func (m *rigLedger) Record(_ context.Context, row domain.Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows = append(m.rows, row)
	return nil
}

func (m *rigLedger) classes() []domain.FailureClass {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]domain.FailureClass, 0, len(m.rows))
	for _, row := range m.rows {
		out = append(out, row.Class)
	}
	return out
}

// newStreamRig mounts the real gateway handler behind the service signature
// check, with fake streaming adapters on a two-link chat route: model m1 on
// adapter flaky, model m2 on adapter solid. writeTimeout, when not zero, is
// the server's write timeout, shorter than the stream it serves.
func newStreamRig(t *testing.T, writeTimeout time.Duration, flaky, solid app.StreamChatter) (*httptest.Server, *rigLedger) {
	t.Helper()
	house := "house"
	first := domain.Account{ID: "1", Adapter: "flaky", CredentialRef: house, House: true}
	second := domain.Account{ID: "2", Adapter: "solid", CredentialRef: house, House: true}
	route := domain.Route{
		Name: "draft", Capability: domain.CapChat,
		Links: []domain.Link{
			{Position: 1, Account: first, Model: "m1", RetryBudget: 0},
			{Position: 2, Account: second, Model: "m2", RetryBudget: 0},
		},
	}
	led := &rigLedger{}
	mux := http.NewServeMux()
	httpapi.Handler{
		Dispatch: app.Dispatch{
			Routes:  rigRoutes{route},
			Secrets: rigSecrets{map[string]string{house: "secret"}},
			Ledger:  led,
			Stream:  map[string]app.StreamChatter{"flaky": flaky, "solid": solid},
		},
	}.Mount(mux)
	srv := httptest.NewUnstartedServer(httpserver.Protect(mustGateway(t, serviceKey(t, 'k'), mux)))
	if writeTimeout > 0 {
		srv.Config.WriteTimeout = writeTimeout
	}
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, led
}

func rigClient(t *testing.T, srv *httptest.Server) chatgateway.Client {
	t.Helper()
	client, err := chatgateway.Open(srv.URL, serviceKey(t, 'k'))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestChatStreamFramesArriveIncrementally(t *testing.T) {
	srv, led := newStreamRig(t, 0,
		fake.Adapter{Reply: "one two three four", StreamDelay: 120 * time.Millisecond},
		fake.Adapter{Reply: "unused"})
	client := rigClient(t, srv)

	first := make(chan string, 1)
	type result struct {
		done chatgateway.Done
		err  error
	}
	out := make(chan result, 1)
	go func() {
		var text strings.Builder
		done, err := client.ChatStream(t.Context(), chatgateway.Request{Route: "draft", ChatID: rigChatID, UserID: rigUserID, Prompt: "hi"}, func(delta string) {
			text.WriteString(delta)
			select {
			case first <- delta:
			default:
			}
		}, nil)
		if err == nil && text.String() != "one two three four " {
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
	// The fake still has words to stream, so the reply cannot be finished.
	select {
	case res := <-out:
		t.Fatalf("the reply finished with the first delta: %+v %v", res.done, res.err)
	case <-time.After(60 * time.Millisecond):
	}
	res := <-out
	if res.err != nil {
		t.Fatal(res.err)
	}
	if res.done.Model != "m1" || res.done.InputTokens != 1 || res.done.OutputTokens != 4 {
		t.Fatalf("done = %+v", res.done)
	}
	classes := led.classes()
	if len(classes) != 1 || classes[0] != domain.ClassOK {
		t.Fatalf("ledger = %v", classes)
	}
}

func TestChatStreamAttemptNoticeThenText(t *testing.T) {
	srv, _ := newStreamRig(t, 0,
		fake.Adapter{Reply: "no", StreamFailClass: domain.ClassRateLimit},
		fake.Adapter{Reply: "hello world"})
	client := rigClient(t, srv)

	var mu sync.Mutex
	var order []string
	var attempts []chatgateway.Attempt
	done, err := client.ChatStream(t.Context(), chatgateway.Request{Route: "draft", ChatID: rigChatID, UserID: rigUserID, Prompt: "hi"}, func(delta string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, "delta")
	}, func(a chatgateway.Attempt) {
		mu.Lock()
		defer mu.Unlock()
		attempts = append(attempts, a)
		order = append(order, "attempt")
	})
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 3 || order[0] != "attempt" {
		t.Fatalf("order = %v", order)
	}
	if len(attempts) != 1 || attempts[0] != (chatgateway.Attempt{FailedModel: "m1", FailureClass: "rate_limit", NextModel: "m2"}) {
		t.Fatalf("attempts = %+v", attempts)
	}
	if done.Model != "m2" || done.OutputTokens != 2 {
		t.Fatalf("done = %+v", done)
	}
}

func TestChatStreamErrorFrameCarriesClassAndModel(t *testing.T) {
	srv, _ := newStreamRig(t, 0,
		fake.Adapter{Reply: "one two three", StreamFailWords: 1, StreamFailClass: domain.ClassTransient},
		fake.Adapter{Reply: "unused"})
	client := rigClient(t, srv)

	var text strings.Builder
	done, err := client.ChatStream(t.Context(), chatgateway.Request{Route: "draft", ChatID: rigChatID, UserID: rigUserID, Prompt: "hi"}, func(delta string) {
		text.WriteString(delta)
	}, nil)
	var failure *chatgateway.Failure
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

func TestChatStreamRefusalBeforeTheStreamStaysPlainHTTP(t *testing.T) {
	srv, _ := newStreamRig(t, 0,
		fake.Adapter{Reply: "hello"},
		fake.Adapter{Reply: "unused"})
	client := rigClient(t, srv)
	_, err := client.ChatStream(t.Context(), chatgateway.Request{Route: "no-such-route", ChatID: rigChatID, UserID: rigUserID, Prompt: "hi"}, nil, nil)
	var failure *chatgateway.Failure
	if err == nil || errors.Is(err, chatgateway.ErrCut) || errors.As(err, &failure) {
		t.Fatalf("err = %v", err)
	}
}

func TestChatStreamSurvivesTheServerWriteTimeout(t *testing.T) {
	// The server cuts a response off after 400 ms; six words 200 ms apart
	// stream for 1.2 s, so only a deadline extended per frame survives.
	srv, _ := newStreamRig(t, 400*time.Millisecond,
		fake.Adapter{Reply: "a b c d e f", StreamDelay: 200 * time.Millisecond},
		fake.Adapter{Reply: "unused"})
	client := rigClient(t, srv)

	deltas := 0
	done, err := client.ChatStream(t.Context(), chatgateway.Request{Route: "draft", ChatID: rigChatID, UserID: rigUserID, Prompt: "hi"}, func(string) {
		deltas++
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if deltas != 6 || done.OutputTokens != 6 {
		t.Fatalf("deltas = %d done = %+v", deltas, done)
	}
}

func TestChatStreamClientCancelStopsTheProvider(t *testing.T) {
	srv, led := newStreamRig(t, 0,
		fake.Adapter{Reply: "one two three", StreamDelay: 50 * time.Millisecond},
		fake.Adapter{Reply: "unused"})
	client := rigClient(t, srv)

	ctx, cancel := context.WithCancel(t.Context())
	deltas := 0
	_, err := client.ChatStream(ctx, chatgateway.Request{Route: "draft", ChatID: rigChatID, UserID: rigUserID, Prompt: "hi"}, func(string) {
		deltas++
		if deltas == 1 {
			cancel()
		}
	}, nil)
	if err == nil || deltas != 1 {
		t.Fatalf("err = %v deltas = %d", err, deltas)
	}
	// The cancelled call still wrote its attempt row: the provider stopped,
	// the ledger did not.
	deadline := time.Now().Add(5 * time.Second)
	for {
		classes := led.classes()
		if len(classes) == 1 && classes[0] == domain.ClassCancelled {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("ledger = %v", classes)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestChatStreamCutWithoutAFinalFrame(t *testing.T) {
	srv := httptest.NewServer(mustGateway(t, serviceKey(t, 'k'), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(`{"delta":"x"}` + "\n"))
	})))
	t.Cleanup(srv.Close)
	client := rigClient(t, srv)

	deltas := 0
	_, err := client.ChatStream(t.Context(), chatgateway.Request{Route: "draft", ChatID: rigChatID, UserID: rigUserID, Prompt: "hi"}, func(string) {
		deltas++
	}, nil)
	if !errors.Is(err, chatgateway.ErrCut) || deltas != 1 {
		t.Fatalf("err = %v deltas = %d", err, deltas)
	}
}

func TestChatStreamFirstFrameAfterTheServerWriteTimeout(t *testing.T) {
	// The server cuts a response off after 300 ms and the first word only
	// arrives at 500 ms, past that cut: only a write deadline extended
	// before the very first frame lets the word and the done frame through.
	srv, _ := newStreamRig(t, 300*time.Millisecond,
		fake.Adapter{Reply: "one two", StreamDelay: 500 * time.Millisecond},
		fake.Adapter{Reply: "unused"})
	client := rigClient(t, srv)

	deltas := 0
	done, err := client.ChatStream(t.Context(), chatgateway.Request{Route: "draft", ChatID: rigChatID, UserID: rigUserID, Prompt: "hi"}, func(string) {
		deltas++
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if deltas != 2 || done.Model != "m1" || done.OutputTokens != 2 {
		t.Fatalf("deltas = %d done = %+v", deltas, done)
	}
}

// leakyStream fails after one delta with an error that carries a secret, to
// prove the error frame keeps it off the wire.
type leakyStream struct{}

func (leakyStream) ChatStream(_ context.Context, _ app.Attempt, _ domain.Request, emit func(string) error) (domain.Usage, domain.FailureClass, error) {
	_ = emit("one ")
	return domain.Usage{Arrived: true, Input: 1, Output: 1}, domain.ClassTransient,
		fmt.Errorf("gateway: provider 500 with key sk-leak on prompt")
}

func TestChatStreamErrorFrameCarriesNoProviderDetail(t *testing.T) {
	srv, _ := newStreamRig(t, 0, leakyStream{}, fake.Adapter{Reply: "unused"})
	client := rigClient(t, srv)

	var text strings.Builder
	_, err := client.ChatStream(t.Context(), chatgateway.Request{Route: "draft", ChatID: rigChatID, UserID: rigUserID, Prompt: "hi"}, func(delta string) {
		text.WriteString(delta)
	}, nil)
	var failure *chatgateway.Failure
	if !errors.As(err, &failure) {
		t.Fatalf("err = %v", err)
	}
	if failure.Class != "transient" || failure.Model != "m1" {
		t.Fatalf("failure = %+v", failure)
	}
	if failure.Message == "" || strings.Contains(failure.Message, "sk-leak") || strings.Contains(failure.Message, "prompt") {
		t.Fatalf("message = %q", failure.Message)
	}
	if text.String() != "one " {
		t.Fatalf("text = %q", text.String())
	}
}

// panickyStream writes one delta and then panics, like a broken adapter.
type panickyStream struct{}

func (panickyStream) ChatStream(_ context.Context, _ app.Attempt, _ domain.Request, emit func(string) error) (domain.Usage, domain.FailureClass, error) {
	_ = emit("one ")
	panic("adapter exploded")
}

func TestChatStreamPanicAfterAFrameEndsWithOneErrorFrame(t *testing.T) {
	srv, led := newStreamRig(t, 0, panickyStream{}, fake.Adapter{Reply: "unused"})
	client := rigClient(t, srv)

	var text strings.Builder
	_, err := client.ChatStream(t.Context(), chatgateway.Request{Route: "draft", ChatID: rigChatID, UserID: rigUserID, Prompt: "hi"}, func(delta string) {
		text.WriteString(delta)
	}, nil)
	var failure *chatgateway.Failure
	if !errors.As(err, &failure) {
		t.Fatalf("err = %v", err)
	}
	if failure.Class != "internal" {
		t.Fatalf("failure = %+v", failure)
	}
	if failure.Message != "the gateway failed" {
		t.Fatalf("message = %q", failure.Message)
	}
	if text.String() != "one " {
		t.Fatalf("text = %q", text.String())
	}
	if classes := led.classes(); len(classes) != 1 || classes[0] != domain.ClassInternal {
		t.Fatalf("ledger = %v", classes)
	}
}

func serviceKey(t *testing.T, b byte) string {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = b
	}
	return svcsig.KeyPrefix + base64.RawStdEncoding.EncodeToString(raw)
}
