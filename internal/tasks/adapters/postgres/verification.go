package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/tasks/app"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

const spendUpdateSQL = `
UPDATE tasks.tasks
SET spent = $2, paused = $3, updated_at = now()
WHERE id = $1::uuid
  AND owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid`

const recordUpdateSQL = `
UPDATE tasks.tasks
SET passes = $2,
    plan = CASE WHEN $3::jsonb IS NULL THEN plan ELSE $3::jsonb END,
    updated_at = now()
WHERE id = $1::uuid
  AND owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid`

const recordInsertSQL = `
INSERT INTO tasks.checks (
  task_id, batch, seq, name, code, unit, criterion, kind, verdict, digest, excerpt, regressed
) VALUES (
  $1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
)`

// Record locks the task on the owner pool, appends one check batch, and records whether it passed.
func (s Store) Record(ctx context.Context, access app.Access, taskID string, checks []domain.Check, inputs map[int]string) (bool, error) {
	tx, err := s.beginOwner(ctx, access)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status, ownerID string
	var planRaw []byte
	err = tx.QueryRow(ctx, `
		SELECT status, plan, owner_user_id::text
		FROM tasks.tasks
		WHERE id = $1::uuid
		FOR UPDATE`, taskID).Scan(&status, &planRaw, &ownerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, app.ErrGone
	}
	if err != nil {
		return false, fmt.Errorf("tasks: lock: %w", err)
	}
	if ownerID != access.UserID {
		return false, app.ErrAccess
	}
	if domain.Status(status) != domain.Verifying {
		return false, domain.ErrInput
	}

	var plan *domain.TaskPlan
	if planRaw != nil {
		plan = &domain.TaskPlan{}
		if err := json.Unmarshal(planRaw, plan); err != nil {
			return false, fmt.Errorf("tasks: plan: %w", err)
		}
	}

	var batch int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(batch), 0) + 1
		FROM tasks.checks
		WHERE task_id = $1::uuid`, taskID).Scan(&batch); err != nil {
		return false, fmt.Errorf("tasks: check batch: %w", err)
	}

	stored := make([]domain.Check, len(checks))
	touched := map[int]struct{}{}
	for index, check := range checks {
		check = normalizeRecorded(check)
		if check.Digest == "" {
			if crit, ok := criterionAt(plan, check.Unit, check.Criterion); ok {
				check.Digest = domain.CheckDigest(crit, inputs[check.Unit])
			}
		}
		if inputs[check.Unit] != "" {
			touched[check.Unit] = struct{}{}
		}
		if _, err := tx.Exec(ctx, recordInsertSQL,
			taskID, batch, index+1, check.Name, check.Code, check.Unit, check.Criterion,
			string(check.Kind), string(check.Verdict), check.Digest, check.Excerpt, check.Regressed); err != nil {
			return false, fmt.Errorf("tasks: check: %w", err)
		}
		check.Batch = batch
		check.Seq = index + 1
		stored[index] = check
	}
	if plan != nil {
		for i := range plan.Units {
			if _, ok := touched[plan.Units[i].Unit]; !ok {
				continue
			}
			in := inputs[plan.Units[i].Unit]
			for j := range plan.Units[i].Criteria {
				plan.Units[i].Criteria[j].Digest = domain.CheckDigest(plan.Units[i].Criteria[j], in)
			}
		}
	}

	passes, planArg, err := recordedPasses(ctx, tx, taskID, plan, stored, inputs)
	if err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, recordUpdateSQL, taskID, passes, planArg)
	if err != nil {
		return false, fmt.Errorf("tasks: passes: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return false, app.ErrGone
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return passes, nil
}

func normalizeRecorded(check domain.Check) domain.Check {
	if check.Kind == "" {
		check.Kind = domain.CheckCommand
	}
	if check.Verdict == "" {
		if check.Code == 0 {
			check.Verdict = domain.VerdictPass
		} else {
			check.Verdict = domain.VerdictFail
		}
	}
	return check
}

func criterionAt(plan *domain.TaskPlan, unit, criterion int) (domain.Criterion, bool) {
	if plan == nil {
		return domain.Criterion{}, false
	}
	for _, item := range plan.Units {
		if item.Unit != unit {
			continue
		}
		for _, crit := range item.Criteria {
			if crit.Criterion == criterion {
				return crit, true
			}
		}
	}
	return domain.Criterion{}, false
}

func recordedPasses(ctx context.Context, tx pgx.Tx, taskID string, plan *domain.TaskPlan, stored []domain.Check, inputs map[int]string) (bool, any, error) {
	if plan == nil {
		return domain.Passes(stored), nil, nil
	}
	checks, err := loadChecks(ctx, tx, taskID)
	if err != nil {
		return false, nil, err
	}
	findings, err := loadFindings(ctx, tx, taskID)
	if err != nil {
		return false, nil, err
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return false, nil, domain.ErrPlan
	}
	return domain.PlanPasses(plan, checks, findings, inputs), raw, nil
}

func loadChecks(ctx context.Context, tx pgx.Tx, taskID string) ([]domain.Check, error) {
	rows, err := tx.Query(ctx, `
		SELECT batch, seq, unit, criterion, name, code, kind, verdict, digest, excerpt, regressed
		FROM tasks.checks
		WHERE task_id = $1::uuid
		ORDER BY batch ASC, seq ASC`, taskID)
	if err != nil {
		return nil, fmt.Errorf("tasks: checks: %w", err)
	}
	defer rows.Close()
	var out []domain.Check
	for rows.Next() {
		var check domain.Check
		var kind, verdict string
		if err := rows.Scan(&check.Batch, &check.Seq, &check.Unit, &check.Criterion, &check.Name, &check.Code, &kind, &verdict, &check.Digest, &check.Excerpt, &check.Regressed); err != nil {
			return nil, fmt.Errorf("tasks: checks: %w", err)
		}
		check.Kind = domain.CheckKind(kind)
		check.Verdict = domain.Verdict(verdict)
		out = append(out, check)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tasks: checks: %w", err)
	}
	return out, nil
}

func loadFindings(ctx context.Context, tx pgx.Tx, taskID string) ([]domain.ReviewFinding, error) {
	rows, err := tx.Query(ctx, `
		SELECT seq, unit, criterion, title, detail, evidence, blocking, state, round, settled_by::text
		FROM tasks.review_findings
		WHERE task_id = $1::uuid
		ORDER BY seq ASC`, taskID)
	if err != nil {
		return nil, fmt.Errorf("tasks: findings: %w", err)
	}
	defer rows.Close()
	var out []domain.ReviewFinding
	for rows.Next() {
		var finding domain.ReviewFinding
		var state string
		var settled *string
		if err := rows.Scan(&finding.Seq, &finding.Unit, &finding.Criterion, &finding.Title, &finding.Detail, &finding.Evidence, &finding.Blocking, &state, &finding.Round, &settled); err != nil {
			return nil, fmt.Errorf("tasks: findings: %w", err)
		}
		finding.State = domain.FindingState(state)
		if settled != nil {
			finding.SettledBy = *settled
		}
		out = append(out, finding)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tasks: findings: %w", err)
	}
	return out, nil
}

// SavePlan stores a plan on a task that is still open. The owner pool writes it.
func (s Store) SavePlan(ctx context.Context, access app.Access, taskID string, plan domain.TaskPlan, digest string) error {
	raw, err := json.Marshal(plan)
	if err != nil {
		return domain.ErrPlan
	}
	return s.ownerWrite(ctx, access, taskID, `
		UPDATE tasks.tasks
		SET plan = $2::jsonb, plan_digest = $3, passes = false, updated_at = now()
		WHERE id = $1::uuid
		  AND owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
		  AND status NOT IN ('Done', 'Failed')`, raw, digest)
}

// SaveBrake stores the loop-brake counters on a task that is still open.
func (s Store) SaveBrake(ctx context.Context, access app.Access, taskID string, state domain.BrakeState) error {
	return s.ownerWrite(ctx, access, taskID, `
		UPDATE tasks.tasks
		SET stalls = $2, failures = $3, replanned = $4, last_gap = $5, updated_at = now()
		WHERE id = $1::uuid
		  AND owner_user_id = NULLIF(current_setting('upmo.user_id', true), '')::uuid
		  AND status NOT IN ('Done', 'Failed')`, state.Stalls, state.Failures, state.Replanned, state.LastGap)
}

func (s Store) ownerWrite(ctx context.Context, access app.Access, taskID, query string, args ...any) error {
	tx, err := s.beginOwner(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status, ownerID string
	err = tx.QueryRow(ctx, `
		SELECT status, owner_user_id::text
		FROM tasks.tasks
		WHERE id = $1::uuid
		FOR UPDATE`, taskID).Scan(&status, &ownerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrGone
	}
	if err != nil {
		return fmt.Errorf("tasks: lock: %w", err)
	}
	if ownerID != access.UserID {
		return app.ErrAccess
	}
	if domain.Status(status) == domain.Done || domain.Status(status) == domain.Failed {
		return domain.ErrTerminal
	}
	params := append([]any{taskID}, args...)
	tag, err := tx.Exec(ctx, query, params...)
	if err != nil {
		return fmt.Errorf("tasks: write: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return app.ErrGone
	}
	return tx.Commit(ctx)
}
