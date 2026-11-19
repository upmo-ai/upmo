package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

// CheckAccount holds the owner-pool view of a provider account needed by the
// daily background check job.
type CheckAccount struct {
	ID             string
	Name           string
	Provider       string
	Adapter        string
	PlanKind       string
	BaseURL        string
	Status         string
	Enabled        bool
	CredentialRef  string
	HasKey         bool
	OwnerUserID    string
	Visibility     string
	House          bool
	NotifiedCauses []string
}

// ProbeApplyResult summarizes the model state and route links observed when an
// account check succeeds with ClassOK.
type ProbeApplyResult struct {
	ProviderModels   []string
	RetiredModels    []string
	RouteModelsInUse []string
}

// ListEnabledAccountIDs returns every enabled provider account id on the owner pool.
func ListEnabledAccountIDs(ctx context.Context, owner *pgxpool.Pool) ([]string, error) {
	if owner == nil {
		return nil, fmt.Errorf("gateway: no database")
	}
	rows, err := owner.Query(ctx, `SELECT id::text FROM gateway.provider_accounts WHERE enabled ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GetAccountForCheck loads one account on the owner pool for background probing.
func GetAccountForCheck(ctx context.Context, owner *pgxpool.Pool, id string) (CheckAccount, bool, error) {
	if owner == nil {
		return CheckAccount{}, false, fmt.Errorf("gateway: no database")
	}
	if _, err := uuid.Parse(id); err != nil {
		return CheckAccount{}, false, nil
	}
	var a CheckAccount
	err := owner.QueryRow(ctx, `
		SELECT a.id::text, a.name, COALESCE(NULLIF(a.provider, ''), a.adapter), a.adapter,
			a.plan_kind, a.base_url, a.status, a.enabled, a.credential_ref,
			EXISTS (SELECT 1 FROM gateway.credentials AS c WHERE c.ref = a.credential_ref),
			COALESCE(a.owner_user_id::text, ''), a.visibility, a.house, a.notified_causes
		FROM gateway.provider_accounts AS a
		WHERE a.id = $1::uuid`, id).Scan(
		&a.ID, &a.Name, &a.Provider, &a.Adapter,
		&a.PlanKind, &a.BaseURL, &a.Status, &a.Enabled, &a.CredentialRef,
		&a.HasKey, &a.OwnerUserID, &a.Visibility, &a.House, &a.NotifiedCauses,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return CheckAccount{}, false, nil
	}
	if err != nil {
		return CheckAccount{}, false, err
	}
	return a, true, nil
}

// UpdateAccountCheckStatus records a non-OK probe status without touching
// discovered models, miss counters, or retired flags.
func UpdateAccountCheckStatus(ctx context.Context, owner *pgxpool.Pool, id, status string) error {
	if owner == nil {
		return fmt.Errorf("gateway: no database")
	}
	_, err := owner.Exec(ctx, `
		UPDATE gateway.provider_accounts
		SET status = $2, checked_at = now(), updated_at = now()
		WHERE id = $1::uuid`, id, status)
	return err
}

// ApplyAccountProbe stores a ClassOK probe outcome on the owner pool: updates
// status to Connected, clears 'key_rejected', updates account_models_seen and
// provider_models misses/retired when models is non-nil and within limits, and
// clears 'retired:<model>' for any model no longer retired.
func ApplyAccountProbe(ctx context.Context, owner *pgxpool.Pool, id string, models []domain.SeenModel) (ProbeApplyResult, error) {
	if owner == nil {
		return ProbeApplyResult{}, fmt.Errorf("gateway: no database")
	}
	tx, err := owner.Begin(ctx)
	if err != nil {
		return ProbeApplyResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	res, err := ApplyAccountProbeTx(ctx, tx, id, models)
	if err != nil {
		return ProbeApplyResult{}, err
	}
	return res, tx.Commit(ctx)
}

// ApplyAccountProbeTx applies a ClassOK probe outcome inside tx, locking the
// account row first before reading or updating model discovery and miss state.
func ApplyAccountProbeTx(ctx context.Context, tx pgx.Tx, id string, models []domain.SeenModel) (ProbeApplyResult, error) {
	if tx == nil {
		return ProbeApplyResult{}, fmt.Errorf("gateway: no transaction")
	}
	var causes []string
	err := tx.QueryRow(ctx, `
		UPDATE gateway.provider_accounts
		SET status = $2,
			checked_at = now(),
			updated_at = now(),
			notified_causes = array_remove(notified_causes, 'key_rejected')
		WHERE id = $1::uuid
		RETURNING notified_causes`, id, domain.StatusConnected).Scan(&causes)
	if err != nil {
		return ProbeApplyResult{}, err
	}

	var seenSet map[string]bool
	updateModels := models != nil && len(models) <= domain.MaxSeenModels
	if updateModels {
		seenSet, err = replaceSeenModels(ctx, tx, id, models)
		if err != nil {
			return ProbeApplyResult{}, err
		}
	}

	rows, err := tx.Query(ctx, `
		SELECT model, misses, retired
		FROM gateway.provider_models
		WHERE account_id = $1::uuid
		ORDER BY position
		FOR UPDATE`, id)
	if err != nil {
		return ProbeApplyResult{}, err
	}
	type pmRow struct {
		model   string
		misses  int
		retired bool
	}
	var pmRows []pmRow
	for rows.Next() {
		var r pmRow
		if err := rows.Scan(&r.model, &r.misses, &r.retired); err != nil {
			rows.Close()
			return ProbeApplyResult{}, err
		}
		pmRows = append(pmRows, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return ProbeApplyResult{}, err
	}

	var res ProbeApplyResult
	retiredSet := make(map[string]bool)
	for _, r := range pmRows {
		res.ProviderModels = append(res.ProviderModels, r.model)
		nextRetired := r.retired
		if updateModels {
			present := seenSet[r.model]
			nextMisses, retired := domain.NextModelMissState(r.misses, present)
			nextRetired = retired
			if _, err := tx.Exec(ctx, `
				UPDATE gateway.provider_models
				SET listed = $3, misses = $4, retired = $5
				WHERE account_id = $1::uuid AND model = $2`,
				id, r.model, present, nextMisses, nextRetired); err != nil {
				return ProbeApplyResult{}, err
			}
		}
		if nextRetired {
			retiredSet[r.model] = true
			res.RetiredModels = append(res.RetiredModels, r.model)
		}
	}

	var clearRetired []string
	for _, c := range causes {
		if m, ok := strings.CutPrefix(c, "retired:"); ok && !retiredSet[m] {
			clearRetired = append(clearRetired, c)
		}
	}
	if len(clearRetired) > 0 {
		if err := clearCausesTx(ctx, tx, id, clearRetired); err != nil {
			return ProbeApplyResult{}, err
		}
	}

	routeRows, err := tx.Query(ctx, `
		SELECT DISTINCT model
		FROM gateway.route_links
		WHERE provider_account_id = $1::uuid
		ORDER BY model`, id)
	if err != nil {
		return ProbeApplyResult{}, err
	}
	defer routeRows.Close()
	for routeRows.Next() {
		var m string
		if err := routeRows.Scan(&m); err != nil {
			return ProbeApplyResult{}, err
		}
		res.RouteModelsInUse = append(res.RouteModelsInUse, m)
	}
	if err := routeRows.Err(); err != nil {
		return ProbeApplyResult{}, err
	}

	return res, nil
}

// DeprecatedModels returns which models resolve to lifecycle = 'deprecated'
// in gateway.prices for vendor, falling back to the model-only row (vendor = ”).
func DeprecatedModels(ctx context.Context, owner *pgxpool.Pool, vendor string, models []string) (map[string]bool, error) {
	out := make(map[string]bool)
	if owner == nil || len(models) == 0 {
		return out, nil
	}
	vendor = domain.VendorKey(vendor)
	rows, err := owner.Query(ctx, `
		SELECT vendor, model, lifecycle
		FROM gateway.prices
		WHERE model = ANY($1::text[])
		  AND vendor IN ($2, '')`, models, vendor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byKey := make(map[domain.VendorModel]string)
	for rows.Next() {
		var v, m, lc string
		if err := rows.Scan(&v, &m, &lc); err != nil {
			return nil, err
		}
		byKey[domain.VendorModel{Vendor: v, Model: m}] = lc
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, m := range models {
		lc, ok := byKey[domain.VendorModel{Vendor: vendor, Model: m}]
		if !ok {
			lc = byKey[domain.VendorModel{Vendor: "", Model: m}]
		}
		if lc == domain.LifecycleDeprecated {
			out[m] = true
		}
	}
	return out, nil
}

// MarkCauseNotifiedTx atomically appends cause to notified_causes inside tx
// when absent. It reports true only when this call added the cause.
func MarkCauseNotifiedTx(ctx context.Context, tx pgx.Tx, id, cause string) (bool, error) {
	if tx == nil {
		return false, fmt.Errorf("gateway: no transaction")
	}
	tag, err := tx.Exec(ctx, `
		UPDATE gateway.provider_accounts
		SET notified_causes = array_append(notified_causes, $2)
		WHERE id = $1::uuid
		  AND NOT ($2 = ANY(notified_causes))`, id, cause)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ClearCauses removes any matching causes from an account's notified_causes.
func ClearCauses(ctx context.Context, owner *pgxpool.Pool, id string, causes []string) error {
	if owner == nil || len(causes) == 0 {
		return nil
	}
	_, err := owner.Exec(ctx, `
		UPDATE gateway.provider_accounts
		SET notified_causes = COALESCE((
			SELECT array_agg(c ORDER BY ord)
			FROM unnest(notified_causes) WITH ORDINALITY AS t(c, ord)
			WHERE NOT (c = ANY($2::text[]))
		), '{}'::text[])
		WHERE id = $1::uuid`, id, causes)
	return err
}

func clearCausesTx(ctx context.Context, tx pgx.Tx, id string, causes []string) error {
	if len(causes) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `
		UPDATE gateway.provider_accounts
		SET notified_causes = COALESCE((
			SELECT array_agg(c ORDER BY ord)
			FROM unnest(notified_causes) WITH ORDINALITY AS t(c, ord)
			WHERE NOT (c = ANY($2::text[]))
		), '{}'::text[])
		WHERE id = $1::uuid`, id, slices.Clone(causes))
	return err
}
