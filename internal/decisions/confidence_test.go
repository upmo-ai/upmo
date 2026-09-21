package decisions

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const confidenceUser = "018f3b2c-7a10-7c4a-8e11-111111111111"

var confidenceHops = []Hop{
	{Position: 1, Adapter: "openai", Model: "small", Weight: 1, Retry: 0},
}

func TestConfidenceRejectsInvalidOutputsBeforePreparingOrDeciding(t *testing.T) {
	tests := []struct {
		name    string
		outputs []Output
	}{
		{name: "empty output list"},
		{
			name: "blank summary",
			outputs: []Output{{
				Summary: " \t\n ",
			}},
		},
		{
			name: "summary over 200 runes",
			outputs: []Output{{
				Summary: strings.Repeat("é", 201),
			}},
		},
		{
			name: "detail over 2000 runes",
			outputs: []Output{{
				Summary: "Sound output",
				Detail:  strings.Repeat("界", 2001),
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			book := &confidenceGates{}
			run := &confidenceRunner{answers: []bool{true}}
			err := Confidence(
				context.Background(),
				book,
				confidenceAccess(),
				confidenceHops,
				100,
				tt.outputs,
				run,
			)
			if !errors.Is(err, ErrConfig) {
				t.Fatalf("error = %v", err)
			}
			if book.acceptedCalls != 0 {
				t.Fatalf("accepted calls = %d", book.acceptedCalls)
			}
			if run.calls != 0 {
				t.Fatalf("runner calls = %d", run.calls)
			}
		})
	}
}

func TestConfidenceRefusesCallerWithoutSettingsManage(t *testing.T) {
	book := &confidenceGates{}
	run := &confidenceRunner{answers: []bool{true}}
	err := Confidence(
		context.Background(),
		book,
		Access{UserID: confidenceUser, Allow: "task:edit"},
		confidenceHops,
		100,
		[]Output{{Summary: "Sound output", Yes: true}},
		run,
	)
	if !errors.Is(err, ErrAccess) {
		t.Fatalf("error = %v", err)
	}
	if run.calls != 0 {
		t.Fatalf("runner calls = %d", run.calls)
	}
}

func TestConfidenceSkipsRunnerForStoredFingerprint(t *testing.T) {
	fingerprint, err := Fingerprint(Config{
		Name: RouteConfidence,
		Kind: KindYesNo,
		Hops: confidenceHops,
	})
	if err != nil {
		t.Fatal(err)
	}
	book := &confidenceGates{fingerprint: fingerprint, accepted: true}
	run := &confidenceRunner{answers: []bool{true}}
	err = Confidence(
		context.Background(),
		book,
		confidenceAccess(),
		confidenceHops,
		100,
		[]Output{{Summary: "Sound output", Yes: true}},
		run,
	)
	if err != nil {
		t.Fatal(err)
	}
	if run.calls != 0 {
		t.Fatalf("runner calls = %d", run.calls)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestConfidenceRejectsNilRunnerForNewFingerprint(t *testing.T) {
	book := &confidenceGates{}
	err := Confidence(
		context.Background(),
		book,
		confidenceAccess(),
		confidenceHops,
		100,
		[]Output{{Summary: "Sound output", Yes: true}},
		nil,
	)
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("error = %v", err)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestConfidenceStoresPassingPrecision(t *testing.T) {
	book := &confidenceGates{}
	run := &confidenceRunner{answers: []bool{true, true, true, false}}
	if err := Confidence(
		context.Background(),
		book,
		confidenceAccess(),
		confidenceHops,
		66,
		confidenceOutputs(),
		run,
	); err != nil {
		t.Fatal(err)
	}
	if len(book.saved) != 1 {
		t.Fatalf("saved = %d", len(book.saved))
	}
	got := book.saved[0]
	if got.Route != RouteConfidence ||
		got.Kind != KindYesNo ||
		got.Hit != 2 ||
		got.Total != 3 ||
		got.Bar != 66 {
		t.Fatalf("gate = %+v", got)
	}
}

func TestConfidenceFailedPrecisionSavesNothing(t *testing.T) {
	book := &confidenceGates{}
	run := &confidenceRunner{answers: []bool{true, true, true, false}}
	err := Confidence(
		context.Background(),
		book,
		confidenceAccess(),
		confidenceHops,
		67,
		confidenceOutputs(),
		run,
	)
	if !errors.Is(err, ErrGate) {
		t.Fatalf("error = %v", err)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestConfidenceAllNoReturnsGateError(t *testing.T) {
	book := &confidenceGates{}
	run := &confidenceRunner{answers: []bool{false, false, false, false}}
	err := Confidence(
		context.Background(),
		book,
		confidenceAccess(),
		confidenceHops,
		1,
		confidenceOutputs(),
		run,
	)
	if !errors.Is(err, ErrGate) {
		t.Fatalf("error = %v", err)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestConfidenceReturnsDecideErrorWithoutSaving(t *testing.T) {
	want := errors.New("runner failed")
	book := &confidenceGates{}
	run := &confidenceRunner{err: want}
	err := Confidence(
		context.Background(),
		book,
		confidenceAccess(),
		confidenceHops,
		100,
		[]Output{{Summary: "Sound output", Yes: true}},
		run,
	)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestConfidenceBuildsTrimmedPromptsWithoutMutatingOutputs(t *testing.T) {
	book := &confidenceGates{}
	run := &confidenceRunner{answers: []bool{true, true}}
	outputs := []Output{
		{Summary: "  Summary only \t", Detail: " \n\t ", Yes: true},
		{Summary: "\n Summary with detail ", Detail: "  Supporting detail\n", Yes: true},
	}
	wantOutputs := append([]Output(nil), outputs...)
	if err := Confidence(
		context.Background(),
		book,
		confidenceAccess(),
		confidenceHops,
		100,
		outputs,
		run,
	); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(outputs, wantOutputs) {
		t.Fatalf("outputs mutated to %+v", outputs)
	}
	wantPrompts := []string{"Summary only", "Summary with detail\nSupporting detail"}
	if !reflect.DeepEqual(run.prompts, wantPrompts) {
		t.Fatalf("runner prompts = %q", run.prompts)
	}
	if len(run.options) != 2 || run.options[0] != nil || run.options[1] != nil {
		t.Fatalf("runner options = %+v", run.options)
	}
}

func confidenceOutputs() []Output {
	return []Output{
		{Summary: "One", Yes: true},
		{Summary: "Two", Yes: true},
		{Summary: "Three", Yes: false},
		{Summary: "Four", Yes: true},
	}
}

func confidenceAccess() Access {
	return Access{UserID: confidenceUser, Allow: "settings:manage"}
}

type confidenceGates struct {
	fingerprint   string
	accepted      bool
	acceptedCalls int
	saved         []Gate
}

func (g *confidenceGates) Accepted(context.Context, Access, string) (string, bool, error) {
	g.acceptedCalls++
	return g.fingerprint, g.accepted, nil
}

func (g *confidenceGates) Save(_ context.Context, _ Access, gate Gate) error {
	g.saved = append(g.saved, gate)
	return nil
}

type confidenceRunner struct {
	answers []bool
	err     error
	calls   int
	prompts []string
	options [][]string
}

func (r *confidenceRunner) Decide(_ context.Context, prompt string, options []string) (bool, string, error) {
	r.prompts = append(r.prompts, prompt)
	r.options = append(r.options, append([]string(nil), options...))
	answer := false
	if r.calls < len(r.answers) {
		answer = r.answers[r.calls]
	}
	r.calls++
	return answer, "ignored", r.err
}
