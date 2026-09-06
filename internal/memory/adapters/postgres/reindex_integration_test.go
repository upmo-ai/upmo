//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/memory/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/memory/app"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestReindexReplacesFactVectorAndPin(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	claim := "Ada prefers tea"
	if _, err := owner.Exec(ctx, insertEmbedded, person, bytes32(1), claim, axis(0), "model-a"); err != nil {
		t.Fatal(err)
	}

	store := postgres.Facts{Pool: pgtest.App(t, owner)}
	embed := &reindexEmbedder{model: "model-b", vector: reindexVector(1)}
	n, err := app.Reindex(ctx, store, embed, person, "model-b")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || embed.calls != 1 {
		t.Fatalf("n=%d calls=%d", n, embed.calls)
	}

	var gotClaim, factModel, pinModel string
	if err := owner.QueryRow(ctx, `
		SELECT claim, embedding_model
		FROM memory.facts
		WHERE user_id = $1`, person).Scan(&gotClaim, &factModel); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `
		SELECT model
		FROM memory.embed_models
		WHERE user_id = $1`, person).Scan(&pinModel); err != nil {
		t.Fatal(err)
	}
	if gotClaim != claim || factModel != "model-b" || pinModel != "model-b" {
		t.Fatalf("claim=%q fact model=%q pin model=%q", gotClaim, factModel, pinModel)
	}

	again := &reindexEmbedder{model: "model-b", vector: reindexVector(2)}
	n, err = app.Reindex(ctx, store, again, person, "model-b")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 || again.calls != 0 {
		t.Fatalf("n=%d calls=%d", n, again.calls)
	}
}

func TestReindexEmbedderErrorKeepsOldVector(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	if _, err := owner.Exec(ctx, insertEmbedded, person, bytes32(2), "Ada prefers tea", axis(0), "model-a"); err != nil {
		t.Fatal(err)
	}

	wantErr := errors.New("embed failed")
	store := postgres.Facts{Pool: pgtest.App(t, owner)}
	n, err := app.Reindex(ctx, store, &reindexEmbedder{err: wantErr}, person, "model-b")
	if n != 0 || !errors.Is(err, wantErr) {
		t.Fatalf("n=%d err=%v", n, err)
	}

	var model string
	if err := owner.QueryRow(ctx, `
		SELECT embedding_model
		FROM memory.facts
		WHERE user_id = $1`, person).Scan(&model); err != nil {
		t.Fatal(err)
	}
	if model != "model-a" {
		t.Fatalf("model = %q", model)
	}
}

type reindexEmbedder struct {
	calls  int
	model  string
	vector []float32
	err    error
}

func (e *reindexEmbedder) Embed(_ context.Context, texts []string) (string, [][]float32, error) {
	e.calls++
	if e.err != nil {
		return "", nil, e.err
	}
	vectors := make([][]float32, len(texts))
	for i := range vectors {
		vectors[i] = append([]float32(nil), e.vector...)
	}
	return e.model, vectors, nil
}

func reindexVector(i int) []float32 {
	vector := make([]float32, 1024)
	vector[i] = 1
	return vector
}
