package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
)

var (
	_ app.OverrideStore    = Providers{}
	_ app.PricingMapStore  = Providers{}
	_ app.CallPricer       = Store{}
	_ app.EstimateNotifier = EstimateNotifier{}
)

func (p Providers) SetPricingMap(ctx context.Context, who app.Caller, accountID string, vendor *string, models map[string]string) error {
	if _, err := uuid.Parse(accountID); err != nil {
		return app.ErrNotFound
	}
	tx, err := p.begin(ctx, who)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('gateway.pricing_map:' || $1::text, 0))`, accountID); err != nil {
		return err
	}
	before, err := getAccount(ctx, tx, accountID)
	if err != nil {
		return err
	}
	merged := maps.Clone(before.PricingModels)
	if merged == nil {
		merged = map[string]string{}
	}
	changedBefore := map[string]string{}
	changedAfter := map[string]string{}
	values := map[string]audit.Change{}
	fields := []string{}
	updatedVendor := before.PricingProvider
	if vendor != nil {
		updatedVendor = *vendor
	}
	if before.PricingProvider != updatedVendor {
		fields = append(fields, "pricing_provider")
		values["pricing_provider"] = audit.Change{From: before.PricingProvider, To: updatedVendor}
	}
	for model, mapped := range models {
		old, hadOld := merged[model]
		if mapped == "" {
			delete(merged, model)
		} else {
			merged[model] = mapped
		}
		if next, hasNext := merged[model]; hadOld != hasNext || old != next {
			if hadOld {
				changedBefore[model] = old
			}
			if hasNext {
				changedAfter[model] = next
			}
		}
	}
	if len(changedBefore) > 0 || len(changedAfter) > 0 {
		fields = append(fields, "pricing_model")
		values["pricing_model"] = audit.Change{From: changedBefore, To: changedAfter}
	}
	if len(fields) == 0 {
		audit.NoChange(ctx)
		return tx.Commit(ctx)
	}
	slices.Sort(fields)
	encoded, err := json.Marshal(merged)
	if err != nil {
		return err
	}
	var updated bool
	if err := tx.QueryRow(ctx, `SELECT gateway.set_provider_pricing_map($1::uuid,$2,$3::jsonb)`, accountID, updatedVendor, encoded).Scan(&updated); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23514" && pgErr.ConstraintName == "provider_accounts_pricing_models_check" {
			return app.ErrPricingMapLimit
		}
		return mapOverrideErr(err)
	}
	if !updated {
		return app.ErrNotFound
	}
	if err := audit.Record(ctx, tx, audit.Event{Action: audit.PricingMapSet, Outcome: audit.Success, TargetID: accountID, Changes: audit.Changes{Fields: fields, Values: values}}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// LookupCallPricing loads the override, discovered provider-list pricing,
// exact vendor catalog pricing, cross-provider (”) fallback pricing, and
// vendor highest rate for one call on accountID. Uses the store's serving pool.
func (s Store) LookupCallPricing(ctx context.Context, accountID, vendor, model string) (app.CallPricing, error) {
	var out app.CallPricing
	catalogModel := model
	if s.Pool == nil {
		return out, nil
	}
	if _, err := uuid.Parse(accountID); err == nil {
		var pricingVendor string
		var raw []byte
		if err := s.Pool.QueryRow(ctx, `SELECT pricing_provider, pricing_models FROM gateway.provider_accounts WHERE id=$1::uuid`, accountID).Scan(&pricingVendor, &raw); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return app.CallPricing{}, err
		}
		var mapped map[string]string
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &mapped); err != nil {
				return app.CallPricing{}, err
			}
		}
		if pricingVendor != "" {
			vendor = pricingVendor
		}
		if mapped[model] != "" {
			catalogModel = mapped[model]
		}
		ov, err := readOverride(ctx, s.Pool, accountID, model)
		if err != nil {
			return app.CallPricing{}, err
		}
		out.Override = ov

		seen, err := readSeenModel(ctx, s.Pool, accountID, model)
		if err != nil {
			return app.CallPricing{}, err
		}
		if seen != nil {
			if p, ok := domain.ParseSeenPricingMicroUSD(seen.Pricing); ok {
				out.ProviderList = &p
			}
		}
	}
	cat, found, cross, err := s.lookupCatalogPrices(ctx, vendor, catalogModel)
	if err != nil {
		return app.CallPricing{}, err
	}
	out.Catalog = cat
	out.CatalogFound = found
	out.CrossProvider = cross
	if rate, hasRate, err := s.HighestRate(ctx, vendor); err != nil {
		return app.CallPricing{}, err
	} else if hasRate && rate > 0 {
		out.EstimateRate = rate
		out.EstimateRateKnown = true
	}
	out.ZeroCostAllowed = domain.ZeroCostAllowed(model, out.ProviderList)
	return out, nil
}

// EstimateNotifier sends a one-time notice per (account, model) to users with
// settings:manage when a metered call has an unknown price (5c).
type EstimateNotifier struct {
	Pool             *pgxpool.Pool
	SettingsManagers func(ctx context.Context) ([]string, error)
	Notify           func(ctx context.Context, tx pgx.Tx, userID, title string) error
}

// NotifyEstimatedModel sends one notice per (account, model) to settings
// managers. Concurrent callers race safely via a ledger unknown_notified_at
// marker enforced by the partial unique index ledger_unknown_notified_pair.
func (n EstimateNotifier) NotifyEstimatedModel(ctx context.Context, account domain.Account, model string) error {
	if n.Pool == nil || n.SettingsManagers == nil || n.Notify == nil || model == "" {
		return nil
	}
	if _, err := uuid.Parse(account.ID); err != nil {
		return nil
	}
	var acctName string
	var already bool
	err := n.Pool.QueryRow(ctx, `
		SELECT name, EXISTS (
			SELECT 1 FROM gateway.ledger
			WHERE provider_account_id = $1::uuid
			  AND model = $2
			  AND unknown_notified_at IS NOT NULL
		)
		FROM gateway.provider_accounts
		WHERE id = $1::uuid`, account.ID, model).Scan(&acctName, &already)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if already {
		return nil
	}
	recipients, err := n.SettingsManagers(ctx)
	if err != nil {
		return err
	}
	if len(recipients) == 0 {
		return nil
	}
	tx, err := n.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('gateway.unknown_notice:' || $1::text || ':' || $2, 0))`, account.ID, model); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE gateway.ledger
		SET unknown_notified_at = now()
		WHERE id = (
			SELECT id FROM gateway.ledger
			WHERE provider_account_id = $1::uuid
			  AND model = $2
			  AND price_source = 'unknown'
			  AND unknown_notified_at IS NULL
			ORDER BY at DESC, id DESC
			LIMIT 1
		)
		AND NOT EXISTS (
			SELECT 1 FROM gateway.ledger
			WHERE provider_account_id = $1::uuid
			  AND model = $2
			  AND unknown_notified_at IS NOT NULL
		)`, account.ID, model)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "ledger_unknown_notified_pair" {
			return nil
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	label := acctName
	if label == "" {
		label = account.Provider
	}
	if label == "" {
		label = account.ID
	}
	title := fmt.Sprintf("Provider %s: model %s price is unknown; set a price override for accurate billing.", label, model)
	for _, uid := range recipients {
		if err := n.Notify(ctx, tx, uid, title); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// SeenModel returns one discovered model row for an account the person can see,
// or app.ErrNotFound, without emitting a provider.models.read audit event.
func (p Providers) SeenModel(ctx context.Context, who app.Caller, accountID, model string) (*domain.SeenModel, error) {
	if _, err := uuid.Parse(accountID); err != nil {
		return nil, app.ErrNotFound
	}
	tx, err := p.begin(ctx, who)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := ensureVisibleAccount(ctx, tx, accountID); err != nil {
		return nil, err
	}
	seen, err := readSeenModel(ctx, tx, accountID, model)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return seen, nil
}

// GetOverride reads the stored price override for an account and model visible
// to who. Missing override returns nil, nil.
func (p Providers) GetOverride(ctx context.Context, who app.Caller, accountID, model string) (*domain.PriceOverride, error) {
	if _, err := uuid.Parse(accountID); err != nil {
		return nil, app.ErrNotFound
	}
	tx, err := p.begin(ctx, who)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := ensureVisibleAccount(ctx, tx, accountID); err != nil {
		return nil, err
	}
	ov, err := readOverride(ctx, tx, accountID, model)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return ov, nil
}

// SetOverride inserts or updates a price override under RLS and records an
// audit event (or audit.NoChange when rates are unchanged).
func (p Providers) SetOverride(ctx context.Context, who app.Caller, accountID string, override domain.PriceOverride) (domain.PriceOverride, error) {
	if _, err := uuid.Parse(accountID); err != nil {
		return domain.PriceOverride{}, app.ErrNotFound
	}
	tx, err := p.begin(ctx, who)
	if err != nil {
		return domain.PriceOverride{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := ensureVisibleAccount(ctx, tx, accountID); err != nil {
		return domain.PriceOverride{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('gateway.price_overrides:' || $1::text || ':' || $2, 0))`, accountID, override.Model); err != nil {
		return domain.PriceOverride{}, err
	}
	prev, err := readOverrideForUpdate(ctx, tx, accountID, override.Model)
	if err != nil {
		return domain.PriceOverride{}, err
	}
	if prev != nil && prev.EqualRates(override) {
		audit.NoChange(ctx)
		return *prev, tx.Commit(ctx)
	}
	var updatedAt time.Time
	err = tx.QueryRow(ctx, `
		INSERT INTO gateway.price_overrides (
			account_id, model,
			input_micro_usd_per_mtok, output_micro_usd_per_mtok,
			cache_read_micro_usd_per_mtok, cache_write_micro_usd_per_mtok,
			updated_by, updated_at
		) VALUES ($1::uuid, $2, $3, $4, $5, $6, $7::uuid, now())
		ON CONFLICT (account_id, model) DO UPDATE
		SET input_micro_usd_per_mtok = EXCLUDED.input_micro_usd_per_mtok,
		    output_micro_usd_per_mtok = EXCLUDED.output_micro_usd_per_mtok,
		    cache_read_micro_usd_per_mtok = EXCLUDED.cache_read_micro_usd_per_mtok,
		    cache_write_micro_usd_per_mtok = EXCLUDED.cache_write_micro_usd_per_mtok,
		    updated_by = EXCLUDED.updated_by,
		    updated_at = now()
		RETURNING updated_at`,
		accountID, override.Model,
		microOrNil(override.InputMicroUSDPerMTok),
		microOrNil(override.OutputMicroUSDPerMTok),
		microOrNil(override.CacheReadMicroUSDPerMTok),
		microOrNil(override.CacheWriteMicroUSDPerMTok),
		who.User,
	).Scan(&updatedAt)
	if err != nil {
		return domain.PriceOverride{}, mapOverrideErr(err)
	}
	at := updatedAt.UTC()
	saved := domain.PriceOverride{
		AccountID:                 accountID,
		Model:                     override.Model,
		InputMicroUSDPerMTok:      override.InputMicroUSDPerMTok,
		OutputMicroUSDPerMTok:     override.OutputMicroUSDPerMTok,
		CacheReadMicroUSDPerMTok:  override.CacheReadMicroUSDPerMTok,
		CacheWriteMicroUSDPerMTok: override.CacheWriteMicroUSDPerMTok,
		UpdatedBy:                 who.User,
		UpdatedAt:                 &at,
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.PriceOverrideSet,
		Outcome:  audit.Success,
		TargetID: accountID,
		Changes:  overrideSetChanges(prev, saved),
	}); err != nil {
		return domain.PriceOverride{}, err
	}
	return saved, tx.Commit(ctx)
}

