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

	"github.com/upmo-ai/upmo/internal/platform/audit"
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

// List returns the notices for userID that are not dismissed, newest first,
// at most limit of them.
func (s Store) List(ctx context.Context, userID uuid.UUID, limit int) ([]Notice, error) {
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
		WHERE dismissed_at IS NULL
		ORDER BY created_at DESC, id DESC
		LIMIT $1`, limit)
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

// Dismiss hides one of userID's notices from the list. It reports false for
// an id that is not theirs or does not exist. Dismissing a notice again keeps
// the first time and reports true.
func (s Store) Dismiss(ctx context.Context, userID, id uuid.UUID) (bool, error) {
	if s.Pool == nil {
		return false, fmt.Errorf("notices: no database")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := setUser(ctx, tx, userID); err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE notices.notifications
		SET dismissed_at = now()
		WHERE id = $1 AND dismissed_at IS NULL`, id)
	if err != nil {
		return false, fmt.Errorf("notices: dismiss: %w", err)
	}
	// Only a row dismissed now seals its event. A repeat dismiss and a miss
	// changed nothing, so neither leaves one.
	if tag.RowsAffected() == 1 {
		if err := audit.Record(ctx, tx, audit.Event{
			Action:   audit.NotificationDismiss,
			Outcome:  audit.Success,
			TargetID: id.String(),
		}); err != nil {
			return false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return false, err
		}
		return true, nil
	}
	// A repeat dismiss keeps the first time and answers found; the notice has
	// to exist and be this person's, so a miss still answers not found.
	var stored bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM notices.notifications WHERE id = $1)`, id).Scan(&stored); err != nil {
		return false, fmt.Errorf("notices: dismiss: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return stored, nil
}

func setUser(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	_, err := tx.Exec(ctx, `SELECT set_config('upmo.user_id', $1, true)`, userID.String())
	return err
}
