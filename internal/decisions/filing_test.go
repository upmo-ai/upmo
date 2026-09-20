package decisions

import (
	"context"
	"errors"
	"testing"
)

const (
	filingUser    = "018f3b2c-7a10-7c4a-8e11-111111111111"
	filingFolderA = "018f3b2c-7a10-7c4a-8e11-aaaaaaaaaaaa"
	filingFolderB = "018f3b2c-7a10-7c4a-8e11-bbbbbbbbbbbb"
)

var filingHops = []Hop{
	{Position: 1, Adapter: "openai", Model: "small", Weight: 1, Retry: 0},
}

func TestFileSourceRejectsLabelOutsideOptionsBeforeRunning(t *testing.T) {
	book := &filingGates{}
	run := &filingRunner{choice: filingFolderA}
	err := FileSource(context.Background(), book, filingAccess(), filingHops, 100, []Labelled{{
		Prompt:  "A source excerpt",
		Options: []string{filingFolderA, filingFolderB},
		Label:   "018f3b2c-7a10-7c4a-8e11-cccccccccccc",
	}}, run)
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("error = %v", err)
	}
	if run.calls != 0 {
		t.Fatalf("runner calls = %d", run.calls)
	}
}

func TestFileSourceRejectsNonUUIDOptionBeforeRunning(t *testing.T) {
	book := &filingGates{}
	run := &filingRunner{choice: filingFolderA}
	err := FileSource(context.Background(), book, filingAccess(), filingHops, 100, []Labelled{{
		Prompt:  "A source excerpt",
		Options: []string{filingFolderA, "not-a-folder"},
		Label:   filingFolderA,
	}}, run)
	if !errors.Is(err, ErrConfig) {
		t.Fatalf("error = %v", err)
	}
	if run.calls != 0 {
		t.Fatalf("runner calls = %d", run.calls)
	}
}

func TestFileSourceSavesChoiceRouteAndCallerBar(t *testing.T) {
	book := &filingGates{}
	run := &filingRunner{choice: filingFolderA}
	if err := FileSource(context.Background(), book, filingAccess(), filingHops, 80, filingCases(), run); err != nil {
		t.Fatal(err)
	}
	if len(book.saved) != 1 {
		t.Fatalf("saved = %d", len(book.saved))
	}
	got := book.saved[0]
	if got.Route != RouteFiling || got.Kind != KindChoice || got.Bar != 80 {
		t.Fatalf("gate = %+v", got)
	}
}

func TestFileSourceMissAtFullBarSavesNothing(t *testing.T) {
	book := &filingGates{}
	run := &filingRunner{choice: filingFolderB}
	err := FileSource(context.Background(), book, filingAccess(), filingHops, 100, filingCases(), run)
	if !errors.Is(err, ErrGate) {
		t.Fatalf("error = %v", err)
	}
	if len(book.saved) != 0 {
		t.Fatalf("saved = %+v", book.saved)
	}
}

func TestFileSourceSkipsRunnerForStoredFingerprint(t *testing.T) {
	fingerprint, err := Fingerprint(Config{Name: RouteFiling, Kind: KindChoice, Hops: filingHops})
	if err != nil {
		t.Fatal(err)
	}
	book := &filingGates{fingerprint: fingerprint, accepted: true}
	run := &filingRunner{choice: filingFolderA}
	if err := FileSource(context.Background(), book, filingAccess(), filingHops, 100, filingCases(), run); err != nil {
		t.Fatal(err)
	}
	if run.calls != 0 {
		t.Fatalf("runner calls = %d", run.calls)
	}
}

func TestFileSourceRefusesCallerWithoutSettingsManage(t *testing.T) {
	book := &filingGates{}
	run := &filingRunner{choice: filingFolderA}
	access := Access{UserID: filingUser, Allow: "task:edit"}
	err := FileSource(context.Background(), book, access, filingHops, 100, filingCases(), run)
	if !errors.Is(err, ErrAccess) {
		t.Fatalf("error = %v", err)
	}
	if run.calls != 0 {
		t.Fatalf("runner calls = %d", run.calls)
	}
}

func filingAccess() Access {
	return Access{UserID: filingUser, Allow: "settings:manage"}
}

func filingCases() []Labelled {
	return []Labelled{{
		Prompt:  "A source excerpt",
		Options: []string{filingFolderA, filingFolderB},
		Label:   filingFolderA,
	}}
}

type filingGates struct {
	fingerprint string
	accepted    bool
	saved       []Gate
}

func (g *filingGates) Accepted(context.Context, Access, string) (string, bool, error) {
	return g.fingerprint, g.accepted, nil
}

func (g *filingGates) Save(_ context.Context, _ Access, gate Gate) error {
	g.saved = append(g.saved, gate)
	return nil
}

type filingRunner struct {
	choice string
	calls  int
}

func (r *filingRunner) Decide(context.Context, string, []string) (bool, string, error) {
	r.calls++
	return false, r.choice, nil
}
