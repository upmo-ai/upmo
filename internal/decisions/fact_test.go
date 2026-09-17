package decisions

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const factUser = "018f3b2c-7a10-7c4a-8e11-111111111111"

var factHops = []Hop{
	{Position: 1, Adapter: "openai", Model: "small", Weight: 1, Retry: 0},
}

func TestFactKeepRejectsBlankClaimBeforeRunning(t *testing.T) {
	book := &factGates{}
	run := &factRunner{answers: []bool{true, true}}
	err := FactKeep(context.Background(), book, factAccess(), factHops, 100, []Fact{
		{Claim: "Worth keeping", Yes: true},
		{Claim: " \t ", Yes: true},
	}, run)
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("error = %v", err)
	}
	if run.calls != 0 {
		t.Fatalf("runner calls = %d", run.calls)
	}
}

func TestFactKeepRejectsClaimOverOneThousandRunesBeforeRunning(t *testing.T) {
	book := &factGates{}
	run := &factRunner{answers: []bool{true, true}}
	err := FactKeep(context.Background(), book, factAccess(), factHops, 100, []Fact{
		{Claim: "Worth keeping", Yes: true},
		{Claim: strings.Repeat("界", 1001), Yes: true},
	}, run)
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("error = %v", err)
	}
	if run.calls != 0 {
		t.Fatalf("runner calls = %d", run.calls)
	}
}

func TestFactKeepRefusesCallerWithoutSettingsManage(t *testing.T) {
	book := &factGates{}
	run := &factRunner{answers: []bool{true}}
	access := Access{UserID: factUser, Allow: "task:edit"}
	err := FactKeep(context.Background(), book, access, factHops, 100, []Fact{{
		Claim: "Worth keeping",
		Yes:   true,
	}}, run)
	if !errors.Is(err, ErrAccess) {
		t.Fatalf("error = %v", err)
	}
	if run.calls != 0 {
		t.Fatalf("runner calls = %d", run.calls)
	}
}

func TestFactKeepSkipsRunnerForStoredFingerprint(t *testing.T) {
	fp, err := Fingerprint(Config{Name: RouteFact, Kind: KindYesNo, Hops: factHops})
	if err != nil {
		t.Fatal(err)
	}
	book := &factGates{fingerprint: fp, accepted: true}
	run := &factRunner{answers: []bool{true}}
	err = FactKeep(context.Background(), book, factAccess(), factHops, 100, []Fact{{
		Claim: "Worth keeping",
		Yes:   true,
	}}, run)
	if err != nil {
		t.Fatal(err)
	}
	if run.calls != 0 {
		t.Fatalf("runner calls = %d", run.calls)
	}
}

func TestFactKeepRejectsNilRunnerForNewFingerprint(t *testing.T) {
	book := &factGates{}
	err := FactKeep(context.Background(), book, factAccess(), factHops, 100, []Fact{{
		Claim: "Worth keeping",
		Yes:   true,
	}}, nil)
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("error = %v", err)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestFactKeepStoresRecallNotPrecision(t *testing.T) {
	book := &factGates{}
	run := &factRunner{answers: []bool{true, false, true, false}}
	facts := []Fact{
		{Claim: "One", Yes: true},
		{Claim: "Two", Yes: true},
		{Claim: "Three", Yes: false},
		{Claim: "Four", Yes: false},
	}
	if err := FactKeep(context.Background(), book, factAccess(), factHops, 50, facts, run); err != nil {
		t.Fatal(err)
	}
	if len(book.saved) != 1 {
		t.Fatalf("saved = %d", len(book.saved))
	}
	got := book.saved[0]
	if got.Route != RouteFact || got.Kind != KindYesNo || got.Hit != 1 || got.Total != 2 || got.Bar != 50 {
		t.Fatalf("gate = %+v", got)
	}
}

func TestFactKeepFailedRecallSavesNothing(t *testing.T) {
	book := &factGates{}
	run := &factRunner{answers: []bool{true, false, true, false}}
	facts := []Fact{
		{Claim: "One", Yes: true},
		{Claim: "Two", Yes: true},
		{Claim: "Three", Yes: false},
		{Claim: "Four", Yes: false},
	}
	err := FactKeep(context.Background(), book, factAccess(), factHops, 100, facts, run)
	if !errors.Is(err, ErrGate) {
		t.Fatalf("error = %v", err)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestFactKeepWithoutLabelledYesReturnsGateError(t *testing.T) {
	book := &factGates{}
	run := &factRunner{answers: []bool{true, true}}
	err := FactKeep(context.Background(), book, factAccess(), factHops, 1, []Fact{
		{Claim: "One", Yes: false},
		{Claim: "Two", Yes: false},
	}, run)
	if !errors.Is(err, ErrGate) {
		t.Fatalf("error = %v", err)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestFactKeepRunnerErrorSavesNothing(t *testing.T) {
	want := errors.New("runner failed")
	book := &factGates{}
	run := &factRunner{err: want}
	err := FactKeep(context.Background(), book, factAccess(), factHops, 100, []Fact{{
		Claim: "Worth keeping",
		Yes:   true,
	}}, run)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestFactKeepTrimsWithoutMutatingFacts(t *testing.T) {
	book := &factGates{}
	run := &factRunner{answers: []bool{true}}
	facts := []Fact{{Claim: "  Worth keeping \t", Yes: true}}
	wantClaim := facts[0].Claim
	if err := FactKeep(context.Background(), book, factAccess(), factHops, 100, facts, run); err != nil {
		t.Fatal(err)
	}
	if facts[0].Claim != wantClaim {
		t.Fatalf("claim mutated to %q", facts[0].Claim)
	}
	if len(run.prompts) != 1 || run.prompts[0] != "Worth keeping" {
		t.Fatalf("runner prompts = %q", run.prompts)
	}
	if len(run.options) != 1 || run.options[0] != nil {
		t.Fatalf("runner options = %v", run.options)
	}
}

func factAccess() Access {
	return Access{UserID: factUser, Allow: "settings:manage"}
}

type factGates struct {
	fingerprint string
	accepted    bool
	saved       []Gate
}

func (g *factGates) Accepted(context.Context, Access, string) (string, bool, error) {
	return g.fingerprint, g.accepted, nil
}

func (g *factGates) Save(_ context.Context, _ Access, gate Gate) error {
	g.saved = append(g.saved, gate)
	return nil
}

type factRunner struct {
	answers []bool
	err     error
	calls   int
	prompts []string
	options [][]string
}

func (r *factRunner) Decide(_ context.Context, prompt string, options []string) (bool, string, error) {
	r.prompts = append(r.prompts, prompt)
	r.options = append(r.options, options)
	answer := false
	if r.calls < len(r.answers) {
		answer = r.answers[r.calls]
	}
	r.calls++
	return answer, "ignored", r.err
}
