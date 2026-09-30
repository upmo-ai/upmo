package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/chat/domain"
)

// Store is the Postgres writer for chats.
type Store struct {
	Pool *pgxpool.Pool
}

// Open inserts one private chat. It does not touch a login session or the profile.
func (s Store) Open(ctx context.Context, access app.Access) (string, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO chat.chats (owner_user_id, visibility)
		VALUES ($1::uuid, 'private')
		RETURNING id::text`, access.UserID).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("chat: open: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return id, nil
}

// Say appends one line.
func (s Store) Say(ctx context.Context, access app.Access, chatID, body string) error {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var one int
	err = tx.QueryRow(ctx, `SELECT 1 FROM chat.chats WHERE id = $1::uuid FOR UPDATE`, chatID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrGone
	}
	if err != nil {
		return fmt.Errorf("chat: lock: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO chat.lines (chat_id, seq, body)
		SELECT $1::uuid,
		       COALESCE((SELECT MAX(seq) FROM chat.lines WHERE chat_id = $1::uuid), 0) + 1,
		       $2`, chatID, body)
	if err != nil {
		return writeErr(err)
	}
	return tx.Commit(ctx)
}

// Read returns the lines in order.
func (s Store) Read(ctx context.Context, access app.Access, chatID string) (app.Chat, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return app.Chat{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var one int
	err = tx.QueryRow(ctx, `SELECT 1 FROM chat.chats WHERE id = $1::uuid`, chatID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Chat{}, app.ErrGone
	}
	if err != nil {
		return app.Chat{}, fmt.Errorf("chat: read: %w", err)
	}
	rows, err := tx.Query(ctx, `
		SELECT body
		FROM chat.lines
		WHERE chat_id = $1::uuid
		ORDER BY seq`, chatID)
	if err != nil {
		return app.Chat{}, fmt.Errorf("chat: lines: %w", err)
	}
	defer rows.Close()
	lines := make([]string, 0)
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return app.Chat{}, fmt.Errorf("chat: line: %w", err)
		}
		lines = append(lines, body)
	}
	if err := rows.Err(); err != nil {
		return app.Chat{}, fmt.Errorf("chat: lines: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return app.Chat{}, err
	}
	return app.Chat{ID: chatID, Lines: lines}, nil
}

func (s Store) begin(ctx context.Context, access app.Access) (pgx.Tx, error) {
	if s.Pool == nil {
		return nil, fmt.Errorf("chat: no database")
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

func writeErr(err error) error {
	if strings.Contains(err.Error(), "chat: writer") {
		return domain.ErrWriter
	}
	return fmt.Errorf("chat: line: %w", err)
}
