package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
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

// SaveShare replaces the grants and stores visibility in one transaction,
// then seals task.share with the grants before and after.
func (s Store) SaveShare(ctx context.Context, access app.Access, taskID, visibility, teamID string, grants []domain.ShareGrant, grantedBy string) error {
	if s.ReplaceTaskGrants == nil {
		return fmt.Errorf("tasks: save share: grants not wired")
	}
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	grantsBefore, err := readShareGrants(ctx, tx, taskID)
	if err != nil {
		return err
	}
	if err := s.ReplaceTaskGrants(ctx, tx, taskID, grants, grantedBy); err != nil {
		return err
	}
	priorVisibility, priorTeam, err := updateShareVisibility(ctx, tx, taskID, visibility, teamID)
	if err != nil {
		return err
	}
	if err := sealTaskShare(ctx, tx, taskID, priorVisibility, priorTeam, visibility, teamID, grantsBefore, grants); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SaveVisibility stores one task's visibility and its team under row security,
// and seals the share's audit event: the grants as they were and as they now
// stand, and the visibility when it changed. Only the owner's UPDATE reaches a
// row, so a missing one is ErrAccess.
func (s Store) SaveVisibility(ctx context.Context, access app.Access, taskID, visibility, teamID string, grantsBefore, grantsAfter []domain.ShareGrant) error {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	priorVisibility, priorTeam, err := updateShareVisibility(ctx, tx, taskID, visibility, teamID)
	if err != nil {
		return err
	}
	if err := sealTaskShare(ctx, tx, taskID, priorVisibility, priorTeam, visibility, teamID, grantsBefore, grantsAfter); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func readShareGrants(ctx context.Context, tx pgx.Tx, taskID string) ([]domain.ShareGrant, error) {
	rows, err := tx.Query(ctx, `
		SELECT CASE WHEN share.user_id IS NOT NULL THEN 'person' ELSE 'team' END,
		       COALESCE(share.user_id::text, share.team_id::text),
		       share.level
		FROM identity.shares AS share
		WHERE share.object_type = 'task'
		  AND share.object_id = $1::uuid
		  AND share.grantee_role_id IS NULL
		ORDER BY share.id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("tasks: read share grants: %w", err)
	}
	defer rows.Close()
	var out []domain.ShareGrant
	for rows.Next() {
		var grant domain.ShareGrant
		if err := rows.Scan(&grant.SubjectType, &grant.SubjectID, &grant.Role); err != nil {
			return nil, err
		}
		out = append(out, grant)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func updateShareVisibility(ctx context.Context, tx pgx.Tx, taskID, visibility, teamID string) (priorVisibility, priorTeam string, err error) {
	err = tx.QueryRow(ctx, `
		SELECT visibility, COALESCE(visibility_team_id::text, '')
		FROM tasks.tasks
		WHERE id = $1::uuid`, taskID).Scan(&priorVisibility, &priorTeam)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", app.ErrAccess
	}
	if err != nil {
		return "", "", fmt.Errorf("tasks: save visibility: %w", err)
	}

	tag, err := tx.Exec(ctx, `
		UPDATE tasks.tasks
		SET visibility = $2, visibility_team_id = NULLIF($3, '')::uuid, updated_at = now()
		WHERE id = $1::uuid`, taskID, visibility, teamID)
	if err != nil {
		return "", "", fmt.Errorf("tasks: save visibility: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return "", "", app.ErrAccess
	}
	return priorVisibility, priorTeam, nil
}

func sealTaskShare(ctx context.Context, tx pgx.Tx, taskID, priorVisibility, priorTeam, visibility, teamID string, grantsBefore, grantsAfter []domain.ShareGrant) error {
	changes := audit.Changes{
		Fields: []string{"grants"},
		Values: map[string]audit.Change{"grants": {From: shareGrants(grantsBefore), To: shareGrants(grantsAfter)}},
	}
	if priorVisibility != visibility || priorTeam != teamID {
		changes.Fields = append(changes.Fields, "visibility")
		changes.Values["visibility"] = audit.Change{From: priorVisibility, To: visibility}
	}
	return audit.Record(ctx, tx, audit.Event{
		Action:   audit.TaskShare,
		Outcome:  audit.Success,
		TargetID: taskID,
		Changes:  changes,
	})
}

// shareGrants lists a share's grants as subject type, subject id and level.
func shareGrants(grants []domain.ShareGrant) []map[string]string {
	out := make([]map[string]string, 0, len(grants))
	for _, grant := range grants {
		out = append(out, map[string]string{
			"subject_type": grant.SubjectType,
			"subject_id":   grant.SubjectID,
			"level":        grant.Role,
		})
	}
	return out
}
