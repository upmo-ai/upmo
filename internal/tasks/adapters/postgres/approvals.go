package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

// uniqueViolation is the Postgres code for a duplicate key, raiseException
// the one for RAISE, and decidedRefusal the line the decide_once trigger
// raises.
const (
	uniqueViolation = "23505"
	raiseException  = "P0001"
	decidedRefusal  = "tasks: approval"
)

// Request pauses the task through the same step the other transitions use and
// stores one pending approval for its owner, in one transaction.
func (s Store) Request(ctx context.Context, access app.Access, taskID string, ask domain.Ask) (app.Approval, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return app.Approval{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var owner, status string
	err = tx.QueryRow(ctx, `
		SELECT owner_user_id::text, status
		FROM tasks.tasks
		WHERE id = $1::uuid
		FOR UPDATE`, taskID).Scan(&owner, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Approval{}, app.ErrGone
	}
	if err != nil {
		return app.Approval{}, fmt.Errorf("tasks: lock: %w", err)
	}
	// The owner decides, so only the owner's own run asks.
	if owner != access.UserID {
		return app.Approval{}, app.ErrAccess
	}

	var pending bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM tasks.approvals
			WHERE task_id = $1::uuid AND user_id = $2::uuid AND state = 'pending'
		)`, taskID, access.UserID).Scan(&pending); err != nil {
		return app.Approval{}, fmt.Errorf("tasks: pending approval: %w", err)
	}
	if pending {
		return app.Approval{}, domain.ErrPending
	}

	kind := approvalEdge(domain.Status(status), ask.Reason)
	task, _, err := stepLocked(ctx, tx, taskID, domain.Input{Kind: kind, Reason: ask.Reason, From: domain.Status(status)})
	if err != nil {
		return app.Approval{}, err
	}
	approval := app.Approval{
		TaskID: taskID,
		Title:  task.Goal,
		Action: ask.Action,
		State:  domain.ApprovalPending,
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO tasks.approvals (task_id, user_id, tool, reason, action, from_status)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6)
		RETURNING id::text`,
		taskID, owner, ask.Tool, ask.Reason, ask.Action, status).Scan(&approval.ID)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return app.Approval{}, domain.ErrPending
	}
	if err != nil {
		return app.Approval{}, fmt.Errorf("tasks: approval: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return app.Approval{}, err
	}
	return approval, nil
}

// Decide settles a pending approval and resumes its task, in one transaction.
func (s Store) Decide(ctx context.Context, access app.Access, approvalID string, kind domain.Kind) (app.Approval, error) {
	return s.DecideWithInput(ctx, access, approvalID, kind, "")
}

