package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/upmo-ai/upmo/internal/agents/app"
	"github.com/upmo-ai/upmo/internal/agents/domain"
)

type agentReader struct {
	tx pgx.Tx
}

func (s Store) InAgent(ctx context.Context, agentID string, fn func(app.AgentReader) error) error {
	if s.Pool == nil {
		return fmt.Errorf("agents: no database")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		SELECT set_config('upmo.lesson_access', '', true),
		       set_config('upmo.agent_id', $1, true)`, agentID); err != nil {
		return err
	}
	if err := fn(agentReader{tx: tx}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r agentReader) Inline(ctx context.Context, agentID string) ([]domain.Lesson, error) {
	return listInlineLessonsTx(ctx, r.tx, agentID)
}

func (r agentReader) Index(ctx context.Context, agentID string) ([]domain.Card, error) {
	return listLessonCardsTx(ctx, r.tx, agentID)
}

func (r agentReader) Open(ctx context.Context, agentID, lessonID string) (domain.Lesson, error) {
	return openLessonTx(ctx, r.tx, agentID, lessonID)
}

func (s Store) InlineLessons(ctx context.Context, access app.Access, agentID string) ([]domain.Lesson, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := getAgent(ctx, tx, agentID, false); err != nil {
		return nil, err
	}
	out, err := listInlineLessonsTx(ctx, tx, agentID)
	if err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}

func (s Store) ListLessonCards(ctx context.Context, access app.Access, agentID string) ([]domain.Card, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := getAgent(ctx, tx, agentID, false); err != nil {
		return nil, err
	}
	cards, err := listLessonCardsTx(ctx, tx, agentID)
	if err != nil {
		return nil, err
	}
	return cards, tx.Commit(ctx)
}

func (s Store) OpenLesson(ctx context.Context, access app.Access, agentID, lessonID string) (domain.Lesson, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return domain.Lesson{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := getAgent(ctx, tx, agentID, false); err != nil {
		return domain.Lesson{}, err
	}
	lesson, err := openLessonTx(ctx, tx, agentID, lessonID)
	if err != nil {
		return domain.Lesson{}, err
	}
	return lesson, tx.Commit(ctx)
}

func (s Store) SaveLesson(ctx context.Context, access app.Access, agentID string, in app.LessonFields, at time.Time) (string, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	a, err := getAgent(ctx, tx, agentID, false)
	if err != nil {
		return "", err
	}
	if !a.Share.CanManage {
		return "", app.ErrAgentManaged
	}
	if !in.AlwaysInline {
		n, err := countIndexLessonsTx(ctx, tx, agentID)
		if err != nil {
			return "", err
		}
		if n >= domain.LessonIndexCap {
			return "", domain.ErrLessonFull
		}
	}
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO agents.lessons (agent_id, title, cue, body, always_inline, updated_at)
		VALUES ($1::uuid, $2, $3, $4, $5, $6)
		RETURNING id::text`,
		agentID, in.Title, in.Cue, in.Body, in.AlwaysInline, at.UTC()).Scan(&id)
	if err != nil {
		return "", lessonWriteErr(err)
	}
	if in.AlwaysInline {
		if err := bumpAgentVersionTx(ctx, tx, access.UserID, agentID); err != nil {
			return "", err
		}
	}
	return id, tx.Commit(ctx)
}

