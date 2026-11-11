//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/knowledge"
	"github.com/upmo-ai/upmo/internal/knowledge/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/knowledge/app"
	"github.com/upmo-ai/upmo/internal/knowledge/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestIngestPublishesVerbatimChunksAndSkipsTheSameHash(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	folder, err := app.CreateFolder(ctx, store, ownerAccess, "Notes")
	if err != nil {
		t.Fatal(err)
	}
	body := domainNumbered(900)
	sum := &countingPrefix{text: "oolong ceremonial"}
	embed := &recordingEmbed{model: "model-a"}
	first, err := app.Ingest(ctx, store, ownerAccess, sum, embed, app.Draft{
		FolderID: folder, Title: "ZZZTITLE", Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Skipped || first.Revision != 1 || first.Chunks < 2 {
		t.Fatalf("first = %+v", first)
	}
	if strings.Contains(strings.Join(embed.texts, "\n"), "ZZZTITLE") {
		t.Fatal("title was embedded")
	}
	var stored string
	if err := owner.QueryRow(ctx, `
		SELECT body FROM knowledge.chunks
		WHERE source_id = $1 AND revision = 1 AND seq = 0`, first.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, stored) {
		t.Fatal("stored chunk is not verbatim")
	}
	second, err := app.Ingest(ctx, store, ownerAccess, sum, embed, app.Draft{
		FolderID: folder, SourceID: first.ID, Title: "ZZZTITLE", Body: body,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Skipped || second.Revision != 1 || sum.calls != first.Chunks {
		t.Fatalf("second = %+v summariser=%d chunks=%d", second, sum.calls, first.Chunks)
	}
}

func TestIngestSwapsTheRevision(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	folder, err := app.CreateFolder(ctx, store, ownerAccess, "Notes")
	if err != nil {
		t.Fatal(err)
	}
	firstBody := "Ada prefers oolong tea in the morning."
	first, err := app.Ingest(ctx, store, ownerAccess, nil, nil, app.Draft{
		FolderID: folder, Title: "Tea", Body: firstBody,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.Ingest(ctx, store, ownerAccess, nil, nil, app.Draft{
		FolderID: folder, SourceID: first.ID, Title: "Tea", Body: "Ada now prefers green tea in the morning.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision != 2 {
		t.Fatalf("revision = %d", second.Revision)
	}
	var old, current int
	if err := owner.QueryRow(ctx, `
		SELECT
		  count(*) FILTER (WHERE revision = 1),
		  count(*) FILTER (WHERE revision = 2)
		FROM knowledge.chunks WHERE source_id = $1`, first.ID).Scan(&old, &current); err != nil {
		t.Fatal(err)
	}
	if old != 0 || current != 1 {
		t.Fatalf("old=%d current=%d", old, current)
	}
}

func TestSearchStaysInsideTheFolderAndIgnoresAPrefixOnlyMatch(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	folderA, err := app.CreateFolder(ctx, store, ownerAccess, "A")
	if err != nil {
		t.Fatal(err)
	}
	folderB, err := app.CreateFolder(ctx, store, ownerAccess, "B")
	if err != nil {
		t.Fatal(err)
	}
	sum := &countingPrefix{text: "rareword ceremonial"}
	if _, err := app.Ingest(ctx, store, ownerAccess, sum, nil, app.Draft{
		FolderID: folderA, Title: "Tea", Body: "Ada prefers oolong tea.",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Ingest(ctx, store, ownerAccess, nil, nil, app.Draft{
		FolderID: folderB, Title: "Tea", Body: "Ada prefers oolong tea.",
	}); err != nil {
		t.Fatal(err)
	}
	hits, err := app.Search(ctx, store, nil, nil, ownerAccess, folderA, "oolong")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Body != "Ada prefers oolong tea." {
		t.Fatalf("hits = %+v", hits)
	}
	prefixOnly, err := app.Search(ctx, store, nil, nil, ownerAccess, folderA, "rareword")
	if err != nil {
		t.Fatal(err)
	}
	if len(prefixOnly) != 0 {
		t.Fatalf("prefix-only hits = %+v", prefixOnly)
	}
	other, err := app.Search(ctx, store, nil, nil, otherAccess, folderA, "oolong")
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 0 {
		t.Fatalf("other person hits = %+v", other)
	}
}

func TestPrefixRaisesTheRankOfABodyMatch(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	folder, err := app.CreateFolder(ctx, store, ownerAccess, "Tea")
	if err != nil {
		t.Fatal(err)
	}
	plain := &countingPrefix{text: "stored on a shelf"}
	boosted := &countingPrefix{text: "tea is the subject"}
	if _, err := app.Ingest(ctx, store, ownerAccess, plain, nil, app.Draft{
		FolderID: folder, Title: "Plain", Body: "The kettle holds tea for the team.",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Ingest(ctx, store, ownerAccess, boosted, nil, app.Draft{
		FolderID: folder, Title: "Boosted", Body: "The kettle holds tea for the team.",
	}); err != nil {
		t.Fatal(err)
	}
	hits, err := app.Search(ctx, store, nil, nil, ownerAccess, folder, "tea")
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) < 2 {
		t.Fatalf("hits = %+v", hits)
	}
	var boostedID string
	if err := owner.QueryRow(ctx, `
		SELECT source_id::text FROM knowledge.chunks
		WHERE context_prefix = 'tea is the subject'`).Scan(&boostedID); err != nil {
		t.Fatal(err)
	}
	if hits[0].SourceID != boostedID {
		t.Fatalf("first hit = %+v, want source %s", hits[0], boostedID)
	}
}

func TestSearchRRFAddsDenseHitMissedByFTS(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	folder, err := app.CreateFolder(ctx, store, ownerAccess, "Tea")
	if err != nil {
		t.Fatal(err)
	}
	ingestEmbed := &axisEmbed{model: "model-a", axis: 0}
	if _, err := app.Ingest(ctx, store, ownerAccess, nil, ingestEmbed, app.Draft{
		FolderID: folder, Title: "Exact", Body: "Ada prefers oolong tea.",
	}); err != nil {
		t.Fatal(err)
	}
	ingestEmbed.axis = 1
	paraphrase, err := app.Ingest(ctx, store, ownerAccess, nil, ingestEmbed, app.Draft{
		FolderID: folder, Title: "Dense", Body: "She likes a warm leaf infusion in the afternoon.",
	})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := app.Search(ctx, store, &axisEmbed{model: "model-a", axis: 1}, nil, ownerAccess, folder, "oolong")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, hit := range hits {
		if hit.SourceID == paraphrase.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("paraphrase missing from %+v", hits)
	}
}

func TestMixedEmbeddingModelIsRefused(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	folder, err := app.CreateFolder(ctx, store, ownerAccess, "Tea")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Ingest(ctx, store, ownerAccess, nil, &recordingEmbed{model: "model-a"}, app.Draft{
		FolderID: folder, Title: "A", Body: "Ada prefers oolong tea.",
	}); err != nil {
		t.Fatal(err)
	}
	_, err = app.Ingest(ctx, store, ownerAccess, nil, &recordingEmbed{model: "model-b"}, app.Draft{
		FolderID: folder, Title: "B", Body: "The kettle holds tea for the team.",
	})
	if err == nil {
		t.Fatal("mixed embedding_model was accepted")
	}
}

func TestEmptySourceStoresNothing(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	folder, err := app.CreateFolder(ctx, store, ownerAccess, "Notes")
	if err != nil {
		t.Fatal(err)
	}
	_, err = app.Ingest(ctx, store, ownerAccess, nil, nil, app.Draft{
		FolderID: folder, Title: "Blank", Body: "  \n",
	})
	if !errors.Is(err, domain.ErrEmpty) {
		t.Fatalf("err = %v", err)
	}
	var n int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM knowledge.sources`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("sources = %d", n)
	}
}

func TestChunkTrustStaysDocument(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	folder, err := app.CreateFolder(ctx, store, ownerAccess, "Notes")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Ingest(ctx, store, ownerAccess, nil, nil, app.Draft{
		FolderID: folder, Title: "Tea", Body: "Ada prefers oolong tea.",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `UPDATE knowledge.chunks SET trust = 'tool'`); err == nil {
		t.Fatal("a chunk accepted a trust other than document")
	}
}

const (
	ownerUser = "018f3b2c-7a10-7c4a-8e11-111111111111"
	otherUser = "018f3b2c-7a10-7c4a-8e11-222222222222"
)

var (
	ownerAccess = app.Access{UserID: ownerUser, Allow: "folder:create,folder:view,folder:edit,folder:delete"}
	otherAccess = app.Access{UserID: otherUser, Allow: "folder:view"}
)

func up(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := pgtest.New(t)
	ctx := context.Background()
	// The stores record audit events, so the audit schema comes up first.
	if err := audit.Up(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, pool, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, pool, knowledge.Schema, knowledge.Migrations()); err != nil {
		t.Fatal(err)
	}
	return pool
}

func domainNumbered(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString("w")
		b.WriteString(strings.Repeat("x", 4))
		b.WriteByte(' ')
	}
	return b.String()
}

type countingPrefix struct {
	calls int
	text  string
}

func (c *countingPrefix) Prefix(context.Context, string) (string, error) {
	c.calls++
	return c.text, nil
}

type recordingEmbed struct {
	model string
	texts []string
}

func (r *recordingEmbed) Embed(_ context.Context, texts []string) (string, [][]float32, error) {
	r.texts = append(r.texts, texts...)
	vector := make([]float32, domain.Dim)
	vector[0] = 1
	vectors := make([][]float32, len(texts))
	for i := range vectors {
		vectors[i] = vector
	}
	return r.model, vectors, nil
}

type axisEmbed struct {
	model string
	axis  int
}

func (a *axisEmbed) Embed(_ context.Context, texts []string) (string, [][]float32, error) {
	vector := make([]float32, domain.Dim)
	vector[a.axis] = 1
	vectors := make([][]float32, len(texts))
	for i := range vectors {
		vectors[i] = vector
	}
	return a.model, vectors, nil
}
