package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

// ClaimState is the task and run state returned by ClaimRun.
type ClaimState struct {
	Token       string
	Status      domain.Status
	Tainted     bool
	PrivateRead bool
	TurnSeq     int
}

// ClaimRun acquires or takes over an expired runner lease for turn on runID.
func (s Store) ClaimRun(ctx context.Context, access app.Access, taskID, runID string, turn int) (ClaimState, error) {
	if _, err := uuid.Parse(access.UserID); err != nil || !hasPermission(access.Allow, "task:edit") {
		return ClaimState{}, app.ErrAccess
	}
	if _, err := uuid.Parse(taskID); err != nil {
		return ClaimState{}, app.ErrTask
	}
	if _, err := uuid.Parse(runID); err != nil {
		return ClaimState{}, app.ErrRun
	}

	tx, err := s.beginRunner(ctx, access)
	if err != nil {
		return ClaimState{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var ownerID, status, claimToken string
	var runnerOwned, paused, tainted, privateRead, leaseLive bool
	err = tx.QueryRow(ctx, `
		SELECT owner_user_id::text, runner_owned, status, paused, tainted, private_read,
		       COALESCE(claim_token::text, ''), COALESCE(lease_until >= now(), false)
		FROM tasks.tasks
		WHERE id = $1::uuid
		FOR UPDATE`, taskID).Scan(
		&ownerID, &runnerOwned, &status, &paused, &tainted, &privateRead,
		&claimToken, &leaseLive,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ClaimState{}, app.ErrGone
	}
	if err != nil {
		return ClaimState{}, fmt.Errorf("tasks: lock claim: %w", err)
	}
	if ownerID != access.UserID {
		return ClaimState{}, app.ErrAccess
	}

	var runState string
	var turnSeq int
	err = tx.QueryRow(ctx, `
		SELECT state, turn_seq
		FROM tasks.runs
		WHERE id = $1::uuid AND task_id = $2::uuid
		FOR UPDATE`, runID, taskID).Scan(&runState, &turnSeq)
	if errors.Is(err, pgx.ErrNoRows) {
		return ClaimState{}, app.ErrNotRunnable
	}
	if err != nil {
		return ClaimState{}, fmt.Errorf("tasks: lock run claim: %w", err)
	}

	// Replay check runs before runnable checks so settled turns always return ErrAlready.
	if turn <= turnSeq {
		return ClaimState{}, app.ErrAlready
	}

	if !runnerOwned ||
		domain.RunState(runState) != domain.RunAdmitted ||
		(domain.Status(status) != domain.Planning && domain.Status(status) != domain.Working) ||
		paused {
		return ClaimState{}, app.ErrNotRunnable
	}

	var pending bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM tasks.approvals
			WHERE task_id = $1::uuid AND state = 'pending'
		)`, taskID).Scan(&pending); err != nil {
		return ClaimState{}, fmt.Errorf("tasks: claim pending approval: %w", err)
	}
	if pending {
		return ClaimState{}, app.ErrNotRunnable
	}

	if claimToken != "" && leaseLive {
		return ClaimState{}, app.ErrBusy
	}

	if turn != turnSeq+1 {
		return ClaimState{}, app.ErrNotRunnable
	}

	var token string
	err = tx.QueryRow(ctx, `
		UPDATE tasks.tasks
		SET claim_token = uuidv7(),
		    lease_until = now() + ($2::bigint * interval '1 microsecond'),
		    reserved = 0,
		    updated_at = now()
		WHERE id = $1::uuid
		  AND owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
		RETURNING claim_token::text`, taskID, app.ClaimLease.Microseconds()).Scan(&token)
	if errors.Is(err, pgx.ErrNoRows) {
		return ClaimState{}, app.ErrGone
	}
	if err != nil {
		return ClaimState{}, fmt.Errorf("tasks: take claim: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return ClaimState{}, err
	}
	return ClaimState{
		Token:       token,
		Status:      domain.Status(status),
		Tainted:     tainted,
		PrivateRead: privateRead,
		TurnSeq:     turnSeq,
	}, nil
}

// Reserve holds x micro-USD against a live runner claim, pausing on budget exhaustion.
func (s Store) Reserve(ctx context.Context, access app.Access, taskID, token string, x int64) error {
	if _, err := uuid.Parse(access.UserID); err != nil {
		return app.ErrAccess
	}
	if _, err := uuid.Parse(taskID); err != nil {
		return app.ErrTask
	}

	tx, err := s.beginRunner(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var ownerID, claimToken string
	var budget, held, spent, reserved int64
	var paused, leaseLive bool
	err = tx.QueryRow(ctx, `
		SELECT owner_user_id::text, budget, held, spent, reserved, paused,
		       COALESCE(claim_token::text, ''), COALESCE(lease_until >= now(), false)
		FROM tasks.tasks
		WHERE id = $1::uuid
		FOR UPDATE`, taskID).Scan(
		&ownerID, &budget, &held, &spent, &reserved, &paused,
		&claimToken, &leaseLive,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrGone
	}
	if err != nil {
		return fmt.Errorf("tasks: lock reserve: %w", err)
	}
	if ownerID != access.UserID {
		return app.ErrAccess
	}
	if _, err := uuid.Parse(token); err != nil || claimToken == "" || claimToken != token || !leaseLive {
		return app.ErrFenced
	}
	if x <= 0 || paused {
		return app.ErrNotRunnable
	}
	if reserved != 0 {
		return app.ErrReserved
	}

	// held <= budget <= 1_000_000_000 and spent >= 0, so (budget - held) - spent stays in int64.
	remaining := (budget - held) - spent
	if remaining < x {
		tag, err := tx.Exec(ctx, `
			UPDATE tasks.tasks
			SET paused = true, updated_at = now()
			WHERE id = $1::uuid
			  AND owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid`, taskID)
		if err != nil {
			return fmt.Errorf("tasks: pause reserve: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return app.ErrGone
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return app.ErrReserve
	}

	tag, err := tx.Exec(ctx, `
		UPDATE tasks.tasks
		SET reserved = $2, updated_at = now()
		WHERE id = $1::uuid
		  AND owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid`, taskID, x)
	if err != nil {
		return fmt.Errorf("tasks: set reserved: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return app.ErrGone
	}
	return tx.Commit(ctx)
}

// Release clears the claim and reservation only if claim_token still matches token.
func (s Store) Release(ctx context.Context, access app.Access, taskID, token string) error {
	if _, err := uuid.Parse(access.UserID); err != nil {
		return app.ErrAccess
	}
	if _, err := uuid.Parse(taskID); err != nil {
		return app.ErrTask
	}

	tx, err := s.beginRunner(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var ownerID string
	err = tx.QueryRow(ctx, `
		SELECT owner_user_id::text
		FROM tasks.tasks
		WHERE id = $1::uuid
		FOR UPDATE`, taskID).Scan(&ownerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrGone
	}
	if err != nil {
		return fmt.Errorf("tasks: lock release: %w", err)
	}
	if ownerID != access.UserID {
		return app.ErrAccess
	}
	if _, err := uuid.Parse(token); err != nil {
		return tx.Commit(ctx)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE tasks.tasks
		SET claim_token = NULL,
		    lease_until = NULL,
		    reserved = 0,
		    updated_at = now()
		WHERE id = $1::uuid
		  AND owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
		  AND claim_token = $2::uuid`, taskID, token); err != nil {
		return fmt.Errorf("tasks: release claim: %w", err)
	}
	return tx.Commit(ctx)
}

