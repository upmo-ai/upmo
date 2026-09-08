//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/upmo-ai/upmo/internal/memory/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/memory/app"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestErasePersonRemovesOnlyTheirMemory(t *testing.T) {
	owner := up(t)
	ctx := context.Background()

	for _, profile := range []struct {
		user string
		body string
	}{
		{person, "Ada profile"},
		{otherPerson, "Lee profile"},
	} {
		if _, err := owner.Exec(ctx, `
			INSERT INTO memory.profiles (user_id, body, updated_at)
			VALUES ($1, $2, now())`, profile.user, profile.body); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := owner.Exec(ctx, insertEraseEmbedded,
		person, bytes32(1), "Ada prefers tea", axis(0), "model-a", eraseSourceOne); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, insertEraseEmbedded,
		otherPerson, bytes32(2), "Lee prefers coffee", axis(1), "model-b", eraseSourceOne); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO memory.extracts (user_id, source_kind, source_id, source_seq, created_at)
		VALUES
		  ($1, 'task', $3, 0, now()),
		  ($2, 'task', $3, 0, now())`,
		person, otherPerson, eraseSourceOne); err != nil {
		t.Fatal(err)
	}

	store := postgres.Facts{Pool: pgtest.App(t, owner)}
	if err := app.Erase(ctx, store, person, ""); err != nil {
		t.Fatal(err)
	}

	var profiles, facts, embeddings, pins, extracts int
	if err := owner.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM memory.profiles WHERE user_id = $1),
		  (SELECT count(*) FROM memory.facts WHERE user_id = $1),
		  (SELECT count(*) FROM memory.facts WHERE user_id = $1 AND embedding IS NOT NULL),
		  (SELECT count(*) FROM memory.embed_models WHERE user_id = $1),
		  (SELECT count(*) FROM memory.extracts WHERE user_id = $1)`,
		person).Scan(&profiles, &facts, &embeddings, &pins, &extracts); err != nil {
		t.Fatal(err)
	}
	if profiles != 0 || facts != 0 || embeddings != 0 || pins != 0 || extracts != 0 {
		t.Fatalf("erased person: profiles=%d facts=%d embeddings=%d pins=%d extracts=%d",
			profiles, facts, embeddings, pins, extracts)
	}

	var body, claim, model string
	var embedded bool
	if err := owner.QueryRow(ctx, `
		SELECT profile.body, fact.claim, fact.embedding IS NOT NULL, pin.model
		FROM memory.profiles AS profile
		JOIN memory.facts AS fact USING (user_id)
		JOIN memory.embed_models AS pin USING (user_id)
		WHERE profile.user_id = $1`,
		otherPerson).Scan(&body, &claim, &embedded, &model); err != nil {
		t.Fatal(err)
	}
	if body != "Lee profile" || claim != "Lee prefers coffee" || !embedded || model != "model-b" {
		t.Fatalf("other person: body=%q claim=%q embedded=%v model=%q",
			body, claim, embedded, model)
	}
	if err := owner.QueryRow(ctx,
		`SELECT count(*) FROM memory.extracts WHERE user_id = $1`,
		otherPerson).Scan(&extracts); err != nil {
		t.Fatal(err)
	}
	if extracts != 1 {
		t.Fatalf("other person extracts=%d", extracts)
	}
}

func TestEraseTaskKeepsOtherMemoryAndProfile(t *testing.T) {
	owner := up(t)
	ctx := context.Background()

	if _, err := owner.Exec(ctx, `
		INSERT INTO memory.profiles (user_id, body, updated_at)
		VALUES ($1, 'Ada profile', now())`, person); err != nil {
		t.Fatal(err)
	}
	var firstID, secondID string
	if err := owner.QueryRow(ctx, insertEraseEmbedded+` RETURNING id`,
		person, bytes32(3), "Ada prefers tea", axis(0), "model-a", eraseSourceOne).Scan(&firstID); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, insertEraseEmbedded+` RETURNING id`,
		person, bytes32(4), "Ada prefers coffee", axis(1), "model-a", eraseSourceTwo).Scan(&secondID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		UPDATE memory.facts
		SET invalid_at = now(), superseded_by = $2
		WHERE id = $1`, secondID, firstID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO memory.extracts (user_id, source_kind, source_id, source_seq, created_at)
		VALUES ($1, 'task', $2, 0, now())`, person, eraseSourceOne); err != nil {
		t.Fatal(err)
	}

	store := postgres.Facts{Pool: pgtest.App(t, owner)}
	if err := app.Erase(ctx, store, person, eraseSourceOne); err != nil {
		t.Fatal(err)
	}

	var firstFacts, firstExtracts int
	if err := owner.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM memory.facts WHERE user_id = $1 AND source_id = $2),
		  (SELECT count(*) FROM memory.extracts WHERE user_id = $1 AND source_id = $2)`,
		person, eraseSourceOne).Scan(&firstFacts, &firstExtracts); err != nil {
		t.Fatal(err)
	}
	if firstFacts != 0 || firstExtracts != 0 {
		t.Fatalf("first source: facts=%d extracts=%d", firstFacts, firstExtracts)
	}

	var body, claim, model string
	var embedded, supersededByCleared bool
	if err := owner.QueryRow(ctx, `
		SELECT profile.body, fact.claim, fact.embedding IS NOT NULL,
		       fact.superseded_by IS NULL, pin.model
		FROM memory.profiles AS profile
		JOIN memory.facts AS fact USING (user_id)
		JOIN memory.embed_models AS pin USING (user_id)
		WHERE profile.user_id = $1 AND fact.source_id = $2`,
		person, eraseSourceTwo).Scan(&body, &claim, &embedded, &supersededByCleared, &model); err != nil {
		t.Fatal(err)
	}
	if body != "Ada profile" || claim != "Ada prefers coffee" || !embedded ||
		!supersededByCleared || model != "model-a" {
		t.Fatalf("remaining memory: body=%q claim=%q embedded=%v superseded_by_cleared=%v model=%q",
			body, claim, embedded, supersededByCleared, model)
	}

	if err := app.Erase(ctx, store, person, eraseSourceTwo); err != nil {
		t.Fatal(err)
	}

	var remainingFacts, remainingPins, remainingProfiles int
	if err := owner.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM memory.facts WHERE user_id = $1),
		  (SELECT count(*) FROM memory.embed_models WHERE user_id = $1),
		  (SELECT count(*) FROM memory.profiles WHERE user_id = $1)`,
		person).Scan(&remainingFacts, &remainingPins, &remainingProfiles); err != nil {
		t.Fatal(err)
	}
	if remainingFacts != 0 || remainingPins != 0 || remainingProfiles != 1 {
		t.Fatalf("second erase: facts=%d pins=%d profiles=%d",
			remainingFacts, remainingPins, remainingProfiles)
	}
}

const (
	otherPerson    = "018f3b2c-7a10-7c4a-8e11-222222222222"
	eraseSourceOne = "018f3b2c-7a10-7c4a-8e11-444444444444"
	eraseSourceTwo = "018f3b2c-7a10-7c4a-8e11-555555555555"
)

const insertEraseEmbedded = `
	INSERT INTO memory.facts (
	  user_id, trust, claim, claim_hash, valid_from, last_confirmed_at,
	  embedding, embedding_model, source_kind, source_id, source_seq, created_at
	) VALUES (
	  $1, 'tool', $3, $2, now(), now(),
	  $4::halfvec(1024), $5, 'task', $6, 0, now()
	)`
