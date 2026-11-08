package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/chat/domain"
	"github.com/upmo-ai/upmo/internal/memory"
	"github.com/upmo-ai/upmo/internal/platform/jobs"
	"github.com/upmo-ai/upmo/internal/tasks"
)

// Store is the Postgres writer for chats.
type Store struct {
	Pool     *pgxpool.Pool
	Enqueuer jobs.Enqueuer
}

// Open inserts one private chat. It does not touch a login session or the profile.
func (s Store) Open(ctx context.Context, access app.Access) (app.ChatSummary, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return app.ChatSummary{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id string
	var createdAt time.Time
	err = tx.QueryRow(ctx, `
		INSERT INTO chat.chats (owner_user_id, visibility)
		VALUES ($1::uuid, 'private')
		RETURNING id::text, created_at`, access.UserID).Scan(&id, &createdAt)
	if err != nil {
		return app.ChatSummary{}, fmt.Errorf("chat: open: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return app.ChatSummary{}, err
	}
	return app.ChatSummary{ID: id, Title: domain.NewChatTitle, LastActivity: createdAt.UTC()}, nil
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
		return goneOrSay(ctx, tx, chatID)
	}
	if err != nil {
		return fmt.Errorf("chat: lock: %w", err)
	}
	var seq int
	err = tx.QueryRow(ctx, `
		INSERT INTO chat.lines (chat_id, seq, body)
		SELECT $1::uuid,
		       COALESCE((SELECT MAX(seq) FROM chat.lines WHERE chat_id = $1::uuid), 0) + 1,
		       $2
		RETURNING seq`, chatID, body).Scan(&seq)
	if err != nil {
		return writeErr(err)
	}
	if s.Enqueuer != nil && access.UserID != "" {
		if _, err := s.Enqueuer.InsertTx(ctx, tx, tasks.ScoutJobArgs{
			UserID:   access.UserID,
			Trigger:  "chat",
			SourceID: chatID,
		}, nil); err != nil {
			return fmt.Errorf("chat: scout enqueue: %w", err)
		}
		if _, err := s.Enqueuer.InsertTx(ctx, tx, memory.ExtractJobArgs{
			UserID:     access.UserID,
			SourceKind: "chat",
			ID:         chatID,
			Seq:        seq,
			Text:       body,
			AtUnix:     time.Now().UTC().Unix(),
		}, nil); err != nil {
			return fmt.Errorf("chat: extract enqueue: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// goneOrSay splits a locked miss in Say. The lock asks for edit rights too, so
// a chat this person can only view hides inside it: one unlocked look tells a
// read-only chat from one that is gone.
func goneOrSay(ctx context.Context, tx pgx.Tx, chatID string) error {
	var one int
	err := tx.QueryRow(ctx, `SELECT 1 FROM chat.chats WHERE id = $1::uuid`, chatID).Scan(&one)
	switch {
	case err == nil:
		return app.ErrSay
	case errors.Is(err, pgx.ErrNoRows):
		return app.ErrGone
	default:
		return fmt.Errorf("chat: view: %w", err)
	}
}

// List returns the chats this person can view under row security, newest
// activity first, at most 100.
func (s Store) List(ctx context.Context, access app.Access) ([]app.ChatSummary, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT chat.id::text,
		       COALESCE((
		         SELECT body FROM chat.lines AS line
		         WHERE line.chat_id = chat.id AND line.role = 'user'
		         ORDER BY line.seq
		         LIMIT 1
		       ), '') AS first_user_line,
		       COALESCE((
		         SELECT body FROM chat.lines AS line
		         WHERE line.chat_id = chat.id
		         ORDER BY line.seq DESC
		         LIMIT 1
		       ), '') AS newest_line,
		       COALESCE((
		         SELECT line.at FROM chat.lines AS line
		         WHERE line.chat_id = chat.id
		         ORDER BY line.seq DESC
		         LIMIT 1
		       ), chat.created_at) AS last_activity
		FROM chat.chats AS chat
		ORDER BY last_activity DESC, chat.id DESC
		LIMIT 100`)
	if err != nil {
		return nil, fmt.Errorf("chat: list: %w", err)
	}
	defer rows.Close()

	out := []app.ChatSummary{}
	for rows.Next() {
		var summary app.ChatSummary
		var firstUser, newest string
		if err := rows.Scan(&summary.ID, &firstUser, &newest, &summary.LastActivity); err != nil {
			return nil, fmt.Errorf("chat: list: %w", err)
		}
		summary.Title = domain.Title(firstUser)
		summary.Preview = domain.Preview(newest)
		summary.LastActivity = summary.LastActivity.UTC()
		out = append(out, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("chat: list: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
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
		SELECT id::text, role, body, model, tokens, cost_micro_usd, at
		FROM chat.lines
		WHERE chat_id = $1::uuid
		ORDER BY seq`, chatID)
	if err != nil {
		return app.Chat{}, fmt.Errorf("chat: lines: %w", err)
	}
	defer rows.Close()
	lines := make([]app.Line, 0)
	for rows.Next() {
		var line app.Line
		if err := rows.Scan(&line.ID, &line.Role, &line.Body, &line.Model, &line.Tokens, &line.CostMicroUSD, &line.At); err != nil {
			return app.Chat{}, fmt.Errorf("chat: line: %w", err)
		}
		lines = append(lines, line)
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
