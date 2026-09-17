package decisions

import (
	"context"
	"errors"
	"testing"
)

const memoryUser = "018f3b2c-7a10-7c4a-8e11-111111111111"

var memoryHops = []Hop{
	{Position: 1, Adapter: "openai", Model: "small", Weight: 1, Retry: 0},
}

func TestMemoryCheckRejectsBlankPromptBeforeRunning(t *testing.T) {
	book := &memoryGates{}
	run := &memoryRunner{answers: []bool{true}}
	err := MemoryCheck(context.Background(), book, memoryAccess(), memoryHops, 100, []Turn{{
		Prompt: " \t ",
		Yes:    true,
	}}, run)
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("error = %v", err)
	}
	if run.calls != 0 {
		t.Fatalf("runner calls = %d", run.calls)
	}
}

func TestMemoryCheckRefusesCallerWithoutSettingsManage(t *testing.T) {
	book := &memoryGates{}
	run := &memoryRunner{answers: []bool{true}}
	access := Access{UserID: memoryUser, Allow: "task:edit"}
	err := MemoryCheck(context.Background(), book, access, memoryHops, 100, []Turn{{
		Prompt: "Read memory?",
		Yes:    true,
	}}, run)
	if !errors.Is(err, ErrAccess) {
		t.Fatalf("error = %v", err)
	}
	if run.calls != 0 {
		t.Fatalf("runner calls = %d", run.calls)
	}
}

func TestMemoryCheckSkipsRunnerForStoredFingerprint(t *testing.T) {
	fp, err := Fingerprint(Config{Name: RouteMemory, Kind: KindYesNo, Hops: memoryHops})
	if err != nil {
		t.Fatal(err)
	}
	book := &memoryGates{fingerprint: fp, accepted: true}
	run := &memoryRunner{answers: []bool{true}}
	err = MemoryCheck(context.Background(), book, memoryAccess(), memoryHops, 100, []Turn{{
		Prompt: "Read memory?",
		Yes:    true,
	}}, run)
	if err != nil {
		t.Fatal(err)
	}
	if run.calls != 0 {
		t.Fatalf("runner calls = %d", run.calls)
	}
}

func TestMemoryCheckStoresPrecisionNotAccuracy(t *testing.T) {
	book := &memoryGates{}
	run := &memoryRunner{answers: []bool{true, false, false, false}}
	turns := []Turn{
		{Prompt: "One", Yes: true},
		{Prompt: "Two", Yes: false},
		{Prompt: "Three", Yes: false},
		{Prompt: "Four", Yes: true},
	}
	if err := MemoryCheck(context.Background(), book, memoryAccess(), memoryHops, 100, turns, run); err != nil {
		t.Fatal(err)
	}
	if len(book.saved) != 1 {
		t.Fatalf("saved = %d", len(book.saved))
	}
	got := book.saved[0]
	if got.Route != RouteMemory || got.Kind != KindYesNo || got.Hit != 1 || got.Total != 1 || got.Bar != 100 {
		t.Fatalf("gate = %+v", got)
	}
}

func TestMemoryCheckFailedPrecisionSavesNothing(t *testing.T) {
	book := &memoryGates{}
	run := &memoryRunner{answers: []bool{true, true, false}}
	turns := []Turn{
		{Prompt: "One", Yes: false},
		{Prompt: "Two", Yes: true},
		{Prompt: "Three", Yes: false},
	}
	err := MemoryCheck(context.Background(), book, memoryAccess(), memoryHops, 60, turns, run)
	if !errors.Is(err, ErrGate) {
		t.Fatalf("error = %v", err)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestMemoryCheckAllNoReturnsGateError(t *testing.T) {
	book := &memoryGates{}
	run := &memoryRunner{answers: []bool{false, false}}
	err := MemoryCheck(context.Background(), book, memoryAccess(), memoryHops, 1, []Turn{
		{Prompt: "One", Yes: true},
		{Prompt: "Two", Yes: false},
	}, run)
	if !errors.Is(err, ErrGate) {
		t.Fatalf("error = %v", err)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestMemoryCheckRunnerErrorSavesNothing(t *testing.T) {
	want := errors.New("runner failed")
	book := &memoryGates{}
	run := &memoryRunner{err: want}
	err := MemoryCheck(context.Background(), book, memoryAccess(), memoryHops, 100, []Turn{{
		Prompt: "Read memory?",
		Yes:    true,
	}}, run)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestMemoryCheckTrimsWithoutMutatingTurns(t *testing.T) {
	book := &memoryGates{}
	run := &memoryRunner{answers: []bool{true}}
	turns := []Turn{{Prompt: "  Read memory? \t", Yes: true}}
	wantPrompt := turns[0].Prompt
	if err := MemoryCheck(context.Background(), book, memoryAccess(), memoryHops, 100, turns, run); err != nil {
		t.Fatal(err)
	}
	if turns[0].Prompt != wantPrompt {
		t.Fatalf("prompt mutated to %q", turns[0].Prompt)
	}
	if len(run.prompts) != 1 || run.prompts[0] != "Read memory?" {
		t.Fatalf("runner prompts = %q", run.prompts)
	}
}

func memoryAccess() Access {
	return Access{UserID: memoryUser, Allow: "settings:manage"}
}

type memoryGates struct {
	fingerprint string
	accepted    bool
	saved       []Gate
}

func (g *memoryGates) Accepted(context.Context, Access, string) (string, bool, error) {
	return g.fingerprint, g.accepted, nil
}

func (g *memoryGates) Save(_ context.Context, _ Access, gate Gate) error {
	g.saved = append(g.saved, gate)
	return nil
}

type memoryRunner struct {
	answers []bool
	err     error
	calls   int
	prompts []string
}

func (r *memoryRunner) Decide(_ context.Context, prompt string, _ []string) (bool, string, error) {
	r.prompts = append(r.prompts, prompt)
	answer := false
	if r.calls < len(r.answers) {
		answer = r.answers[r.calls]
	}
	r.calls++
	return answer, "", r.err
}
