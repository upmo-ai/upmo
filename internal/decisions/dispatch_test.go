package decisions

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

const (
	dispatchUser   = "018f3b2c-7a10-7c4a-8e11-111111111111"
	dispatchAgentA = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaaa"
	dispatchAgentB = "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"
)

var dispatchHops = []Hop{
	{Position: 1, Adapter: "openai", Model: "small", Weight: 1, Retry: 0},
}

func TestDispatchRejectsInvalidCasesBeforeRunning(t *testing.T) {
	tests := []struct {
		name  string
		cases []Labeled
	}{
		{
			name: "label outside options",
			cases: []Labeled{{
				Prompt:  "Choose",
				Options: []string{dispatchAgentA, dispatchAgentB},
				Label:   "018f3b2c-7a10-7c4a-8e11-cccccccccccc",
			}},
		},
		{
			name: "non UUID option",
			cases: []Labeled{{
				Prompt:  "Choose",
				Options: []string{dispatchAgentA, "not-an-agent"},
				Label:   dispatchAgentA,
			}},
		},
		{
			name: "one option",
			cases: []Labeled{{
				Prompt:  "Choose",
				Options: []string{dispatchAgentA},
				Label:   dispatchAgentA,
			}},
		},
		{
			name: "duplicate options",
			cases: []Labeled{{
				Prompt:  "Choose",
				Options: []string{dispatchAgentA, dispatchAgentA},
				Label:   dispatchAgentA,
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			book := &dispatchGates{}
			run := &dispatchRunner{choice: dispatchAgentA}
			err := Dispatch(context.Background(), book, dispatchAccess(), dispatchHops, 100, tt.cases, run)
			if !errors.Is(err, ErrConfig) {
				t.Fatalf("error = %v", err)
			}
			if run.calls != 0 {
				t.Fatalf("runner calls = %d", run.calls)
			}
		})
	}
}

func TestIntentRejectsOptionWithSpaceBeforeRunning(t *testing.T) {
	book := &dispatchGates{}
	run := &dispatchRunner{choice: "write"}
	err := Intent(context.Background(), book, dispatchAccess(), dispatchHops, 100, []Labeled{{
		Prompt:  "Choose",
		Options: []string{"write", "read later"},
		Label:   "write",
	}}, run)
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("error = %v", err)
	}
	if run.calls != 0 {
		t.Fatalf("runner calls = %d", run.calls)
	}
}

func TestDispatchSavesChoiceRoute(t *testing.T) {
	book := &dispatchGates{}
	run := &dispatchRunner{choice: dispatchAgentA}
	err := Dispatch(context.Background(), book, dispatchAccess(), dispatchHops, 80, dispatchCases(), run)
	if err != nil {
		t.Fatal(err)
	}
	if len(book.saved) != 1 {
		t.Fatalf("saved = %d", len(book.saved))
	}
	got := book.saved[0]
	if got.Route != RouteDispatch || got.Kind != KindChoice || got.Bar != 80 {
		t.Fatalf("gate = %+v", got)
	}
}

func TestIntentSavesRoute(t *testing.T) {
	book := &dispatchGates{}
	run := &dispatchRunner{choice: "write"}
	err := Intent(context.Background(), book, dispatchAccess(), dispatchHops, 100, []Labeled{{
		Prompt:  "Choose",
		Options: []string{"read", "write"},
		Label:   "write",
	}}, run)
	if err != nil {
		t.Fatal(err)
	}
	if len(book.saved) != 1 || book.saved[0].Route != RouteIntent {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestDispatchMissAtFullBarSavesNothing(t *testing.T) {
	book := &dispatchGates{}
	run := &dispatchRunner{choice: dispatchAgentB}
	err := Dispatch(context.Background(), book, dispatchAccess(), dispatchHops, 100, dispatchCases(), run)
	if !errors.Is(err, ErrGate) {
		t.Fatalf("error = %v", err)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestDispatchSkipsRunnerForStoredFingerprint(t *testing.T) {
	fp, err := Fingerprint(Config{Name: RouteDispatch, Kind: KindChoice, Hops: dispatchHops})
	if err != nil {
		t.Fatal(err)
	}
	book := &dispatchGates{fingerprint: fp, accepted: true}
	run := &dispatchRunner{choice: dispatchAgentA}
	if err := Dispatch(context.Background(), book, dispatchAccess(), dispatchHops, 100, dispatchCases(), run); err != nil {
		t.Fatal(err)
	}
	if run.calls != 0 {
		t.Fatalf("runner calls = %d", run.calls)
	}
}

func TestDispatchRefusesCallerWithoutSettingsManage(t *testing.T) {
	book := &dispatchGates{}
	run := &dispatchRunner{choice: dispatchAgentA}
	access := Access{UserID: dispatchUser, Allow: "task:edit"}
	err := Dispatch(context.Background(), book, access, dispatchHops, 100, dispatchCases(), run)
	if !errors.Is(err, ErrAccess) {
		t.Fatalf("error = %v", err)
	}
	if run.calls != 0 {
		t.Fatalf("runner calls = %d", run.calls)
	}
}

func TestDispatchTrimsCopiesWithoutMutatingCases(t *testing.T) {
	options := []string{" " + dispatchAgentA + " ", "\t" + dispatchAgentB}
	cases := []Labeled{{
		Prompt:  " Choose an agent ",
		Options: options,
		Label:   "\t" + dispatchAgentA + " ",
	}}
	wantCases := []Labeled{{
		Prompt:  cases[0].Prompt,
		Options: append([]string(nil), options...),
		Label:   cases[0].Label,
	}}
	run := &dispatchRunner{choice: dispatchAgentA}
	if err := Dispatch(context.Background(), &dispatchGates{}, dispatchAccess(), dispatchHops, 100, cases, run); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cases, wantCases) {
		t.Fatalf("cases mutated: %+v", cases)
	}
	if run.prompt != "Choose an agent" || !reflect.DeepEqual(run.options, []string{dispatchAgentA, dispatchAgentB}) {
		t.Fatalf("runner got %q %+v", run.prompt, run.options)
	}
}

func dispatchAccess() Access {
	return Access{UserID: dispatchUser, Allow: "settings:manage"}
}

func dispatchCases() []Labeled {
	return []Labeled{{
		Prompt:  "Choose an agent",
		Options: []string{dispatchAgentA, dispatchAgentB},
		Label:   dispatchAgentA,
	}}
}

type dispatchGates struct {
	fingerprint string
	accepted    bool
	saved       []Gate
}

func (g *dispatchGates) Accepted(context.Context, Access, string) (string, bool, error) {
	return g.fingerprint, g.accepted, nil
}

func (g *dispatchGates) Save(_ context.Context, _ Access, gate Gate) error {
	g.saved = append(g.saved, gate)
	return nil
}

type dispatchRunner struct {
	choice  string
	err     error
	calls   int
	prompt  string
	options []string
}

func (r *dispatchRunner) Decide(_ context.Context, prompt string, options []string) (bool, string, error) {
	r.calls++
	r.prompt = prompt
	r.options = append([]string(nil), options...)
	return false, r.choice, r.err
}
