package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/platform/metrics"
)

type memRoutes struct{ r domain.Route }

func (m memRoutes) ByName(context.Context, string) (domain.Route, error) { return m.r, nil }

type memSecrets struct{ v map[string]string }

func (m memSecrets) Get(ref string) (string, error) {
	s, ok := m.v[ref]
	if !ok {
		return "", errors.New("missing")
	}
	return s, nil
}

type scriptDecider struct {
	calls int
	class []domain.FailureClass
	pick  domain.DecideResult
}

func (s *scriptDecider) Decide(context.Context, Attempt, domain.DecideRequest) (domain.DecideResult, domain.Usage, domain.FailureClass, error) {
	i := s.calls
	s.calls++
	if i >= len(s.class) {
		return s.pick, domain.Usage{}, domain.ClassOK, nil
	}
	c := s.class[i]
	if c == domain.ClassOK {
		return s.pick, domain.Usage{}, c, nil
	}
	return domain.DecideResult{}, domain.Usage{}, c, errors.New(string(c))
}

type memLedger struct{ rows []domain.Entry }

func (m *memLedger) Record(_ context.Context, row domain.Entry) error {
	m.rows = append(m.rows, row)
	return nil
}

func TestDispatchRetriesThenAdvances(t *testing.T) {
	house := domain.Account{ID: "h", Adapter: "fake", CredentialRef: "house", House: true}
	r := domain.Route{
		Name: "memory-check", Capability: domain.CapDecide, Decision: domain.DecisionYesNo,
		Links: []domain.Link{
			{Position: 1, Account: house, Model: "a", RetryBudget: 1},
			{Position: 2, Account: house, Model: "b", RetryBudget: 0},
		},
	}
	d := &scriptDecider{
		class: []domain.FailureClass{domain.ClassTransient, domain.ClassTransient, domain.ClassOK},
		pick:  domain.DecideResult{YesNo: true},
	}
	led := &memLedger{}
	disp := Dispatch{
		Routes:  memRoutes{r},
		Secrets: memSecrets{map[string]string{"house": "secret"}},
		Ledger:  led,
		Decide:  map[string]Decider{"fake": d},
	}
	got, err := disp.DecideCall(context.Background(), "memory-check", "", domain.Principal{}, domain.DecideRequest{Kind: domain.DecisionYesNo, Prompt: "keep?"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.YesNo || d.calls != 3 {
		t.Fatalf("result=%v calls=%d", got, d.calls)
	}
	if len(led.rows) != 3 {
		t.Fatalf("ledger rows = %d", len(led.rows))
	}
}

func TestDispatchDoesNotAdvanceOnCallerFault(t *testing.T) {
	house := domain.Account{ID: "h", Adapter: "fake", CredentialRef: "house", House: true}
	r := domain.Route{
		Name: "memory-check", Capability: domain.CapDecide, Decision: domain.DecisionYesNo,
		Links: []domain.Link{
			{Position: 1, Account: house, Model: "a", RetryBudget: 1},
			{Position: 2, Account: house, Model: "b", RetryBudget: 1},
		},
	}
	d := &scriptDecider{class: []domain.FailureClass{domain.ClassCaller}}
	disp := Dispatch{
		Routes:  memRoutes{r},
		Secrets: memSecrets{map[string]string{"house": "secret"}},
		Decide:  map[string]Decider{"fake": d},
	}
	_, err := disp.DecideCall(context.Background(), "memory-check", "", domain.Principal{}, domain.DecideRequest{Kind: domain.DecisionYesNo, Prompt: "keep?"})
	if err == nil || d.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, d.calls)
	}
}

func TestDispatchSkipsCredentialAtOnce(t *testing.T) {
	first := domain.Account{ID: "1", Adapter: "fake", CredentialRef: "house", House: true}
	second := domain.Account{ID: "2", Adapter: "other", CredentialRef: "house", House: true}
	r := domain.Route{
		Name: "memory-check", Capability: domain.CapDecide, Decision: domain.DecisionYesNo,
		Links: []domain.Link{
			{Position: 1, Account: first, Model: "a", RetryBudget: 3},
			{Position: 2, Account: second, Model: "b", RetryBudget: 0},
		},
	}
	bad := &scriptDecider{class: []domain.FailureClass{domain.ClassCredential}}
	ok := &scriptDecider{pick: domain.DecideResult{YesNo: false}}
	disp := Dispatch{
		Routes:  memRoutes{r},
		Secrets: memSecrets{map[string]string{"house": "secret"}},
		Decide:  map[string]Decider{"fake": bad, "other": ok},
	}
	got, err := disp.DecideCall(context.Background(), "memory-check", "", domain.Principal{}, domain.DecideRequest{Kind: domain.DecisionYesNo, Prompt: "keep?"})
	if err != nil {
		t.Fatal(err)
	}
	if got.YesNo || bad.calls != 1 || ok.calls != 1 {
		t.Fatalf("got=%v bad=%d ok=%d", got, bad.calls, ok.calls)
	}
}

type scriptChatter struct {
	calls int
	reply string
	got   domain.Request
}

func (s *scriptChatter) Chat(_ context.Context, _ Attempt, req domain.Request) (domain.ChatResult, domain.FailureClass, error) {
	s.calls++
	s.got = req
	return domain.ChatResult{Text: s.reply, Usage: domain.Usage{Arrived: true, Input: 2, Output: 1}}, domain.ClassOK, nil
}

