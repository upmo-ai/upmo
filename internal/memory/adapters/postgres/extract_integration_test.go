//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/memory/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/memory/app"
	"github.com/upmo-ai/upmo/internal/memory/domain"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

const sourceID = "018f3b2c-7a10-7c4a-8e11-444444444444"

func TestExtractInsertsToolFactOnce(t *testing.T) {
	owner := up(t)
	store := postgres.Facts{Pool: pgtest.App(t, owner)}
	prop := &scriptPropose{bodies: []string{`[{"claim":"Ada prefers tea","changes_behavior":true}]`}}
	emb := &scriptEmbed{model: "model-a"}
	src := canned(1, "Ada prefers tea every morning")
	rep, err := app.Extract(context.Background(), store, prop, emb, src)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Inserted != 1 {
		t.Fatalf("report = %+v", rep)
	}
	again, err := app.Extract(context.Background(), store, prop, emb, src)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Duplicate || prop.calls != 1 {
		t.Fatalf("again=%+v calls=%d", again, prop.calls)
	}
	var n int
	var trust, model string
	var gone bool
	var superseded *string
	if err := owner.QueryRow(context.Background(), `
		SELECT count(*), min(trust), min(embedding_model), bool_or(invalid_at IS NOT NULL), min(superseded_by::text)
		FROM memory.facts WHERE user_id = $1`, person).Scan(&n, &trust, &model, &gone, &superseded); err != nil {
		t.Fatal(err)
	}
	if n != 1 || trust != "tool" || model != "model-a" || gone || superseded != nil {
		t.Fatalf("n=%d trust=%s model=%s gone=%v superseded=%v", n, trust, model, gone, superseded)
	}
}

