package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestEvaluateCountsAHitAndStillChargesAMiss(t *testing.T) {
	store := &evalStore{byQuery: map[string][]Candidate{
		"ab":    {{ID: "1", Claim: "abcd"}},
		"abcde": {{ID: "1", Claim: "abcd"}},
	}}
	score, err := Evaluate(
		context.Background(), store, goodEmbed(), nil, testUser,
		[]Question{
			{Query: "ab", Expect: "abcd"},
			{Query: "abcde", Expect: "missing"},
		},
		retrieveAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if score.Asked != 2 || score.Answered != 1 || score.Tokens != 5 {
		t.Fatalf("score = %+v", score)
	}
	if score.TokensPerAnswered() != 5 {
		t.Fatalf("per answered = %d", score.TokensPerAnswered())
	}
}

func TestEvaluateChargesTheQueryWhenNothingComesBack(t *testing.T) {
	store := &evalStore{}
	score, err := Evaluate(
		context.Background(), store, goodEmbed(), nil, testUser,
		[]Question{{Query: "ab", Expect: "abcd"}},
		retrieveAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	if score.Asked != 1 || score.Answered != 0 || score.Tokens != 1 {
		t.Fatalf("score = %+v", score)
	}
	if score.TokensPerAnswered() != 0 {
		t.Fatalf("per answered = %d", score.TokensPerAnswered())
	}
}

func TestEvaluateDropsAPartialRunWhenRetrievalFails(t *testing.T) {
	store := &evalStore{byQuery: map[string][]Candidate{
		"ab": {{ID: "1", Claim: "abcd"}},
	}}
	embed := goodEmbed()
	embed.err = errors.New("embed failed")
	_, err := Evaluate(
		context.Background(), store, embed, nil, testUser,
		[]Question{{Query: "ab", Expect: "abcd"}},
		retrieveAt,
	)
	if err == nil || store.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, store.calls)
	}
}

func TestCanaryQuestionsNameStoredClaims(t *testing.T) {
	stored := map[string]bool{}
	for _, claim := range CanaryClaims() {
		stored[claim] = true
	}
	questions := CanaryQuestions()
	if len(questions) < 3 {
		t.Fatalf("questions = %d", len(questions))
	}
	if len(CanaryClaims()) <= 8 {
		t.Fatalf("claims = %d", len(CanaryClaims()))
	}
	seen := map[string]bool{}
	for _, question := range questions {
		if question.Query == "" || question.Expect == "" || !stored[question.Expect] {
			t.Fatalf("question = %+v", question)
		}
		if seen[question.Expect] {
			t.Fatalf("repeated expect %q", question.Expect)
		}
		seen[question.Expect] = true
	}
}

type evalStore struct {
	calls   int
	byQuery map[string][]Candidate
}

func (s *evalStore) InRetrieve(_ context.Context, _ string, fn func(RetrievalTx) error) error {
	s.calls++
	return fn(s)
}

func (s *evalStore) NearText(context.Context, time.Time, []float32, int) ([]Candidate, error) {
	return nil, nil
}

func (s *evalStore) MatchText(_ context.Context, _ time.Time, query string, _ int) ([]Candidate, error) {
	return s.byQuery[query], nil
}