func TestDispatchChatUsesBuilder(t *testing.T) {
	house := domain.Account{ID: "h", Adapter: "fake", CredentialRef: "house", House: true}
	r := domain.Route{
		Name: "draft", Capability: domain.CapChat,
		Links: []domain.Link{{Position: 1, Account: house, Model: "a", RetryBudget: 0}},
	}
	c := &scriptChatter{reply: "ok"}
	disp := Dispatch{
		Routes:  memRoutes{r},
		Secrets: memSecrets{map[string]string{"house": "secret"}},
		Chat:    map[string]Chatter{"fake": c},
	}
	got, err := disp.ChatCall(context.Background(), "draft", "", domain.Principal{}, domain.Input{
		Tools:           []domain.Tool{{Name: "search", Schema: json.RawMessage(`{"b":1,"a":2}`)}},
		System:          "be brief",
		Messages:        []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
		MaxOutputTokens: 256,
		ToolChoice:      domain.ToolChoice{Mode: domain.ToolChoiceAuto},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "ok" || got.Model != "a" || got.ContextWindow != domain.DefaultContextWindow || c.calls != 1 {
		t.Fatalf("got=%+v calls=%d", got, c.calls)
	}
	if len(c.got.Tools) != 3 || c.got.Tools[2].Name != "search" || string(c.got.Tools[2].Schema) != `{"a":2,"b":1}` {
		t.Fatalf("tools = %+v", c.got.Tools)
	}
	if c.got.MaxOutputTokens != 256 || c.got.ToolChoice.Mode != domain.ToolChoiceAuto {
		t.Fatalf("req = %+v", c.got)
	}
	if len(c.got.Cache) != 2 {
		t.Fatalf("cache = %v", c.got.Cache)
	}
}

type memBundles struct {
	byName map[string]domain.Bundle
}

func (m memBundles) Bundle(_ context.Context, name string) (domain.Bundle, error) {
	b, ok := m.byName[name]
	if !ok {
		return domain.Bundle{}, errors.New("gateway: no bundle " + name)
	}
	return b, nil
}

func TestDispatchChatMasksToAllowTools(t *testing.T) {
	house := domain.Account{ID: "h", Adapter: "fake", CredentialRef: "house", House: true}
	r := domain.Route{
		Name: "draft", Capability: domain.CapChat,
		Links: []domain.Link{{Position: 1, Account: house, Model: "a", RetryBudget: 0}},
	}
	c := &scriptChatter{reply: "ok"}
	disp := Dispatch{
		Routes:  memRoutes{r},
		Secrets: memSecrets{map[string]string{"house": "secret"}},
		Chat:    map[string]Chatter{"fake": c},
	}
	_, err := disp.ChatCall(context.Background(), "draft", "", domain.Principal{Agent: "11111111-1111-1111-1111-111111111111"}, domain.Input{
		AllowTools: []string{"search"},
		Mask:       true,
		Tools: []domain.Tool{
			{Name: "search", Schema: json.RawMessage(`{}`)},
			{Name: "write", Schema: json.RawMessage(`{}`)},
		},
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.got.Tools) != 3 || c.got.Tools[0].Name != "retrieve_output" || c.got.Tools[1].Name != "find_tool" || c.got.Tools[2].Name != "search" {
		t.Fatalf("tools = %+v", c.got.Tools)
	}
}

func TestDispatchChatBundleDoesNotLeakCatalog(t *testing.T) {
	house := domain.Account{ID: "h", Adapter: "fake", CredentialRef: "house", House: true}
	r := domain.Route{
		Name: "draft", Capability: domain.CapChat,
		Links: []domain.Link{{Position: 1, Account: house, Model: "a", RetryBudget: 0}},
	}
	c := &scriptChatter{reply: "ok"}
	disp := Dispatch{
		Routes:  memRoutes{r},
		Secrets: memSecrets{map[string]string{"house": "secret"}},
		Bundles: memBundles{byName: map[string]domain.Bundle{"core": {Name: "core", Tools: []string{"search"}}}},
		Chat:    map[string]Chatter{"fake": c},
	}
	_, err := disp.ChatCall(context.Background(), "draft", "", domain.Principal{}, domain.Input{
		Bundle:   "core",
		Tools:    []domain.Tool{{Name: "write", Schema: json.RawMessage(`{}`)}},
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.got.Tools) != 0 {
		t.Fatalf("leaked catalog = %+v", c.got.Tools)
	}
}

type scriptEmbedder struct {
	calls   int
	vectors [][]float32
	class   domain.FailureClass
}

func (s *scriptEmbedder) Embed(context.Context, Attempt, []string) ([][]float32, domain.Usage, domain.FailureClass, error) {
	s.calls++
	if s.class != domain.ClassOK {
		return nil, domain.Usage{}, s.class, errors.New(string(s.class))
	}
	return s.vectors, domain.Usage{Arrived: true, Input: 1}, domain.ClassOK, nil
}

func unitVector() []float32 {
	v := make([]float32, domain.EmbedDim)
	v[0] = 1
	return v
}

func TestDispatchEmbedReturnsModelAndVectors(t *testing.T) {
	house := domain.Account{ID: "h", Adapter: "fake", CredentialRef: "house", House: true}
	r := domain.Route{
		Name: "embed", Capability: domain.CapEmbed,
		Links: []domain.Link{{Position: 1, Account: house, Model: "model-a", RetryBudget: 0}},
	}
	e := &scriptEmbedder{vectors: [][]float32{unitVector()}}
	led := &memLedger{}
	disp := Dispatch{
		Routes:  memRoutes{r},
		Secrets: memSecrets{map[string]string{"house": "secret"}},
		Ledger:  led,
		Embed:   map[string]Embedder{"fake": e},
	}
	model, vectors, err := disp.EmbedCall(context.Background(), "embed", "", domain.Principal{}, []string{"ada prefers tea"})
	if err != nil {
		t.Fatal(err)
	}
	if model != "model-a" || len(vectors) != 1 || len(vectors[0]) != domain.EmbedDim || e.calls != 1 {
		t.Fatalf("model=%s vectors=%d calls=%d", model, len(vectors), e.calls)
	}
	if len(led.rows) != 1 || led.rows[0].Class != domain.ClassOK || led.rows[0].Model != "model-a" {
		t.Fatalf("ledger = %+v", led.rows)
	}
}

func TestDispatchEmbedSkipsWrongDimension(t *testing.T) {
	first := domain.Account{ID: "1", Adapter: "wide", CredentialRef: "house", House: true}
	second := domain.Account{ID: "2", Adapter: "ok", CredentialRef: "house", House: true}
	r := domain.Route{
		Name: "embed", Capability: domain.CapEmbed,
		Links: []domain.Link{
			{Position: 1, Account: first, Model: "wide", RetryBudget: 3},
			{Position: 2, Account: second, Model: "model-a", RetryBudget: 0},
		},
	}
	wide := &scriptEmbedder{vectors: [][]float32{{1, 0, 0}}}
	good := &scriptEmbedder{vectors: [][]float32{unitVector()}}
	disp := Dispatch{
		Routes:  memRoutes{r},
		Secrets: memSecrets{map[string]string{"house": "secret"}},
		Embed:   map[string]Embedder{"wide": wide, "ok": good},
	}
	model, vectors, err := disp.EmbedCall(context.Background(), "embed", "", domain.Principal{}, []string{"ada prefers tea"})
	if err != nil {
		t.Fatal(err)
	}
	if model != "model-a" || len(vectors) != 1 || wide.calls != 1 || good.calls != 1 {
		t.Fatalf("model=%s wide=%d good=%d", model, wide.calls, good.calls)
	}
}

func TestDispatchEmbedRejectsChatRoute(t *testing.T) {
	house := domain.Account{ID: "h", Adapter: "fake", CredentialRef: "house", House: true}
	r := domain.Route{
		Name: "draft", Capability: domain.CapChat,
		Links: []domain.Link{{Position: 1, Account: house, Model: "a"}},
	}
	disp := Dispatch{
		Routes:  memRoutes{r},
		Secrets: memSecrets{map[string]string{"house": "secret"}},
		Embed:   map[string]Embedder{"fake": &scriptEmbedder{vectors: [][]float32{unitVector()}}},
	}
	_, _, err := disp.EmbedCall(context.Background(), "draft", "", domain.Principal{}, []string{"ada prefers tea"})
	if err == nil {
		t.Fatal("chat route accepted an embed call")
	}
}

func TestDispatchEmbedEmptyInput(t *testing.T) {
	disp := Dispatch{Routes: memRoutes{domain.Route{Name: "embed", Capability: domain.CapEmbed}}}
	_, _, err := disp.EmbedCall(context.Background(), "embed", "", domain.Principal{}, nil)
	if err == nil {
		t.Fatal("empty input")
	}
}

func TestDispatchChatUnknownBundleFailsClosed(t *testing.T) {
	house := domain.Account{ID: "h", Adapter: "fake", CredentialRef: "house", House: true}
	r := domain.Route{
		Name: "draft", Capability: domain.CapChat,
		Links: []domain.Link{{Position: 1, Account: house, Model: "a", RetryBudget: 0}},
	}
	disp := Dispatch{
		Routes:  memRoutes{r},
		Secrets: memSecrets{map[string]string{"house": "secret"}},
		Bundles: memBundles{byName: map[string]domain.Bundle{}},
		Chat:    map[string]Chatter{"fake": &scriptChatter{reply: "ok"}},
	}
	_, err := disp.ChatCall(context.Background(), "draft", "", domain.Principal{Agent: "11111111-1111-1111-1111-111111111111"}, domain.Input{
		Bundle:   "missing",
		Tools:    []domain.Tool{{Name: "search"}},
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
	})
	if err == nil {
		t.Fatal("unknown bundle succeeded")
	}
}

func TestDispatchSkipsACooledKey(t *testing.T) {
	house := domain.Account{ID: "h", Adapter: "fake", CredentialRef: "house", House: true}
	r := domain.Route{
		Name: "memory-check", Capability: domain.CapDecide, Decision: domain.DecisionYesNo,
		Links: []domain.Link{{Position: 1, Account: house, Model: "a", RetryBudget: 0}},
	}
	d := &scriptDecider{pick: domain.DecideResult{YesNo: true}}
	gates := NewGates()
	gates.now = func() time.Time { return time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) }
	gates.Observe("house", domain.ClassCredential, 0)
	disp := Dispatch{
		Routes:  memRoutes{r},
		Secrets: memSecrets{map[string]string{"house": "secret"}},
		Decide:  map[string]Decider{"fake": d},
		Gates:   gates,
	}
	_, err := disp.DecideCall(context.Background(), "memory-check", "", domain.Principal{}, domain.DecideRequest{Kind: domain.DecisionYesNo, Prompt: "keep?"})
	if !errors.Is(err, domain.ErrCooling) || d.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, d.calls)
	}
}

func TestDispatchRefusesAMissingCapability(t *testing.T) {
	house := domain.Account{ID: "h", Adapter: "fake", CredentialRef: "house", House: true}
	r := domain.Route{
		Name: "draft", Capability: domain.CapChat,
		Links: []domain.Link{{Position: 1, Account: house, Model: "a", Need: []string{"vision"}, RetryBudget: 0}},
	}
	c := &scriptChatter{reply: "ok"}
	disp := Dispatch{
		Routes:  memRoutes{r},
		Secrets: memSecrets{map[string]string{"house": "secret"}},
		Chat:    map[string]Chatter{"fake": c},
		Caps:    func(string) ([]string, bool) { return []string{"tools"}, true },
	}
	_, err := disp.ChatCall(context.Background(), "draft", "", domain.Principal{}, domain.Input{
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
	})
	if !errors.Is(err, domain.ErrCapability) || c.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, c.calls)
	}
}

