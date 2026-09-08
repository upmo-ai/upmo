package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/memory/app"
)

type lessonBook struct {
	tx pgx.Tx
}

var _ app.Playbook = Facts{}
var _ app.Book = lessonBook{}

func (f Facts) InAdmin(ctx context.Context, fn func(app.Book) error) error {
	return f.inLessonTx(ctx, "admin", "", fn)
}

func (f Facts) InAgent(ctx context.Context, agentID string, fn func(app.Book) error) error {
	return f.inLessonTx(ctx, "", agentID, fn)
}

func (f Facts) inLessonTx(
	ctx context.Context,
	access, agentID string,
	fn func(app.Book) error,
) error {
	if f.Pool == nil {
		return fmt.Errorf("memory: no database")
	}
	tx, err := f.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`SELECT set_config('upmo.lesson_access', $1, true)`, access,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`SELECT set_config('upmo.agent_id', $1, true)`, agentID,
	); err != nil {
		return err
	}
	if err := fn(lessonBook{tx: tx}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (b lessonBook) Count(ctx context.Context, agentID string) (int, error) {
	if _, err := b.tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0::bigint))`,
		agentID,
	); err != nil {
		return 0, fmt.Errorf("memory: lesson count: %w", err)
	}
	var count int
	err := b.tx.QueryRow(ctx,
		`SELECT count(*) FROM memory.lessons WHERE agent_id = $1::uuid`,
		agentID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("memory: lesson count: %w", err)
	}
	return count, nil
}

func (b lessonBook) List(ctx context.Context, agentID string) ([]app.Card, error) {
	rows, err := b.tx.Query(ctx, `
		SELECT id::text, title, cue
		FROM memory.lessons
		WHERE agent_id = $1::uuid
		ORDER BY title, id
		LIMIT 24`, agentID)
	if err != nil {
		return nil, fmt.Errorf("memory: lesson list: %w", err)
	}
	defer rows.Close()
	var cards []app.Card
	for rows.Next() {
		var card app.Card
		if err := rows.Scan(&card.ID, &card.Title, &card.Cue); err != nil {
			return nil, fmt.Errorf("memory: lesson list: %w", err)
		}
		cards = append(cards, card)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memory: lesson list: %w", err)
	}
	return cards, nil
}

func (b lessonBook) One(ctx context.Context, agentID, lessonID string) (app.Lesson, error) {
	var lesson app.Lesson
	err := b.tx.QueryRow(ctx, `
		SELECT id::text, title, cue, body
		FROM memory.lessons
		WHERE agent_id = $1::uuid AND id = $2::uuid`,
		agentID, lessonID,
	).Scan(&lesson.ID, &lesson.Title, &lesson.Cue, &lesson.Body)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Lesson{}, app.ErrLessonGone
	}
	if err != nil {
		return app.Lesson{}, fmt.Errorf("memory: lesson open: %w", err)
	}
	return lesson, nil
}

func (b lessonBook) Put(
	ctx context.Context,
	agentID, title, cue, body string,
	at time.Time,
) (string, error) {
	var id string
	err := b.tx.QueryRow(ctx, `
		INSERT INTO memory.lessons (agent_id, title, cue, body, updated_at)
		VALUES ($1::uuid, $2, $3, $4, $5)
		RETURNING id::text`,
		agentID, title, cue, body, at,
	).Scan(&id)
	if dup(err) {
		return "", app.ErrLessonDuplicate
	}
	if err != nil {
		return "", fmt.Errorf("memory: lesson put: %w", err)
	}
	return id, nil
}

func (b lessonBook) Drop(ctx context.Context, agentID, lessonID string) error {
	tag, err := b.tx.Exec(ctx, `
		DELETE FROM memory.lessons
		WHERE agent_id = $1::uuid AND id = $2::uuid`,
		agentID, lessonID)
	if err != nil {
		return fmt.Errorf("memory: lesson drop: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return app.ErrLessonGone
	}
	return nil
}