// DeleteOverride removes a price override under RLS and records an audit event
// (or audit.NoChange if no override existed).
func (p Providers) DeleteOverride(ctx context.Context, who app.Caller, accountID, model string) error {
	if _, err := uuid.Parse(accountID); err != nil {
		return app.ErrNotFound
	}
	tx, err := p.begin(ctx, who)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := ensureVisibleAccount(ctx, tx, accountID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('gateway.price_overrides:' || $1::text || ':' || $2, 0))`, accountID, model); err != nil {
		return err
	}
	prev, err := readOverrideForUpdate(ctx, tx, accountID, model)
	if err != nil {
		return err
	}
	if prev == nil {
		audit.NoChange(ctx)
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM gateway.price_overrides
		WHERE account_id = $1::uuid AND model = $2`,
		accountID, model,
	); err != nil {
		return mapOverrideErr(err)
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.PriceOverrideDelete,
		Outcome:  audit.Success,
		TargetID: accountID,
		Changes:  overrideDeleteChanges(*prev),
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func ensureVisibleAccount(ctx context.Context, q querier, accountID string) error {
	var ok int
	err := q.QueryRow(ctx, `
		SELECT 1 FROM gateway.provider_accounts AS a
		WHERE a.id = $1::uuid AND `+visibleTo(), accountID).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrNotFound
	}
	return err
}

