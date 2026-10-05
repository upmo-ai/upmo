package notices

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrKind is returned when kind is not allowed.
var ErrKind = errors.New("notices: kind is not allowed")

// ErrTitle is returned when title is blank after trim.
var ErrTitle = errors.New("notices: title is required")

// ErrProposalTask is returned when a proposal carries a task id.
var ErrProposalTask = errors.New("notices: proposal must not have a task id")

// KindProposal is a scout-suggested title awaiting a person.
const KindProposal = "proposal"

// KindQuestion is a question notice.
const KindQuestion = "question"

// KindFinished is a task finished notice.
const KindFinished = "finished"

// KindFailed is a task failed notice.
const KindFailed = "failed"

var allowedKinds = map[string]struct{}{
	KindProposal: {},
	KindQuestion: {},
	KindFinished: {},
	KindFailed:   {},
}

// Notice is one persisted bell item.
type Notice struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	Kind      string
	Title     string
	TaskID    *uuid.UUID
	CreatedAt time.Time
}

// Store reads and writes notices.notifications under row security.
type Store struct {
	Pool *pgxpool.Pool
}

// Insert adds one notice for userID.
func (s Store) Insert(ctx context.Context, userID uuid.UUID, kind, title string, taskID *uuid.UUID) (uuid.UUID, error) {
	title = strings.TrimSpace(title)
	if _, ok := allowedKinds[kind]; !ok {
		return uuid.Nil, ErrKind
	}
	if title == "" {
		return uuid.Nil, ErrTitle
	}
	if kind == KindProposal && taskID != nil {
		return uuid.Nil, ErrProposalTask
	}
	if s.Pool == nil {
		return uuid.Nil, fmt.Errorf("notices: no database")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := setUser(ctx, tx, userID); err != nil {
		return uuid.Nil, err
	}
	var id uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO notices.notifications (user_id, kind, title, task_id)
		VALUES ($1, $2, $3, $4)
		RETURNING id`, userID, kind, title, taskID).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("notices: insert: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// List returns every notice for userID, newest first.
func (s Store) List(ctx context.Context, userID uuid.UUID) ([]Notice, error) {
	if s.Pool == nil {
		return nil, fmt.Errorf("notices: no database")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := setUser(ctx, tx, userID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT id, user_id, kind, title, task_id, created_at
		FROM notices.notifications
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("notices: list: %w", err)
	}
	defer rows.Close()
	var out []Notice
	for rows.Next() {
		var n Notice
		if err := rows.Scan(&n.ID, &n.UserID, &n.Kind, &n.Title, &n.TaskID, &n.CreatedAt); err != nil {
			return nil, fmt.Errorf("notices: scan: %w", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func setUser(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	_, err := tx.Exec(ctx, `SELECT set_config('upmo.user_id', $1, true)`, userID.String())
	return err
}