// recordingSink collects what one streamed call sent.
type recordingSink struct {
	text    strings.Builder
	deltas  int
	notices []StreamNotice
	done    *StreamDone
	fail    *StreamFail
}

func (s *recordingSink) Delta(text string) error {
	s.text.WriteString(text)
	s.deltas++
	return nil
}

func (s *recordingSink) Notice(n StreamNotice) error {
	s.notices = append(s.notices, n)
	return nil
}
func (s *recordingSink) Done(d StreamDone) { s.done = &d }
func (s *recordingSink) Fail(f StreamFail) { s.fail = &f }

// cancelSink records like recordingSink and cancels the call on the first
// delta.
type cancelSink struct {
	recordingSink
	cancel context.CancelFunc
}

func (s *cancelSink) Delta(text string) error {
	if err := s.recordingSink.Delta(text); err != nil {
		return err
	}
	if s.deltas == 1 {
		s.cancel()
	}
	return nil
}

// scriptStream streams reply word by word, pausing delay between words. A
// scripted failure strikes after failAfter words, zero failing before the
// first word. Usage counts the words that were sent.
type scriptStream struct {
	reply     string
	delay     time.Duration
	failAfter int
	failClass domain.FailureClass
	calls     int
}

func (s *scriptStream) ChatStream(ctx context.Context, _ Attempt, _ domain.Request, emit func(string) error) (domain.Usage, domain.FailureClass, error) {
	s.calls++
	part := func(sent int) domain.Usage { return domain.Usage{Arrived: true, Input: 1, Output: sent} }
	sent := 0
	for _, word := range strings.Fields(s.reply) {
		if s.failClass != "" && sent == s.failAfter {
			return part(sent), s.failClass, fmt.Errorf("gateway: scripted %s", s.failClass)
		}
		if s.delay > 0 {
			select {
			case <-time.After(s.delay):
			case <-ctx.Done():
				return part(sent), domain.ClassCancelled, ctx.Err()
			}
		}
		if err := emit(word + " "); err != nil {
			return part(sent), domain.ClassCancelled, err
		}
		sent++
	}
	if s.failClass != "" && sent == s.failAfter {
		return part(sent), s.failClass, fmt.Errorf("gateway: scripted %s", s.failClass)
	}
	return part(sent), domain.ClassOK, nil
}

// streamRoute is a two-link chat route: model m1 on adapter flaky, model m2
// on adapter solid.
func streamRoute() domain.Route {
	first := domain.Account{ID: "1", Adapter: "flaky", CredentialRef: "house", House: true}
	second := domain.Account{ID: "2", Adapter: "solid", CredentialRef: "house", House: true}
	return domain.Route{
		Name: "draft", Capability: domain.CapChat,
		Links: []domain.Link{
			{Position: 1, Account: first, Model: "m1", RetryBudget: 0},
			{Position: 2, Account: second, Model: "m2", RetryBudget: 0},
		},
	}
}

func streamInput() domain.Input {
	return domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}
}

func TestDispatchChatStreamFailsOverBeforeText(t *testing.T) {
	led := &memLedger{}
	sink := &recordingSink{}
	disp := Dispatch{
		Routes:  memRoutes{streamRoute()},
		Secrets: memSecrets{map[string]string{"house": "secret"}},
		Ledger:  led,
		Stream: map[string]StreamChatter{
			"flaky": &scriptStream{reply: "no", failClass: domain.ClassRateLimit},
			"solid": &scriptStream{reply: "hello world"},
		},
	}
	if err := disp.ChatStreamCall(context.Background(), "draft", "", domain.Principal{}, streamInput(), 0, sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.notices) != 1 || sink.notices[0] != (StreamNotice{FailedModel: "m1", Class: domain.ClassRateLimit, NextModel: "m2"}) {
		t.Fatalf("notices = %+v", sink.notices)
	}
	if sink.text.String() != "hello world " || sink.fail != nil {
		t.Fatalf("text = %q fail = %+v", sink.text.String(), sink.fail)
	}
	if sink.done == nil || sink.done.Model != "m2" || !sink.done.Usage.Arrived || sink.done.Usage.Output != 2 {
		t.Fatalf("done = %+v", sink.done)
	}
	if len(led.rows) != 2 || led.rows[0].Class != domain.ClassRateLimit || led.rows[1].Class != domain.ClassOK {
		t.Fatalf("ledger = %+v", led.rows)
	}
}

func TestDispatchChatStreamFailsAfterTextEndsTheCall(t *testing.T) {
	solid := &scriptStream{reply: "never"}
	led := &memLedger{}
	sink := &recordingSink{}
	disp := Dispatch{
		Routes:  memRoutes{streamRoute()},
		Secrets: memSecrets{map[string]string{"house": "secret"}},
		Ledger:  led,
		Stream: map[string]StreamChatter{
			"flaky": &scriptStream{reply: "one two three", failAfter: 2, failClass: domain.ClassTransient},
			"solid": solid,
		},
	}
	if err := disp.ChatStreamCall(context.Background(), "draft", "", domain.Principal{}, streamInput(), 0, sink); err != nil {
		t.Fatal(err)
	}
	if sink.text.String() != "one two " || sink.done != nil || solid.calls != 0 {
		t.Fatalf("text = %q done = %+v second calls = %d", sink.text.String(), sink.done, solid.calls)
	}
	if sink.fail == nil || sink.fail.Model != "m1" || sink.fail.Class != domain.ClassTransient {
		t.Fatalf("fail = %+v", sink.fail)
	}
	if len(led.rows) != 1 || led.rows[0].Class != domain.ClassTransient || led.rows[0].Usage.Output != 2 {
		t.Fatalf("ledger = %+v", led.rows)
	}
}