// SettleTx settles a live claim inside the caller's runner transaction.
func (s Store) SettleTx(ctx context.Context, tx pgx.Tx, taskID, runID, token string, actual int64) error {
	if _, err := uuid.Parse(taskID); err != nil {
		return app.ErrTask
	}
	if _, err := uuid.Parse(runID); err != nil {
		return app.ErrRun
	}
	if _, err := uuid.Parse(token); err != nil {
		return app.ErrFenced
	}
	if actual < 0 {
		return domain.ErrBudget
	}

	var budget, held, spent int64
	var paused bool
	var claimToken string
	err := tx.QueryRow(ctx, `
		SELECT budget, held, spent, paused, COALESCE(claim_token::text, '')
		FROM tasks.tasks
		WHERE id = $1::uuid
		FOR UPDATE`, taskID).Scan(&budget, &held, &spent, &paused, &claimToken)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrGone
	}
	if err != nil {
		return fmt.Errorf("tasks: lock settle: %w", err)
	}
	if claimToken == "" || claimToken != token {
		return app.ErrFenced
	}

	if err := insertSettlement(ctx, tx, taskID, token, actual); err != nil {
		return err
	}
	if err := applySettlementCharge(ctx, tx, taskID, budget, held, spent, actual, paused, true); err != nil {
		return err
	}

	tag, err := tx.Exec(ctx, `
		UPDATE tasks.runs
		SET turn_seq = turn_seq + 1
		WHERE id = $1::uuid AND task_id = $2::uuid`, runID, taskID)
	if err != nil {
		return fmt.Errorf("tasks: advance turn_seq: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return app.ErrNotRunnable
	}
	return nil
}

