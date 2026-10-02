//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/upmo-ai/upmo/internal/knowledge/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/knowledge/app"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestSourceKindColumnsBackfillOnCreate(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()

	folder, err := app.CreateFolder(ctx, store, ownerAccess, "Notes")
	if err != nil {
		t.Fatal(err)
	}
	result, err := app.Ingest(ctx, store, ownerAccess, nil, nil, app.Draft{
		FolderID: folder, Title: "Guide", Body: "Ada prefers oolong tea.",
	})
	if err != nil {
		t.Fatal(err)
	}

	var kind, ref, provenance, status, errText string
	if err := owner.QueryRow(ctx, `
		SELECT kind, source_ref, provenance, status, error
		FROM knowledge.sources
		WHERE id = $1::uuid`, result.ID).Scan(&kind, &ref, &provenance, &status, &errText); err != nil {
		t.Fatal(err)
	}
	if kind != "markdown" || provenance != "curated" || status != "ready" || ref != "" || errText != "" {
		t.Fatalf("kind=%q ref=%q provenance=%q status=%q error=%q", kind, ref, provenance, status, errText)
	}
}

func TestSourceKindConstraintRejectsUnknown(t *testing.T) {
	owner := up(t)
	ctx := context.Background()
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	folder, err := app.CreateFolder(ctx, store, ownerAccess, "Notes")
	if err != nil {
		t.Fatal(err)
	}

	_, err = owner.Exec(ctx, `
		INSERT INTO knowledge.sources (folder_id, title, body, sha256, revision, updated_at, kind, provenance, status)
		VALUES ($1::uuid, 'Bad', 'x', decode(repeat('ab', 16), 'hex'), 0, now(), 'sideways', 'curated', 'ready')`, folder)
	if err == nil {
		t.Fatal("expected kind check to refuse sideways")
	}
}
