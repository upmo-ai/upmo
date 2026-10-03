//go:build integration

package postgres_test

import (
	"context"
	"errors"
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

func TestFileMarkdownPendingThenReady(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	folder, err := app.CreateFolder(ctx, store, ownerAccess, "Notes")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := app.IngestFile(ctx, store, ownerAccess, app.FileDraft{
		FolderID: folder,
		Title:    "Guide",
		Name:     "guide.md",
		Bytes:    []byte("Ada prefers oolong tea."),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.ProcessFile(ctx, store, store, ownerAccess, app.PassThroughText{}, nil, nil,
		folder, pending.ID, "guide.md", "Guide", []byte("Ada prefers oolong tea.")); err != nil {
		t.Fatal(err)
	}
	var kind, status string
	var chunks int
	if err := owner.QueryRow(ctx, `
		SELECT kind, status,
		       (SELECT count(*) FROM knowledge.chunks WHERE source_id = $1::uuid AND revision = sources.revision)
		FROM knowledge.sources AS sources WHERE id = $1::uuid`, pending.ID).
		Scan(&kind, &status, &chunks); err != nil {
		t.Fatal(err)
	}
	if kind != "file" || status != "ready" || chunks < 1 {
		t.Fatalf("kind=%q status=%q chunks=%d", kind, status, chunks)
	}
}

func TestClipDedupesByNormalisedURL(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	folder, err := app.CreateFolder(ctx, store, ownerAccess, "Clips")
	if err != nil {
		t.Fatal(err)
	}
	first, err := app.IngestClip(ctx, store, ownerAccess, nil, nil, app.ClipDraft{
		FolderID: folder,
		URL:      "https://Example.com/doc#a",
		Title:    "Doc",
		Markdown: "First clip body.",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.IngestClip(ctx, store, ownerAccess, nil, nil, app.ClipDraft{
		FolderID: folder,
		URL:      "https://example.com/doc",
		Markdown: "Second clip body revised.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || second.Revision <= first.Revision {
		t.Fatalf("first %#v second %#v", first, second)
	}
	var kind, provenance, ref string
	if err := owner.QueryRow(ctx, `
		SELECT kind, provenance, source_ref FROM knowledge.sources WHERE id = $1::uuid`, first.ID).
		Scan(&kind, &provenance, &ref); err != nil {
		t.Fatal(err)
	}
	if kind != "clip" || provenance != "web" || ref != "https://example.com/doc" {
		t.Fatalf("kind=%q provenance=%q ref=%q", kind, provenance, ref)
	}
	var n int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM knowledge.sources WHERE kind = 'clip'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("clip rows = %d", n)
	}
}

func TestReingestFailedURLReachesReady(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	folder, err := app.CreateFolder(ctx, store, ownerAccess, "Notes")
	if err != nil {
		t.Fatal(err)
	}
	pending, err := app.IngestURL(ctx, store, ownerAccess, app.URLDraft{
		FolderID: folder, URL: "https://example.com/guide.txt", Title: "Guide",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.ProcessURL(ctx, store, store, ownerAccess,
		staticFetch{ctype: "text/html", body: []byte("<p>x</p>")},
		app.PassThroughText{}, nil, nil,
		folder, pending.ID, "https://example.com/guide.txt", "Guide"); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := owner.QueryRow(ctx, `SELECT status FROM knowledge.sources WHERE id = $1::uuid`, pending.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "failed" {
		t.Fatalf("status=%q", status)
	}
	got, err := app.Reingest(ctx, store, ownerAccess,
		staticFetch{ctype: "text/plain", body: []byte("Ada prefers oolong tea.")},
		app.PassThroughText{}, nil, nil,
		folder, pending.ID, func(fn func()) { fn() })
	if err != nil || !got.Async {
		t.Fatalf("got %#v err %v", got, err)
	}
	var chunks int
	if err := owner.QueryRow(ctx, `
		SELECT status,
		       (SELECT count(*) FROM knowledge.chunks WHERE source_id = $1::uuid AND revision = sources.revision)
		FROM knowledge.sources AS sources WHERE id = $1::uuid`, pending.ID).Scan(&status, &chunks); err != nil {
		t.Fatal(err)
	}
	if status != "ready" || chunks < 1 {
		t.Fatalf("status=%q chunks=%d", status, chunks)
	}
}

func TestDeleteSourceRemovesChunksAndEmbeddings(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	folder, err := app.CreateFolder(ctx, store, ownerAccess, "Notes")
	if err != nil {
		t.Fatal(err)
	}
	result, err := app.Ingest(ctx, store, ownerAccess, nil, &recordingEmbed{model: "model-a"}, app.Draft{
		FolderID: folder, Title: "Guide", Body: "Ada prefers oolong tea.",
	})
	if err != nil {
		t.Fatal(err)
	}
	var chunks, embedded int
	if err := owner.QueryRow(ctx, `
		SELECT count(*), count(embedding)
		FROM knowledge.chunks WHERE source_id = $1::uuid`, result.ID).Scan(&chunks, &embedded); err != nil {
		t.Fatal(err)
	}
	if chunks < 1 || embedded < 1 {
		t.Fatalf("chunks=%d embedded=%d before delete", chunks, embedded)
	}
	if err := app.DeleteSource(ctx, store, ownerAccess, folder, result.ID); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `
		SELECT count(*), count(embedding)
		FROM knowledge.chunks WHERE source_id = $1::uuid`, result.ID).Scan(&chunks, &embedded); err != nil {
		t.Fatal(err)
	}
	if chunks != 0 || embedded != 0 {
		t.Fatalf("chunks=%d embedded=%d after delete", chunks, embedded)
	}
	var sources int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM knowledge.sources WHERE id = $1::uuid`, result.ID).Scan(&sources); err != nil {
		t.Fatal(err)
	}
	if sources != 0 {
		t.Fatalf("source rows = %d", sources)
	}
	if err := app.DeleteSource(ctx, store, ownerAccess, folder, result.ID); !errors.Is(err, app.ErrGone) {
		t.Fatalf("second delete err %v", err)
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
