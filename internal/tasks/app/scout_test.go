package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

type fakeGoals struct {
	goals []OpenGoal
	err   error
}

func (f *fakeGoals) ListOpenGoals(context.Context, uuid.UUID) ([]OpenGoal, error) {
	return f.goals, f.err
}

type scriptProposer struct {
	script []string
	calls  int
	err    error
}

func (s *scriptProposer) Propose(context.Context, string) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	if s.calls >= len(s.script) {
		return "[]", nil
	}
	raw := s.script[s.calls]
	s.calls++
	return raw, nil
}

type fakeNotices struct {
	inserts []struct {
		kind, title string
	}
	err error
}

func (f *fakeNotices) Insert(_ context.Context, _ uuid.UUID, kind, title string, taskID *uuid.UUID) (uuid.UUID, error) {
	if f.err != nil {
		return uuid.Nil, f.err
	}
	if taskID != nil {
		return uuid.Nil, domain.ErrScout
	}
	f.inserts = append(f.inserts, struct {
		kind, title string
	}{kind, title})
	return uuid.New(), nil
}

func scoutDeps(goals *fakeGoals, prop *scriptProposer, notes *fakeNotices) ScoutDeps {
	return ScoutDeps{
		Goals: goals,
		Propose: func(context.Context, uuid.UUID) Proposer {
			return prop
		},
		Notices: notes,
	}
}

func TestRunRefusesCredentialTitles(t *testing.T) {
	userID := uuid.New()
	batch, _ := json.Marshal([]string{"File the receipts", "Rotate SK-live key"})
	prop := &scriptProposer{script: []string{string(batch)}}
	notes := &fakeNotices{}
	kept, _, err := RunScout(context.Background(), scoutDeps(&fakeGoals{}, prop, notes), ScoutArgs{
		UserID:  userID,
		Trigger: scoutTriggerTask,
	})
	if err != nil || kept != 0 || len(notes.inserts) != 0 {
		t.Fatalf("kept=%d err=%v inserts=%d", kept, err, len(notes.inserts))
	}
}

func TestRunDropsOpenGoalDupes(t *testing.T) {
	userID := uuid.New()
	batch, _ := json.Marshal([]string{"File the receipts", "Chase the invoice"})
	prop := &scriptProposer{script: []string{string(batch)}}
	notes := &fakeNotices{}
	goals := &fakeGoals{goals: []OpenGoal{{ID: "t1", Goal: "File the receipts"}}}
	kept, audit, err := RunScout(context.Background(), scoutDeps(goals, prop, notes), ScoutArgs{
		UserID:  userID,
		Trigger: scoutTriggerTask,
	})
	if err != nil || kept != 1 || len(notes.inserts) != 1 {
		t.Fatalf("kept=%d err=%v inserts=%v", kept, err, notes.inserts)
	}
	if notes.inserts[0].kind != domain.NoticeProposal || notes.inserts[0].title != "Chase the invoice" {
		t.Fatalf("insert = %#v", notes.inserts[0])
	}
	if len(audit) == 0 || audit[0].ID != "t1" {
		t.Fatalf("audit = %#v", audit)
	}
}

func TestRunInsertsProposals(t *testing.T) {
	userID := uuid.New()
	batch, _ := json.Marshal([]string{"  File the receipts  ", "Chase the invoice"})
	prop := &scriptProposer{script: []string{string(batch)}}
	notes := &fakeNotices{}
	kept, _, err := RunScout(context.Background(), scoutDeps(&fakeGoals{}, prop, notes), ScoutArgs{
		UserID:  userID,
		Trigger: scoutTriggerKnowledge,
	})
	if err != nil || kept != 2 || len(notes.inserts) != 2 {
		t.Fatalf("kept=%d err=%v inserts=%v", kept, err, notes.inserts)
	}
	if notes.inserts[0].title != "File the receipts" {
		t.Fatalf("first = %q", notes.inserts[0].title)
	}
}

