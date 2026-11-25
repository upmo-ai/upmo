package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

// SaveAutomation stores a trigger that starts on. The template must belong to the caller.
func (s Store) SaveAutomation(ctx context.Context, access app.Access, templateID, kind, spec string) (string, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var one int
	err = tx.QueryRow(ctx, `SELECT 1 FROM tasks.templates WHERE id = $1::uuid`, templateID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", app.ErrGone
	}
	if err != nil {
		return "", fmt.Errorf("tasks: template: %w", err)
	}
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO tasks.automations (owner_user_id, template_id, kind, spec)
		VALUES ($1::uuid, $2::uuid, $3, $4)
		RETURNING id::text`, access.UserID, templateID, kind, spec).Scan(&id)
	if err != nil {
		return "", automationWriteErr(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return id, nil
}

// Fire opens one task from the template. The same dedup key returns the task already opened.
func (s Store) Fire(ctx context.Context, access app.Access, automationID, dedup string, values map[string]string, sourceTask string) (string, bool, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var mode string
	var failures int
	err = tx.QueryRow(ctx, `
		SELECT mode, failures
		FROM tasks.automations
		WHERE id = $1::uuid
		FOR UPDATE`, automationID).Scan(&mode, &failures)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, app.ErrGone
	}
	if err != nil {
		return "", false, fmt.Errorf("tasks: lock automation: %w", err)
	}
	if mode != domain.ModeOn {
		return "", false, domain.ErrMode
	}
	if sourceTask != "" {
		var same bool
		err = tx.QueryRow(ctx, `
			SELECT COALESCE(automation_id = $2::uuid, false)
			FROM tasks.tasks
			WHERE id = $1::uuid`, sourceTask, automationID).Scan(&same)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, app.ErrGone
		}
		if err != nil {
			return "", false, fmt.Errorf("tasks: source: %w", err)
		}
		if same {
			return "", false, domain.ErrSelf
		}
	}
	var existing string
	err = tx.QueryRow(ctx, `
		SELECT task_id::text
		FROM tasks.automation_fires
		WHERE automation_id = $1::uuid AND dedup_key = $2`, automationID, dedup).Scan(&existing)
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return "", false, err
		}
		return existing, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", false, fmt.Errorf("tasks: dedup: %w", err)
	}

	var goal string
	var budget int64
	err = tx.QueryRow(ctx, `SELECT goal, budget FROM tasks.automation_template($1::uuid)`, automationID).Scan(&goal, &budget)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.trip(ctx, tx, automationID, failures, app.ErrGone)
	}
	if err != nil {
		return "", false, fmt.Errorf("tasks: template body: %w", err)
	}
	filled, err := domain.Fill(goal, values)
	if err != nil {
		return s.trip(ctx, tx, automationID, failures, err)
	}
	next, effect, err := domain.Step("", domain.Input{Kind: domain.Open, Goal: filled})
	if err != nil {
		return "", false, err
	}
	var taskID string
	err = tx.QueryRow(ctx, `
		INSERT INTO tasks.tasks (owner_user_id, visibility, goal, status, allow, automation_id, budget)
		VALUES ($1::uuid, 'private', $2, $3, $4, $5::uuid, $6)
		RETURNING id::text`,
		access.UserID, filled, string(next), access.Allow, automationID, budgetOrDefault(budget)).Scan(&taskID)
	if err != nil {
		return "", false, fmt.Errorf("tasks: open from automation: %w", err)
	}
	if err := insertTaskEvent(ctx, tx, taskID, effect); err != nil {
		return "", false, err
	}
	// The automation fired, so the system opened the task on its owner's
	// behalf; the owner stays the task's owner, not the event's actor.
	if err := audit.Record(audit.WithRunner(ctx, audit.CredentialRun), tx, audit.Event{
		Action:   audit.TaskCreate,
		Outcome:  audit.Success,
		TargetID: taskID,
	}); err != nil {
		return "", false, err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO tasks.automation_fires (automation_id, dedup_key, task_id)
		VALUES ($1::uuid, $2, $3::uuid)`, automationID, dedup, taskID)
	if err != nil {
		return "", false, fmt.Errorf("tasks: fire: %w", err)
	}
	if failures > 0 {
		if _, err := tx.Exec(ctx, `UPDATE tasks.automations SET failures = 0 WHERE id = $1::uuid`, automationID); err != nil {
			return "", false, fmt.Errorf("tasks: reset breaker: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", false, err
	}
	return taskID, false, nil
}

// SetMode turns the automation on or off. On clears a breaker. On is refused after the owner leaves.
func (s Store) SetMode(ctx context.Context, access app.Access, automationID, mode string) error {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var current, reason string
	err = tx.QueryRow(ctx, `
		SELECT mode, reason
		FROM tasks.automations
		WHERE id = $1::uuid
		FOR UPDATE`, automationID).Scan(&current, &reason)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrGone
	}
	if err != nil {
		return fmt.Errorf("tasks: lock mode: %w", err)
	}
	if current == mode {
		return tx.Commit(ctx)
	}
	if mode == domain.ModeOn && reason == domain.ReasonOwnerLeft {
		return domain.ErrMode
	}
	if mode == domain.ModeOn {
		_, err = tx.Exec(ctx, `
			UPDATE tasks.automations
			SET mode = $2, reason = '', failures = 0
			WHERE id = $1::uuid`, automationID, mode)
	} else {
		_, err = tx.Exec(ctx, `
			UPDATE tasks.automations
			SET mode = $2
			WHERE id = $1::uuid`, automationID, mode)
	}
	if err != nil {
		return automationWriteErr(err)
	}
	return tx.Commit(ctx)
}