func TestDispatchChatStreamAllLinksFailBeforeText(t *testing.T) {
	led := &memLedger{}
	sink := &recordingSink{}
	disp := Dispatch{
		Routes:  memRoutes{streamRoute()},
		Secrets: memSecrets{map[string]string{"house": "secret"}},
		Ledger:  led,
		Stream: map[string]StreamChatter{
			"flaky": &scriptStream{reply: "no", failClass: domain.ClassRateLimit},
			"solid": &scriptStream{reply: "neither", failClass: domain.ClassRateLimit},
		},
	}
	if err := disp.ChatStreamCall(context.Background(), "draft", "", domain.Principal{}, streamInput(), 0, sink); err != nil {
		t.Fatal(err)
	}
	want := []StreamNotice{
		{FailedModel: "m1", Class: domain.ClassRateLimit, NextModel: "m2"},
		{FailedModel: "m2", Class: domain.ClassRateLimit, NextModel: ""},
	}
	if len(sink.notices) != 2 || sink.notices[0] != want[0] || sink.notices[1] != want[1] {
		t.Fatalf("notices = %+v", sink.notices)
	}
	if sink.deltas != 0 || sink.done != nil || sink.fail == nil || sink.fail.Class != domain.ClassRateLimit || sink.fail.Model != "m2" {
		t.Fatalf("deltas = %d done = %+v fail = %+v", sink.deltas, sink.done, sink.fail)
	}
	if len(led.rows) != 2 {
		t.Fatalf("ledger rows = %d", len(led.rows))
	}
}

// panicStream emits one word and then panics, like a broken adapter.
type panicStream struct{}

func (panicStream) ChatStream(_ context.Context, _ Attempt, _ domain.Request, emit func(string) error) (domain.Usage, domain.FailureClass, error) {
	_ = emit("one ")
	panic("adapter exploded")
}

func TestDispatchChatStreamPanicIsAnInternalFailureWithOneLedgerRow(t *testing.T) {
	led := &memLedger{}
	sink := &recordingSink{}
	solid := &scriptStream{reply: "hello"}
	disp := Dispatch{
		Routes:  memRoutes{streamRoute()},
		Secrets: memSecrets{map[string]string{"house": "secret"}},
		Ledger:  led,
		Stream:  map[string]StreamChatter{"flaky": panicStream{}, "solid": solid},
	}
	if err := disp.ChatStreamCall(context.Background(), "draft", "", domain.Principal{}, streamInput(), 0, sink); err != nil {
		t.Fatal(err)
	}
	if sink.fail == nil || sink.fail.Class != domain.ClassInternal || sink.fail.Model != "m1" {
		t.Fatalf("fail = %+v", sink.fail)
	}
	if len(led.rows) != 1 || led.rows[0].Class != domain.ClassInternal || led.rows[0].Usage.Arrived {
		t.Fatalf("ledger = %+v", led.rows)
	}
	if solid.calls != 0 || sink.done != nil {
		t.Fatalf("second link calls = %d done = %+v", solid.calls, sink.done)
	}
}

// panicSink records like recordingSink and panics on the first delta.
type panicSink struct{ recordingSink }

func (s *panicSink) Delta(string) error { panic("sink bug") }

func TestDispatchChatStreamSinkPanicIsNotBookedAgainstTheProvider(t *testing.T) {
	led := &memLedger{}
	disp := Dispatch{
		Routes:  memRoutes{streamRoute()},
		Secrets: memSecrets{map[string]string{"house": "secret"}},
		Ledger:  led,
		Stream:  map[string]StreamChatter{"flaky": &scriptStream{reply: "hello"}},
	}
	defer func() {
		if rec := recover(); rec != "sink bug" {
			t.Fatalf("recovered %v, want the sink's panic", rec)
		}
		if len(led.rows) != 0 {
			t.Fatalf("ledger = %+v, want no provider row", led.rows)
		}
	}()
	_ = disp.ChatStreamCall(context.Background(), "draft", "", domain.Principal{}, streamInput(), 0, &panicSink{})
	t.Fatal("ChatStreamCall returned, want the sink's panic raised again")
}

func TestDispatchChatStreamCancelStopsTheAdapter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &cancelSink{cancel: cancel}
	disp := Dispatch{
		Routes:  memRoutes{streamRoute()},
		Secrets: memSecrets{map[string]string{"house": "secret"}},
		Stream: map[string]StreamChatter{
			"flaky": &scriptStream{reply: "one two three", delay: 20 * time.Millisecond},
		},
	}
	if err := disp.ChatStreamCall(ctx, "draft", "", domain.Principal{}, streamInput(), 0, sink); err != nil {
		t.Fatal(err)
	}
	if sink.deltas != 1 || sink.text.String() != "one " {
		t.Fatalf("deltas = %d text = %q", sink.deltas, sink.text.String())
	}
	if sink.fail == nil || sink.fail.Class != domain.ClassCancelled || sink.fail.Model != "m1" {
		t.Fatalf("fail = %+v", sink.fail)
	}
}

func TestDispatchChatStreamSkipsLinkWithoutStreamAdapter(t *testing.T) {
	led := &memLedger{}
	sink := &recordingSink{}
	disp := Dispatch{
		Routes:  memRoutes{streamRoute()},
		Secrets: memSecrets{map[string]string{"house": "secret"}},
		Ledger:  led,
		Stream:  map[string]StreamChatter{"solid": &scriptStream{reply: "hello world"}},
	}
	if err := disp.ChatStreamCall(context.Background(), "draft", "", domain.Principal{}, streamInput(), 0, sink); err != nil {
		t.Fatal(err)
	}
	if sink.text.String() != "hello world " || len(sink.notices) != 0 || sink.done == nil || sink.done.Model != "m2" {
		t.Fatalf("text = %q notices = %+v done = %+v", sink.text.String(), sink.notices, sink.done)
	}
	if len(led.rows) != 1 {
		t.Fatalf("ledger rows = %d", len(led.rows))
	}
}

// shiftingPrices answers a different rate on every call, so a second lookup
// for the done frame would disagree with the ledger row.
type shiftingPrices struct{ calls int }

func (p *shiftingPrices) Price(context.Context, string, string) (domain.Price, bool, error) {
	p.calls++
	rate := int64(p.calls) * 1_000_000
	return domain.Price{Version: fmt.Sprintf("v%d", p.calls), InputMicroUSDPerMTok: &rate, OutputMicroUSDPerMTok: &rate}, true, nil
}

func TestDispatchChatStreamDoneCostMatchesTheLedgerRow(t *testing.T) {
	house := domain.Account{ID: "1", Adapter: "flaky", CredentialRef: "house", House: true, PlanKind: domain.PlanMetered}
	r := domain.Route{
		Name: "draft", Capability: domain.CapChat,
		Links: []domain.Link{{Position: 1, Account: house, Model: "m1", RetryBudget: 0}},
	}
	led := &memLedger{}
	prices := &shiftingPrices{}
	sink := &recordingSink{}
	disp := Dispatch{
		Routes:  memRoutes{r},
		Secrets: memSecrets{map[string]string{"house": "secret"}},
		Ledger:  led,
		Prices:  prices,
		Stream:  map[string]StreamChatter{"flaky": &scriptStream{reply: "one two"}},
	}
	if err := disp.ChatStreamCall(context.Background(), "draft", "", domain.Principal{}, streamInput(), 0, sink); err != nil {
		t.Fatal(err)
	}
	if sink.done == nil || sink.done.CostMicroUSD == nil || len(led.rows) != 1 || led.rows[0].CostMicroUSD == nil {
		t.Fatalf("done = %+v rows = %+v", sink.done, led.rows)
	}
	if *sink.done.CostMicroUSD != *led.rows[0].CostMicroUSD {
		t.Fatalf("done cost = %v, ledger cost = %v", *sink.done.CostMicroUSD, *led.rows[0].CostMicroUSD)
	}
	if prices.calls != 1 {
		t.Fatalf("price calls = %d", prices.calls)
	}
}