// DecideWithInput settles a pending approval. body is the question answer or the plan note.
func (s Store) DecideWithInput(ctx context.Context, access app.Access, approvalID string, kind domain.Kind, body string) (app.Approval, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return app.Approval{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var taskID string
	err = tx.QueryRow(ctx, `
		SELECT task_id::text
		FROM tasks.approvals
		WHERE id = $1::uuid`, approvalID).Scan(&taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Approval{}, app.ErrNoApproval
	}
	if err != nil {
		return app.Approval{}, fmt.Errorf("tasks: approval: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		SELECT 1 FROM tasks.tasks WHERE id = $1::uuid FOR UPDATE`, taskID); err != nil {
		return app.Approval{}, fmt.Errorf("tasks: lock: %w", err)
	}

	approval := app.Approval{ID: approvalID, TaskID: taskID}
	var state, reason, fromStatus, answer string
	err = tx.QueryRow(ctx, `
		SELECT approval.state, approval.action, approval.reason, approval.from_status, approval.answer, task.goal
		FROM tasks.approvals AS approval
		JOIN tasks.tasks AS task ON task.id = approval.task_id
		WHERE approval.id = $1::uuid
		FOR UPDATE OF approval`, approvalID).Scan(&state, &approval.Action, &reason, &fromStatus, &answer, &approval.Title)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Approval{}, app.ErrNoApproval
	}
	if err != nil {
		return app.Approval{}, fmt.Errorf("tasks: lock approval: %w", err)
	}
	_ = answer
	next, err := domain.Decide(domain.ApprovalState(state), kind)
	if err != nil {
		return app.Approval{}, err
	}
	answerSQL := `
		UPDATE tasks.approvals
		SET state = $2, decided_at = now()
		WHERE id = $1::uuid`
	answerArgs := []any{approvalID, string(next)}
	if kind == domain.Approve && reason == domain.ReasonQuestion {
		answerSQL = `
		UPDATE tasks.approvals
		SET state = $2, decided_at = now(), answer = $3
		WHERE id = $1::uuid`
		answerArgs = append(answerArgs, body)
	}
	tag, err := tx.Exec(ctx, answerSQL, answerArgs...)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == raiseException && pgErr.Message == decidedRefusal {
		return app.Approval{}, domain.ErrDecided
	}
	if err != nil {
		return app.Approval{}, fmt.Errorf("tasks: decide: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return app.Approval{}, app.ErrNoApproval
	}
	if _, _, err := stepLocked(ctx, tx, taskID, domain.Input{
		Kind: kind, Reason: reason, From: domain.Status(fromStatus), Body: body,
	}); err != nil {
		return app.Approval{}, err
	}
	if kind == domain.Deny {
		switch reason {
		case domain.ReasonAsk, domain.ReasonExpired, domain.ReasonTrifecta, domain.ReasonWriteguard:
			if _, err := insertChatEvent(ctx, tx, taskID, domain.DeniedLine); err != nil {
				return app.Approval{}, err
			}
		case domain.ReasonPlan:
			note := strings.TrimSpace(body)
			if note != "" {
				if _, err := insertChatEvent(ctx, tx, taskID, note); err != nil {
					return app.Approval{}, err
				}
			}
		}
	}
	// One event per decision: the resume edge it applies and the denial line
	// it leaves are part of that same decision.
	action := audit.ApprovalApprove
	if kind == domain.Deny {
		action = audit.ApprovalDeny
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   action,
		Outcome:  audit.Success,
		TargetID: approvalID,
		Changes: audit.Changes{
			Fields: []string{"state"},
			Values: map[string]audit.Change{"state": {From: state, To: string(next)}},
		},
	}); err != nil {
		return app.Approval{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return app.Approval{}, err
	}
	approval.State = next
	return approval, nil
}

func approvalEdge(status domain.Status, reason string) domain.Kind {
	if reason == domain.ReasonPlan && status == domain.Planning {
		return domain.AskPlan
	}
	if status == domain.Verifying || (reason != domain.ReasonAsk && reason != domain.ReasonExpired) {
		return domain.Hold
	}
	return domain.Pause
}

// Approvals lists this person's approvals under row security: pending first,
// then the most recent decided ones. Closed rows are left out, so a task that
// ended while it waited asks nothing. The rows are also narrowed to this
// person by hand, past what row security admits: the close policy lets a
// shared editor read a closed row, but an editor never gets the owner's list.
func (s Store) Approvals(ctx context.Context, access app.Access) ([]app.Approval, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT approval.id::text, approval.task_id::text, task.goal, approval.action, approval.state
		FROM tasks.approvals AS approval
		JOIN tasks.tasks AS task ON task.id = approval.task_id
		WHERE approval.user_id = $2::uuid
		  AND approval.state <> 'closed'
		ORDER BY (approval.state = 'pending') DESC,
		         COALESCE(approval.decided_at, approval.created_at) DESC,
		         approval.id DESC
		LIMIT $1`, app.ApprovalsShown, access.UserID)
	if err != nil {
		return nil, fmt.Errorf("tasks: approvals: %w", err)
	}
	defer rows.Close()

	out := []app.Approval{}
	for rows.Next() {
		var approval app.Approval
		var state string
		if err := rows.Scan(&approval.ID, &approval.TaskID, &approval.Title, &approval.Action, &state); err != nil {
			return nil, fmt.Errorf("tasks: approvals: %w", err)
		}
		approval.State = domain.ApprovalState(state)
		out = append(out, approval)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tasks: approvals: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}