func TestExtractReinforcesUserFactWithoutInvalidating(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	hash := domain.ClaimHash("Ada drinks coffee")
	if _, err := owner.Exec(ctx, `
		INSERT INTO memory.facts (
			user_id, trust, claim, claim_hash, valid_from, last_confirmed_at,
			embedding, embedding_model, source_kind, source_id, source_seq, created_at
		) VALUES (
			$1, 'user', 'Ada drinks coffee', $2, $3, $3,
			$4::halfvec(1024), 'model-a', 'person', $5, 0, $3
		)`, person, hash[:], at.Add(-time.Hour), axis(0), sourceID); err != nil {
		t.Fatal(err)
	}
	store := postgres.Facts{Pool: pgtest.App(t, owner)}
	prop := &scriptPropose{bodies: []string{`[{"claim":"Ada prefers tea","changes_behavior":true}]`}}
	emb := &scriptEmbed{model: "model-a", vecs: [][]float32{axisVec(0)}}
	rep, err := app.Extract(ctx, store, prop, emb, canned(2, "Ada prefers tea every morning"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Reinforced != 1 || rep.Inserted != 0 {
		t.Fatalf("report = %+v", rep)
	}
	var n int
	var trust string
	var confirmed time.Time
	var gone bool
	var superseded *string
	if err := owner.QueryRow(ctx, `
		SELECT count(*), min(trust), min(last_confirmed_at), bool_or(invalid_at IS NOT NULL), min(superseded_by::text)
		FROM memory.facts WHERE user_id = $1`, person).Scan(&n, &trust, &confirmed, &gone, &superseded); err != nil {
		t.Fatal(err)
	}
	if n != 1 || trust != "user" || gone || superseded != nil || !confirmed.Equal(at) {
		t.Fatalf("n=%d trust=%s confirmed=%s gone=%v superseded=%v", n, trust, confirmed, gone, superseded)
	}
}

func TestExtractHashReinforceKeepsUserRow(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	hash := domain.ClaimHash("Ada prefers tea")
	if _, err := owner.Exec(ctx, `
		INSERT INTO memory.facts (
			user_id, trust, claim, claim_hash, valid_from, last_confirmed_at,
			source_kind, source_id, source_seq, created_at
		) VALUES (
			$1, 'user', 'Ada prefers tea', $2, $3, $3,
			'person', $4, 0, $3
		)`, person, hash[:], at.Add(-time.Hour), sourceID); err != nil {
		t.Fatal(err)
	}
	store := postgres.Facts{Pool: pgtest.App(t, owner)}
	prop := &scriptPropose{bodies: []string{`[{"claim":"Ada prefers tea","changes_behavior":true}]`}}
	emb := &scriptEmbed{model: "model-a"}
	rep, err := app.Extract(ctx, store, prop, emb, canned(6, "Ada prefers tea every morning"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Reinforced != 1 || emb.calls != 0 {
		t.Fatalf("report=%+v embed=%d", rep, emb.calls)
	}
	var trust string
	var confirmed time.Time
	var gone bool
	if err := owner.QueryRow(ctx, `
		SELECT trust, last_confirmed_at, invalid_at IS NOT NULL
		FROM memory.facts WHERE user_id = $1`, person).Scan(&trust, &confirmed, &gone); err != nil {
		t.Fatal(err)
	}
	if trust != "user" || gone || !confirmed.Equal(at) {
		t.Fatalf("trust=%s confirmed=%s gone=%v", trust, confirmed, gone)
	}
}

func TestExtractDoesNotReinsertForgotten(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	hash := domain.ClaimHash("Ada prefers tea")
	if _, err := owner.Exec(ctx, `
		INSERT INTO memory.facts (
			user_id, trust, claim, claim_hash, valid_from, invalid_at, last_confirmed_at,
			embedding, embedding_model, source_kind, source_id, source_seq, created_at
		) VALUES (
			$1, 'tool', 'Ada prefers tea', $2, $3, $3, $3,
			$4::halfvec(1024), 'model-a', 'person', $5, 0, $3
		)`, person, hash[:], at.Add(-time.Hour), axis(0), sourceID); err != nil {
		t.Fatal(err)
	}
	store := postgres.Facts{Pool: pgtest.App(t, owner)}
	prop := &scriptPropose{bodies: []string{`[{"claim":"Ada prefers tea","changes_behavior":true}]`}}
	emb := &scriptEmbed{model: "model-a", vecs: [][]float32{axisVec(0)}}
	rep, err := app.Extract(ctx, store, prop, emb, canned(3, "Ada prefers tea every morning"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Dropped != 1 || emb.calls != 0 {
		t.Fatalf("report=%+v embed=%d", rep, emb.calls)
	}

	prop.bodies = []string{`[{"claim":"Ada drinks tea","changes_behavior":true}]`}
	emb.calls = 0
	emb.vecs = [][]float32{axisVec(0)}
	rep, err = app.Extract(ctx, store, prop, emb, canned(4, "Ada drinks tea every morning"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Dropped != 1 || rep.Inserted != 0 || emb.calls != 1 {
		t.Fatalf("near report=%+v embed=%d", rep, emb.calls)
	}
	var n int
	var active int
	if err := owner.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE invalid_at IS NULL)
		FROM memory.facts WHERE user_id = $1`, person).Scan(&n, &active); err != nil {
		t.Fatal(err)
	}
	if n != 1 || active != 0 {
		t.Fatalf("rows=%d active=%d", n, active)
	}
}

func TestExtractTransientEmbedStoresNothing(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	store := postgres.Facts{Pool: pgtest.App(t, owner)}
	prop := &scriptPropose{bodies: []string{`[{"claim":"Ada prefers tea","changes_behavior":true}]`}}
	emb := &scriptEmbed{err: errors.Join(app.ErrRetry, errors.New("timeout"))}
	_, err := app.Extract(ctx, store, prop, emb, canned(5, "Ada prefers tea every morning"))
	if !errors.Is(err, app.ErrRetry) {
		t.Fatalf("err = %v", err)
	}
	var facts, runs int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM memory.facts`).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM memory.extracts`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if facts != 0 || runs != 0 {
		t.Fatalf("facts=%d runs=%d", facts, runs)
	}
	emb.err = nil
	emb.model = "model-a"
	if _, err := app.Extract(ctx, store, prop, emb, canned(5, "Ada prefers tea every morning")); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM memory.facts`).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	if facts != 1 {
		t.Fatalf("facts after retry = %d", facts)
	}
}

func canned(seq int, text string) app.Source {
	return app.Source{
		UserID: person,
		Kind:   "chat",
		ID:     sourceID,
		Seq:    seq,
		Text:   text,
		At:     time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC),
	}
}

type scriptPropose struct {
	calls  int
	bodies []string
}

func (s *scriptPropose) Propose(context.Context, string) (string, error) {
	s.calls++
	i := s.calls - 1
	if i >= len(s.bodies) {
		return s.bodies[len(s.bodies)-1], nil
	}
	return s.bodies[i], nil
}

type scriptEmbed struct {
	calls int
	model string
	vecs  [][]float32
	err   error
}

func (s *scriptEmbed) Embed(context.Context, []string) (string, [][]float32, error) {
	s.calls++
	if s.err != nil {
		return "", nil, s.err
	}
	if s.vecs != nil {
		return s.model, s.vecs, nil
	}
	return s.model, [][]float32{axisVec(0)}, nil
}

func axisVec(i int) []float32 {
	v := make([]float32, 1024)
	v[i] = 1
	return v
}