func TestDispatchChatStreamNoticesAMissingCredential(t *testing.T) {
	first := domain.Account{ID: "1", Adapter: "flaky", CredentialRef: "gone", House: true}
	second := domain.Account{ID: "2", Adapter: "solid", CredentialRef: "house", House: true}
	r := domain.Route{
		Name: "draft", Capability: domain.CapChat,
		Links: []domain.Link{
			{Position: 1, Account: first, Model: "m1", RetryBudget: 0},
			{Position: 2, Account: second, Model: "m2", RetryBudget: 0},
		},
	}
	led := &memLedger{}
	sink := &recordingSink{}
	disp := Dispatch{
		Routes:  memRoutes{r},
		Secrets: memSecrets{map[string]string{"house": "secret"}},
		Ledger:  led,
		Stream: map[string]StreamChatter{
			"flaky": &scriptStream{reply: "never"},
			"solid": &scriptStream{reply: "hello"},
		},
	}
	if err := disp.ChatStreamCall(context.Background(), "draft", "", domain.Principal{}, streamInput(), 0, sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.notices) != 1 || sink.notices[0] != (StreamNotice{FailedModel: "m1", Class: domain.ClassCredential, NextModel: "m2"}) {
		t.Fatalf("notices = %+v", sink.notices)
	}
	if sink.text.String() != "hello " || sink.done == nil || sink.done.Model != "m2" {
		t.Fatalf("text = %q done = %+v", sink.text.String(), sink.done)
	}
	if len(led.rows) != 2 || led.rows[0].Class != domain.ClassCredential || led.rows[0].Model != "m1" ||
		led.rows[1].Class != domain.ClassOK || led.rows[1].Model != "m2" {
		t.Fatalf("ledger = %+v", led.rows)
	}
}

func TestDispatchChatStreamMissingCredentialsCarryModelAndClass(t *testing.T) {
	first := domain.Account{ID: "1", Adapter: "flaky", CredentialRef: "gone", House: true}
	second := domain.Account{ID: "2", Adapter: "solid", CredentialRef: "lost", House: true}
	r := domain.Route{
		Name: "draft", Capability: domain.CapChat,
		Links: []domain.Link{
			{Position: 1, Account: first, Model: "m1", RetryBudget: 0},
			{Position: 2, Account: second, Model: "m2", RetryBudget: 0},
		},
	}
	led := &memLedger{}
	sink := &recordingSink{}
	disp := Dispatch{
		Routes:  memRoutes{r},
		Secrets: memSecrets{},
		Ledger:  led,
		Stream: map[string]StreamChatter{
			"flaky": &scriptStream{reply: "never"},
			"solid": &scriptStream{reply: "neither"},
		},
	}
	if err := disp.ChatStreamCall(context.Background(), "draft", "", domain.Principal{}, streamInput(), 0, sink); err != nil {
		t.Fatal(err)
	}
	want := []StreamNotice{
		{FailedModel: "m1", Class: domain.ClassCredential, NextModel: "m2"},
		{FailedModel: "m2", Class: domain.ClassCredential, NextModel: ""},
	}
	if len(sink.notices) != 2 || sink.notices[0] != want[0] || sink.notices[1] != want[1] {
		t.Fatalf("notices = %+v", sink.notices)
	}
	if sink.fail == nil || sink.fail.Model != "m2" || sink.fail.Class != domain.ClassCredential {
		t.Fatalf("fail = %+v", sink.fail)
	}
	if len(led.rows) != 2 || led.rows[0].Class != domain.ClassCredential || led.rows[1].Class != domain.ClassCredential {
		t.Fatalf("ledger = %+v", led.rows)
	}
}

// goneSink records like recordingSink, but its caller is gone: every notice
// write fails.
type goneSink struct {
	recordingSink
	noticeErr error
}

func (s *goneSink) Notice(StreamNotice) error { return s.noticeErr }

func TestDispatchChatStreamStopsWhenTheNoticeWriteFails(t *testing.T) {
	gone := errors.New("caller gone")
	sink := &goneSink{noticeErr: gone}
	solid := &scriptStream{reply: "hello"}
	disp := Dispatch{
		Routes:  memRoutes{streamRoute()},
		Secrets: memSecrets{map[string]string{"house": "secret"}},
		Stream: map[string]StreamChatter{
			"flaky": &scriptStream{reply: "no", failClass: domain.ClassRateLimit},
			"solid": solid,
		},
	}
	err := disp.ChatStreamCall(context.Background(), "draft", "", domain.Principal{}, streamInput(), 0, sink)
	if !errors.Is(err, gone) {
		t.Fatalf("err = %v", err)
	}
	if solid.calls != 0 || sink.done != nil || sink.fail != nil {
		t.Fatalf("second calls = %d done = %+v fail = %+v", solid.calls, sink.done, sink.fail)
	}
}

// ownedRoute is a chat route with a link of one person's own, then a house
// link: model mine on adapter own, model m2 on adapter solid.
func ownedRoute(owner string) domain.Route {
	mine := domain.Account{ID: "1", Adapter: "own", CredentialRef: "personal", OwnerUserID: owner, Visibility: domain.VisibilityPrivate}
	house := domain.Account{ID: "2", Adapter: "solid", CredentialRef: "house", House: true}
	return domain.Route{
		Name: "draft", Capability: domain.CapChat,
		Links: []domain.Link{
			{Position: 1, Account: mine, Model: "mine"},
			{Position: 2, Account: house, Model: "m2"},
		},
	}
}

func ownedDispatch(own, solid *scriptStream) Dispatch {
	return Dispatch{
		Routes:  memRoutes{ownedRoute("u1")},
		Secrets: memSecrets{map[string]string{"house": "secret", "personal": "mine"}},
		Ledger:  &memLedger{},
		Stream:  map[string]StreamChatter{"own": own, "solid": solid},
	}
}

func TestDispatchChatStreamUsesTheAuthorsOwnLinkAndTheHouseForOthers(t *testing.T) {
	own, solid := &scriptStream{reply: "mine"}, &scriptStream{reply: "house"}
	disp := ownedDispatch(own, solid)

	sink := &recordingSink{}
	if err := disp.ChatStreamCall(context.Background(), "draft", "", domain.Principal{User: "u1"}, streamInput(), 0, sink); err != nil {
		t.Fatal(err)
	}
	if sink.done == nil || sink.done.Model != "mine" || own.calls != 1 || solid.calls != 0 {
		t.Fatalf("author: done %+v own %d house %d", sink.done, own.calls, solid.calls)
	}

	sink = &recordingSink{}
	if err := disp.ChatStreamCall(context.Background(), "draft", "", domain.Principal{User: "u2"}, streamInput(), 0, sink); err != nil {
		t.Fatal(err)
	}
	if sink.done == nil || sink.done.Model != "m2" || own.calls != 1 || solid.calls != 1 {
		t.Fatalf("other person: done %+v own %d house %d", sink.done, own.calls, solid.calls)
	}
}

// An account the person may use is one more link of the chain, whoever it is
// shared by: UPM-245 settled that availability is the union, with no rule that
// keeps a person's own failure from the workspace's account. Their own link is
// still the first one tried.
func TestDispatchChatStreamTriesTheOwnLinkFirstAndThenTheWorkspaceLink(t *testing.T) {
	own, solid := &scriptStream{reply: "no", failClass: domain.ClassCredential}, &scriptStream{reply: "house"}
	led := &memLedger{}
	disp := ownedDispatch(own, solid)
	disp.Ledger = led
	sink := &recordingSink{}
	if err := disp.ChatStreamCall(context.Background(), "draft", "", domain.Principal{User: "u1"}, streamInput(), 0, sink); err != nil {
		t.Fatal(err)
	}
	if own.calls != 1 || solid.calls != 1 || sink.done == nil || sink.done.Model != "m2" || sink.fail != nil {
		t.Fatalf("own %d house %d done %+v fail %+v", own.calls, solid.calls, sink.done, sink.fail)
	}
	if len(sink.notices) != 1 || sink.notices[0] != (StreamNotice{FailedModel: "mine", Class: domain.ClassCredential, NextModel: "m2"}) {
		t.Fatalf("notices = %+v", sink.notices)
	}
	// Neither link was reached through a team.
	if len(led.rows) != 2 || led.rows[0].OwnerTeamID != "" || led.rows[1].OwnerTeamID != "" {
		t.Fatalf("ledger = %+v", led.rows)
	}
}

type memTeams struct {
	ids   []string
	err   error
	asked []string
}

