package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/upmo-ai/upmo/internal/platform/agentloop"
	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

// NarrowedTx opens a transaction with single-team RLS narrowing applied for taskID.
func (s Store) NarrowedTx(ctx context.Context, scope agentloop.AccessScope, fn func(pgx.Tx) error) error {
	if s.Pool == nil {
		return fmt.Errorf("tasks: no database")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := agentloop.ApplyRLSTx(ctx, tx, agentloop.NarrowRLS(scope)); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// NarrowedRunnerTx is NarrowedTx on the upmo_runner pool. Runner columns are revoked from upmo_app.
func (s Store) NarrowedRunnerTx(ctx context.Context, scope agentloop.AccessScope, fn func(pgx.Tx) error) error {
	if s.Runner == nil {
		return fmt.Errorf("tasks: no runner database")
	}
	tx, err := s.Runner.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := agentloop.ApplyRLSTx(ctx, tx, agentloop.NarrowRLS(scope)); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const defaultTurnActor = "worker" // shortcut: 237a2 maps stage to actor

// RequestBatchApproval pauses the task and persists a batch hold (calls jsonb[])
// with deterministic action/preview and untrusted_origin flag.
func (s Store) RequestBatchApproval(ctx context.Context, access app.Access, taskID, toolName string, batch agentloop.BatchHold) (app.Approval, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return app.Approval{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var owner string
	err = tx.QueryRow(ctx, `
		SELECT owner_user_id::text
		FROM tasks.tasks
		WHERE id = $1::uuid
		FOR UPDATE`, taskID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Approval{}, app.ErrGone
	}
	if err != nil {
		return app.Approval{}, fmt.Errorf("tasks: lock batch approval: %w", err)
	}
	if owner != access.UserID {
		return app.Approval{}, app.ErrAccess
	}
	approval, err := insertBatchApprovalTx(ctx, tx, taskID, owner, toolName, batch)
	if err != nil {
		return app.Approval{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return app.Approval{}, err
	}
	return approval, nil
}

func insertBatchApprovalTx(ctx context.Context, tx pgx.Tx, taskID, ownerID, toolName string, batch agentloop.BatchHold) (app.Approval, error) {
	calls, err := batch.CallsJSONB()
	if err != nil {
		return app.Approval{}, err
	}
	if toolName == "" && len(batch.Calls) > 0 {
		toolName = batch.Calls[0].ToolName
	}
	if toolName == "" {
		toolName = "batch"
	}
	if len(toolName) > 64 {
		toolName = toolName[:64]
	}
	var status string
	if err := tx.QueryRow(ctx, `
		SELECT status FROM tasks.tasks WHERE id = $1::uuid`, taskID).Scan(&status); err != nil {
		return app.Approval{}, fmt.Errorf("tasks: status: %w", err)
	}
	kind := approvalEdge(domain.Status(status), batch.Reason)
	task, _, err := stepLocked(ctx, tx, ownerID, taskID, domain.Input{Kind: kind, Reason: batch.Reason, From: domain.Status(status)})
	if err != nil {
		return app.Approval{}, err
	}
	approval := app.Approval{
		TaskID: taskID,
		Title:  task.Goal,
		Action: batch.Action,
		State:  domain.ApprovalPending,
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO tasks.approvals (task_id, user_id, tool, reason, action, preview, from_status, calls, untrusted_origin)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8::jsonb[], $9)
		RETURNING id::text`,
		taskID, ownerID, toolName, batch.Reason, batch.Action, batch.Preview, status, calls, batch.UntrustedOrigin).Scan(&approval.ID)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return app.Approval{}, domain.ErrPending
	}
	if err != nil {
		return app.Approval{}, fmt.Errorf("tasks: batch approval: %w", err)
	}
	return approval, nil
}

// TaskActionLog implements agentloop.ActionLog against tasks.action_logs.
type TaskActionLog struct {
	Store Store
	Scope agentloop.AccessScope
}

// BeginDial (tx1) inserts (scope_id, call_id) in state="dialing", or returns
// ErrDuplicateAction if it already exists in dialing, done, or simulated.
func (l TaskActionLog) BeginDial(ctx context.Context, rec agentloop.ActionRecord) error {
	scopeID := agentloop.ActionScopeID(rec)
	if scopeID == "" {
		return fmt.Errorf("%w: empty action scope_id", agentloop.ErrInvalidConfig)
	}
	return l.Store.NarrowedTx(ctx, l.Scope, func(tx pgx.Tx) error {
		var existing string
		err := tx.QueryRow(ctx, `
			SELECT state FROM tasks.action_logs WHERE scope_id = $1::uuid AND call_id = $2 FOR UPDATE`,
			scopeID, rec.CallID).Scan(&existing)
		if err == nil {
			if existing == string(agentloop.ActionStateDialing) ||
				existing == string(agentloop.ActionStateDone) ||
				existing == string(agentloop.ActionStateSimulated) {
				return fmt.Errorf("%w: state=%s", agentloop.ErrDuplicateAction, existing)
			}
			_, uerr := tx.Exec(ctx, `
				UPDATE tasks.action_logs
				SET state = 'dialing', response_digest = '', error_text = '', completed_at = NULL
				WHERE scope_id = $1::uuid AND call_id = $2`, scopeID, rec.CallID)
			return uerr
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("tasks: action_log check: %w", err)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO tasks.action_logs (
				scope_id, call_id, task_id, run_id, user_id, tool_name, action, preview,
				state, untrusted_origin, dry_run, executed_at
			) VALUES (
				COALESCE(NULLIF($3, ''), $2)::uuid, $1, $2::uuid, NULLIF($3, '')::uuid, $4::uuid, $5, $6, $7,
				'dialing', $8, false, $9
			)`,
			rec.CallID, rec.ObjectID, rec.RunID, rec.UserID, rec.ToolName,
			rec.Action, rec.Preview, rec.UntrustedOrigin, rec.ExecutedAt.UTC())
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
			return fmt.Errorf("%w: concurrent insert", agentloop.ErrDuplicateAction)
		}
		return err
	})
}

// FinishDial (tx2) marks (scope_id, call_id) as done or failed with its response digest.
func (l TaskActionLog) FinishDial(ctx context.Context, scopeID, callID string, state agentloop.ActionState, responseDigest, errText string, completedAt time.Time) error {
	if scopeID == "" {
		scopeID = l.Scope.ObjectID
	}
	return l.Store.NarrowedTx(ctx, l.Scope, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE tasks.action_logs
			SET state = $3, response_digest = $4, error_text = $5, completed_at = $6
			WHERE scope_id = $1::uuid AND call_id = $2`,
			scopeID, callID, string(state), responseDigest, errText, completedAt.UTC())
		if err != nil {
			return fmt.Errorf("tasks: action_log finish: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return app.ErrGone
		}
		return nil
	})
}

// RecordSimulated persists a dry_run simulated write in tasks.action_logs.
func (l TaskActionLog) RecordSimulated(ctx context.Context, rec agentloop.ActionRecord) error {
	if agentloop.ActionScopeID(rec) == "" {
		return fmt.Errorf("%w: empty action scope_id", agentloop.ErrInvalidConfig)
	}
	return l.Store.NarrowedTx(ctx, l.Scope, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO tasks.action_logs (
				scope_id, call_id, task_id, run_id, user_id, tool_name, action, preview,
				state, untrusted_origin, dry_run, executed_at, completed_at
			) VALUES (
				COALESCE(NULLIF($3, ''), $2)::uuid, $1, $2::uuid, NULLIF($3, '')::uuid, $4::uuid, $5, $6, $7,
				'simulated', $8, true, $9, $9
			)
			ON CONFLICT (scope_id, call_id) DO NOTHING`,
			rec.CallID, rec.ObjectID, rec.RunID, rec.UserID, rec.ToolName,
			rec.Action, rec.Preview, rec.UntrustedOrigin, rec.ExecutedAt.UTC())
		return err
	})
}