func (s Store) UpdateLesson(ctx context.Context, access app.Access, agentID, lessonID string, in app.LessonFields, at time.Time) error {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	a, err := getAgent(ctx, tx, agentID, false)
	if err != nil {
		return err
	}
	if !a.Share.CanManage {
		return app.ErrAgentManaged
	}
	current, err := openLessonTx(ctx, tx, agentID, lessonID)
	if err != nil {
		return err
	}
	if current.Title == in.Title && current.Cue == in.Cue && current.Body == in.Body && current.AlwaysInline == in.AlwaysInline {
		return tx.Commit(ctx)
	}
	if current.AlwaysInline && !in.AlwaysInline {
		n, err := countIndexLessonsTx(ctx, tx, agentID)
		if err != nil {
			return err
		}
		if n >= domain.LessonIndexCap {
			return domain.ErrLessonFull
		}
	}
	tag, err := tx.Exec(ctx, `
		UPDATE agents.lessons
		SET title = $3, cue = $4, body = $5, always_inline = $6, updated_at = $7
		WHERE agent_id = $1::uuid AND id = $2::uuid`,
		agentID, lessonID, in.Title, in.Cue, in.Body, in.AlwaysInline, at.UTC())
	if err != nil {
		return lessonWriteErr(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrLessonGone
	}
	if current.AlwaysInline || in.AlwaysInline {
		if err := bumpAgentVersionTx(ctx, tx, access.UserID, agentID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s Store) RemoveLesson(ctx context.Context, access app.Access, agentID, lessonID string, _ time.Time) error {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	a, err := getAgent(ctx, tx, agentID, false)
	if err != nil {
		return err
	}
	if !a.Share.CanManage {
		return app.ErrAgentManaged
	}
	var wasInline bool
	err = tx.QueryRow(ctx, `
		DELETE FROM agents.lessons
		WHERE agent_id = $1::uuid AND id = $2::uuid
		RETURNING always_inline`, agentID, lessonID).Scan(&wasInline)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrLessonGone
	}
	if err != nil {
		return lessonWriteErr(err)
	}
	if wasInline {
		if err := bumpAgentVersionTx(ctx, tx, access.UserID, agentID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func countIndexLessonsTx(ctx context.Context, tx pgx.Tx, agentID string) (int, error) {
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0::bigint))`,
		agentID,
	); err != nil {
		return 0, fmt.Errorf("agents: lesson count: %w", err)
	}
	var count int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM agents.lessons WHERE agent_id = $1::uuid AND NOT always_inline`,
		agentID,
	).Scan(&count); err != nil {
		return 0, fmt.Errorf("agents: lesson count: %w", err)
	}
	return count, nil
}

func listInlineLessonsTx(ctx context.Context, tx pgx.Tx, agentID string) ([]domain.Lesson, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, agent_id::text, title, cue, body, always_inline
		FROM agents.lessons
		WHERE agent_id = $1::uuid AND always_inline
		ORDER BY title, id`, agentID)
	if err != nil {
		return nil, fmt.Errorf("agents: inline lessons: %w", err)
	}
	defer rows.Close()
	out := []domain.Lesson{}
	for rows.Next() {
		var l domain.Lesson
		if err := rows.Scan(&l.ID, &l.AgentID, &l.Title, &l.Cue, &l.Body, &l.AlwaysInline); err != nil {
			return nil, fmt.Errorf("agents: inline lessons: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func listLessonCardsTx(ctx context.Context, tx pgx.Tx, agentID string) ([]domain.Card, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, title, cue
		FROM agents.lessons
		WHERE agent_id = $1::uuid AND NOT always_inline
		ORDER BY title, id
		LIMIT 24`, agentID)
	if err != nil {
		return nil, fmt.Errorf("agents: lesson index: %w", err)
	}
	defer rows.Close()
	cards := []domain.Card{}
	for rows.Next() {
		var c domain.Card
		if err := rows.Scan(&c.ID, &c.Title, &c.Cue); err != nil {
			return nil, fmt.Errorf("agents: lesson index: %w", err)
		}
		cards = append(cards, c)
	}
	return cards, rows.Err()
}

func openLessonTx(ctx context.Context, tx pgx.Tx, agentID, lessonID string) (domain.Lesson, error) {
	var l domain.Lesson
	err := tx.QueryRow(ctx, `
		SELECT id::text, agent_id::text, title, cue, body, always_inline
		FROM agents.lessons
		WHERE agent_id = $1::uuid AND id = $2::uuid`,
		agentID, lessonID,
	).Scan(&l.ID, &l.AgentID, &l.Title, &l.Cue, &l.Body, &l.AlwaysInline)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Lesson{}, domain.ErrLessonGone
	}
	if err != nil {
		return domain.Lesson{}, fmt.Errorf("agents: lesson open: %w", err)
	}
	return l, nil
}

func lessonWriteErr(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23505":
			return domain.ErrLessonDuplicate
		case "42501":
			return app.ErrAgentManaged
		}
	}
	return fmt.Errorf("agents: lesson write: %w", err)
}
