package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/knowledge/app"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/jobs"
	"github.com/upmo-ai/upmo/internal/tasks"
)

// Store is the knowledge tables. One method call is one transaction.
type Store struct {
	Pool     *pgxpool.Pool
	Enqueuer jobs.Enqueuer
}

type rowTx struct {
	tx     pgx.Tx
	store  *Store
	access app.Access
}

func (s Store) InTx(ctx context.Context, access app.Access, fn func(app.Tx) error) error {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(rowTx{tx: tx, store: &s, access: access}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s Store) CreateFolder(ctx context.Context, access app.Access, name string) (string, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO knowledge.folders (owner_user_id, visibility, name)
		VALUES ($1::uuid, 'private', $2)
		RETURNING id::text`, access.UserID, name).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("knowledge: folder: %w", err)
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.FolderCreate,
		Outcome:  audit.Success,
		TargetID: id,
	}); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return id, nil
}

// RenameFolder updates the folder name under edit policy. The name is
// content, so the event records the field name alone.
func (s Store) RenameFolder(ctx context.Context, access app.Access, folderID, name string) error {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		UPDATE knowledge.folders SET name = $2 WHERE id = $1::uuid`, folderID, name)
	if err != nil {
		return fmt.Errorf("knowledge: rename folder: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return app.ErrGone
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.FolderUpdate,
		Outcome:  audit.Success,
		TargetID: folderID,
		Changes:  audit.Changes{Fields: []string{"name"}},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// DeleteFolder removes a folder. Sources and chunks CASCADE. Blob keys for
// original uploads are returned so the caller can remove volume files.
func (s Store) DeleteFolder(ctx context.Context, access app.Access, folderID string) ([]string, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT blob_key FROM knowledge.sources
		WHERE folder_id = $1::uuid AND blob_key <> ''`, folderID)
	if err != nil {
		return nil, fmt.Errorf("knowledge: delete folder: %w", err)
	}
	keys := make([]string, 0)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return nil, fmt.Errorf("knowledge: delete folder: %w", err)
		}
		keys = append(keys, key)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("knowledge: delete folder: %w", err)
	}

	tag, err := tx.Exec(ctx, `DELETE FROM knowledge.folders WHERE id = $1::uuid`, folderID)
	if err != nil {
		return nil, fmt.Errorf("knowledge: delete folder: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, app.ErrGone
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.FolderDelete,
		Outcome:  audit.Success,
		TargetID: folderID,
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return keys, nil
}

// CreatePending inserts a source waiting on fetch or conversion.
func (s Store) CreatePending(ctx context.Context, access app.Access, pending app.Pending) (string, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var found string
	err = tx.QueryRow(ctx, `SELECT id::text FROM knowledge.folders WHERE id = $1::uuid`, pending.FolderID).Scan(&found)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", app.ErrGone
	}
	if err != nil {
		return "", fmt.Errorf("knowledge: pending: %w", err)
	}
	zero := make([]byte, 32)
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO knowledge.sources (
			folder_id, title, body, sha256, revision, updated_at,
			kind, source_ref, provenance, status, error
		) VALUES (
			$1::uuid, $2, '', $3, 0, $4,
			$5, $6, $7, 'pending', ''
		)
		RETURNING id::text`,
		pending.FolderID, pending.Title, zero, time.Now().UTC(),
		pending.Kind, pending.SourceRef, pending.Provenance).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("knowledge: pending: %w", err)
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.SourceAdd,
		Outcome:  audit.Success,
		TargetID: id,
		Changes: audit.Changes{
			Fields: []string{"kind", "revision"},
			Values: map[string]audit.Change{"kind": {To: pending.Kind}, "revision": {To: 0}},
		},
	}); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return id, nil
}

