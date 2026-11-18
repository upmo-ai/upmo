package artifacts

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store is the Postgres writer for artifacts.
type Store struct {
	Pool *pgxpool.Pool
}

// Access is who is asking and what they may do.
// Teams, SharedView and SharedEdit are uuid array literals; empty is "{}".
type Access struct {
	UserID     string
	Allow      string
	Teams      string
	SharedView string
	SharedEdit string
}

// Put records one private artifact for its owner. Nothing here removes a row,
// so the record stays after the sandbox that produced it has gone.
// The caller needs artifact:view as well as artifact:create, because the
// returned id is read back under row security.
func (s Store) Put(ctx context.Context, access Access, in KeepRecord) (string, error) {
	a, err := CheckRecord(in)
	if err != nil {
		return "", err
	}
	user, ok := canonical(access.UserID)
	if !ok || user != a.Owner {
		return "", ErrArtifact
	}
	access.UserID = user
	tx, err := s.begin(ctx, access)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO artifacts.artifacts (
			object_type, owner_user_id, task_id, name, store, object_key, visibility, kind, mime, size_bytes)
		VALUES ('artifact', $1::uuid, $2::uuid, $3, $4, $5, 'private', $6, $7, $8)
		RETURNING id::text`,
		a.Owner, a.Task, a.Name, a.Store, a.Key, a.Kind, a.Mime, a.Size).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("artifacts: put: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return id, nil
}

// Erase removes every artifact the owner holds and returns their object keys,
// so the caller can remove the bytes from the volume or object storage.
func (s Store) Erase(ctx context.Context, access Access, owner string) ([]string, error) {
	owner, ok := canonical(owner)
	if !ok {
		return nil, ErrArtifact
	}
	user, ok := canonical(access.UserID)
	if !ok || user != owner {
		return nil, ErrArtifact
	}
	access.UserID = user
	tx, err := s.begin(ctx, access)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		DELETE FROM artifacts.artifacts
		WHERE owner_user_id = $1::uuid
		RETURNING object_key`, owner)
	if err != nil {
		return nil, fmt.Errorf("artifacts: erase: %w", err)
	}
	keys := make([]string, 0)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return nil, fmt.Errorf("artifacts: erase: %w", err)
		}
		keys = append(keys, key)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("artifacts: erase: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return keys, nil
}

func (s Store) begin(ctx context.Context, access Access) (pgx.Tx, error) {
	if s.Pool == nil {
		return nil, fmt.Errorf("artifacts: no database")
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
func accessArgs(access Access) []any {
	return []any{access.UserID, access.Allow, arrayOr(access.Teams), arrayOr(access.SharedView), arrayOr(access.SharedEdit)}
}

func arrayOr(literal string) string {
	if literal == "" {
		return "{}"
	}
	return literal
}

// ListByTask returns the artifacts for a task this person can see under row
// security. An empty list is not an error.
func (s Store) ListByTask(ctx context.Context, access Access, taskID string) ([]Record, error) {
	taskID, ok := canonical(taskID)
	if !ok {
		return nil, ErrArtifact
	}
	user, ok := canonical(access.UserID)
	if !ok || access.Allow == "" {
		return nil, ErrArtifact
	}
	access.UserID = user
	tx, err := s.begin(ctx, access)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT id::text, task_id::text, name, store, object_key, kind, mime, size_bytes, created_at
		FROM artifacts.artifacts
		WHERE task_id = $1::uuid
		ORDER BY created_at ASC, id ASC`, taskID)
	if err != nil {
		return nil, fmt.Errorf("artifacts: list: %w", err)
	}
	defer rows.Close()

	out := make([]Record, 0)
	for rows.Next() {
		var rec Record
		if err := rows.Scan(&rec.ID, &rec.Task, &rec.Name, &rec.Store, &rec.Key, &rec.Kind, &rec.Mime, &rec.Size, &rec.CreatedAt); err != nil {
			return nil, fmt.Errorf("artifacts: list: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("artifacts: list: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// Get returns one artifact this person can see under row security.
func (s Store) Get(ctx context.Context, access Access, id string) (Record, error) {
	id, ok := canonical(id)
	if !ok {
		return Record{}, ErrArtifact
	}
	user, ok := canonical(access.UserID)
	if !ok || access.Allow == "" {
		return Record{}, ErrArtifact
	}
	access.UserID = user
	tx, err := s.begin(ctx, access)
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var rec Record
	err = tx.QueryRow(ctx, `
		SELECT id::text, task_id::text, name, store, object_key, kind, mime, size_bytes, created_at
		FROM artifacts.artifacts
		WHERE id = $1::uuid`, id).Scan(
		&rec.ID, &rec.Task, &rec.Name, &rec.Store, &rec.Key, &rec.Kind, &rec.Mime, &rec.Size, &rec.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrGone
	}
	if err != nil {
		return Record{}, fmt.Errorf("artifacts: get: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Record{}, err
	}
	return rec, nil
}
