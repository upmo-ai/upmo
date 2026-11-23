package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/platform/agentloop"
)

const uniqueViolation = "23505"

// NarrowedTx opens a Postgres transaction with single-team RLS narrowing
// applied for scope.
func (s Store) NarrowedTx(ctx context.Context, scope agentloop.AccessScope, fn func(pgx.Tx) error) error {
	if s.Pool == nil {
		return fmt.Errorf("chat: no database")
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

// SaveHold persists a batch hold in chat.holds.
func (s Store) SaveHold(ctx context.Context, scope agentloop.AccessScope, chatID, lineID string, batch agentloop.BatchHold) (string, error) {
	var id string
	err := s.NarrowedTx(ctx, scope, func(tx pgx.Tx) error {
		var err error
		id, err = insertHoldTx(ctx, tx, chatID, lineID, scope.UserID, batch)
		return err
	})
	return id, err
}

func insertHoldTx(ctx context.Context, tx pgx.Tx, chatID, lineID, userID string, batch agentloop.BatchHold) (string, error) {
	calls, err := batch.CallsJSONB()
	if err != nil {
		return "", err
	}
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO chat.holds (chat_id, line_id, user_id, reason, action, preview, calls, untrusted_origin)
		VALUES ($1::uuid, NULLIF($2, '')::uuid, $3::uuid, $4, $5, $6, $7::jsonb[], $8)
		RETURNING id::text`,
		chatID, lineID, userID, batch.Reason, batch.Action, batch.Preview, calls, batch.UntrustedOrigin).Scan(&id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return "", fmt.Errorf("chat: pending hold exists: %w", err)
	}
	return id, err
}

// SaveFinding stores an offloaded output or finding in chat.findings.
func (s Store) SaveFinding(ctx context.Context, scope agentloop.AccessScope, chatID, lineID, kind, title, body, sourceRef string, untrusted bool) (string, error) {
	if kind == "" {
		kind = "output"
	}
	var id string
	err := s.NarrowedTx(ctx, scope, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO chat.findings (chat_id, line_id, kind, title, body, source_ref, untrusted)
			VALUES ($1::uuid, NULLIF($2, '')::uuid, $3, $4, $5, $6, $7)
			RETURNING id::text`,
			chatID, lineID, kind, title, body, sourceRef, untrusted).Scan(&id)
	})
	return id, err
}

// ActionLog is the chat's log of tool calls that act outside it.
func (s Store) ActionLog(scope agentloop.AccessScope) agentloop.ActionLog {
	return ChatActionLog{Store: s, Scope: scope}
}

func hasAllowanceTx(ctx context.Context, tx pgx.Tx, chatID, userID, toolName string) (bool, error) {
	var allowed bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM chat.tool_allowances AS a
			JOIN chat.chats AS c ON c.id = a.chat_id
			WHERE a.chat_id = $1::uuid
			  AND a.user_id = $2::uuid
			  AND a.tool = $3
			  AND NOT c.tainted
		)`, chatID, userID, toolName).Scan(&allowed)
	if err != nil {
		return false, fmt.Errorf("chat: allowance: %w", err)
	}
	return allowed, nil
}

// HasAllowance reports whether the person allowed the tool for the chat and
// the chat is untainted.
func (s Store) HasAllowance(ctx context.Context, access app.Access, chatID, toolName string) (bool, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	allowed, err := hasAllowanceTx(ctx, tx, chatID, access.UserID, toolName)
	if err != nil {
		return false, err
	}
	return allowed, tx.Commit(ctx)
}

func allowToolForChatTx(ctx context.Context, tx pgx.Tx, chatID, userID, toolName string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO chat.tool_allowances (chat_id, user_id, tool)
		VALUES ($1::uuid, $2::uuid, $3)
		ON CONFLICT (chat_id, user_id, tool) DO NOTHING`, chatID, userID, toolName)
	if err != nil {
		return fmt.Errorf("chat: allow tool: %w", err)
	}
	return nil
}