// SetBlobKey records the relative key for original upload bytes.
func (s Store) SetBlobKey(ctx context.Context, access app.Access, sourceID, blobKey string) error {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		UPDATE knowledge.sources
		SET blob_key = $2, updated_at = $3
		WHERE id = $1::uuid AND status = 'pending'`,
		sourceID, blobKey, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("knowledge: blob key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return app.ErrGone
	}
	return tx.Commit(ctx)
}

// MarkFailed records a failed fetch or conversion without publishing chunks.
func (s Store) MarkFailed(ctx context.Context, access app.Access, sourceID, message string) error {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		UPDATE knowledge.sources
		SET status = 'failed', error = $2, updated_at = $3
		WHERE id = $1::uuid AND status = 'pending'`,
		sourceID, message, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("knowledge: fail: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return app.ErrGone
	}
	return tx.Commit(ctx)
}

// ResetPending moves a failed or ready source back to pending for retry. Only
// Reingest calls it, so the event is the reingest the route applied.
func (s Store) ResetPending(ctx context.Context, access app.Access, sourceID string) error {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var prior string
	err = tx.QueryRow(ctx, `
		SELECT status FROM knowledge.sources
		WHERE id = $1::uuid AND status IN ('failed', 'ready')`,
		sourceID).Scan(&prior)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrGone
	}
	if err != nil {
		return fmt.Errorf("knowledge: reset: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE knowledge.sources
		SET status = 'pending', error = '', updated_at = $2
		WHERE id = $1::uuid AND status IN ('failed', 'ready')`,
		sourceID, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("knowledge: reset: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return app.ErrGone
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.SourceReingest,
		Outcome:  audit.Success,
		TargetID: sourceID,
		Changes: audit.Changes{
			Fields: []string{"status"},
			Values: map[string]audit.Change{"status": {From: prior, To: "pending"}},
		},
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s Store) Lexical(ctx context.Context, access app.Access, folderID, query string, limit int) ([]app.Lexical, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
		SELECT chunk.source_id::text, chunk.seq, chunk.body, chunk.embedding::text, source.provenance
		FROM knowledge.chunks AS chunk
		JOIN knowledge.sources AS source
		  ON source.id = chunk.source_id
		 AND source.revision = chunk.revision
		WHERE source.folder_id = $1::uuid
		  AND chunk.body_tsv @@ websearch_to_tsquery('english', $2)
		ORDER BY
		  ts_rank(chunk.body_tsv, websearch_to_tsquery('english', $2))
		  + 0.3 * ts_rank(chunk.prefix_tsv, websearch_to_tsquery('english', $2)) DESC,
		  chunk.source_id::text,
		  chunk.seq
		LIMIT $3`, folderID, query, limit)
	if err != nil {
		return nil, fmt.Errorf("knowledge: search: %w", err)
	}
	defer rows.Close()
	hits, err := scanLexical(rows)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return hits, nil
}

// Near returns the closest embedded chunks in a folder.
func (s Store) Near(ctx context.Context, access app.Access, folderID string, vector []float32, limit int) ([]app.Lexical, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL hnsw.iterative_scan = relaxed_order`); err != nil {
		return nil, fmt.Errorf("knowledge: near: %w", err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL hnsw.ef_search = 100`); err != nil {
		return nil, fmt.Errorf("knowledge: near: %w", err)
	}
	rows, err := tx.Query(ctx, `
		SELECT chunk.source_id::text, chunk.seq, chunk.body, chunk.embedding::text, source.provenance
		FROM knowledge.chunks AS chunk
		JOIN knowledge.sources AS source
		  ON source.id = chunk.source_id
		 AND source.revision = chunk.revision
		WHERE source.folder_id = $1::uuid
		  AND chunk.embedding IS NOT NULL
		ORDER BY chunk.embedding <=> $2::halfvec(1024)
		LIMIT $3`, folderID, formatHalfvec(vector), limit)
	if err != nil {
		return nil, fmt.Errorf("knowledge: near: %w", err)
	}
	defer rows.Close()
	hits, err := scanLexical(rows)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return hits, nil
}

func scanLexical(rows pgx.Rows) ([]app.Lexical, error) {
	var hits []app.Lexical
	for rows.Next() {
		var hit app.Lexical
		var raw *string
		var provenance string
		if err := rows.Scan(&hit.SourceID, &hit.Seq, &hit.Body, &raw, &provenance); err != nil {
			return nil, fmt.Errorf("knowledge: search: %w", err)
		}
		hit.Provenance = app.Provenance(provenance)
		if raw != nil {
			hit.Vector = parseHalfvec(*raw)
		}
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("knowledge: search: %w", err)
	}
	return hits, nil
}

