package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

var _ app.Limits = Store{}

type limitTarget struct {
	kind      string
	subjectID string
	limit     *int64
}

func reservationTargets(userID, source, automationID string, wsLimit, personLimit, autoLimit *int64) []limitTarget {
	targets := []limitTarget{{kind: domain.LimitWorkspace, limit: wsLimit}}
	if userID != "" && source != domain.SourceSystem {
		targets = append(targets, limitTarget{kind: domain.LimitPerson, subjectID: userID, limit: personLimit})
	}
	if source == domain.SourceAutomation {
		targets = append(targets, limitTarget{kind: domain.LimitAutomation, subjectID: automationID, limit: autoLimit})
	}
	return targets
}

func lockSpendRow(ctx context.Context, tx pgx.Tx, kind, subjectID, period string) (int64, int64, error) {
	var spent, held int64
	var err error
	if subjectID == "" {
		err = tx.QueryRow(ctx, `
			SELECT spent_micro_usd, held_micro_usd
			FROM gateway.usage_spend
			WHERE kind = $1
			  AND subject_id IS NULL
			  AND period_start = $2::date
			FOR UPDATE`,
			kind, period,
		).Scan(&spent, &held)
	} else {
		err = tx.QueryRow(ctx, `
			SELECT spent_micro_usd, held_micro_usd
			FROM gateway.usage_spend
			WHERE kind = $1
			  AND subject_id = $2::uuid
			  AND period_start = $3::date
			FOR UPDATE`,
			kind, subjectID, period,
		).Scan(&spent, &held)
	}
	return spent, held, err
}

func adjustSpendRow(ctx context.Context, tx pgx.Tx, kind, subjectID, period string, deltaHeld, deltaSpent int64) error {
	var tag pgconn.CommandTag
	var err error
	if subjectID == "" {
		tag, err = tx.Exec(ctx, `
			UPDATE gateway.usage_spend
			SET held_micro_usd = held_micro_usd + $3,
			    spent_micro_usd = spent_micro_usd + $4
			WHERE kind = $1
			  AND subject_id IS NULL
			  AND period_start = $2::date`,
			kind, period, deltaHeld, deltaSpent,
		)
	} else {
		tag, err = tx.Exec(ctx, `
			UPDATE gateway.usage_spend
			SET held_micro_usd = held_micro_usd + $4,
			    spent_micro_usd = spent_micro_usd + $5
			WHERE kind = $1
			  AND subject_id = $2::uuid
			  AND period_start = $3::date`,
			kind, subjectID, period, deltaHeld, deltaSpent,
		)
	}
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("gateway: missing spend row for %s", kind)
	}
	return nil
}