func (m *memTeams) TeamsOf(_ context.Context, user string) ([]string, error) {
	m.asked = append(m.asked, user)
	return m.ids, m.err
}

// teamRoute is a chat route with a workspace link first and a link on an
// account shared with teams t1 and t2 second.
func teamRoute() domain.Route {
	workspace := domain.Account{ID: "w", Adapter: "solid", CredentialRef: "house", House: true}
	shared := domain.Account{ID: "s", Adapter: "team", CredentialRef: "shared", OwnerUserID: "u9", Visibility: domain.VisibilityTeam, TeamIDs: []string{"t1", "t2"}}
	return domain.Route{
		Name: "draft", Capability: domain.CapChat,
		Links: []domain.Link{
			{Position: 1, Account: workspace, Model: "m2"},
			{Position: 2, Account: shared, Model: "team-m"},
		},
	}
}

func teamDispatch(teams Teams, team, solid *scriptStream, led *memLedger) Dispatch {
	return Dispatch{
		Routes:  memRoutes{teamRoute()},
		Teams:   teams,
		Secrets: memSecrets{map[string]string{"house": "secret", "shared": "team-secret"}},
		Ledger:  led,
		Stream:  map[string]StreamChatter{"team": team, "solid": solid},
	}
}

func TestDispatchChatStreamTriesATeamsAccountBeforeTheWorkspaceAndBooksTheTeam(t *testing.T) {
	team, solid := &scriptStream{reply: "no", failClass: domain.ClassRateLimit}, &scriptStream{reply: "house"}
	led := &memLedger{}
	teams := &memTeams{ids: []string{"t2", "t3"}}
	disp := teamDispatch(teams, team, solid, led)
	sink := &recordingSink{}
	if err := disp.ChatStreamCall(context.Background(), "draft", "", domain.Principal{User: "u1"}, streamInput(), 0, sink); err != nil {
		t.Fatal(err)
	}
	if team.calls != 1 || solid.calls != 1 || sink.done == nil || sink.done.Model != "m2" {
		t.Fatalf("team %d house %d done %+v", team.calls, solid.calls, sink.done)
	}
	if len(sink.notices) != 1 || sink.notices[0].FailedModel != "team-m" || sink.notices[0].NextModel != "m2" {
		t.Fatalf("notices = %+v", sink.notices)
	}
	// The team's attempt is booked against the team the person reached it through.
	if len(led.rows) != 2 || led.rows[0].Model != "team-m" || led.rows[0].OwnerTeamID != "t2" ||
		led.rows[1].Model != "m2" || led.rows[1].OwnerTeamID != "" {
		t.Fatalf("ledger = %+v", led.rows)
	}
	if len(teams.asked) != 1 || teams.asked[0] != "u1" {
		t.Fatalf("teams asked for %v", teams.asked)
	}
}

func TestDispatchChatStreamLeavesATeamsAccountToItsMembers(t *testing.T) {
	team, solid := &scriptStream{reply: "team"}, &scriptStream{reply: "house"}
	disp := teamDispatch(&memTeams{ids: []string{"t9"}}, team, solid, &memLedger{})
	sink := &recordingSink{}
	if err := disp.ChatStreamCall(context.Background(), "draft", "", domain.Principal{User: "u1"}, streamInput(), 0, sink); err != nil {
		t.Fatal(err)
	}
	if team.calls != 0 || solid.calls != 1 || sink.done == nil || sink.done.Model != "m2" {
		t.Fatalf("team %d house %d done %+v", team.calls, solid.calls, sink.done)
	}
	// Without a lookup nobody is on a team.
	team, solid = &scriptStream{reply: "team"}, &scriptStream{reply: "house"}
	disp = teamDispatch(nil, team, solid, &memLedger{})
	disp.Teams = nil
	if err := disp.ChatStreamCall(context.Background(), "draft", "", domain.Principal{User: "u1"}, streamInput(), 0, &recordingSink{}); err != nil {
		t.Fatal(err)
	}
	if team.calls != 0 || solid.calls != 1 {
		t.Fatalf("team %d house %d", team.calls, solid.calls)
	}
}

func TestDispatchChatStreamStopsWhenTheTeamsCannotBeRead(t *testing.T) {
	team, solid := &scriptStream{reply: "team"}, &scriptStream{reply: "house"}
	broken := errors.New("identity is down")
	disp := teamDispatch(&memTeams{err: broken}, team, solid, &memLedger{})
	err := disp.ChatStreamCall(context.Background(), "draft", "", domain.Principal{User: "u1"}, streamInput(), 0, &recordingSink{})
	if !errors.Is(err, broken) || team.calls != 0 || solid.calls != 0 {
		t.Fatalf("err = %v team %d house %d", err, team.calls, solid.calls)
	}
}

// memPins answers pins from a fixed set of accounts.
type memPins struct{ accounts map[string]domain.Account }

func (m memPins) PinAccount(_ context.Context, id, _ string) (domain.Account, bool, error) {
	account, ok := m.accounts[id]
	return account, ok, nil
}

func (m memPins) PinProvider(context.Context, string, []string, string, string) (domain.Account, bool, error) {
	return domain.Account{}, false, nil
}

const sharedPinID = "018f3b2c-7a10-7c4a-8e11-0000000000b1"

func TestDispatchPinToATeamsAccountIsForItsMembersAndBooksTheTeam(t *testing.T) {
	shared := domain.Account{ID: sharedPinID, Adapter: "team", CredentialRef: "shared", OwnerUserID: "u9", Visibility: domain.VisibilityTeam, TeamIDs: []string{"t1", "t2"}}
	pins := memPins{accounts: map[string]domain.Account{sharedPinID: shared}}
	pin := domain.Pin{AccountID: sharedPinID, Model: "pinned"}

	// A member of one of its teams: the pin is tried first, at position 0.
	team, solid := &scriptStream{reply: "team"}, &scriptStream{reply: "house"}
	led := &memLedger{}
	disp := teamDispatch(&memTeams{ids: []string{"t2"}}, team, solid, led)
	disp.Pins = pins
	sink := &recordingSink{}
	if err := disp.ChatStreamPinned(context.Background(), "draft", "", domain.Principal{User: "u1"}, streamInput(), pin, 0, sink); err != nil {
		t.Fatal(err)
	}
	if team.calls != 1 || solid.calls != 0 || sink.done == nil || sink.done.Model != "pinned" {
		t.Fatalf("team %d house %d done %+v", team.calls, solid.calls, sink.done)
	}
	if len(led.rows) != 1 || led.rows[0].LinkPosition != domain.PinPosition || led.rows[0].OwnerTeamID != "t2" {
		t.Fatalf("ledger = %+v", led.rows)
	}

	// Someone on none of them is refused before any call, and the refusal
	// names nothing.
	team, solid = &scriptStream{reply: "team"}, &scriptStream{reply: "house"}
	disp = teamDispatch(&memTeams{ids: []string{"t9"}}, team, solid, &memLedger{})
	disp.Pins = pins
	err := disp.ChatStreamPinned(context.Background(), "draft", "", domain.Principal{User: "u2"}, streamInput(), pin, 0, &recordingSink{})
	if !errors.Is(err, domain.ErrPinAccount) || team.calls != 0 || solid.calls != 0 {
		t.Fatalf("err = %v team %d house %d", err, team.calls, solid.calls)
	}
	if strings.Contains(err.Error(), sharedPinID) || strings.Contains(err.Error(), "t1") {
		t.Fatalf("the refusal names the account or a team: %v", err)
	}
}

type toolRecordingSink struct {
	recordingSink
	toolDeltas []ToolCallDelta
}

func (s *toolRecordingSink) ToolCallDelta(d ToolCallDelta) error {
	s.toolDeltas = append(s.toolDeltas, d)
	return nil
}

type scriptToolStream struct {
	deltas    []ToolCallDelta
	calls     []domain.ToolCall
	failAfter int
	failClass domain.FailureClass
	invoked   int
}

