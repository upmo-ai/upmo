package artifacts

import (
	"context"
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
func (s Store) Put(ctx context.Context, access Access, owner, task, name, store, key string) (string, error) {
	a, err := Keep(owner, task, name, store, key)
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
		INSERT INTO artifacts.artifacts (object_type, owner_user_id, task_id, name, store, object_key, visibility)
		VALUES ('artifact', $1::uuid, $2::uuid, $3, $4, $5, 'private')
		RETURNING id::text`, a.Owner, a.Task, a.Name, a.Store, a.Key).Scan(&id)
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
