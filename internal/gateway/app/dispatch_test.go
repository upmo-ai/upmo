package app

import (
	"context"
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

func (s *scriptDecider) Decide(context.Context, Attempt, domain.DecideRequest) (domain.DecideResult, domain.FailureClass, error) {
	i := s.calls
	s.calls++
	if i >= len(s.class) {
		return s.pick, domain.ClassOK, nil
	}
	c := s.class[i]
	if c == domain.ClassOK {
		return s.pick, c, nil
	}
	return domain.DecideResult{}, c, errors.New(string(c))
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
	disp := Dispatch{
		Routes:  memRoutes{r},
		Secrets: memSecrets{map[string]string{"house": "secret"}},
		Decide:  map[string]Decider{"fake": d},
	}
	got, err := disp.DecideCall(context.Background(), "memory-check", "", domain.DecideRequest{Kind: domain.DecisionYesNo, Prompt: "keep?"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.YesNo || d.calls != 3 {
		t.Fatalf("result=%v calls=%d", got, d.calls)
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
	_, err := disp.DecideCall(context.Background(), "memory-check", "", domain.DecideRequest{Kind: domain.DecisionYesNo, Prompt: "keep?"})
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
	got, err := disp.DecideCall(context.Background(), "memory-check", "", domain.DecideRequest{Kind: domain.DecisionYesNo, Prompt: "keep?"})
	if err != nil {
		t.Fatal(err)
	}
	if got.YesNo || bad.calls != 1 || ok.calls != 1 {
		t.Fatalf("got=%v bad=%d ok=%d", got, bad.calls, ok.calls)
	}
}
