package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/upmo-ai/upmo/internal/platform/agentloop"
	"github.com/upmo-ai/upmo/internal/tasks/app"
)

const (
	checkViolation  = "23514"
	maxHistoryBytes = 1048576
	outputCapBytes  = 262144
)

var (
	errHistoryTooLarge = errors.New("tasks: history too large")
	errNoOutput        = errors.New("no such output")
)

// LoadHistory returns the stored runner message history for (taskID, runID).
// It requires the caller to be the task owner on the runner pool, and returns
// nil, nil when no history row exists yet for that run.
func (s Store) LoadHistory(ctx context.Context, access app.Access, taskID, runID string) ([]agentloop.Message, error) {
	if _, err := uuid.Parse(access.UserID); err != nil {
		return nil, app.ErrAccess
	}
	if _, err := uuid.Parse(taskID); err != nil {
		return nil, app.ErrTask
	}
	if _, err := uuid.Parse(runID); err != nil {
		return nil, app.ErrRun
	}

	tx, err := s.beginRunner(ctx, access)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var ownerID string
	err = tx.QueryRow(ctx, `
		SELECT owner_user_id::text
		FROM tasks.tasks
		WHERE id = $1::uuid`, taskID).Scan(&ownerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, app.ErrGone
	}
	if err != nil {
		return nil, fmt.Errorf("tasks: load history owner: %w", err)
	}
	if ownerID != access.UserID {
		return nil, app.ErrAccess
	}

	var raw []byte
	err = tx.QueryRow(ctx, `
		SELECT rh.history
		FROM tasks.runs AS r
		LEFT JOIN tasks.run_history AS rh ON rh.run_id = r.id
		WHERE r.id = $1::uuid AND r.task_id = $2::uuid`, runID, taskID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, app.ErrGone
	}
	if err != nil {
		return nil, fmt.Errorf("tasks: load history: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var msgs []agentloop.Message
	if err := json.Unmarshal(raw, &msgs); err != nil {
		return nil, fmt.Errorf("tasks: decode history: %w", err)
	}
	return msgs, nil
}

// SaveHistoryTx replaces the stored runner message history for (taskID, runID)
// inside the caller's runner transaction, fenced by a live claim token.
func (s Store) SaveHistoryTx(ctx context.Context, tx pgx.Tx, taskID, runID, token string, history []agentloop.Message) error {
	if _, err := uuid.Parse(taskID); err != nil {
		return app.ErrTask
	}
	if _, err := uuid.Parse(runID); err != nil {
		return app.ErrRun
	}
	if _, err := uuid.Parse(token); err != nil {
		return app.ErrFenced
	}
	if history == nil {
		history = []agentloop.Message{}
	}
	raw, err := json.Marshal(history)
	if err != nil || len(raw) > maxHistoryBytes {
		return errHistoryTooLarge
	}

	var ownerID, callerID, claimToken string
	var leaseLive bool
	err = tx.QueryRow(ctx, `
		SELECT owner_user_id::text,
		       COALESCE(current_setting('upmo.user_id', true), ''),
		       COALESCE(claim_token::text, ''),
		       COALESCE(lease_until >= now(), false)
		FROM tasks.tasks
		WHERE id = $1::uuid
		FOR UPDATE`, taskID).Scan(&ownerID, &callerID, &claimToken, &leaseLive)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrGone
	}
	if err != nil {
		return fmt.Errorf("tasks: lock history save: %w", err)
	}
	if ownerID != callerID {
		return app.ErrAccess
	}
	if claimToken == "" || claimToken != token || !leaseLive {
		return app.ErrFenced
	}

	tag, err := tx.Exec(ctx, `
		INSERT INTO tasks.run_history (run_id, history)
		SELECT r.id, $3::jsonb
		FROM tasks.runs AS r
		WHERE r.id = $1::uuid AND r.task_id = $2::uuid
		ON CONFLICT (run_id) DO UPDATE
		SET history = EXCLUDED.history,
		    updated_at = now()`, runID, taskID, raw)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == checkViolation && pgErr.ConstraintName == "run_history_size_check" {
			return errHistoryTooLarge
		}
		return fmt.Errorf("tasks: save history: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return app.ErrGone
	}
	return nil
}

type taskOutputs struct {
	store         Store
	taskID, runID string
	access        func() (app.Access, error)
}

// Outputs returns the agentloop.OutputStore of a run: SaveOutput and
// RetrieveOutput on tasks.findings, under the access resolved at call time.
func (s Store) Outputs(taskID, runID string, access func() (app.Access, error)) agentloop.OutputStore {
	return taskOutputs{store: s, taskID: taskID, runID: runID, access: access}
}

func requireTaskOwner(ctx context.Context, tx pgx.Tx, taskID, userID string) error {
	var ownerID string
	err := tx.QueryRow(ctx, `
		SELECT owner_user_id::text
		FROM tasks.tasks
		WHERE id = $1::uuid`, taskID).Scan(&ownerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrGone
	}
	if err != nil {
		return fmt.Errorf("tasks: output owner: %w", err)
	}
	if ownerID != userID {
		return app.ErrAccess
	}
	return nil
}

// SaveOutput stores one tool output of the run on tasks.findings, capping the
// body at outputCapBytes on a rune boundary.
func (o taskOutputs) SaveOutput(ctx context.Context, run *agentloop.RunState, title, body string, untrusted bool) (string, error) {
	if o.access == nil {
		return "", app.ErrAccess
	}
	a, err := o.access()
	if err != nil {
		return "", err
	}
	if _, err := uuid.Parse(a.UserID); err != nil {
		return "", app.ErrAccess
	}
	if _, err := uuid.Parse(o.taskID); err != nil {
		return "", app.ErrTask
	}
	if _, err := uuid.Parse(o.runID); err != nil {
		return "", app.ErrRun
	}

	tx, err := o.store.beginRunner(ctx, a)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := requireTaskOwner(ctx, tx, o.taskID, a.UserID); err != nil {
		return "", err
	}

	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO tasks.findings (task_id, run_id, kind, title, body, untrusted, private_read)
		SELECT r.task_id, r.id, 'tool_output', $3, $4, $5, $6
		FROM tasks.runs AS r
		WHERE r.id = $2::uuid AND r.task_id = $1::uuid
		RETURNING id::text`,
		o.taskID, o.runID, title, capOutput(body), untrusted, run != nil && run.Exposure.PrivateRead).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", app.ErrGone
	}
	if err != nil {
		return "", fmt.Errorf("tasks: save output: %w", err)
	}
	return id, tx.Commit(ctx)
}

// RetrieveOutput reads one output or tool_output row scoped to (taskID, runID).
func (o taskOutputs) RetrieveOutput(ctx context.Context, _ *agentloop.RunState, id string) (string, bool, error) {
	if _, err := uuid.Parse(id); err != nil {
		return "", false, errNoOutput
	}
	if o.access == nil {
		return "", false, app.ErrAccess
	}
	a, err := o.access()
	if err != nil {
		return "", false, err
	}
	if _, err := uuid.Parse(a.UserID); err != nil {
		return "", false, app.ErrAccess
	}
	if _, err := uuid.Parse(o.taskID); err != nil {
		return "", false, errNoOutput
	}
	if _, err := uuid.Parse(o.runID); err != nil {
		return "", false, errNoOutput
	}

	tx, err := o.store.beginRunner(ctx, a)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := requireTaskOwner(ctx, tx, o.taskID, a.UserID); err != nil {
		return "", false, err
	}

	var body string
	var untrusted bool
	err = tx.QueryRow(ctx, `
		SELECT f.body, f.untrusted
		FROM tasks.findings AS f
		JOIN tasks.runs AS r ON r.id = f.run_id AND r.task_id = f.task_id
		WHERE f.id = $1::uuid
		  AND f.task_id = $2::uuid
		  AND f.run_id = $3::uuid
		  AND f.kind IN ('output', 'tool_output')`,
		id, o.taskID, o.runID).Scan(&body, &untrusted)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, errNoOutput
	}
	if err != nil {
		return "", false, fmt.Errorf("tasks: retrieve output: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", false, err
	}
	return body, untrusted, nil
}

// capOutput cuts an output down to outputCapBytes with a marker, on a rune
// boundary.
func capOutput(body string) string {
	if len(body) <= outputCapBytes {
		return body
	}
	marker := fmt.Sprintf("\n[output truncated: %d bytes in all]", len(body))
	keep := outputCapBytes - len(marker)
	for keep > 0 && !utf8.RuneStart(body[keep]) {
		keep--
	}
	return body[:keep] + marker
}