func (s Store) begin(ctx context.Context, access app.Access) (pgx.Tx, error) {
	if s.Pool == nil {
		return nil, fmt.Errorf("knowledge: no database")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `
		SELECT set_config('upmo.user_id', $1, true),
		       set_config('upmo.allow', $2, true),
		       set_config('upmo.teams', $3, true),
		       set_config('upmo.shared_view', $4, true),
		       set_config('upmo.shared_edit', $5, true)`,
		accessArgs(access)...)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

// accessArgs are the begin settings. An empty team or share list is "{}".
func accessArgs(access app.Access) []any {
	return []any{access.UserID, access.Allow, arrayOr(access.Teams), arrayOr(access.SharedView), arrayOr(access.SharedEdit)}
}

func arrayOr(literal string) string {
	if literal == "" {
		return "{}"
	}
	return literal
}

func (t rowTx) Current(ctx context.Context, folderID, sourceID string) (app.Current, error) {
	var folder string
	var revision int
	var hash []byte
	err := t.tx.QueryRow(ctx, `
		SELECT folder_id::text, revision, sha256
		FROM knowledge.sources
		WHERE id = $1::uuid`, sourceID).Scan(&folder, &revision, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Current{}, nil
	}
	if err != nil {
		return app.Current{}, fmt.Errorf("knowledge: source: %w", err)
	}
	if folder != folderID {
		return app.Current{}, app.ErrFolder
	}
	if len(hash) != 32 {
		return app.Current{}, fmt.Errorf("knowledge: source hash")
	}
	current := app.Current{Found: true, ID: sourceID, Revision: revision}
	copy(current.Hash[:], hash)
	return current, nil
}

func (t rowTx) FindClip(ctx context.Context, sourceRef string) (string, string, error) {
	var id, folderID string
	err := t.tx.QueryRow(ctx, `
		SELECT id::text, folder_id::text
		FROM knowledge.sources
		WHERE kind = 'clip' AND source_ref = $1`, sourceRef).Scan(&id, &folderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("knowledge: clip: %w", err)
	}
	return id, folderID, nil
}

func (t rowTx) Save(
	ctx context.Context,
	draft app.Draft,
	hash [32]byte,
	pieces []app.Piece,
) (app.Result, error) {
	folderID, sourceID, title, body := draft.FolderID, draft.SourceID, draft.Title, draft.Body
	kind := draft.Kind
	if kind == "" {
		kind = "markdown"
	}
	provenance := draft.Provenance
	if provenance == "" {
		provenance = "curated"
	}
	ref := draft.SourceRef
	id := sourceID
	revision := 0
	if sourceID == "" {
		err := t.tx.QueryRow(ctx, `
			INSERT INTO knowledge.sources (
				folder_id, title, body, sha256, revision, updated_at,
				kind, source_ref, provenance, status, error
			) VALUES (
				$1::uuid, $2, $3, $4, 0, $5,
				$6, $7, $8, 'ready', ''
			)
			RETURNING id::text`,
			folderID, title, body, hash[:], time.Now().UTC(),
			kind, ref, provenance).Scan(&id)
		if err != nil {
			return app.Result{}, fmt.Errorf("knowledge: source: %w", err)
		}
	} else {
		tag, err := t.tx.Exec(ctx, `
			UPDATE knowledge.sources
			SET title = $3, body = $4, sha256 = $5, updated_at = $6,
			    status = 'ready', error = '',
			    source_ref = CASE WHEN $7 <> '' THEN $7 ELSE source_ref END,
			    provenance = CASE WHEN $8 <> '' THEN $8 ELSE provenance END
			WHERE id = $1::uuid AND folder_id = $2::uuid`,
			sourceID, folderID, title, body, hash[:], time.Now().UTC(), ref, provenance)
		if err != nil {
			return app.Result{}, fmt.Errorf("knowledge: source: %w", err)
		}
		if tag.RowsAffected() == 0 {
			err = t.tx.QueryRow(ctx, `
				INSERT INTO knowledge.sources (
					id, folder_id, title, body, sha256, revision, updated_at,
					kind, source_ref, provenance, status, error
				) VALUES (
					$1::uuid, $2::uuid, $3, $4, $5, 0, $6,
					$7, $8, $9, 'ready', ''
				)
				RETURNING id::text`,
				sourceID, folderID, title, body, hash[:], time.Now().UTC(),
				kind, ref, provenance).Scan(&id)
			if err != nil {
				return app.Result{}, fmt.Errorf("knowledge: source: %w", err)
			}
		} else if err = t.tx.QueryRow(ctx, `
			SELECT revision, kind FROM knowledge.sources WHERE id = $1::uuid`, sourceID).Scan(&revision, &kind); err != nil {
			return app.Result{}, fmt.Errorf("knowledge: source: %w", err)
		}
	}
	newRev := revision + 1
	for i, piece := range pieces {
		vector := ""
		model := ""
		if piece.Vector != nil {
			vector = formatHalfvec(piece.Vector)
			model = piece.Model
		}
		if _, err := t.tx.Exec(ctx, `
			INSERT INTO knowledge.chunks (
				source_id, revision, seq, body, context_prefix, embedding, embedding_model
			) VALUES (
				$1::uuid, $2, $3, $4, $5, NULLIF($6, '')::halfvec(1024), $7
			)`, id, newRev, i, piece.Body, piece.Prefix, vector, model); err != nil {
			return app.Result{}, fmt.Errorf("knowledge: chunk: %w", err)
		}
	}
	if _, err := t.tx.Exec(ctx, `
		UPDATE knowledge.sources SET revision = $2 WHERE id = $1::uuid`, id, newRev); err != nil {
		return app.Result{}, fmt.Errorf("knowledge: revision: %w", err)
	}
	if _, err := t.tx.Exec(ctx, `
		DELETE FROM knowledge.chunks WHERE source_id = $1::uuid AND revision <> $2`, id, newRev); err != nil {
		return app.Result{}, fmt.Errorf("knowledge: revision: %w", err)
	}
	if err := audit.Record(ctx, t.tx, audit.Event{
		Action:   audit.SourceAdd,
		Outcome:  audit.Success,
		TargetID: id,
		Changes: audit.Changes{
			Fields: []string{"kind", "revision"},
			Values: map[string]audit.Change{"kind": {To: kind}, "revision": {To: newRev}},
		},
	}); err != nil {
		return app.Result{}, err
	}
	if t.store != nil && t.store.Enqueuer != nil && t.access.UserID != "" {
		if _, err := t.store.Enqueuer.InsertTx(ctx, t.tx, tasks.ScoutJobArgs{
			UserID:   t.access.UserID,
			Trigger:  "knowledge",
			SourceID: id,
		}, nil); err != nil {
			return app.Result{}, fmt.Errorf("knowledge: scout enqueue: %w", err)
		}
	}
	return app.Result{ID: id, Revision: newRev, Chunks: len(pieces)}, nil
}

func formatHalfvec(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'f', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

func parseHalfvec(raw string) []float32 {
	raw = strings.Trim(raw, "[]")
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]float32, len(parts))
	for i, part := range parts {
		value, err := strconv.ParseFloat(strings.TrimSpace(part), 32)
		if err != nil {
			return nil
		}
		out[i] = float32(value)
	}
	return out
}

