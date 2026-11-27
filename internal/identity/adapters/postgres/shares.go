package postgres

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/identity/app"
)

// TaskGrants returns the person and team grants of one task with the names of
// their subjects, in the order they were made, through identity.object_grants
// for the actor behind actorTokenHash. Role grants are not listed.
func (p Passkeys) TaskGrants(ctx context.Context, actorTokenHash []byte, taskID string) ([]app.TaskGrant, error) {
	return p.objectGrants(ctx, actorTokenHash, "task", taskID)
}

// ChatGrants returns the person and team grants of one chat with the names of
// their subjects, in the order they were made, through identity.object_grants
// for the actor behind actorTokenHash. Role grants are not listed.
func (p Passkeys) ChatGrants(ctx context.Context, actorTokenHash []byte, chatID string) ([]app.TaskGrant, error) {
	return p.objectGrants(ctx, actorTokenHash, "chat", chatID)
}

func (p Passkeys) objectGrants(ctx context.Context, actorTokenHash []byte, objectType, objectID string) ([]app.TaskGrant, error) {
	rows, err := p.Pool.Query(ctx, `SELECT * FROM identity.object_grants($1, $2, $3::uuid)`, actorTokenHash, objectType, objectID)
	if err != nil {
		return nil, mapActorDefinerError(err)
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
	if err := rows.Err(); err != nil {
		return nil, mapActorDefinerError(err)
	}
	return out, nil
}

// ReplaceTaskGrants replaces the person and team grants of one task in one
// transaction: grants not listed are deleted, new ones are inserted with the
// actor behind actorTokenHash as grantor, and the level changes where it
// differs. Role grants are untouched.
func (p Passkeys) ReplaceTaskGrants(ctx context.Context, actorTokenHash []byte, taskID string, grants []app.GrantWrite) error {
	return p.replaceObjectGrantsInTx(ctx, actorTokenHash, "task", taskID, grants)
}

// ReplaceTaskGrantsTx replaces the person and team grants inside a transaction
// the caller already opened, for example when tasks seals task.share in the
// same commit as the visibility write.
func (p Passkeys) ReplaceTaskGrantsTx(ctx context.Context, actorTokenHash []byte, tx pgx.Tx, taskID string, grants []app.GrantWrite) error {
	return p.replaceObjectGrants(ctx, tx, actorTokenHash, "task", taskID, grants)
}

// ReplaceChatGrants replaces the person and team grants of one chat in one
// transaction: grants not listed are deleted, new ones are inserted with the
// actor behind actorTokenHash as grantor, and the level changes where it
// differs. Role grants are untouched.
func (p Passkeys) ReplaceChatGrants(ctx context.Context, actorTokenHash []byte, chatID string, grants []app.GrantWrite) error {
	return p.replaceObjectGrantsInTx(ctx, actorTokenHash, "chat", chatID, grants)
}

// ReplaceChatGrantsTx replaces the person and team grants of one chat inside a
// transaction the caller already opened.
func (p Passkeys) ReplaceChatGrantsTx(ctx context.Context, actorTokenHash []byte, tx pgx.Tx, chatID string, grants []app.GrantWrite) error {
	return p.replaceObjectGrants(ctx, tx, actorTokenHash, "chat", chatID, grants)
}

func (p Passkeys) replaceObjectGrantsInTx(ctx context.Context, actorTokenHash []byte, objectType, objectID string, grants []app.GrantWrite) error {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := p.replaceObjectGrants(ctx, tx, actorTokenHash, objectType, objectID, grants); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// grantJSON is one grant in the shape identity.replace_object_grants reads.
type grantJSON struct {
	SubjectType string `json:"subject_type"`
	SubjectID   string `json:"subject_id"`
	Level       string `json:"level"`
}

// replaceObjectGrants runs identity.replace_object_grants on tx, which takes
// the per-object lock and stores the actor as grantor.
func (p Passkeys) replaceObjectGrants(ctx context.Context, tx pgx.Tx, actorTokenHash []byte, objectType, objectID string, grants []app.GrantWrite) error {
	list := make([]grantJSON, 0, len(grants))
	for _, grant := range grants {
		list = append(list, grantJSON(grant))
	}
	body, err := json.Marshal(list)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT * FROM identity.replace_object_grants($1, $2, $3::uuid, $4::jsonb)`,
		actorTokenHash, objectType, objectID, string(body)); err != nil {
		return mapActorDefinerError(err)
	}
	return nil
}

// NamesOf returns the display names of the enabled people in persons and the
// names of the teams in teams, through identity.names_of for the actor behind
// actorTokenHash. An id that is no enabled member, or no team, is missing from
// the answer. A hash with no live joined session is app.ErrNotSignedIn.
func (p Passkeys) NamesOf(ctx context.Context, actorTokenHash []byte, persons, teams []string) (map[string]string, map[string]string, error) {
	rows, err := p.Pool.Query(ctx, `SELECT * FROM identity.names_of($1, $2::uuid[], $3::uuid[])`, actorTokenHash, persons, teams)
	if err != nil {
		return nil, nil, mapActorDefinerError(err)
	}
	defer rows.Close()
	people, teamNames := map[string]string{}, map[string]string{}
	for rows.Next() {
		var kind, id, name string
		if err := rows.Scan(&kind, &id, &name); err != nil {
			return nil, nil, err
		}
		if kind == "person" {
			people[id] = name
		} else {
			teamNames[id] = name
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, mapActorDefinerError(err)
	}
	return people, teamNames, nil
}
