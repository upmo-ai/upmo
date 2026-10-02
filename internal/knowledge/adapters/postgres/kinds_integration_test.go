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

func TestURLPendingThenReady(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()

	folder, err := app.CreateFolder(ctx, store, ownerAccess, "Notes")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := app.IngestURL(ctx, store, ownerAccess, app.URLDraft{
		FolderID: folder,
		URL:      "https://example.com/guide.txt",
		Title:    "Guide",
	})
	if err != nil || pending.Status != "pending" {
		t.Fatalf("pending %#v err %v", pending, err)
	}
	var kind, status string
	var revision int
	if err := owner.QueryRow(ctx, `
		SELECT kind, status, revision FROM knowledge.sources WHERE id = $1::uuid`, pending.ID).
		Scan(&kind, &status, &revision); err != nil {
		t.Fatal(err)
	}
	if kind != "url" || status != "pending" || revision != 0 {
		t.Fatalf("kind=%q status=%q revision=%d", kind, status, revision)
	}

	err = app.ProcessURL(ctx, store, store, ownerAccess,
		staticFetch{ctype: "text/plain", body: []byte("Ada prefers oolong tea.")},
		app.PassThroughText{}, nil, nil,
		folder, pending.ID, "https://example.com/guide.txt", "Guide")
	if err != nil {
		t.Fatal(err)
	}
	var chunks int
	if err := owner.QueryRow(ctx, `
		SELECT status, revision,
		       (SELECT count(*) FROM knowledge.chunks WHERE source_id = $1::uuid AND revision = sources.revision)
		FROM knowledge.sources AS sources WHERE id = $1::uuid`, pending.ID).
		Scan(&status, &revision, &chunks); err != nil {
		t.Fatal(err)
	}
	if status != "ready" || revision != 1 || chunks < 1 {
		t.Fatalf("status=%q revision=%d chunks=%d", status, revision, chunks)
	}
}

func TestURLProcessFailureLeavesNoChunks(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	folder, err := app.CreateFolder(ctx, store, ownerAccess, "Notes")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := app.IngestURL(ctx, store, ownerAccess, app.URLDraft{
		FolderID: folder, URL: "https://example.com/page.html",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.ProcessURL(ctx, store, store, ownerAccess,
		staticFetch{ctype: "text/html", body: []byte("<p>x</p>")},
		app.PassThroughText{}, nil, nil,
		folder, pending.ID, "https://example.com/page.html", ""); err != nil {
		t.Fatal(err)
	}
	var status, errText string
	var chunks int
	if err := owner.QueryRow(ctx, `
		SELECT status, error,
		       (SELECT count(*) FROM knowledge.chunks WHERE source_id = $1::uuid)
		FROM knowledge.sources WHERE id = $1::uuid`, pending.ID).
		Scan(&status, &errText, &chunks); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || errText == "" || chunks != 0 {
		t.Fatalf("status=%q error=%q chunks=%d", status, errText, chunks)
	}
}

type staticFetch struct {
	ctype string
	body  []byte
}

func (s staticFetch) Fetch(context.Context, string) (string, []byte, error) {
	return s.ctype, s.body, nil
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
