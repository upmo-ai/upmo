package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/chat/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
)

// ShareRow returns the chat row behind a share under row security, with
// identity's answer to whether this person may edit the chat and whether
// earlier turns read private data. A chat this person cannot see is ErrGone.
func (s Store) ShareRow(ctx context.Context, access app.Access, chatID string) (app.ChatShare, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return app.ChatShare{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var row app.ChatShare
	err = tx.QueryRow(ctx, `
		SELECT owner_user_id::text, visibility, COALESCE(visibility_team_id::text, ''),
		       identity.may('edit', object_type, owner_user_id, visibility, visibility_team_id, id),
		       private_read
		FROM chat.chats
		WHERE id = $1::uuid`, chatID).Scan(&row.OwnerID, &row.Visibility, &row.TeamID, &row.Editable, &row.PrivateRead)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ChatShare{}, app.ErrGone
	}
	if err != nil {
		return app.ChatShare{}, fmt.Errorf("chat: share row: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return app.ChatShare{}, err
	}
	return row, nil
}

// SaveShare locks the chat row, fails stale runs, refuses with ErrBusy while a
// reply run is running or waiting, replaces identity.shares rows, updates
// visibility, and seals chat.share in one transaction.
func (s Store) SaveShare(ctx context.Context, access app.Access, chatID, visibility, teamID string, grants []domain.ShareGrant, grantedBy string) error {
	if s.ReplaceChatGrants == nil {
		return fmt.Errorf("chat: save share: grants not wired")
	}
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var ownerID, priorVisibility, priorTeam string
	err = tx.QueryRow(ctx, `
		SELECT owner_user_id::text, visibility, COALESCE(visibility_team_id::text, '')
		FROM chat.chats
		WHERE id = $1::uuid
		FOR NO KEY UPDATE`, chatID).Scan(&ownerID, &priorVisibility, &priorTeam)
	if errors.Is(err, pgx.ErrNoRows) {
		err = goneOrSay(ctx, tx, chatID)
		if errors.Is(err, app.ErrSay) {
			return app.ErrAccess
		}
		return err
	}
	if err != nil {
		return fmt.Errorf("chat: lock share: %w", err)
	}
	if ownerID != access.UserID {
		return app.ErrAccess
	}

	if err := failStaleRuns(ctx, tx, chatID); err != nil {
		return err
	}
	var busy bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM chat.runs
			WHERE chat_id = $1::uuid
			  AND state IN ('running', 'waiting')
		)`, chatID).Scan(&busy); err != nil {
		return fmt.Errorf("chat: share active run: %w", err)
	}
	if busy {
		return app.ErrBusy
	}

	grantsBefore, err := readChatShareGrants(ctx, tx, chatID)
	if err != nil {
		return err
	}
	if err := s.ReplaceChatGrants(ctx, tx, chatID, grants, grantedBy); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE chat.chats
		SET visibility = $2, visibility_team_id = NULLIF($3, '')::uuid
		WHERE id = $1::uuid`, chatID, visibility, teamID)
	if err != nil {
		if strings.Contains(err.Error(), "chat: owner") {
			return app.ErrAccess
		}
		return fmt.Errorf("chat: save visibility: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return app.ErrAccess
	}
	if err := sealChatShare(ctx, tx, chatID, priorVisibility, priorTeam, visibility, teamID, grantsBefore, grants); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func readChatShareGrants(ctx context.Context, tx pgx.Tx, chatID string) ([]domain.ShareGrant, error) {
	rows, err := tx.Query(ctx, `
		SELECT CASE WHEN share.user_id IS NOT NULL THEN 'person' ELSE 'team' END,
		       COALESCE(share.user_id::text, share.team_id::text),
		       share.level
		FROM identity.shares AS share
		WHERE share.object_type = 'chat'
		  AND share.object_id = $1::uuid
		  AND share.grantee_role_id IS NULL
		ORDER BY share.id`, chatID)
	if err != nil {
		return nil, fmt.Errorf("chat: read share grants: %w", err)
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

func sealChatShare(ctx context.Context, tx pgx.Tx, chatID, priorVisibility, priorTeam, visibility, teamID string, grantsBefore, grantsAfter []domain.ShareGrant) error {
	changes := audit.Changes{
		Fields: []string{"grants"},
		Values: map[string]audit.Change{"grants": {From: chatShareGrants(grantsBefore), To: chatShareGrants(grantsAfter)}},
	}
	if priorVisibility != visibility || priorTeam != teamID {
		changes.Fields = append(changes.Fields, "visibility")
		changes.Values["visibility"] = audit.Change{From: priorVisibility, To: visibility}
	}
	return audit.Record(ctx, tx, audit.Event{
		Action:   audit.ChatShare,
		Outcome:  audit.Success,
		TargetID: chatID,
		Changes:  changes,
	})
}

func chatShareGrants(grants []domain.ShareGrant) []map[string]string {
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