// SettleOrphan charges actual for a lost or stopped claim without touching any live claim or reservation.
func (s Store) SettleOrphan(ctx context.Context, access app.Access, taskID, token string, actual int64) error {
	if _, err := uuid.Parse(access.UserID); err != nil {
		return app.ErrAccess
	}
	if _, err := uuid.Parse(taskID); err != nil {
		return app.ErrTask
	}
	if _, err := uuid.Parse(token); err != nil {
		return app.ErrFenced
	}
	if actual < 0 {
		return domain.ErrBudget
	}

	tx, err := s.beginRunner(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var ownerID string
	var budget, held, spent int64
	var paused bool
	err = tx.QueryRow(ctx, `
		SELECT owner_user_id::text, budget, held, spent, paused
		FROM tasks.tasks
		WHERE id = $1::uuid
		FOR UPDATE`, taskID).Scan(&ownerID, &budget, &held, &spent, &paused)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrGone
	}
	if err != nil {
		return fmt.Errorf("tasks: lock orphan settle: %w", err)
	}
	if ownerID != access.UserID {
		return app.ErrAccess
	}

	if err := insertSettlement(ctx, tx, taskID, token, actual); err != nil {
		return err
	}
	if err := applySettlementCharge(ctx, tx, taskID, budget, held, spent, actual, paused, false); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const (
	settleClearExactSQL = `
		UPDATE tasks.tasks
		SET spent = $2,
		    paused = $3,
		    reserved = 0,
		    claim_token = NULL,
		    lease_until = NULL,
		    updated_at = now()
		WHERE id = $1::uuid
		  AND owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid`

	settleClearSaturateSQL = `
		UPDATE tasks.tasks
		SET spent = CASE WHEN $2::bigint > 9223372036854775807 - spent THEN 9223372036854775807 ELSE spent + $2::bigint END,
		    paused = paused OR (CASE WHEN $2::bigint > 9223372036854775807 - spent THEN 9223372036854775807 ELSE spent + $2::bigint END) >= budget - held,
		    reserved = 0,
		    claim_token = NULL,
		    lease_until = NULL,
		    updated_at = now()
		WHERE id = $1::uuid
		  AND owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid`

	settleOrphanExactSQL = `
		UPDATE tasks.tasks
		SET spent = $2,
		    paused = $3,
		    updated_at = now()
		WHERE id = $1::uuid
		  AND owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid`

	settleOrphanSaturateSQL = `
		UPDATE tasks.tasks
		SET spent = CASE WHEN $2::bigint > 9223372036854775807 - spent THEN 9223372036854775807 ELSE spent + $2::bigint END,
		    paused = paused OR (CASE WHEN $2::bigint > 9223372036854775807 - spent THEN 9223372036854775807 ELSE spent + $2::bigint END) >= budget - held,
		    updated_at = now()
		WHERE id = $1::uuid
		  AND owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid`
)

func insertSettlement(ctx context.Context, tx pgx.Tx, taskID, token string, actual int64) error {
	tag, err := tx.Exec(ctx, `
		INSERT INTO tasks.settlements (task_id, claim_token, amount)
		VALUES ($1::uuid, $2::uuid, $3)
		ON CONFLICT (task_id, claim_token) DO NOTHING`, taskID, token, actual)
	if err != nil {
		return fmt.Errorf("tasks: insert settlement: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return app.ErrAlready
	}
	return nil
}

func applySettlementCharge(ctx context.Context, tx pgx.Tx, taskID string, budget, held, spent, actual int64, paused, clearClaim bool) error {
	nextSpent, nextPaused, chargeErr := domain.Charge(budget, spent, actual, paused)
	if chargeErr != nil {
		if !errors.Is(chargeErr, domain.ErrExhausted) && !errors.Is(chargeErr, domain.ErrBudget) {
			return chargeErr
		}
		query := settleOrphanSaturateSQL
		if clearClaim {
			query = settleClearSaturateSQL
		}
		tag, err := tx.Exec(ctx, query, taskID, actual)
		if err != nil {
			return fmt.Errorf("tasks: settle task: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return app.ErrGone
		}
		return nil
	}
	nextPaused = nextPaused || nextSpent >= budget-held
	query := settleOrphanExactSQL
	if clearClaim {
		query = settleClearExactSQL
	}
	tag, err := tx.Exec(ctx, query, taskID, nextSpent, nextPaused)
	if err != nil {
		return fmt.Errorf("tasks: settle task: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return app.ErrGone
	}
	return nil
}

func hasPermission(allow, want string) bool {
	for _, part := range strings.Split(allow, ",") {
		if strings.TrimSpace(part) == want {
			return true
		}
	}
	return false
}
