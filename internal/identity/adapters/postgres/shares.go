package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
)

// TaskGrants returns the person and team grants of one task with the names of
// their subjects, in the order they were made. Role grants are not listed.
func (p Passkeys) TaskGrants(ctx context.Context, taskID string) ([]app.TaskGrant, error) {
	return p.objectGrants(ctx, "task", taskID)
}

// ChatGrants returns the person and team grants of one chat with the names of
// their subjects, in the order they were made. Role grants are not listed.
func (p Passkeys) ChatGrants(ctx context.Context, chatID string) ([]app.TaskGrant, error) {
	return p.objectGrants(ctx, "chat", chatID)
}

func (p Passkeys) objectGrants(ctx context.Context, objectType, objectID string) ([]app.TaskGrant, error) {
	rows, err := p.Pool.Query(ctx, `
		SELECT CASE WHEN share.user_id IS NOT NULL THEN 'person' ELSE 'team' END,
		       COALESCE(share.user_id::text, share.team_id::text),
		       COALESCE(person.name, team.name, ''),
		       share.level
		FROM identity.shares AS share
		LEFT JOIN identity.users AS person ON person.id = share.user_id
		LEFT JOIN identity.teams AS team ON team.id = share.team_id
		WHERE share.object_type = $1
		  AND share.object_id = $2::uuid
		  AND share.grantee_role_id IS NULL
		ORDER BY share.id`, objectType, objectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []app.TaskGrant
	for rows.Next() {
		var grant app.TaskGrant
		if err := rows.Scan(&grant.SubjectType, &grant.SubjectID, &grant.Name, &grant.Level); err != nil {
			return nil, err
		}
		out = append(out, grant)
	}
	return out, rows.Err()
}

// ReplaceTaskGrants replaces the person and team grants of one task in one
// transaction: grants not listed are deleted, new ones are inserted with the
// grantor, and the level changes where it differs. Role grants are untouched.
func (p Passkeys) ReplaceTaskGrants(ctx context.Context, taskID string, grants []app.GrantWrite, grantedBy domain.UserID) error {
	return p.replaceObjectGrantsInTx(ctx, "task", taskID, grants, grantedBy)
}

// ReplaceTaskGrantsTx replaces the person and team grants inside a transaction
// the caller already opened, for example when tasks seals task.share in the
// same commit as the visibility write.
func (p Passkeys) ReplaceTaskGrantsTx(ctx context.Context, tx pgx.Tx, taskID string, grants []app.GrantWrite, grantedBy domain.UserID) error {
	return p.replaceObjectGrants(ctx, tx, "task", taskID, grants, grantedBy)
}

// ReplaceChatGrants replaces the person and team grants of one chat in one
// transaction: grants not listed are deleted, new ones are inserted with the
// grantor, and the level changes where it differs. Role grants are untouched.
func (p Passkeys) ReplaceChatGrants(ctx context.Context, chatID string, grants []app.GrantWrite, grantedBy domain.UserID) error {
	return p.replaceObjectGrantsInTx(ctx, "chat", chatID, grants, grantedBy)
}

// ReplaceChatGrantsTx replaces the person and team grants of one chat inside a
// transaction the caller already opened.
func (p Passkeys) ReplaceChatGrantsTx(ctx context.Context, tx pgx.Tx, chatID string, grants []app.GrantWrite, grantedBy domain.UserID) error {
	return p.replaceObjectGrants(ctx, tx, "chat", chatID, grants, grantedBy)
}

func (p Passkeys) replaceObjectGrantsInTx(ctx context.Context, objectType, objectID string, grants []app.GrantWrite, grantedBy domain.UserID) error {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := p.replaceObjectGrants(ctx, tx, objectType, objectID, grants, grantedBy); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p Passkeys) replaceObjectGrants(ctx context.Context, tx pgx.Tx, objectType, objectID string, grants []app.GrantWrite, grantedBy domain.UserID) error {
	// An object with no grants has no rows to lock, so two replacements would
	// both insert and the loser would fail the unique index: one advisory
	// lock per object makes the second wait and then converge.
	if _, err := tx.Exec(ctx, `
		SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0::bigint))`, objectID); err != nil {
		return err
	}

	// shareRow is one stored grant; user and team say which kind it is.
	type shareRow struct {
		id, user, team, level string
	}
	rows, err := tx.Query(ctx, `
		SELECT id::text, COALESCE(user_id::text, ''), COALESCE(team_id::text, ''), level
		FROM identity.shares
		WHERE object_type = $1 AND object_id = $2::uuid AND grantee_role_id IS NULL
		ORDER BY id
		FOR UPDATE`, objectType, objectID)
	if err != nil {
		return err
	}
	stored := map[string]shareRow{}
	for rows.Next() {
		var row shareRow
		if err := rows.Scan(&row.id, &row.user, &row.team, &row.level); err != nil {
			rows.Close()
			return err
		}
		if row.user != "" {
			stored["person:"+row.user] = row
		} else {
			stored["team:"+row.team] = row
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	wanted := make(map[string]bool, len(grants))
	for _, grant := range grants {
		wanted[grant.SubjectType+":"+grant.SubjectID] = true
	}
	for key, row := range stored {
		if wanted[key] {
			continue
		}
		if _, err := tx.Exec(ctx, `DELETE FROM identity.shares WHERE id = $1::uuid`, row.id); err != nil {
			return err
		}
	}
	for _, grant := range grants {
		row, ok := stored[grant.SubjectType+":"+grant.SubjectID]
		if ok {
			if row.level == grant.Level {
				continue
			}
			if _, err := tx.Exec(ctx, `
				UPDATE identity.shares SET level = $2 WHERE id = $1::uuid`, row.id, grant.Level); err != nil {
				return err
			}
			continue
		}
		var err error
		if grant.SubjectType == "person" {
			_, err = tx.Exec(ctx, `
				INSERT INTO identity.shares (object_type, object_id, user_id, level, granted_by)
				VALUES ($1, $2::uuid, $3::uuid, $4, $5)`, objectType, objectID, grant.SubjectID, grant.Level, grantedBy)
		} else {
			_, err = tx.Exec(ctx, `
				INSERT INTO identity.shares (object_type, object_id, team_id, level, granted_by)
				VALUES ($1, $2::uuid, $3::uuid, $4, $5)`, objectType, objectID, grant.SubjectID, grant.Level, grantedBy)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// NamesOf returns the display names of the enabled people in persons and the
// names of the teams in teams. An id that is no enabled member, or no team, is
// missing from the answer.
func (p Passkeys) NamesOf(ctx context.Context, persons, teams []string) (map[string]string, map[string]string, error) {
	people, err := p.names(ctx, `
		SELECT id::text, name FROM identity.users
		WHERE disabled_at IS NULL AND id = ANY($1::uuid[])`, persons)
	if err != nil {
		return nil, nil, err
	}
	teamNames, err := p.names(ctx, `
		SELECT id::text, name FROM identity.teams
		WHERE id = ANY($1::uuid[])`, teams)
	if err != nil {
		return nil, nil, err
	}
	return people, teamNames, nil
}

// names runs one id-and-name query.
func (p Passkeys) names(ctx context.Context, query string, ids []string) (map[string]string, error) {
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := p.Pool.Query(ctx, query, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}
