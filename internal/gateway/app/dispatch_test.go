package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/gateway/domain"
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

func (s *scriptChatter) Chat(_ context.Context, _ Attempt, req domain.Request) (string, domain.Usage, domain.FailureClass, error) {
	s.calls++
	s.got = req
	return s.reply, domain.Usage{}, domain.ClassOK, nil
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
		Tools:    []domain.Tool{{Name: "search", Schema: json.RawMessage(`{"b":1,"a":2}`)}},
		System:   "be brief",
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "ok" || c.calls != 1 {
		t.Fatalf("got=%q calls=%d", got, c.calls)
	}
	if string(c.got.Tools[0].Schema) != `{"a":2,"b":1}` {
		t.Fatalf("schema = %s", c.got.Tools[0].Schema)
	}
	if len(c.got.Cache) != 2 {
		t.Fatalf("cache = %v", c.got.Cache)
	}
}

type memBundles struct {
	byName map[string]domain.Bundle
	agent  map[string]string
}

func (m memBundles) Bundle(_ context.Context, name string) (domain.Bundle, error) {
	b, ok := m.byName[name]
	if !ok {
		return domain.Bundle{}, errors.New("gateway: no bundle " + name)
	}
	return b, nil
}

func (m memBundles) AgentBundle(ctx context.Context, agentID string) (domain.Bundle, error) {
	name, ok := m.agent[agentID]
	if !ok {
		return domain.Bundle{}, errors.New("gateway: agent has no tool bundle")
	}
	return m.Bundle(ctx, name)
}

func TestDispatchChatMasksToAgentBundle(t *testing.T) {
	house := domain.Account{ID: "h", Adapter: "fake", CredentialRef: "house", House: true}
	r := domain.Route{
		Name: "draft", Capability: domain.CapChat,
		Links: []domain.Link{{Position: 1, Account: house, Model: "a", RetryBudget: 0}},
	}
	c := &scriptChatter{reply: "ok"}
	disp := Dispatch{
		Routes:  memRoutes{r},
		Secrets: memSecrets{map[string]string{"house": "secret"}},
		Bundles: memBundles{
			byName: map[string]domain.Bundle{"core": {Name: "core", Tools: []string{"search"}}},
			agent:  map[string]string{"11111111-1111-1111-1111-111111111111": "core"},
		},
		Chat: map[string]Chatter{"fake": c},
	}
	_, err := disp.ChatCall(context.Background(), "draft", "", domain.Principal{Agent: "11111111-1111-1111-1111-111111111111"}, domain.Input{
		Tools: []domain.Tool{
			{Name: "search", Schema: json.RawMessage(`{}`)},
			{Name: "write", Schema: json.RawMessage(`{}`)},
		},
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.got.Tools) != 1 || c.got.Tools[0].Name != "search" {
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

func TestDispatchChatAgentWithoutBundleFailsClosed(t *testing.T) {
	house := domain.Account{ID: "h", Adapter: "fake", CredentialRef: "house", House: true}
	r := domain.Route{
		Name: "draft", Capability: domain.CapChat,
		Links: []domain.Link{{Position: 1, Account: house, Model: "a", RetryBudget: 0}},
	}
	disp := Dispatch{
		Routes:  memRoutes{r},
		Secrets: memSecrets{map[string]string{"house": "secret"}},
		Bundles: memBundles{byName: map[string]domain.Bundle{}, agent: map[string]string{}},
		Chat:    map[string]Chatter{"fake": &scriptChatter{reply: "ok"}},
	}
	_, err := disp.ChatCall(context.Background(), "draft", "", domain.Principal{Agent: "11111111-1111-1111-1111-111111111111"}, domain.Input{
		Tools:    []domain.Tool{{Name: "search"}},
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
	})
	if err == nil {
		t.Fatal("agent without bundle")
	}
}
