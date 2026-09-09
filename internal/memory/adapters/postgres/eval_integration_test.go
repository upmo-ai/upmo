//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/memory/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/memory/app"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestCanaryAnswersFromStoredFacts(t *testing.T) {
	store, asOf := canaryStore(t)
	ctx := context.Background()
	for _, question := range app.CanaryQuestions() {
		one, err := app.Evaluate(
			ctx, store, retrieveEmbedder{index: 0}, nil,
			person, []app.Question{question}, asOf,
		)
		if err != nil {
			t.Fatal(err)
		}
		if one.Answered != 1 {
			t.Fatalf("%q report = %+v", question.Query, one)
		}
	}
	report, err := app.Evaluate(
		ctx, store, retrieveEmbedder{index: 0}, nil,
		person, app.CanaryQuestions(), asOf,
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Asked != len(app.CanaryQuestions()) || report.Answered != report.Asked {
		t.Fatalf("report = %+v", report)
	}
	if report.Tokens <= report.Asked || report.TokensPerAnswered() != report.Tokens/report.Answered {
		t.Fatalf("report = %+v", report)
	}
}

func TestCanaryDoesNotAnswerFromAForgottenFact(t *testing.T) {
	store, asOf := canaryStore(t)
	report, err := app.Evaluate(
		context.Background(), store, retrieveEmbedder{index: 0}, nil,
		person, []app.Question{{
			Query:  "which tea did Ada previously prefer",
			Expect: "Ada previously preferred green tea",
		}}, asOf,
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Asked != 1 || report.Answered != 0 || report.Tokens == 0 {
		t.Fatalf("report = %+v", report)
	}
	if report.TokensPerAnswered() != 0 {
		t.Fatalf("per answered = %d", report.TokensPerAnswered())
	}
}

func canaryStore(t *testing.T) (postgres.Facts, time.Time) {
	t.Helper()
	owner := up(t)
	ctx := context.Background()
	asOf := time.Now().UTC()
	validFrom := asOf.Add(-time.Hour)
	for i, claim := range app.CanaryClaims() {
		if _, err := owner.Exec(ctx, insertRetrieveFact,
			person, bytes32(byte(i+1)), claim, validFrom, nil, axis(i),
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := owner.Exec(ctx, insertRetrieveFact,
		person, bytes32(99), "Ada previously preferred green tea",
		validFrom, asOf.Add(-time.Minute), axis(len(app.CanaryClaims())),
	); err != nil {
		t.Fatal(err)
	}
	return postgres.Facts{Pool: pgtest.App(t, owner)}, asOf
}