// ListFolders returns folders this person can see under row security.
func (s Store) ListFolders(ctx context.Context, access app.Access) ([]app.Folder, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT id::text, name
		FROM knowledge.folders
		ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("knowledge: list folders: %w", err)
	}
	defer rows.Close()

	out := make([]app.Folder, 0)
	for rows.Next() {
		var folder app.Folder
		if err := rows.Scan(&folder.ID, &folder.Name); err != nil {
			return nil, fmt.Errorf("knowledge: list folders: %w", err)
		}
		out = append(out, folder)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("knowledge: list folders: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// ListSources returns sources in a folder this person can see. The body stays out.
func (s Store) ListSources(ctx context.Context, access app.Access, folderID string) ([]app.Source, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var found string
	err = tx.QueryRow(ctx, `SELECT id::text FROM knowledge.folders WHERE id = $1::uuid`, folderID).Scan(&found)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, app.ErrGone
	}
	if err != nil {
		return nil, fmt.Errorf("knowledge: list sources: %w", err)
	}

	rows, err := tx.Query(ctx, `
		SELECT id::text, title, kind, source_ref, provenance, status, error, revision, updated_at
		FROM knowledge.sources
		WHERE folder_id = $1::uuid
		ORDER BY updated_at DESC, id`, folderID)
	if err != nil {
		return nil, fmt.Errorf("knowledge: list sources: %w", err)
	}
	defer rows.Close()

	out := make([]app.Source, 0)
	for rows.Next() {
		var source app.Source
		var updated time.Time
		if err := rows.Scan(
			&source.ID, &source.Title, &source.Kind, &source.SourceRef, &source.Provenance,
			&source.Status, &source.Error, &source.Revision, &updated,
		); err != nil {
			return nil, fmt.Errorf("knowledge: list sources: %w", err)
		}
		source.UpdatedAt = updated.UTC().Format(time.RFC3339)
		out = append(out, source)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("knowledge: list sources: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// ViewSource returns one source in a folder this person can see, including the body.
func (s Store) ViewSource(ctx context.Context, access app.Access, folderID, sourceID string) (app.SourceDetail, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return app.SourceDetail{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var found string
	err = tx.QueryRow(ctx, `SELECT id::text FROM knowledge.folders WHERE id = $1::uuid`, folderID).Scan(&found)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.SourceDetail{}, app.ErrGone
	}
	if err != nil {
		return app.SourceDetail{}, fmt.Errorf("knowledge: view source: %w", err)
	}

	var source app.SourceDetail
	var updated time.Time
	err = tx.QueryRow(ctx, `
		SELECT id::text, title, body, kind, source_ref, provenance, status, error, blob_key, revision, updated_at
		FROM knowledge.sources
		WHERE id = $1::uuid AND folder_id = $2::uuid`, sourceID, folderID).Scan(
		&source.ID, &source.Title, &source.Body, &source.Kind, &source.SourceRef,
		&source.Provenance, &source.Status, &source.Error, &source.BlobKey, &source.Revision, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.SourceDetail{}, app.ErrGone
	}
	if err != nil {
		return app.SourceDetail{}, fmt.Errorf("knowledge: view source: %w", err)
	}
	source.UpdatedAt = updated.UTC().Format(time.RFC3339)
	if err := tx.Commit(ctx); err != nil {
		return app.SourceDetail{}, err
	}
	return source, nil
}

// DeleteSource removes a source in a folder the person can edit. Chunks and
// their embeddings go with it via ON DELETE CASCADE. The blob_key is returned
// so the caller can remove original upload bytes.
func (s Store) DeleteSource(ctx context.Context, access app.Access, folderID, sourceID string) (string, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var found string
	err = tx.QueryRow(ctx, `SELECT id::text FROM knowledge.folders WHERE id = $1::uuid`, folderID).Scan(&found)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", app.ErrGone
	}
	if err != nil {
		return "", fmt.Errorf("knowledge: delete source: %w", err)
	}

	var blobKey string
	err = tx.QueryRow(ctx, `
		SELECT blob_key FROM knowledge.sources
		WHERE id = $1::uuid AND folder_id = $2::uuid`, sourceID, folderID).Scan(&blobKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", app.ErrGone
	}
	if err != nil {
		return "", fmt.Errorf("knowledge: delete source: %w", err)
	}

	tag, err := tx.Exec(ctx, `
		DELETE FROM knowledge.sources
		WHERE id = $1::uuid AND folder_id = $2::uuid`, sourceID, folderID)
	if err != nil {
		return "", fmt.Errorf("knowledge: delete source: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return "", app.ErrGone
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.SourceDelete,
		Outcome:  audit.Success,
		TargetID: sourceID,
	}); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return blobKey, nil
}
