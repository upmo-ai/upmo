package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
)

// UsageLimits reads and updates gateway.usage_limits and reads
// gateway.usage_spend under row security.
type UsageLimits struct {
	Pool *pgxpool.Pool
}

var _ app.UsageLimitStore = UsageLimits{}

func (u UsageLimits) begin(ctx context.Context, who app.Caller) (pgx.Tx, error) {
	return (Providers(u)).begin(ctx, who)
}

// GetUsageLimits returns the caller's effective personal and workspace limits
// and their own current-period spend, plus every limit and spend row when
// includeAll is true.
func (u UsageLimits) GetUsageLimits(ctx context.Context, who app.Caller, periodStart time.Time, includeAll bool) (app.UsageLimitsView, error) {
	tx, err := u.begin(ctx, who)
	if err != nil {
		return app.UsageLimitsView{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	period := periodStart.Format("2006-01-02")
	rows, err := tx.Query(ctx, `
		SELECT kind, amount_micro_usd
		FROM gateway.usage_limits
		WHERE (kind IN ('workspace', 'person_default', 'ceiling') AND subject_id IS NULL)
		   OR (kind = 'person' AND subject_id = $1::uuid)`,
		who.User,
	)
	if err != nil {
		return app.UsageLimitsView{}, err
	}
	var wsRaw, ceilingRaw, personDefaultRaw, personRaw *int64
	for rows.Next() {
		var kind string
		var amount int64
		if err := rows.Scan(&kind, &amount); err != nil {
			rows.Close()
			return app.UsageLimitsView{}, err
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
		}
	}
	if err := rows.Err(); err != nil {
		return app.UsageLimitsView{}, err
	}

	var spent, held int64
	err = tx.QueryRow(ctx, `
		SELECT spent_micro_usd, held_micro_usd
		FROM gateway.usage_spend
		WHERE kind = 'person'
		  AND subject_id = $1::uuid
		  AND period_start = $2::date`,
		who.User, period,
	).Scan(&spent, &held)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return app.UsageLimitsView{}, err
	}

	out := app.UsageLimitsView{
		PeriodStart: period,
		Own: app.OwnUsageLimits{
			LimitMicroUSD:          domain.EffectivePersonLimit(personRaw, personDefaultRaw),
			WorkspaceLimitMicroUSD: domain.EffectiveWorkspaceLimit(wsRaw, ceilingRaw),
			SpentMicroUSD:          spent,
			HeldMicroUSD:           held,
		},
	}

	if includeAll {
		all, err := listAllUsageLimits(ctx, tx, period)
		if err != nil {
			return app.UsageLimitsView{}, err
		}
		out.Rows = &all
	}

	if err := tx.Commit(ctx); err != nil {
		return app.UsageLimitsView{}, err
	}
	return out, nil
}

func listAllUsageLimits(ctx context.Context, q querier, period string) ([]app.UsageLimitRow, error) {
	rows, err := q.Query(ctx, `
		WITH period_spend AS (
			SELECT kind, subject_id, spent_micro_usd, held_micro_usd
			FROM gateway.usage_spend
			WHERE period_start = $1::date
		)
		SELECT
			COALESCE(l.kind, s.kind) AS kind,
			COALESCE(COALESCE(l.subject_id, s.subject_id)::text, '') AS subject_id,
			l.amount_micro_usd,
			COALESCE(s.spent_micro_usd, 0)::bigint AS spent_micro_usd,
			COALESCE(s.held_micro_usd, 0)::bigint AS held_micro_usd,
			l.updated_at
		FROM gateway.usage_limits AS l
		FULL OUTER JOIN period_spend AS s
			ON l.kind = s.kind
		   AND l.subject_id IS NOT DISTINCT FROM s.subject_id
		ORDER BY
			CASE COALESCE(l.kind, s.kind)
				WHEN 'workspace' THEN 0
				WHEN 'ceiling' THEN 1
				WHEN 'person_default' THEN 2
				WHEN 'person' THEN 3
				WHEN 'automation' THEN 4
				ELSE 5
			END,
			COALESCE(l.subject_id, s.subject_id)::text NULLS FIRST`,
		period,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []app.UsageLimitRow{}
	for rows.Next() {
		var row app.UsageLimitRow
		if err := rows.Scan(&row.Kind, &row.SubjectID, &row.AmountMicroUSD, &row.SpentMicroUSD, &row.HeldMicroUSD, &row.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// SetUsageLimit creates, updates, or deletes one limit row and records an
// audit event in the same transaction. No-op writes record nothing and mark the
// request NoChange.
func (u UsageLimits) SetUsageLimit(ctx context.Context, who app.Caller, kind, subjectID string, amount *int64) (app.UpdatedUsageLimit, error) {
	tx, err := u.begin(ctx, who)
	if err != nil {
		return app.UpdatedUsageLimit{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var oldAmount *int64
	if err := tx.QueryRow(ctx,
		`SELECT gateway.usage_limit_set($1, $2::uuid, $3)`,
		kind, uuidOrNil(subjectID), amount,
	).Scan(&oldAmount); err != nil {
		return app.UpdatedUsageLimit{}, mapLimitWriteError(kind, err)
	}

	if int64PtrEqual(oldAmount, amount) {
		if err := tx.Commit(ctx); err != nil {
			return app.UpdatedUsageLimit{}, err
		}
		audit.NoChange(ctx)
		return app.UpdatedUsageLimit{
			Kind:           kind,
			SubjectID:      subjectID,
			AmountMicroUSD: oldAmount,
		}, nil
	}

	targetID := kind
	fields := []string{"amount_micro_usd", "kind"}
	values := map[string]audit.Change{
		"kind":             {To: kind},
		"amount_micro_usd": {From: nullableInt64(oldAmount), To: nullableInt64(amount)},
	}
	if subjectID != "" {
		targetID = kind + ":" + subjectID
		fields = append(fields, "subject_id")
		values["subject_id"] = audit.Change{To: subjectID}
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.UsageLimitUpdate,
		Outcome:  audit.Success,
		TargetID: targetID,
		Changes: audit.Changes{
			Fields: fields,
			Values: values,
		},
	}); err != nil {
		return app.UpdatedUsageLimit{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return app.UpdatedUsageLimit{}, err
	}
	return app.UpdatedUsageLimit{
		Kind:           kind,
		SubjectID:      subjectID,
		AmountMicroUSD: amount,
	}, nil
}

func int64PtrEqual(a, b *int64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func nullableInt64(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func limitForbiddenFor(kind string) error {
	if kind == domain.LimitCeiling {
		return app.ErrCeilingForbidden
	}
	return app.ErrLimitForbidden
}

func mapLimitWriteError(kind string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return limitForbiddenFor(kind)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42501" {
		return limitForbiddenFor(kind)
	}
	return err
}