func claimHold(ctx context.Context, tx pgx.Tx, id string) (bool, error) {
	if id == "" {
		return true, nil
	}
	tag, err := tx.Exec(ctx, `DELETE FROM gateway.usage_holds WHERE id = $1::uuid`, id)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// Reserve locks the applicable spend rows for req.PeriodStart in a fixed
// order (workspace, person, automation), verifies every active limit, and adds
// req.Estimate to held_micro_usd. Absent limits remain unlimited; any error
// fails closed and rolls back.
func (s Store) Reserve(ctx context.Context, req app.ReserveRequest) (app.Reservation, error) {
	if s.Pool == nil {
		return app.Reservation{}, fmt.Errorf("gateway: no database")
	}
	if req.PeriodStart.IsZero() {
		return app.Reservation{}, fmt.Errorf("gateway: limit period is required")
	}
	if req.Estimate < 0 {
		return app.Reservation{}, fmt.Errorf("gateway: negative reserve estimate")
	}
	if req.UserID != "" {
		if _, err := uuid.Parse(req.UserID); err != nil {
			return app.Reservation{}, fmt.Errorf("gateway: invalid user id")
		}
	}
	automationID := ""
	if req.Source == domain.SourceAutomation {
		if _, err := uuid.Parse(req.AutomationID); err != nil {
			return app.Reservation{}, fmt.Errorf("gateway: invalid automation id")
		}
		automationID = req.AutomationID
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return app.Reservation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT kind, amount_micro_usd
		FROM gateway.usage_limits
		WHERE (kind IN ('workspace', 'person_default', 'ceiling') AND subject_id IS NULL)
		   OR (kind = 'person' AND subject_id = $1::uuid)
		   OR (kind = 'automation' AND subject_id = $2::uuid)`,
		uuidOrNil(req.UserID), uuidOrNil(automationID),
	)
	if err != nil {
		return app.Reservation{}, err
	}
	var wsRaw, ceilingRaw, personDefaultRaw, personRaw, autoRaw *int64
	for rows.Next() {
		var kind string
		var amount int64
		if err := rows.Scan(&kind, &amount); err != nil {
			rows.Close()
			return app.Reservation{}, err
		}
		v := amount
		switch kind {
		case domain.LimitWorkspace:
			wsRaw = &v
		case domain.LimitCeiling:
			ceilingRaw = &v
		case domain.LimitPersonDefault:
			personDefaultRaw = &v
		case domain.LimitPerson:
			personRaw = &v
		case domain.LimitAutomation:
			autoRaw = &v
		}
	}
	if err := rows.Err(); err != nil {
		return app.Reservation{}, err
	}

	period := req.PeriodStart.Format("2006-01-02")
	targets := reservationTargets(
		req.UserID, req.Source, automationID,
		domain.EffectiveWorkspaceLimit(wsRaw, ceilingRaw),
		domain.EffectivePersonLimit(personRaw, personDefaultRaw),
		autoRaw,
	)

	// Acquire spend row locks and update held balances in a fixed global order
	// (workspace -> person -> automation) so concurrent calls cannot deadlock.
	for _, target := range targets {
		if _, err := tx.Exec(ctx, `
			INSERT INTO gateway.usage_spend (kind, subject_id, period_start)
			VALUES ($1, $2::uuid, $3::date)
			ON CONFLICT (kind, subject_id, period_start) DO NOTHING`,
			target.kind, uuidOrNil(target.subjectID), period,
		); err != nil {
			return app.Reservation{}, err
		}
		spent, held, err := lockSpendRow(ctx, tx, target.kind, target.subjectID, period)
		if err != nil {
			return app.Reservation{}, err
		}
		if err := domain.CheckLimit(target.kind, target.limit, spent, held, req.Estimate); err != nil {
			return app.Reservation{}, err
		}
		if err := adjustSpendRow(ctx, tx, target.kind, target.subjectID, period, req.Estimate, 0); err != nil {
			return app.Reservation{}, err
		}
	}

	var holdID string
	if req.Estimate > 0 {
		if err := tx.QueryRow(ctx, `INSERT INTO gateway.usage_holds DEFAULT VALUES RETURNING id::text`).Scan(&holdID); err != nil {
			return app.Reservation{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return app.Reservation{}, err
	}
	return app.Reservation{
		ID:           holdID,
		PeriodStart:  req.PeriodStart,
		Amount:       req.Estimate,
		UserID:       req.UserID,
		Source:       req.Source,
		AutomationID: automationID,
	}, nil
}

func reservationTargetsFor(res app.Reservation) []limitTarget {
	return reservationTargets(res.UserID, res.Source, res.AutomationID, nil, nil, nil)
}

// Settle inserts the ledger row and updates usage_spend (releasing the hold
// and adding real cost when charged_to_limit) in one transaction. An attempt is
// settled or released at most once.
func (s Store) Settle(ctx context.Context, res app.Reservation, row domain.Entry) error {
	if s.Pool == nil {
		return fmt.Errorf("gateway: no database")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	claimed, err := claimHold(ctx, tx, res.ID)
	if err != nil {
		return err
	}
	if !claimed {
		var settled bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM gateway.ledger WHERE hold_id = $1::uuid)`, res.ID).Scan(&settled); err != nil {
			return err
		}
		if settled {
			return nil
		}
		return fmt.Errorf("gateway: reservation %s already claimed", res.ID)
	}

	if err := insertLedger(ctx, tx, row, res.ID); err != nil {
		return err
	}
	var deltaSpent int64
	if row.ChargedToLimit && row.CostMicroUSD != nil {
		deltaSpent = *row.CostMicroUSD
	}
	period := res.PeriodStart.Format("2006-01-02")
	for _, target := range reservationTargetsFor(res) {
		if err := adjustSpendRow(ctx, tx, target.kind, target.subjectID, period, -res.Amount, deltaSpent); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// Release drops an unsettled reservation hold without writing a ledger row or
// charging spent_micro_usd. It is safe to call twice: an attempt is settled or
// released at most once.
func (s Store) Release(ctx context.Context, res app.Reservation) error {
	if res.Amount <= 0 {
		return nil
	}
	if s.Pool == nil {
		return fmt.Errorf("gateway: no database")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	claimed, err := claimHold(ctx, tx, res.ID)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}

	period := res.PeriodStart.Format("2006-01-02")
	for _, target := range reservationTargetsFor(res) {
		if err := adjustSpendRow(ctx, tx, target.kind, target.subjectID, period, -res.Amount, 0); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// SpendMismatch describes one discrepancy between gateway.usage_spend and the
// charged_to_limit sum from gateway.ledger (or a stuck hold on an ended
// period) for a subject and period.
type SpendMismatch struct {
	Kind           string
	SubjectID      string
	PeriodStart    time.Time
	SpendMicroUSD  int64
	LedgerMicroUSD int64
	HeldMicroUSD   int64
}

// CheckSpend compares gateway.usage_spend.spent_micro_usd against
// SUM(cost_micro_usd) WHERE charged_to_limit from gateway.ledger grouped by
// (kind, subject_id, period_start) in timeZone, and reports any held_micro_usd
// > 0 remaining on periods that have ended. It never mutates counters.
func CheckSpend(ctx context.Context, pool *pgxpool.Pool, timeZone string) ([]SpendMismatch, error) {
	if pool == nil {
		return nil, fmt.Errorf("gateway: no database")
	}
	if timeZone == "" {
		timeZone = "UTC"
	}
	rows, err := pool.Query(ctx, `
		WITH ledger_sums AS (
			SELECT kind, subject_id, period_start, SUM(cost_micro_usd)::bigint AS total_micro_usd
			FROM (
				SELECT 'workspace'::text AS kind,
					NULL::uuid AS subject_id,
					date_trunc('month', at AT TIME ZONE $1)::date AS period_start,
					COALESCE(cost_micro_usd, 0) AS cost_micro_usd
				FROM gateway.ledger
				WHERE charged_to_limit
				UNION ALL
				SELECT 'person'::text AS kind,
					user_id AS subject_id,
					date_trunc('month', at AT TIME ZONE $1)::date AS period_start,
					COALESCE(cost_micro_usd, 0) AS cost_micro_usd
				FROM gateway.ledger
				WHERE charged_to_limit AND user_id IS NOT NULL AND source <> 'system'
				UNION ALL
				SELECT 'automation'::text AS kind,
					automation_id AS subject_id,
					date_trunc('month', at AT TIME ZONE $1)::date AS period_start,
					COALESCE(cost_micro_usd, 0) AS cost_micro_usd
				FROM gateway.ledger
				WHERE charged_to_limit AND source = 'automation' AND automation_id IS NOT NULL
			) AS entries
			GROUP BY kind, subject_id, period_start
		)
		SELECT
			COALESCE(s.kind, l.kind) AS kind,
			COALESCE(COALESCE(s.subject_id, l.subject_id)::text, '') AS subject_id,
			COALESCE(s.period_start, l.period_start) AS period_start,
			COALESCE(s.spent_micro_usd, 0)::bigint AS spent_micro_usd,
			COALESCE(l.total_micro_usd, 0)::bigint AS ledger_micro_usd,
			CASE WHEN s.period_start < date_trunc('month', now() AT TIME ZONE $1)::date THEN COALESCE(s.held_micro_usd, 0) ELSE 0 END::bigint AS held_micro_usd
		FROM gateway.usage_spend AS s
		FULL OUTER JOIN ledger_sums AS l
			ON s.kind = l.kind
		   AND COALESCE(s.subject_id, '00000000-0000-0000-0000-000000000000'::uuid)
		     = COALESCE(l.subject_id, '00000000-0000-0000-0000-000000000000'::uuid)
		   AND s.period_start = l.period_start
		WHERE COALESCE(s.spent_micro_usd, 0) <> COALESCE(l.total_micro_usd, 0)
		   OR (COALESCE(s.held_micro_usd, 0) > 0 AND s.period_start < date_trunc('month', now() AT TIME ZONE $1)::date)
		ORDER BY 3, 1, 2`, timeZone)
	if err != nil {
		return nil, fmt.Errorf("gateway: check spend: %w", err)
	}
	defer rows.Close()
	var out []SpendMismatch
	for rows.Next() {
		var m SpendMismatch
		if err := rows.Scan(&m.Kind, &m.SubjectID, &m.PeriodStart, &m.SpendMicroUSD, &m.LedgerMicroUSD, &m.HeldMicroUSD); err != nil {
			return nil, fmt.Errorf("gateway: check spend: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("gateway: check spend: %w", err)
	}
	return out, nil
}
