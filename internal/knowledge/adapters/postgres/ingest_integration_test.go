//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
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
		t.Fatalf("second = %+v summarizer=%d chunks=%d", second, sum.calls, first.Chunks)
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

// TestSearchUsesSourceLanguageForFullText pins that a Dutch source keeps its
// own text search configuration on the source, on every chunk and in the
// generated vectors, and that the query parses with the same per-row
// configuration in both inflection directions.
func TestSearchUsesSourceLanguageForFullText(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	folder, err := app.CreateFolder(ctx, store, ownerAccess, "Dutch")
	if err != nil {
		t.Fatal(err)
	}
	plural := "De eenden zwemmen in de vijver achter de oude molens. " +
		"De eenden vliegen elke ochtend naar het meer en komen terug bij het " +
		"avondeten. De boer telt de eenden en sluit het hek van de vijver."
	singular := "De eend drijft rustig op het water van de gracht. " +
		"Deze eend eet brood van het meisje op de brug en zwemt daarna weg " +
		"onder de platteboom naar het riet aan de overkant vandaag."
	pluralRes, err := app.Ingest(ctx, store, ownerAccess, nil, nil, app.Draft{
		FolderID: folder, Title: "Meervoud", Body: plural,
	})
	if err != nil {
		t.Fatal(err)
	}
	singularRes, err := app.Ingest(ctx, store, ownerAccess, nil, nil, app.Draft{
		FolderID: folder, Title: "Enkelvoud", Body: singular,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{pluralRes.ID, singularRes.ID} {
		var sourceLang, chunkLang string
		if err := owner.QueryRow(ctx, `
			SELECT source.language::text,
			       (SELECT language::text FROM knowledge.chunks
			        WHERE source_id = source.id AND revision = source.revision LIMIT 1)
			FROM knowledge.sources AS source WHERE source.id = $1::uuid`, id).
			Scan(&sourceLang, &chunkLang); err != nil {
			t.Fatal(err)
		}
		if sourceLang != "dutch" || chunkLang != "dutch" {
			t.Fatalf("languages = %q / %q, want dutch", sourceLang, chunkLang)
		}
	}
	var tsv string
	if err := owner.QueryRow(ctx, `
		SELECT body_tsv::text FROM knowledge.chunks
		WHERE source_id = $1::uuid AND revision = 1 AND seq = 0`, pluralRes.ID).Scan(&tsv); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tsv, "'eend'") || strings.Contains(tsv, "'eenden'") {
		t.Fatalf("plural body was not stemmed with the dutch configuration: %s", tsv)
	}
	pluralHits, err := app.Search(ctx, store, nil, nil, ownerAccess, folder, "eend")
	if err != nil {
		t.Fatal(err)
	}
	if !containsSource(pluralHits, pluralRes.ID) {
		t.Fatalf("singular query did not find the plural source: %+v", pluralHits)
	}
	singularHits, err := app.Search(ctx, store, nil, nil, ownerAccess, folder, "eenden")
	if err != nil {
		t.Fatal(err)
	}
	if !containsSource(singularHits, singularRes.ID) {
		t.Fatalf("plural query did not find the singular source: %+v", singularHits)
	}
}

func containsSource(hits []app.Hit, id string) bool {
	for _, hit := range hits {
		if hit.SourceID == id {
			return true
		}
	}
	return false
}

// TestChunkCannotClaimAnotherFolder pins the composite foreign key: a
// chunk whose folder_id disagrees with its source's folder is refused as a
// foreign key violation, not by any other constraint.
func TestChunkCannotClaimAnotherFolder(t *testing.T) {
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
	res, err := app.Ingest(ctx, store, ownerAccess, nil, nil, app.Draft{
		FolderID: folderA, Title: "Tea", Body: "Ada prefers oolong tea.",
	})
	if err != nil {
		t.Fatal(err)
	}
	// seq 7 is unused, so only the folder mismatch can refuse this row.
	_, err = owner.Exec(ctx, `
		INSERT INTO knowledge.chunks (source_id, folder_id, revision, seq, body)
		VALUES ($1::uuid, $2::uuid, 1, 7, 'Ada prefers oolong tea.')`,
		res.ID, folderB)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
		t.Fatalf("mismatched folder err = %v, want a foreign key violation", err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO knowledge.chunks (source_id, folder_id, revision, seq, body)
		VALUES ($1::uuid, $2::uuid, 1, 8, 'Ada prefers green tea.')`,
		res.ID, folderA); err != nil {
		t.Fatalf("a matching folder_id was refused: %v", err)
	}
}

// TestLexicalServesEachLanguageAndHoldsTheLimitAcrossLanguages pins the
// per-language lateral query: each language answers in its own stemming, and
// the limit binds to the whole folder, not to each language.
func TestLexicalServesEachLanguageAndHoldsTheLimitAcrossLanguages(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	folder, err := app.CreateFolder(ctx, store, ownerAccess, "Mixed")
	if err != nil {
		t.Fatal(err)
	}
	dutchBody := "De eenden zwemmen in de vijver achter de oude molens. " +
		"De eenden drinken oolong uit kleine kopjes bij de molen. " +
		"De boer telt de eenden en sluit het hek van de vijver."
	englishBody := "The kettle holds tea for the team near the pond. " +
		"She pours oolong into small cups while the kettle boils. " +
		"Most users read the guide and follow the steps carefully."
	dutch, err := app.Ingest(ctx, store, ownerAccess, nil, nil, app.Draft{
		FolderID: folder, Title: "Eenden", Body: dutchBody,
	})
	if err != nil {
		t.Fatal(err)
	}
	english, err := app.Ingest(ctx, store, ownerAccess, nil, nil, app.Draft{
		FolderID: folder, Title: "Kettle", Body: englishBody,
	})
	if err != nil {
		t.Fatal(err)
	}
	dutchHits, err := store.Lexical(ctx, ownerAccess, folder, "eend", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(dutchHits) != 1 || dutchHits[0].SourceID != dutch.ID {
		t.Fatalf("dutch query hits = %+v", dutchHits)
	}
	englishHits, err := store.Lexical(ctx, ownerAccess, folder, "kettles", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(englishHits) != 1 || englishHits[0].SourceID != english.ID {
		t.Fatalf("english query hits = %+v", englishHits)
	}
	// One shared term matches both languages; the limit must hold across
	// them, not per language.
	shared, err := store.Lexical(ctx, ownerAccess, folder, "oolong", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(shared) != 1 {
		t.Fatalf("limit 1 returned %d hits across languages", len(shared))
	}
}

// TestHeadingPathIsIndexedWithoutChangingBodyOrEmbedding pins that a heading
// term finds a chunk whose body never contains it, because the path lives in
// the indexed document only, and that the embedder never sees the path.
func TestHeadingPathIsIndexedWithoutChangingBodyOrEmbedding(t *testing.T) {
	owner := up(t)
	store := postgres.Store{Pool: pgtest.App(t, owner)}
	ctx := context.Background()
	folder, err := app.CreateFolder(ctx, store, ownerAccess, "Paths")
	if err != nil {
		t.Fatal(err)
	}
	source := "# Zephyrline Setup\n" + dutchFreeIntro() + "\n## Panels\n" + panelBody()
	embed := &recordingEmbed{model: "model-a"}
	if _, err := app.Ingest(ctx, store, ownerAccess, nil, embed, app.Draft{
		FolderID: folder, Title: "Guide", Body: source,
	}); err != nil {
		t.Fatal(err)
	}
	hits, err := app.Search(ctx, store, nil, nil, ownerAccess, folder, "zephyrline")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, hit := range hits {
		if !strings.Contains(source, hit.Body) {
			t.Fatalf("hit body is not a source substring: %q", hit.Body[:40])
		}
		if strings.Contains(hit.Body, "Zephyrline Setup >") {
			t.Fatal("the heading path leaked into the body")
		}
		if strings.Contains(hit.Body, "Zephyrline") {
			continue
		}
		found = true
		embedded := false
		for _, text := range embed.texts {
			if text == hit.Body {
				embedded = true
				if strings.Contains(text, "Zephyrline") {
					t.Fatal("the heading path was embedded")
				}
			}
		}
		if !embedded {
			t.Fatal("the hit body was never embedded verbatim")
		}
	}
	if !found {
		t.Fatalf("no hit carried the heading path alone: %+v", hits)
	}
	rows, err := owner.Query(ctx, `
		SELECT heading_path FROM knowledge.chunks
		WHERE revision = 1 AND body NOT LIKE '%Zephyrline%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	deep := false
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			t.Fatal(err)
		}
		if path == "Zephyrline Setup > Panels" {
			deep = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !deep {
		t.Fatal("no panel chunk carries the deeper heading path")
	}
	if strings.Contains(strings.Join(embed.texts, "\n"), "Zephyrline >") {
		t.Fatal("a joined heading path reached the embedder")
	}
}

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

func longSection(tag string, n int) string {
	var b strings.Builder
	i := 0
	for b.Len() < n {
		fmt.Fprintf(&b, "%s section text %04d. ", tag, i)
		i++
	}
	return b.String()[:n]
}

func dutchFreeIntro() string { return longSection("intro", 1700) }

func panelBody() string { return longSection("panel", 3200) }

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