// OwnerLeft disables every automation that person owns.
func (s Store) OwnerLeft(ctx context.Context, access app.Access, userID string) error {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		UPDATE tasks.automations
		SET mode = 'off', reason = 'owner_left'
		WHERE owner_user_id = $1::uuid`, userID); err != nil {
		return automationWriteErr(err)
	}
	return tx.Commit(ctx)
}

// Reassign moves the automation to another person and clears an owner-left reason. It stays off.
func (s Store) Reassign(ctx context.Context, access app.Access, automationID, userID string) error {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var reason string
	err = tx.QueryRow(ctx, `
		SELECT reason
		FROM tasks.automations
		WHERE id = $1::uuid
		FOR UPDATE`, automationID).Scan(&reason)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrGone
	}
	if err != nil {
		return fmt.Errorf("tasks: lock reassign: %w", err)
	}
	if reason == domain.ReasonOwnerLeft {
		reason = ""
	}
	tag, err := tx.Exec(ctx, `
		UPDATE tasks.automations
		SET owner_user_id = $2::uuid, reason = $3
		WHERE id = $1::uuid`, automationID, userID, reason)
	if err != nil {
		return automationWriteErr(err)
	}
	if tag.RowsAffected() != 1 {
		return app.ErrGone
	}
	return tx.Commit(ctx)
}

func (s Store) trip(ctx context.Context, tx pgx.Tx, automationID string, failures int, cause error) (string, bool, error) {
	next, tripped := domain.Breaker(failures)
	mode := domain.ModeOn
	reason := ""
	if tripped {
		mode = domain.ModeOff
		reason = domain.ReasonBreaker
	}
	tag, err := tx.Exec(ctx, `
		UPDATE tasks.automations
		SET failures = $2, mode = $3, reason = $4
		WHERE id = $1::uuid`, automationID, next, mode, reason)
	if err != nil {
		return "", false, fmt.Errorf("tasks: breaker: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return "", false, app.ErrGone
	}
	if err := tx.Commit(ctx); err != nil {
		return "", false, err
	}
	return "", false, cause
}

func automationWriteErr(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "tasks: template"):
		return app.ErrGone
	case strings.Contains(msg, "automations_live_check"):
		return domain.ErrMode
	default:
		return fmt.Errorf("tasks: automation: %w", err)
	}
}

// AutomationExists reports whether id is an existing automation, via
// tasks.automation_exists on the app pool.
func AutomationExists(ctx context.Context, pool *pgxpool.Pool, id string) (bool, error) {
	if pool == nil {
		return false, fmt.Errorf("tasks: no database")
	}
	if _, err := uuid.Parse(id); err != nil {
		return false, nil
	}
	var exists bool
	err := pool.QueryRow(ctx, `SELECT tasks.automation_exists($1::uuid)`, id).Scan(&exists)
	return exists, err
}

// AutomationUses lists the goals of the automations access may see whose
// template runs one of the agents. tasks.automation_uses applies the
// automations visibility rule (managers see all, others their own) and returns
// only the goal, so an agent id says nothing to a person who cannot see an
// automation that uses it.
func (s Store) AutomationUses(ctx context.Context, access app.Access, agentIDs []string) ([]string, error) {
	if len(agentIDs) == 0 {
		return []string{}, nil
	}
	tx, err := s.begin(ctx, access)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
		SELECT goal
		FROM tasks.automation_uses($1::uuid[])
		ORDER BY lower(goal), id`, agentIDs)
	if err != nil {
		return nil, fmt.Errorf("tasks: automation uses: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var goal string
		if err := rows.Scan(&goal); err != nil {
			return nil, fmt.Errorf("tasks: automation uses: %w", err)
		}
		out = append(out, goal)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tasks: automation uses: %w", err)
	}
	return out, tx.Commit(ctx)
}
