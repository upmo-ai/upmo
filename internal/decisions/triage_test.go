package decisions

import (
	"context"
	"errors"
	"testing"
)

const triageUser = "018f3b2c-7a10-7c4a-8e11-111111111111"

var triageHops = []Hop{
	{Position: 1, Adapter: "openai", Model: "small", Weight: 1, Retry: 0},
}

func TestTriageRejectsBlankBodyBeforeRunning(t *testing.T) {
	book := &triageGates{}
	run := &triageRunner{choices: []string{TriageReply}}
	err := Triage(context.Background(), book, triageAccess(), triageHops, 100, []Message{{
		Body:  " \t\n ",
		Label: TriageReply,
	}}, run)
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("error = %v", err)
	}
	if run.calls != 0 {
		t.Fatalf("runner calls = %d", run.calls)
	}
}

func TestTriageRejectsUnknownLabelBeforeRunning(t *testing.T) {
	book := &triageGates{}
	run := &triageRunner{choices: []string{TriageReply}}
	err := Triage(context.Background(), book, triageAccess(), triageHops, 100, []Message{{
		Body:  "Please review this",
		Label: "archive",
	}}, run)
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("error = %v", err)
	}
	if run.calls != 0 {
		t.Fatalf("runner calls = %d", run.calls)
	}
}

func TestTriageSavesChoiceRouteAndCallerBar(t *testing.T) {
	book := &triageGates{}
	run := &triageRunner{choices: []string{TriageReply}}
	if err := Triage(context.Background(), book, triageAccess(), triageHops, 80, triageMessages(), run); err != nil {
		t.Fatal(err)
	}
	if len(book.saved) != 1 {
		t.Fatalf("saved = %d", len(book.saved))
	}
	got := book.saved[0]
	if got.Route != RouteTriage || got.Kind != KindChoice || got.Bar != 80 {
		t.Fatalf("gate = %+v", got)
	}
	if len(run.options) != 1 ||
		len(run.options[0]) != 3 ||
		run.options[0][0] != TriageReply ||
		run.options[0][1] != TriageTask ||
		run.options[0][2] != TriageIgnore {
		t.Fatalf("runner options = %v", run.options)
	}
}

func TestTriageGivesEachMessageItsOwnOptions(t *testing.T) {
	book := &triageGates{}
	run := &triageRunner{
		choices:     []string{TriageReply, TriageReply},
		mutateFirst: true,
	}
	messages := []Message{
		{Body: "First", Label: TriageReply},
		{Body: "Second", Label: TriageReply},
	}
	if err := Triage(context.Background(), book, triageAccess(), triageHops, 100, messages, run); err != nil {
		t.Fatal(err)
	}
	if len(run.options) != 2 || run.options[1][0] != TriageReply {
		t.Fatalf("runner options = %v", run.options)
	}
}

func TestTriageMissAtFullBarSavesNothing(t *testing.T) {
	book := &triageGates{}
	run := &triageRunner{choices: []string{TriageTask}}
	err := Triage(context.Background(), book, triageAccess(), triageHops, 100, triageMessages(), run)
	if !errors.Is(err, ErrGate) {
		t.Fatalf("error = %v", err)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestTriageSkipsRunnerForStoredFingerprint(t *testing.T) {
	fingerprint, err := Fingerprint(Config{Name: RouteTriage, Kind: KindChoice, Hops: triageHops})
	if err != nil {
		t.Fatal(err)
	}
	book := &triageGates{fingerprint: fingerprint, accepted: true}
	run := &triageRunner{choices: []string{TriageReply}}
	if err := Triage(context.Background(), book, triageAccess(), triageHops, 100, triageMessages(), run); err != nil {
		t.Fatal(err)
	}
	if run.calls != 0 {
		t.Fatalf("runner calls = %d", run.calls)
	}
}

func TestTriageRefusesCallerWithoutSettingsManage(t *testing.T) {
	book := &triageGates{}
	run := &triageRunner{choices: []string{TriageReply}}
	access := Access{UserID: triageUser, Allow: "task:edit"}
	err := Triage(context.Background(), book, access, triageHops, 100, triageMessages(), run)
	if !errors.Is(err, ErrAccess) {
		t.Fatalf("error = %v", err)
	}
	if run.calls != 0 {
		t.Fatalf("runner calls = %d", run.calls)
	}
}

func TestTriageTrimsWithoutMutatingMessages(t *testing.T) {
	book := &triageGates{}
	run := &triageRunner{choices: []string{TriageReply}}
	messages := []Message{{Body: "  Please reply \t", Label: " reply "}}
	want := messages[0]
	if err := Triage(context.Background(), book, triageAccess(), triageHops, 100, messages, run); err != nil {
		t.Fatal(err)
	}
	if messages[0] != want {
		t.Fatalf("message mutated to %+v", messages[0])
	}
	if len(run.prompts) != 1 || run.prompts[0] != "Please reply" {
		t.Fatalf("runner prompts = %q", run.prompts)
	}
}

func triageAccess() Access {
	return Access{UserID: triageUser, Allow: "settings:manage"}
}

func triageMessages() []Message {
	return []Message{{Body: "Please reply", Label: TriageReply}}
}

type triageGates struct {
	fingerprint string
	accepted    bool
	saved       []Gate
}

func (g *triageGates) Accepted(context.Context, Access, string) (string, bool, error) {
	return g.fingerprint, g.accepted, nil
}

func (g *triageGates) Save(_ context.Context, _ Access, gate Gate) error {
	g.saved = append(g.saved, gate)
	return nil
}

type triageRunner struct {
	choices     []string
	calls       int
	prompts     []string
	options     [][]string
	mutateFirst bool
}

func (r *triageRunner) Decide(_ context.Context, prompt string, options []string) (bool, string, error) {
	r.prompts = append(r.prompts, prompt)
	r.options = append(r.options, append([]string(nil), options...))
	if r.mutateFirst && r.calls == 0 {
		options[0] = "changed"
	}
	choice := ""
	if r.calls < len(r.choices) {
		choice = r.choices[r.calls]
	}
	r.calls++
	return false, choice, nil
}