// TaskTurnStore implements agentloop.TurnStore against tasks.turns,
// tasks.tasks, tasks.approvals, and an optional next-turn enqueue callback.
type TaskTurnStore struct {
	Store       Store
	Scope       agentloop.AccessScope
	EnqueueNext func(ctx context.Context, tx pgx.Tx, commit agentloop.TurnCommit) error
}

type RunnerCommitResult struct {
	Continue bool
}

// CommitRunnerTurn persists a runner turn and its optional next job atomically.
func (s TaskTurnStore) CommitRunnerTurn(ctx context.Context, commit agentloop.TurnCommit) (RunnerCommitResult, error) {
	if s.EnqueueNext == nil {
		return RunnerCommitResult{}, app.ErrEnqueueHook
	}
	actingUser := commit.UserID
	if actingUser != "" && s.Scope.UserID != "" && actingUser != s.Scope.UserID {
		return RunnerCommitResult{}, app.ErrAccess
	}
	if actingUser == "" {
		actingUser = s.Scope.UserID
	}
	if _, err := uuid.Parse(actingUser); err != nil || !hasPermission(s.Scope.Allow, "task:edit") {
		return RunnerCommitResult{}, app.ErrAccess
	}
	liveAllow, liveTeams, err := s.Store.liveRunnerAccess(ctx, actingUser)
	if err != nil {
		return RunnerCommitResult{}, err
	}
	if !hasPermission(strings.Join(liveAllow, ","), "task:edit") {
		return RunnerCommitResult{}, app.ErrAccess
	}
	freshScope := s.Scope
	freshScope.UserID = actingUser
	freshScope.Allow = strings.Join(liveAllow, ",")
	freshScope.UserTeams = liveTeams

	var result RunnerCommitResult
	err = s.Store.NarrowedRunnerTx(ctx, freshScope, func(tx pgx.Tx) error {
		var owner string
		var runnerOwned, currentTainted, currentPaused, currentPrivate bool
		var currentBudget, currentSpent int64
		var status string
		err := tx.QueryRow(ctx, `
			SELECT owner_user_id::text, runner_owned, status, tainted, paused, budget, spent, private_read
			FROM tasks.tasks WHERE id = $1::uuid FOR UPDATE`, commit.ObjectID).Scan(
			&owner, &runnerOwned, &status, &currentTainted, &currentPaused,
			&currentBudget, &currentSpent, &currentPrivate,
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrGone
		}
		if err != nil {
			return fmt.Errorf("tasks: lock runner turn commit: %w", err)
		}
		if owner != actingUser {
			return app.ErrAccess
		}
		if err := InsertTurnRows(ctx, tx, commit, defaultTurnActor); err != nil {
			return err
		}
		nextTainted := currentTainted || commit.Exposure.Tainted
		nextPrivate := currentPrivate || commit.Exposure.PrivateRead
		nextSpent, nextPaused, err := domain.Charge(currentBudget, currentSpent, commit.CostMicroUSD, currentPaused)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE tasks.tasks
			SET spent = $2, paused = $3, tainted = $4, private_read = $5, updated_at = now()
			WHERE id = $1::uuid
			  AND owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid`,
			commit.ObjectID, nextSpent, nextPaused, nextTainted, nextPrivate)
		if err != nil {
			return fmt.Errorf("tasks: update runner turn state: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return app.ErrGone
		}
		if commit.Hold != nil {
			if _, err := insertBatchApprovalTx(ctx, tx, commit.ObjectID, owner, "", *commit.Hold); err != nil {
				return err
			}
		}
		var paused, hasPendingApproval bool
		err = tx.QueryRow(ctx, `
			SELECT task.runner_owned, task.status, task.paused,
			       EXISTS (SELECT 1 FROM tasks.approvals WHERE task_id = task.id AND state = 'pending')
			FROM tasks.tasks AS task WHERE task.id = $1::uuid`, commit.ObjectID).Scan(
			&result.Continue, &status, &paused, &hasPendingApproval,
		)
		if err != nil {
			return fmt.Errorf("tasks: read runner continuation state: %w", err)
		}
		result.Continue = result.Continue &&
			(domain.Status(status) == domain.Planning || domain.Status(status) == domain.Working) &&
			!paused && !hasPendingApproval
		if result.Continue {
			return s.EnqueueNext(ctx, tx, commit)
		}
		return nil
	})
	if err != nil {
		return RunnerCommitResult{}, err
	}
	return result, nil
}

// CommitTurnAndEnqueue persists tx3 atomically under narrowed RLS.
func (s TaskTurnStore) CommitTurnAndEnqueue(ctx context.Context, commit agentloop.TurnCommit) error {
	return s.Store.NarrowedRunnerTx(ctx, s.Scope, func(tx pgx.Tx) error {
		if err := InsertTurnRows(ctx, tx, commit, defaultTurnActor); err != nil {
			return err
		}
		var owner string
		var currentTainted, currentPaused, currentPrivate bool
		var currentBudget, currentSpent int64
		if err := tx.QueryRow(ctx, `
			SELECT owner_user_id::text, tainted, paused, budget, spent, private_read
			FROM tasks.tasks
			WHERE id = $1::uuid
			FOR UPDATE`, commit.ObjectID).Scan(&owner, &currentTainted, &currentPaused, &currentBudget, &currentSpent, &currentPrivate); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return app.ErrGone
			}
			return fmt.Errorf("tasks: lock turn commit: %w", err)
		}
		actingUser := commit.UserID
		if actingUser == "" {
			actingUser = s.Scope.UserID
		}
		if owner != actingUser {
			return app.ErrAccess
		}
		nextTainted := currentTainted || commit.Exposure.Tainted
		nextPrivate := currentPrivate || commit.Exposure.PrivateRead
		nextSpent, nextPaused, err := domain.Charge(currentBudget, currentSpent, commit.CostMicroUSD, currentPaused)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			UPDATE tasks.tasks
			SET spent = $2, paused = $3, tainted = $4, private_read = $5, updated_at = now()
			WHERE id = $1::uuid
			  AND owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid`,
			commit.ObjectID, nextSpent, nextPaused, nextTainted, nextPrivate)
		if err != nil {
			return fmt.Errorf("tasks: update turn state: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return app.ErrGone
		}
		if commit.Hold != nil {
			if _, err := insertBatchApprovalTx(ctx, tx, commit.ObjectID, owner, "", *commit.Hold); err != nil {
				return err
			}
		}
		if commit.EnqueueNext && s.EnqueueNext != nil {
			if err := s.EnqueueNext(ctx, tx, commit); err != nil {
				return err
			}
		}
		return nil
	})
}

