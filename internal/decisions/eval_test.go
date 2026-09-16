package decisions

import (
	"context"
	"errors"
	"testing"
)

func TestFingerprintChangesWithTheModel(t *testing.T) {
	cfg := Config{Name: "memory-check", Kind: KindYesNo, Hops: []Hop{
		{Position: 2, Adapter: "openai", Model: "small", Weight: 1, Retry: 1},
		{Position: 1, Adapter: "openai", Model: "large", Weight: 1, Retry: 0},
	}}
	first, err := Fingerprint(cfg)
	if err != nil || len(first) != 64 {
		t.Fatalf("fingerprint = %q %v", first, err)
	}
	cfg.Hops = []Hop{cfg.Hops[1], cfg.Hops[0]}
	again, err := Fingerprint(cfg)
	if err != nil || again != first {
		t.Fatalf("order changed the fingerprint: %s %s %v", first, again, err)
	}
	cfg.Hops[0].Model = "other"
	changed, err := Fingerprint(cfg)
	if err != nil || changed == first {
		t.Fatalf("model change = %s %v", changed, err)
	}
}

func TestVerdictUsesTheBar(t *testing.T) {
	got, err := Verdict(3, 4, 75)
	if err != nil || got != VerdictPass {
		t.Fatalf("pass = %s %v", got, err)
	}
	got, err = Verdict(2, 4, 75)
	if err != nil || got != VerdictFail {
		t.Fatalf("fail = %s %v", got, err)
	}
	if _, err := Verdict(0, 0, 75); !errors.Is(err, ErrGate) {
		t.Fatalf("empty error = %v", err)
	}
}

func TestAcceptSkipsEvalWhenTheModelIsUnchanged(t *testing.T) {
	cfg := Config{Name: "memory-check", Kind: KindYesNo, Hops: []Hop{
		{Position: 1, Adapter: "openai", Model: "small", Weight: 1, Retry: 0},
	}}
	fp, err := Fingerprint(cfg)
	if err != nil {
		t.Fatal(err)
	}
	book := &memStore{fp: fp, ok: true}
	run := &countRunner{}
	access := Access{UserID: "018f3b2c-7a10-7c4a-8e11-111111111111", Allow: "settings:manage"}
	if err := Accept(context.Background(), book, access, cfg, 100, []Example{{Prompt: "keep?", Yes: true}}, run); err != nil {
		t.Fatal(err)
	}
	if run.calls != 0 || book.saves != 0 {
		t.Fatalf("calls = %d saves = %d", run.calls, book.saves)
	}
}

type countRunner struct{ calls int }

func (c *countRunner) Decide(context.Context, string, []string) (bool, string, error) {
	c.calls++
	return true, "", nil
}

type memStore struct {
	fp    string
	ok    bool
	saves int
}

func (m *memStore) Accepted(context.Context, Access, string) (string, bool, error) {
	return m.fp, m.ok, nil
}

func (m *memStore) Save(context.Context, Access, Gate) error {
	m.saves++
	return nil
}