func readSeenModel(ctx context.Context, q querier, accountID, model string) (*domain.SeenModel, error) {
	var m domain.SeenModel
	var ctxLen *int
	var pricingRaw, modRaw []byte
	var seenAt time.Time
	err := q.QueryRow(ctx, `
		SELECT model_id, display_name, created, context_length, pricing, modalities, seen_at
		FROM gateway.account_models_seen
		WHERE account_id = $1::uuid AND model_id = $2`,
		accountID, model,
	).Scan(&m.ID, &m.DisplayName, &m.Created, &ctxLen, &pricingRaw, &modRaw, &seenAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if ctxLen != nil {
		m.ContextLength = *ctxLen
	}
	if len(pricingRaw) > 0 {
		if err := json.Unmarshal(pricingRaw, &m.Pricing); err != nil {
			return nil, err
		}
	}
	if len(modRaw) > 0 {
		var mod domain.ModelModalities
		if err := json.Unmarshal(modRaw, &mod); err != nil {
			return nil, err
		}
		m.Modalities = &mod
	}
	at := seenAt.UTC()
	m.SeenAt = &at
	return &m, nil
}

func readOverride(ctx context.Context, q querier, accountID, model string) (*domain.PriceOverride, error) {
	return scanOverride(q.QueryRow(ctx, `
		SELECT account_id::text, model,
			input_micro_usd_per_mtok, output_micro_usd_per_mtok,
			cache_read_micro_usd_per_mtok, cache_write_micro_usd_per_mtok,
			updated_by::text, updated_at
		FROM gateway.price_overrides
		WHERE account_id = $1::uuid AND model = $2`,
		accountID, model,
	))
}

func readOverrideForUpdate(ctx context.Context, q querier, accountID, model string) (*domain.PriceOverride, error) {
	return scanOverride(q.QueryRow(ctx, `
		SELECT account_id::text, model,
			input_micro_usd_per_mtok, output_micro_usd_per_mtok,
			cache_read_micro_usd_per_mtok, cache_write_micro_usd_per_mtok,
			updated_by::text, updated_at
		FROM gateway.price_overrides
		WHERE account_id = $1::uuid AND model = $2
		FOR UPDATE`,
		accountID, model,
	))
}

func scanOverride(row pgx.Row) (*domain.PriceOverride, error) {
	var ov domain.PriceOverride
	var updatedAt time.Time
	err := row.Scan(
		&ov.AccountID, &ov.Model,
		&ov.InputMicroUSDPerMTok, &ov.OutputMicroUSDPerMTok,
		&ov.CacheReadMicroUSDPerMTok, &ov.CacheWriteMicroUSDPerMTok,
		&ov.UpdatedBy, &updatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	at := updatedAt.UTC()
	ov.UpdatedAt = &at
	return &ov, nil
}

func mapOverrideErr(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "42501" {
		return app.ErrOverridePermission
	}
	return mapWrite(err)
}

func overrideSetChanges(prev *domain.PriceOverride, next domain.PriceOverride) audit.Changes {
	values := map[string]audit.Change{
		"model": {To: next.Model},
	}
	fields := []string{"model"}
	var pIn, pOut, pCR, pCW *int64
	if prev != nil {
		values["model"] = audit.Change{From: prev.Model, To: next.Model}
		pIn = prev.InputMicroUSDPerMTok
		pOut = prev.OutputMicroUSDPerMTok
		pCR = prev.CacheReadMicroUSDPerMTok
		pCW = prev.CacheWriteMicroUSDPerMTok
	}
	add := func(name string, from, to *int64) {
		if equalInt64(from, to) {
			return
		}
		fields = append(fields, name)
		var fromVal, toVal any
		if from != nil {
			fromVal = *from
		}
		if to != nil {
			toVal = *to
		}
		values[name] = audit.Change{From: fromVal, To: toVal}
	}
	add("input_micro_usd_per_mtok", pIn, next.InputMicroUSDPerMTok)
	add("output_micro_usd_per_mtok", pOut, next.OutputMicroUSDPerMTok)
	add("cache_read_micro_usd_per_mtok", pCR, next.CacheReadMicroUSDPerMTok)
	add("cache_write_micro_usd_per_mtok", pCW, next.CacheWriteMicroUSDPerMTok)
	slices.Sort(fields)
	return audit.Changes{Fields: fields, Values: values}
}

func overrideDeleteChanges(prev domain.PriceOverride) audit.Changes {
	values := map[string]audit.Change{
		"model": {From: prev.Model},
	}
	fields := []string{"model"}
	add := func(name string, from *int64) {
		if from == nil {
			return
		}
		fields = append(fields, name)
		values[name] = audit.Change{From: *from}
	}
	add("input_micro_usd_per_mtok", prev.InputMicroUSDPerMTok)
	add("output_micro_usd_per_mtok", prev.OutputMicroUSDPerMTok)
	add("cache_read_micro_usd_per_mtok", prev.CacheReadMicroUSDPerMTok)
	add("cache_write_micro_usd_per_mtok", prev.CacheWriteMicroUSDPerMTok)
	slices.Sort(fields)
	return audit.Changes{Fields: fields, Values: values}
}

func equalInt64(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