// AllowToolForChat lets the person's calls of the tool run without a hold in
// this chat while it stays untainted.
func (s Store) AllowToolForChat(ctx context.Context, access app.Access, chatID, toolName string) error {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := allowToolForChatTx(ctx, tx, chatID, access.UserID, toolName); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ChatActionLog implements agentloop.ActionLog against chat.action_logs.
type ChatActionLog struct {
	Store Store
	Scope agentloop.AccessScope
}

// BeginDial (tx1) inserts (scope_id, call_id) in state="dialing", or returns
// ErrDuplicateAction if it already exists in dialing, done, or simulated.
func (l ChatActionLog) BeginDial(ctx context.Context, rec agentloop.ActionRecord) error {
	scopeID := agentloop.ActionScopeID(rec)
	if scopeID == "" {
		return fmt.Errorf("%w: empty action scope_id", agentloop.ErrInvalidConfig)
	}
	return l.Store.NarrowedTx(ctx, l.Scope, func(tx pgx.Tx) error {
		var existing string
		err := tx.QueryRow(ctx, `
			SELECT state FROM chat.action_logs WHERE scope_id = $1::uuid AND call_id = $2 FOR UPDATE`,
			scopeID, rec.CallID).Scan(&existing)
		if err == nil {
			if existing == string(agentloop.ActionStateDialing) ||
				existing == string(agentloop.ActionStateDone) ||
				existing == string(agentloop.ActionStateSimulated) {
				return fmt.Errorf("%w: state=%s", agentloop.ErrDuplicateAction, existing)
			}
			_, uerr := tx.Exec(ctx, `
				UPDATE chat.action_logs
				SET state = 'dialing', response_digest = '', error_text = '', completed_at = NULL
				WHERE scope_id = $1::uuid AND call_id = $2`, scopeID, rec.CallID)
			return uerr
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("chat: action_log check: %w", err)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO chat.action_logs (
				scope_id, call_id, chat_id, line_id, user_id, tool_name, action, preview,
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
func (l ChatActionLog) FinishDial(ctx context.Context, scopeID, callID string, state agentloop.ActionState, responseDigest, errText string, completedAt time.Time) error {
	if scopeID == "" {
		scopeID = l.Scope.ObjectID
	}
	return l.Store.NarrowedTx(ctx, l.Scope, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE chat.action_logs
			SET state = $3, response_digest = $4, error_text = $5, completed_at = $6
			WHERE scope_id = $1::uuid AND call_id = $2`,
			scopeID, callID, string(state), responseDigest, errText, completedAt.UTC())
		if err != nil {
			return fmt.Errorf("chat: action_log finish: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return app.ErrGone
		}
		return nil
	})
}

// RecordSimulated persists a dry_run simulated write in chat.action_logs.
func (l ChatActionLog) RecordSimulated(ctx context.Context, rec agentloop.ActionRecord) error {
	if agentloop.ActionScopeID(rec) == "" {
		return fmt.Errorf("%w: empty action scope_id", agentloop.ErrInvalidConfig)
	}
	return l.Store.NarrowedTx(ctx, l.Scope, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO chat.action_logs (
				scope_id, call_id, chat_id, line_id, user_id, tool_name, action, preview,
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

// InsertTurnRows writes turn transcript entries into chat.turns inside tx.
func InsertTurnRows(ctx context.Context, tx pgx.Tx, commit agentloop.TurnCommit) error {
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
		if msg.Role == agentloop.RoleAssistant {
			cost = commit.ReportedCostMicroUSD
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO chat.turns (
				chat_id, line_id, seq, role, body, tool_calls, tool_name, tool_id,
				is_error, offloaded, tainted, private_read, model, input_tokens, output_tokens, cost_micro_usd
			) VALUES (
				$1::uuid, NULLIF($2, '')::uuid, $3, $4, $5, $6::jsonb, $7, $8,
				$9, $10, $11, $12, $13, $14, $15, $16
			)`,
			commit.ObjectID, commit.RunID, seq, string(msg.Role), msg.Text, toolCalls,
			msg.ToolName, msg.ToolID, msg.IsError, msg.Offload,
			commit.Exposure.Tainted, commit.Exposure.PrivateRead,
			commit.Model, commit.InputTokens, commit.OutputTokens, cost); err != nil {
			return fmt.Errorf("chat: insert turn: %w", err)
		}
		seq++
	}
	return nil
}