// InsertTurnRows writes turn transcript entries into tasks.turns inside tx.
func InsertTurnRows(ctx context.Context, tx pgx.Tx, commit agentloop.TurnCommit, actor string) error {
	seq := commit.SeqStart
	for _, msg := range commit.Messages {
		var toolCalls any
		if len(msg.ToolCalls) > 0 {
			raw, err := json.Marshal(msg.ToolCalls)
			if err != nil {
				return err
			}
			toolCalls = raw
		}
		var cost *int64
		if msg.Role == agentloop.RoleAssistant && commit.CostMicroUSD > 0 {
			c := commit.CostMicroUSD
			cost = &c
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO tasks.turns (
				task_id, run_id, seq, role, actor, body, tool_calls, tool_name, tool_id,
				is_error, offloaded, tainted, private_read, model, input_tokens, output_tokens, cost_micro_usd
			) VALUES (
				$1::uuid, $2::uuid, $3, $4, $5, $6, $7::jsonb, $8, $9,
				$10, $11, $12, $13, $14, $15, $16, $17
			)`,
			commit.ObjectID, commit.RunID, seq, string(msg.Role), actor, msg.Text, toolCalls,
			msg.ToolName, msg.ToolID, msg.IsError, msg.Offload,
			commit.Exposure.Tainted, commit.Exposure.PrivateRead,
			commit.Model, commit.InputTokens, commit.OutputTokens, cost); err != nil {
			return fmt.Errorf("tasks: insert turn: %w", err)
		}
		seq++
	}
	return nil
}
