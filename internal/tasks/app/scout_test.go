package app

import (
	"context"
	"encoding/json"
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