func (s *scriptToolStream) ChatStream(context.Context, Attempt, domain.Request, func(string) error) (domain.Usage, domain.FailureClass, error) {
	return domain.Usage{}, domain.ClassCaller, errors.New("unexpected plain stream call")
}

func (s *scriptToolStream) ChatStreamTools(_ context.Context, attempt Attempt, _ domain.Request, events StreamEvents) (domain.ChatResult, domain.FailureClass, error) {
	s.invoked++
	for i, d := range s.deltas {
		if s.failClass != "" && i == s.failAfter {
			return domain.ChatResult{Usage: domain.Usage{Arrived: true, Input: 10, Output: i}, Model: attempt.Model},
				s.failClass, fmt.Errorf("gateway: scripted %s", s.failClass)
		}
		if events.ToolCall != nil {
			if err := events.ToolCall(d); err != nil {
				return domain.ChatResult{Usage: domain.Usage{Arrived: true, Input: 10, Output: i}, Model: attempt.Model},
					domain.ClassCancelled, err
			}
		}
	}
	return domain.ChatResult{
		ToolCalls:  s.calls,
		StopReason: "tool_calls",
		Usage:      domain.Usage{Arrived: true, Input: 1000, Output: 500, CacheRead: 200, CacheWrite: 100, Reasoning: 50},
		Model:      attempt.Model,
	}, domain.ClassOK, nil
}

type fixedPrices struct {
	price domain.Price
}

func (p fixedPrices) Price(context.Context, string, string) (domain.Price, bool, error) {
	return p.price, true, nil
}

func TestDispatchChatStreamToolsPropagatesDeltasAndMetadata(t *testing.T) {
	rate := int64(1_000_000)
	house := domain.Account{ID: "1", Adapter: "tools", CredentialRef: "house", House: true, PlanKind: domain.PlanMetered}
	route := domain.Route{
		Name: "draft", Capability: domain.CapChat,
		Links: []domain.Link{{Position: 1, Account: house, Model: "m1", RetryBudget: 0}},
	}
	sink := &toolRecordingSink{}
	stream := &scriptToolStream{
		deltas: []ToolCallDelta{
			{Index: 0, ID: "call_1", Name: "search"},
			{Index: 0, ArgumentsDelta: `{"q":"go"}`},
		},
		calls: []domain.ToolCall{{ID: "call_1", Name: "search", Arguments: json.RawMessage(`{"q":"go"}`)}},
	}
	disp := Dispatch{
		Routes:  memRoutes{route},
		Secrets: memSecrets{map[string]string{"house": "secret"}},
		Prices: fixedPrices{price: domain.Price{
			Version:                   "v1",
			InputMicroUSDPerMTok:      &rate,
			OutputMicroUSDPerMTok:     &rate,
			CacheReadMicroUSDPerMTok:  &rate,
			CacheWriteMicroUSDPerMTok: &rate,
			ContextWindow:             200000,
		}},
		Stream: map[string]StreamChatter{"tools": stream},
	}
	if err := disp.ChatStreamCall(context.Background(), "draft", "", domain.Principal{}, streamInput(), 0, sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.toolDeltas) != 2 || sink.done == nil {
		t.Fatalf("deltas=%+v done=%+v", sink.toolDeltas, sink.done)
	}
	if len(sink.done.ToolCalls) != 1 || sink.done.ToolCalls[0].Name != "search" {
		t.Fatalf("done tool_calls = %+v", sink.done.ToolCalls)
	}
	if sink.done.ContextWindow != 200000 || sink.done.CostMicroUSD == nil || *sink.done.CostMicroUSD != 1800 {
		t.Fatalf("done metadata = %+v", sink.done)
	}
}

func TestDispatchChatStreamDoesNotFailOverAfterToolCallDelta(t *testing.T) {
	flaky := &scriptToolStream{
		deltas:    []ToolCallDelta{{Index: 0, ID: "call_1", Name: "search"}, {Index: 0, ArgumentsDelta: "{}"}},
		failAfter: 1,
		failClass: domain.ClassTransient,
	}
	solid := &scriptToolStream{}
	sink := &toolRecordingSink{}
	disp := Dispatch{
		Routes:  memRoutes{streamRoute()},
		Secrets: memSecrets{map[string]string{"house": "secret"}},
		Stream:  map[string]StreamChatter{"flaky": flaky, "solid": solid},
	}
	if err := disp.ChatStreamCall(context.Background(), "draft", "", domain.Principal{}, streamInput(), 0, sink); err != nil {
		t.Fatal(err)
	}
	if solid.invoked != 0 || sink.fail == nil || sink.fail.Class != domain.ClassTransient {
		t.Fatalf("solid=%d fail=%+v", solid.invoked, sink.fail)
	}
}

type usageChatter struct {
	usages []domain.Usage
	idx    int
}

func (u *usageChatter) Chat(context.Context, Attempt, domain.Request) (domain.ChatResult, domain.FailureClass, error) {
	us := u.usages[u.idx]
	u.idx++
	return domain.ChatResult{Text: "ok", Usage: us}, domain.ClassOK, nil
}

func TestDispatchRecordsMetrics(t *testing.T) {
	reg := metrics.NewRegistry()
	rate := int64(1_000_000) // $1 per million tokens = 1 micro-USD per token
	account := domain.Account{ID: "h", Provider: "anthropic", Adapter: "anthropic", CredentialRef: "house", PlanKind: domain.PlanMetered, House: true}
	route := domain.Route{
		Name: "chat", Capability: domain.CapChat,
		Links: []domain.Link{{Position: 1, Account: account, Model: "claude-test"}},
	}
	chatter := &usageChatter{usages: []domain.Usage{
		{Arrived: true, Input: 100, Output: 50, CacheRead: 200, CacheWrite: 25, Reasoning: 10},
		{Arrived: true, Input: 40, Output: 10},
	}}
	disp := Dispatch{
		Routes:  memRoutes{route},
		Secrets: memSecrets{map[string]string{"house": "secret"}},
		Prices:  fixedPrices{domain.Price{Version: "v1", InputMicroUSDPerMTok: &rate, OutputMicroUSDPerMTok: &rate, CacheReadMicroUSDPerMTok: &rate, CacheWriteMicroUSDPerMTok: &rate}},
		Ledger:  &memLedger{},
		Chat:    map[string]Chatter{"anthropic": chatter},
		Metrics: reg,
	}
	in := domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}
	for range 2 {
		if _, err := disp.ChatCall(context.Background(), "chat", "", domain.Principal{}, in); err != nil {
			t.Fatal(err)
		}
	}

	rec := httptest.NewRecorder()
	reg.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()

	want := []string{
		`upmo_gateway_calls_total{provider="anthropic",model="claude-test",class="ok"} 2`,
		`upmo_gateway_call_duration_seconds_count{provider="anthropic",model="claude-test"} 2`,
		`upmo_gateway_tokens_total{provider="anthropic",model="claude-test",kind="input"} 140`,
		`upmo_gateway_tokens_total{provider="anthropic",model="claude-test",kind="output"} 60`,
		`upmo_gateway_tokens_total{provider="anthropic",model="claude-test",kind="cache_read"} 200`,
		`upmo_gateway_tokens_total{provider="anthropic",model="claude-test",kind="cache_write"} 25`,
		`upmo_gateway_tokens_total{provider="anthropic",model="claude-test",kind="reasoning"} 10`,
		`upmo_gateway_cost_micro_usd_total{provider="anthropic",model="claude-test"} 425`,
		`upmo_gateway_cache_hits_total{provider="anthropic",model="claude-test"} 1`,
		`upmo_gateway_cache_misses_total{provider="anthropic",model="claude-test"} 1`,
	}
	for _, line := range want {
		if !strings.Contains(body, line) {
			t.Errorf("missing %q in:\n%s", line, body)
		}
	}
}

