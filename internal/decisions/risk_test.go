package decisions

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

const riskUser = "018f3b2c-7a10-7c4a-8e11-111111111111"

var riskHops = []Hop{
	{Position: 1, Adapter: "openai", Model: "small", Weight: 1, Retry: 0},
}

func TestSecondOpinionRejectsInvalidCasesBeforeDeciding(t *testing.T) {
	tests := []struct {
		name  string
		cases []Risk
	}{
		{
			name: "blank prompt",
			cases: []Risk{{
				Prompt: " \t ",
				Prior:  RiskAllow,
				Danger: true,
			}},
		},
		{
			name: "unknown prior",
			cases: []Risk{{
				Prompt: "Review permission",
				Prior:  "permit",
				Danger: true,
			}},
		},
		{
			name: "no danger case",
			cases: []Risk{{
				Prompt: "Review permission",
				Prior:  RiskAllow,
				Danger: false,
			}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			book := &riskGates{}
			run := &riskRunner{choices: []string{RiskAsk}}
			err := SecondOpinion(
				context.Background(),
				book,
				riskAccess(),
				riskHops,
				test.cases,
				run,
			)
			if !errors.Is(err, ErrConfig) {
				t.Fatalf("error = %v", err)
			}
			if run.calls != 0 {
				t.Fatalf("runner calls = %d", run.calls)
			}
			if len(book.saved) != 0 {
				t.Fatalf("saved = %+v", book.saved)
			}
		})
	}
}

func TestSecondOpinionRefusesCallerWithoutSettingsManage(t *testing.T) {
	book := &riskGates{}
	run := &riskRunner{choices: []string{RiskAsk}}
	access := Access{UserID: riskUser, Allow: "task:edit"}
	err := SecondOpinion(context.Background(), book, access, riskHops, oneRisk(), run)
	if !errors.Is(err, ErrAccess) {
		t.Fatalf("error = %v", err)
	}
	if run.calls != 0 {
		t.Fatalf("runner calls = %d", run.calls)
	}
}