func TestRunRetriesParse(t *testing.T) {
	userID := uuid.New()
	good, _ := json.Marshal([]string{"File the receipts"})
	prop := &scriptProposer{script: []string{"not json", string(good)}}
	notes := &fakeNotices{}
	kept, _, err := RunScout(context.Background(), scoutDeps(&fakeGoals{}, prop, notes), ScoutArgs{
		UserID:  userID,
		Trigger: scoutTriggerTask,
	})
	if err != nil || kept != 1 || prop.calls != 2 {
		t.Fatalf("kept=%d err=%v calls=%d", kept, err, prop.calls)
	}
}

func TestRunReturnsRetryForGateway(t *testing.T) {
	userID := uuid.New()
	prop := &scriptProposer{err: ErrRetry}
	_, _, err := RunScout(context.Background(), scoutDeps(&fakeGoals{}, prop, &fakeNotices{}), ScoutArgs{
		UserID:  userID,
		Trigger: scoutTriggerTask,
	})
	if !IsRetry(err) {
		t.Fatalf("err = %v", err)
	}
}

type fakeMemory struct {
	profile string
	facts   []MemoryFact
	err     error
}

func (f *fakeMemory) ProfileAndFacts(context.Context, uuid.UUID) (string, []MemoryFact, error) {
	return f.profile, f.facts, f.err
}

type fakeKnowledge struct {
	hits  []KnowledgeHit
	err   error
	query string
}

func (f *fakeKnowledge) Search(_ context.Context, _ uuid.UUID, query string, limit int) ([]KnowledgeHit, error) {
	f.query = query
	if f.err != nil {
		return nil, f.err
	}
	out := f.hits
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func TestRunIncludesMemoryAndKnowledgeInPrompt(t *testing.T) {
	userID := uuid.New()
	batch, _ := json.Marshal([]string{"Chase the invoice"})
	prop := &scriptProposer{script: []string{string(batch)}}
	notes := &fakeNotices{}
	mem := &fakeMemory{
		profile: "Prefers terse tasks",
		facts:   []MemoryFact{{ID: "f1", Claim: "Works in EUR"}},
	}
	know := &fakeKnowledge{hits: []KnowledgeHit{{SourceID: "s1", Body: "Invoice policy: net 30"}}}
	deps := scoutDeps(&fakeGoals{goals: []OpenGoal{{ID: "t1", Goal: "Open books"}}}, prop, notes)
	deps.Memory = mem
	deps.Knowledge = know
	kept, audit, err := RunScout(context.Background(), deps, ScoutArgs{
		UserID:  userID,
		Trigger: scoutTriggerTask,
	})
	if err != nil || kept != 1 {
		t.Fatalf("kept=%d err=%v", kept, err)
	}
	if know.query == "" {
		t.Fatal("knowledge search query empty")
	}
	var sawMemory, sawKnowledge bool
	for _, ref := range audit {
		if ref.Kind == "memory" && ref.ID == "f1" {
			sawMemory = true
		}
		if ref.Kind == "knowledge" && ref.ID == "s1" {
			sawKnowledge = true
		}
	}
	if !sawMemory || !sawKnowledge {
		t.Fatalf("audit = %#v", audit)
	}
	prompt := scoutPrompt(scoutTriggerTask, "", []string{"Open books"}, nil, mem.profile, mem.facts, know.hits)
	if !strings.Contains(prompt, "Prefers terse tasks") || !strings.Contains(prompt, "Works in EUR") || !strings.Contains(prompt, "Invoice policy") {
		t.Fatalf("prompt = %q", prompt)
	}
}

func TestRunSkipsKnowledgeWithoutQuery(t *testing.T) {
	userID := uuid.New()
	batch, _ := json.Marshal([]string{"Chase the invoice"})
	prop := &scriptProposer{script: []string{string(batch)}}
	know := &fakeKnowledge{hits: []KnowledgeHit{{SourceID: "s1", Body: "secret"}}}
	deps := scoutDeps(&fakeGoals{}, prop, &fakeNotices{})
	deps.Knowledge = know
	_, _, err := RunScout(context.Background(), deps, ScoutArgs{
		UserID:  userID,
		Trigger: scoutTriggerTask,
	})
	if err != nil {
		t.Fatal(err)
	}
	if know.query != "" {
		t.Fatalf("query = %q", know.query)
	}
}