func TestDispatchNarrowsTeamsToRequestedTeamIDIntersection(t *testing.T) {
	// User is on teams t1 and t2. teamRoute() has an account shared with [t1, t2]
	// and a workspace fallback.
	// Case 1: TeamID=t2 -> allows t2.
	team, solid := &scriptStream{reply: "team"}, &scriptStream{reply: "house"}
	led := &memLedger{}
	disp := teamDispatch(&memTeams{ids: []string{"t1", "t2"}}, team, solid, led)
	sink := &recordingSink{}
	if err := disp.ChatStreamCall(context.Background(), "draft", "", domain.Principal{User: "u1", TeamID: "t2"}, streamInput(), 0, sink); err != nil {
		t.Fatal(err)
	}
	if team.calls != 1 || solid.calls != 0 || len(led.rows) != 1 || led.rows[0].OwnerTeamID != "t2" {
		t.Fatalf("member intersection: team=%d solid=%d rows=%+v", team.calls, solid.calls, led.rows)
	}

	// Case 2: TeamID=t9 (user is NOT on t9) -> intersection is empty, never grants t9.
	team, solid = &scriptStream{reply: "team"}, &scriptStream{reply: "house"}
	led = &memLedger{}
	disp = teamDispatch(&memTeams{ids: []string{"t1", "t2"}}, team, solid, led)
	sink = &recordingSink{}
	if err := disp.ChatStreamCall(context.Background(), "draft", "", domain.Principal{User: "u1", TeamID: "t9"}, streamInput(), 0, sink); err != nil {
		t.Fatal(err)
	}
	if team.calls != 0 || solid.calls != 1 || len(led.rows) != 1 || led.rows[0].OwnerTeamID != "" {
		t.Fatalf("non-member intersection: team=%d solid=%d rows=%+v", team.calls, solid.calls, led.rows)
	}

	// Case 3: TeamID=t3 (user is on t1,t2, NOT t3; account is shared with t1,t2) -> denied t1/t2.
	team, solid = &scriptStream{reply: "team"}, &scriptStream{reply: "house"}
	led = &memLedger{}
	disp = teamDispatch(&memTeams{ids: []string{"t1", "t2"}}, team, solid, led)
	if err := disp.ChatStreamCall(context.Background(), "draft", "", domain.Principal{User: "u1", TeamID: "t3"}, streamInput(), 0, &recordingSink{}); err != nil {
		t.Fatal(err)
	}
	if team.calls != 0 || solid.calls != 1 {
		t.Fatalf("other-team excluded: team=%d solid=%d", team.calls, solid.calls)
	}
}

func TestDispatchChatCallTeamNarrowing(t *testing.T) {
	chatInput := domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}
	newChatDisp := func(teams Teams, team, solid *scriptChatter, led *memLedger) Dispatch {
		return Dispatch{
			Routes:  memRoutes{teamRoute()},
			Teams:   teams,
			Secrets: memSecrets{map[string]string{"house": "secret", "shared": "team-secret"}},
			Ledger:  led,
			Chat:    map[string]Chatter{"team": team, "solid": solid},
		}
	}

	// Case 1: TeamID=t2 -> allows t2.
	team, solid := &scriptChatter{reply: "team"}, &scriptChatter{reply: "house"}
	led := &memLedger{}
	disp := newChatDisp(&memTeams{ids: []string{"t1", "t2"}}, team, solid, led)
	got, err := disp.ChatCall(context.Background(), "draft", "", domain.Principal{User: "u1", TeamID: "t2"}, chatInput)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "team" || team.calls != 1 || solid.calls != 0 || len(led.rows) != 1 || led.rows[0].OwnerTeamID != "t2" {
		t.Fatalf("member intersection: got=%+v team=%d solid=%d rows=%+v", got, team.calls, solid.calls, led.rows)
	}

	// Case 2: TeamID=t9 (user is NOT on t9) -> intersection is empty, never grants t9.
	team, solid = &scriptChatter{reply: "team"}, &scriptChatter{reply: "house"}
	led = &memLedger{}
	disp = newChatDisp(&memTeams{ids: []string{"t1", "t2"}}, team, solid, led)
	got, err = disp.ChatCall(context.Background(), "draft", "", domain.Principal{User: "u1", TeamID: "t9"}, chatInput)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "house" || team.calls != 0 || solid.calls != 1 || len(led.rows) != 1 || led.rows[0].OwnerTeamID != "" {
		t.Fatalf("non-member intersection: got=%+v team=%d solid=%d rows=%+v", got, team.calls, solid.calls, led.rows)
	}

	// Case 3: TeamID=t3 (user is on t1,t2, NOT t3; account is shared with t1,t2) -> denied t1/t2.
	team, solid = &scriptChatter{reply: "team"}, &scriptChatter{reply: "house"}
	led = &memLedger{}
	disp = newChatDisp(&memTeams{ids: []string{"t1", "t2"}}, team, solid, led)
	got, err = disp.ChatCall(context.Background(), "draft", "", domain.Principal{User: "u1", TeamID: "t3"}, chatInput)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "house" || team.calls != 0 || solid.calls != 1 {
		t.Fatalf("other-team excluded: got=%+v team=%d solid=%d", got, team.calls, solid.calls)
	}
}

type inputPrices struct {
	inputs     map[string][]string
	suggestion string
}

func (p inputPrices) Price(context.Context, string, string) (domain.Price, bool, error) {
	return domain.Price{}, false, nil
}

func (p inputPrices) ModelInputs(_ context.Context, _, _, model string) ([]string, bool, error) {
	in, ok := p.inputs[model]
	return in, ok, nil
}

func (p inputPrices) SuggestModelForInput(context.Context, string, string, string) (string, bool, error) {
	return p.suggestion, p.suggestion != "", nil
}

func TestDispatchRefusesUnsupportedInputBeforeProviderCall(t *testing.T) {
	account := domain.Account{ID: "acc-1", Provider: "openai", Adapter: "solid", CredentialRef: "house", House: true}
	route := domain.Route{
		Name: "draft", Capability: domain.CapChat,
		Links: []domain.Link{{Position: 1, Account: account, Model: "gpt-3.5-turbo", RetryBudget: 0}},
	}
	chatter := &scriptChatter{reply: "never"}
	streamer := &scriptStream{reply: "never"}
	disp := Dispatch{
		Routes:  memRoutes{route},
		Secrets: memSecrets{map[string]string{"house": "secret"}},
		Prices:  inputPrices{inputs: map[string][]string{"gpt-3.5-turbo": {domain.InputText}}, suggestion: "gpt-4o"},
		Chat:    map[string]Chatter{"solid": chatter},
		Stream:  map[string]StreamChatter{"solid": streamer},
	}
	in := domain.Input{
		Messages: []domain.Message{{
			Role:  domain.RoleUser,
			Parts: []domain.Part{{Kind: domain.InputImage, MIMEType: "image/png", Data: "abc"}},
		}},
	}
	_, err := disp.ChatCall(context.Background(), "draft", "", domain.Principal{}, in)
	var unsupported domain.UnsupportedInputError
	if !errors.As(err, &unsupported) || unsupported.Model != "gpt-3.5-turbo" || unsupported.Input != domain.InputImage || unsupported.Suggestion != "gpt-4o" {
		t.Fatalf("ChatCall err = %v", err)
	}
	if chatter.calls != 0 {
		t.Fatalf("provider chatter was called %d times", chatter.calls)
	}

	sink := &recordingSink{}
	err = disp.ChatStreamCall(context.Background(), "draft", "", domain.Principal{}, in, 0, sink)
	if !errors.As(err, &unsupported) {
		t.Fatalf("ChatStreamCall err = %v", err)
	}
	if streamer.calls != 0 || sink.deltas != 0 || sink.done != nil || sink.fail != nil {
		t.Fatalf("stream touched provider or sink: calls=%d sink=%+v", streamer.calls, sink)
	}
}
