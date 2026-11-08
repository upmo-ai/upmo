package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/tasks/app"
)

// ShareRow returns the task row behind a share under row security, with
// identity's answer to whether this person may edit the task. A task this
// person cannot see is ErrGone.
func (s Store) ShareRow(ctx context.Context, access app.Access, taskID string) (app.TaskShare, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return app.TaskShare{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var row app.TaskShare
	err = tx.QueryRow(ctx, `
		SELECT owner_user_id::text, visibility, COALESCE(visibility_team_id::text, ''),
		       identity.may('edit', object_type, owner_user_id, visibility, visibility_team_id, id)
		FROM tasks.tasks
		WHERE id = $1::uuid`, taskID).Scan(&row.OwnerID, &row.Visibility, &row.TeamID, &row.Editable)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.TaskShare{}, app.ErrGone
	}
	if err != nil {
		return app.TaskShare{}, fmt.Errorf("tasks: share row: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return app.TaskShare{}, err
	}
	return row, nil
}

// SaveVisibility stores one task's visibility and its team under row security.
// Only the owner's UPDATE reaches a row, so a missing one is ErrAccess.
func (s Store) SaveVisibility(ctx context.Context, access app.Access, taskID, visibility, teamID string) error {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		UPDATE tasks.tasks
		SET visibility = $2, visibility_team_id = NULLIF($3, '')::uuid, updated_at = now()
		WHERE id = $1::uuid`, taskID, visibility, teamID)
	if err != nil {
		return fmt.Errorf("tasks: save visibility: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return app.ErrAccess
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return nil
}