func TestSecondOpinionSkipsRunnerForStoredFingerprint(t *testing.T) {
	fp, err := Fingerprint(Config{Name: RouteRisk, Kind: KindChoice, Hops: riskHops})
	if err != nil {
		t.Fatal(err)
	}
	book := &riskGates{fingerprint: fp, accepted: true}
	run := &riskRunner{choices: []string{RiskAsk}}
	if err := SecondOpinion(
		context.Background(),
		book,
		riskAccess(),
		riskHops,
		oneRisk(),
		run,
	); err != nil {
		t.Fatal(err)
	}
	if run.calls != 0 {
		t.Fatalf("runner calls = %d", run.calls)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestSecondOpinionRejectsNilRunnerForNewFingerprint(t *testing.T) {
	book := &riskGates{}
	err := SecondOpinion(context.Background(), book, riskAccess(), riskHops, oneRisk(), nil)
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("error = %v", err)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestSecondOpinionStoresPassingDangerEscalation(t *testing.T) {
	book := &riskGates{}
	run := &riskRunner{choices: []string{RiskAsk}}
	if err := SecondOpinion(
		context.Background(),
		book,
		riskAccess(),
		riskHops,
		oneRisk(),
		run,
	); err != nil {
		t.Fatal(err)
	}
	if len(book.saved) != 1 {
		t.Fatalf("saved = %d", len(book.saved))
	}
	got := book.saved[0]
	if got.Route != RouteRisk || got.Kind != KindChoice || got.Hit != 1 || got.Total != 1 || got.Bar != 100 {
		t.Fatalf("gate = %+v", got)
	}
	wantOptions := []string{RiskAllow, RiskAsk, RiskBlock}
	if len(run.options) != 1 || !reflect.DeepEqual(run.options[0], wantOptions) {
		t.Fatalf("runner options = %+v", run.options)
	}
}

func TestSecondOpinionRejectsFalseSafeOnDangerCase(t *testing.T) {
	book := &riskGates{}
	run := &riskRunner{choices: []string{RiskAllow}}
	err := SecondOpinion(context.Background(), book, riskAccess(), riskHops, oneRisk(), run)
	if !errors.Is(err, ErrGate) {
		t.Fatalf("error = %v", err)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestSecondOpinionRejectsRelaxedVerdictAndSavesNothing(t *testing.T) {
	book := &riskGates{}
	run := &riskRunner{choices: []string{RiskBlock, RiskAllow}}
	cases := []Risk{
		{Prompt: "Review dangerous permission", Prior: RiskBlock, Danger: true},
		{Prompt: "Review ordinary permission", Prior: RiskAsk, Danger: false},
	}
	err := SecondOpinion(context.Background(), book, riskAccess(), riskHops, cases, run)
	if !errors.Is(err, ErrGate) {
		t.Fatalf("error = %v", err)
	}
	if run.calls != 2 {
		t.Fatalf("runner calls = %d", run.calls)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestSecondOpinionStoresEveryPassingEscalation(t *testing.T) {
	book := &riskGates{}
	run := &riskRunner{choices: []string{RiskAsk, RiskBlock}}
	cases := []Risk{
		{Prompt: "Review dangerous permission", Prior: RiskAllow, Danger: true},
		{Prompt: "Review ordinary permission", Prior: RiskAllow, Danger: false},
	}
	if err := SecondOpinion(
		context.Background(),
		book,
		riskAccess(),
		riskHops,
		cases,
		run,
	); err != nil {
		t.Fatal(err)
	}
	if len(book.saved) != 1 {
		t.Fatalf("saved = %d", len(book.saved))
	}
	got := book.saved[0]
	if got.Hit != 2 || got.Total != 2 || got.Bar != 100 {
		t.Fatalf("gate = %+v", got)
	}
}

func TestSecondOpinionRejectsUnknownChoice(t *testing.T) {
	book := &riskGates{}
	run := &riskRunner{choices: []string{"permit"}}
	err := SecondOpinion(context.Background(), book, riskAccess(), riskHops, oneRisk(), run)
	if !errors.Is(err, ErrGate) {
		t.Fatalf("error = %v", err)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestSecondOpinionReturnsRunnerErrorWithoutSaving(t *testing.T) {
	want := errors.New("runner failed")
	book := &riskGates{}
	run := &riskRunner{err: want}
	err := SecondOpinion(context.Background(), book, riskAccess(), riskHops, oneRisk(), run)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestSecondOpinionTrimsWithoutMutatingCallerPrompt(t *testing.T) {
	book := &riskGates{}
	cases := []Risk{{
		Prompt: "  Review permission \t",
		Prior:  " allow ",
		Danger: true,
	}}
	want := cases[0]
	run := &riskRunner{choices: []string{" ask "}}
	if err := SecondOpinion(
		context.Background(),
		book,
		riskAccess(),
		riskHops,
		cases,
		run,
	); err != nil {
		t.Fatal(err)
	}
	if cases[0] != want {
		t.Fatalf("case mutated: %+v", cases[0])
	}
	if len(run.prompts) != 1 || run.prompts[0] != "Review permission" {
		t.Fatalf("runner prompts = %q", run.prompts)
	}
}

func oneRisk() []Risk {
	return []Risk{{
		Prompt: "Review permission",
		Prior:  RiskAllow,
		Danger: true,
	}}
}

func riskAccess() Access {
	return Access{UserID: riskUser, Allow: "settings:manage"}
}

type riskGates struct {
	fingerprint string
	accepted    bool
	saved       []Gate
}

func (g *riskGates) Accepted(context.Context, Access, string) (string, bool, error) {
	return g.fingerprint, g.accepted, nil
}

func (g *riskGates) Save(_ context.Context, _ Access, gate Gate) error {
	g.saved = append(g.saved, gate)
	return nil
}

type riskRunner struct {
	choices []string
	err     error
	calls   int
	prompts []string
	options [][]string
}

func (r *riskRunner) Decide(_ context.Context, prompt string, options []string) (bool, string, error) {
	call := r.calls
	r.calls++
	r.prompts = append(r.prompts, prompt)
	r.options = append(r.options, append([]string(nil), options...))
	if r.err != nil {
		return false, "", r.err
	}
	if call < len(r.choices) {
		return true, r.choices[call], nil
	}
	return true, "", nil
}
