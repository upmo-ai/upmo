package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

// catalogLockKey serializes SeedCatalog (during migrate) and daily catalog
// refreshes so they can never interleave.
const catalogLockKey int64 = 35420261119

// CatalogMeta describes the active catalog version, data date, and refresh state.
type CatalogMeta struct {
	Version         string
	AsOf            time.Time
	RefreshedAt     *time.Time
	LastAttemptAt   *time.Time
	LastError       string
	StaleNotifiedAt *time.Time
}

// ReplaceCatalog writes a refreshed catalog in one transaction.
func (s Store) ReplaceCatalog(ctx context.Context, version string, rows []domain.CatalogRow) error {
	if s.Pool == nil {
		return fmt.Errorf("gateway: no database")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, catalogLockKey); err != nil {
		return err
	}
	if err := swapCatalogRowsTx(ctx, tx, rows); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO gateway.catalog_meta (id, version, as_of) VALUES (1, $1, now())
		ON CONFLICT (id) DO UPDATE SET version = EXCLUDED.version, as_of = EXCLUDED.as_of`, version); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SeedCatalog seeds the embedded snapshot if no catalog exists yet or if the
// snapshot's asOf date is strictly newer than the stored catalog_meta.as_of.
// It shares catalogLockKey with the daily refresh job so migrate and refresh
// cannot interleave.
func SeedCatalog(ctx context.Context, owner *pgxpool.Pool, version string, asOf time.Time, rows []domain.CatalogRow) (bool, error) {
	if owner == nil {
		return false, fmt.Errorf("gateway: no database")
	}
	tx, err := owner.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, catalogLockKey); err != nil {
		return false, err
	}
	var curAsOf time.Time
	err = tx.QueryRow(ctx, `SELECT as_of FROM gateway.catalog_meta WHERE id = 1 FOR UPDATE`).Scan(&curAsOf)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if err == nil && !asOf.UTC().After(curAsOf.UTC()) {
		return false, tx.Commit(ctx)
	}
	if err := swapCatalogRowsTx(ctx, tx, rows); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO gateway.catalog_meta (id, version, as_of, refreshed_at, last_error, stale_notified_at)
		VALUES (1, $1, $2, NULL, '', NULL)
		ON CONFLICT (id) DO UPDATE
		SET version = EXCLUDED.version,
		    as_of = EXCLUDED.as_of,
		    refreshed_at = NULL,
		    last_error = '',
		    stale_notified_at = NULL`,
		version, asOf.UTC(),
	); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// TryLockCatalog acquires a dedicated connection and attempts a non-blocking
// advisory lock on catalogLockKey. When locked is false, the caller should skip
// its refresh without error.
func TryLockCatalog(ctx context.Context, owner *pgxpool.Pool) (conn *pgxpool.Conn, locked bool, unlock func(), err error) {
	if owner == nil {
		return nil, false, nil, fmt.Errorf("gateway: no database")
	}
	c, err := owner.Acquire(ctx)
	if err != nil {
		return nil, false, nil, err
	}
	if err := c.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, catalogLockKey).Scan(&locked); err != nil {
		c.Release()
		return nil, false, nil, err
	}
	if !locked {
		c.Release()
		return nil, false, nil, nil
	}
	return c, true, func() {
		uctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := c.Exec(uctx, `SELECT pg_advisory_unlock($1)`, catalogLockKey); err != nil {
			_ = c.Conn().Close(uctx)
		}
		c.Release()
	}, nil
}

// CatalogRowCount returns the number of rows currently stored in gateway.prices.
func CatalogRowCount(ctx context.Context, q querier) (int, error) {
	var count int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM gateway.prices`).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// ApplyCatalogRefreshTx atomically swaps gateway.prices and marks the catalog
// refreshed at asOf, clearing any prior error and stale notice episode.
func ApplyCatalogRefreshTx(ctx context.Context, conn *pgxpool.Conn, version string, asOf time.Time, rows []domain.CatalogRow) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := swapCatalogRowsTx(ctx, tx, rows); err != nil {
		return err
	}
	at := asOf.UTC()
	if _, err := tx.Exec(ctx, `
		INSERT INTO gateway.catalog_meta (id, version, as_of, refreshed_at, last_attempt_at, last_error, stale_notified_at)
		VALUES (1, $1, $2, $2, $2, '', NULL)
		ON CONFLICT (id) DO UPDATE
		SET version = EXCLUDED.version,
		    as_of = EXCLUDED.as_of,
		    refreshed_at = EXCLUDED.refreshed_at,
		    last_attempt_at = EXCLUDED.last_attempt_at,
		    last_error = '',
		    stale_notified_at = NULL`,
		version, at,
	); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RecordCatalogAttempt records the failed refresh attempt on catalog_meta and
// returns the current data date and whether a stale notice has already been sent.
func RecordCatalogAttempt(ctx context.Context, q querier, at time.Time, errMsg string) (asOf time.Time, staleNotifiedAt *time.Time, found bool, err error) {
	now := at.UTC()
	err = q.QueryRow(ctx, `
		UPDATE gateway.catalog_meta
		SET last_attempt_at = $1,
		    last_error = $2
		WHERE id = 1
		RETURNING as_of, stale_notified_at`,
		now, errMsg,
	).Scan(&asOf, &staleNotifiedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, nil, false, nil
	}
	if err != nil {
		return time.Time{}, nil, false, err
	}
	return asOf.UTC(), staleNotifiedAt, true, nil
}

// MarkCatalogStaleNotifiedTx records stale_notified_at inside tx alongside the
// stale-catalog notices.
func MarkCatalogStaleNotifiedTx(ctx context.Context, tx pgx.Tx, at time.Time) error {
	_, err := tx.Exec(ctx, `UPDATE gateway.catalog_meta SET stale_notified_at = $1 WHERE id = 1`, at.UTC())
	return err
}

// GetCatalogMeta reads the singleton gateway.catalog_meta row, if present.
func GetCatalogMeta(ctx context.Context, q querier) (CatalogMeta, bool, error) {
	var m CatalogMeta
	err := q.QueryRow(ctx, `
		SELECT version, as_of, refreshed_at, last_attempt_at, last_error, stale_notified_at
		FROM gateway.catalog_meta
		WHERE id = 1`,
	).Scan(&m.Version, &m.AsOf, &m.RefreshedAt, &m.LastAttemptAt, &m.LastError, &m.StaleNotifiedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return CatalogMeta{}, false, nil
	}
	if err != nil {
		return CatalogMeta{}, false, err
	}
	return m, true, nil
}

func swapCatalogRowsTx(ctx context.Context, tx pgx.Tx, rows []domain.CatalogRow) error {
	if _, err := tx.Exec(ctx, `DELETE FROM gateway.price_tiers`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM gateway.prices`); err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	cols := []string{
		"vendor", "model", "version", "kind", "inputs", "outputs",
		"input_usd_per_mtok", "output_usd_per_mtok",
		"cache_read_usd_per_mtok", "cache_write_usd_per_mtok",
		"capabilities", "context_window", "max_output", "reasoning", "lifecycle", "retires_at",
	}
	_, err := tx.CopyFrom(ctx, pgx.Identifier{"gateway", "prices"}, cols, pgx.CopyFromSlice(len(rows), func(i int) ([]any, error) {
		row := rows[i]
		ctxWindow := row.ContextWindow
		if ctxWindow <= 0 {
			ctxWindow = row.Price.ContextWindow
		}
		if ctxWindow <= 0 {
			ctxWindow = domain.DefaultContextWindow
		}
		kind := row.Kind
		if kind == "" {
			kind = domain.KindChat
		}
		inputs := row.Inputs
		if inputs == nil {
			inputs = []string{domain.InputText}
		}
		outputs := row.Outputs
		if outputs == nil {
			if kind == domain.KindChat {
				outputs = []string{domain.OutputText}
			} else {
				outputs = []string{}
			}
		}
		caps := row.Caps
		if caps == nil {
			caps = []string{}
		}
		var maxOut *int
		if row.MaxOutput > 0 {
			v := row.MaxOutput
			maxOut = &v
		}
		var retires pgtype.Date
		if row.RetiresAt != nil {
			t := row.RetiresAt.UTC()
			retires = pgtype.Date{Time: time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), Valid: true}
		}
		return []any{
			domain.VendorKey(row.Vendor),
			row.Model,
			row.Version,
			string(kind),
			inputs,
			outputs,
			numericFromMicroOrFloat(row.Price.InputMicroUSDPerMTok, row.Price.InputPerMTok),
			numericFromMicroOrFloat(row.Price.OutputMicroUSDPerMTok, row.Price.OutputPerMTok),
			numericFromMicroOrFloat(row.Price.CacheReadMicroUSDPerMTok, row.Price.CacheReadMTok),
			numericFromMicroOrFloat(row.Price.CacheWriteMicroUSDPerMTok, row.Price.CacheWriteMTok),
			caps,
			ctxWindow,
			maxOut,
			row.Reasoning,
			row.Lifecycle,
			retires,
		}, nil
	}))
	if err != nil {
		return err
	}
	var tiers []struct {
		vendor, model string
		tier          domain.PriceTier
	}
	for _, row := range rows {
		for _, tier := range row.Price.Tiers {
			tiers = append(tiers, struct {
				vendor, model string
				tier          domain.PriceTier
			}{domain.VendorKey(row.Vendor), row.Model, tier})
		}
	}
	if len(tiers) == 0 {
		return nil
	}
	_, err = tx.CopyFrom(ctx, pgx.Identifier{"gateway", "price_tiers"}, []string{"vendor", "model", "context_min_tokens", "input_usd_per_mtok", "output_usd_per_mtok", "cache_read_usd_per_mtok", "cache_write_usd_per_mtok"}, pgx.CopyFromSlice(len(tiers), func(i int) ([]any, error) {
		t := tiers[i]
		return []any{t.vendor, t.model, t.tier.ContextMinTokens, numericFromMicroOrFloat(t.tier.Price.InputMicroUSDPerMTok, nil), numericFromMicroOrFloat(t.tier.Price.OutputMicroUSDPerMTok, nil), numericFromMicroOrFloat(t.tier.Price.CacheReadMicroUSDPerMTok, nil), numericFromMicroOrFloat(t.tier.Price.CacheWriteMicroUSDPerMTok, nil)}, nil
	}))
	return err
}

func numericFromMicroOrFloat(micro *int64, fallback *float64) pgtype.Numeric {
	if micro != nil {
		return pgtype.Numeric{Int: big.NewInt(*micro), Exp: -6, Valid: true}
	}
	if fallback != nil {
		var n pgtype.Numeric
		if err := n.Scan(strconv.FormatFloat(*fallback, 'g', -1, 64)); err == nil {
			return n
		}
	}
	return pgtype.Numeric{}
}

// Capabilities returns the catalog caps for a model. ok is false when absent.
// Image input in the inputs array also counts as CapVision for route links.
func (s Store) Capabilities(ctx context.Context, model string) ([]string, bool, error) {
	var caps, inputs []string
	err := s.Pool.QueryRow(ctx, `
		SELECT capabilities, inputs
		FROM gateway.prices
		WHERE model = $1
		ORDER BY (vendor = '') DESC, vendor
		LIMIT 1`, model).Scan(&caps, &inputs)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if slices.Contains(inputs, domain.InputImage) && !slices.Contains(caps, domain.CapVision) {
		caps = append(caps, domain.CapVision)
	}
	return caps, true, nil
}

// VendorCatalog returns the catalogue rows for vendor in model order.
// An empty vendor returns an empty slice rather than the internal fallback rows.
func (s Store) VendorCatalog(ctx context.Context, vendor string) ([]domain.CatalogRow, error) {
	if s.Pool == nil {
		return nil, fmt.Errorf("gateway: no database")
	}
	vendor = domain.VendorKey(vendor)
	if vendor == "" {
		return []domain.CatalogRow{}, nil
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT vendor, model, version, kind, inputs, outputs,
			input_usd_per_mtok, output_usd_per_mtok,
			cache_read_usd_per_mtok, cache_write_usd_per_mtok,
			capabilities, context_window, max_output, reasoning, lifecycle, retires_at
		FROM gateway.prices
		WHERE vendor = $1
		ORDER BY model`, vendor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.CatalogRow{}
	for rows.Next() {
		row, err := scanCatalogRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// CatalogModel returns the catalogue row for (vendor, model), falling back to
// the model-only row (empty vendor).
func (s Store) CatalogModel(ctx context.Context, vendor, model string) (domain.CatalogRow, bool, error) {
	if s.Pool == nil {
		return domain.CatalogRow{}, false, fmt.Errorf("gateway: no database")
	}
	vendor = domain.VendorKey(vendor)
	row, err := scanCatalogRow(s.Pool.QueryRow(ctx, `
		SELECT vendor, model, version, kind, inputs, outputs,
			input_usd_per_mtok, output_usd_per_mtok,
			cache_read_usd_per_mtok, cache_write_usd_per_mtok,
			capabilities, context_window, max_output, reasoning, lifecycle, retires_at
		FROM gateway.prices
		WHERE model = $2
		  AND vendor IN ($1, '')
		ORDER BY (vendor = $1) DESC
		LIMIT 1`, vendor, model))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.CatalogRow{}, false, nil
		}
		return domain.CatalogRow{}, false, err
	}
	return row, true, nil
}

// ModelKinds resolves the catalogue kind for each (vendor, model) pair,
// falling back to the model-only row (empty vendor). Pairs absent from the catalogue
// are omitted from the returned map.
func (s Store) ModelKinds(ctx context.Context, pairs []domain.VendorModel) (map[domain.VendorModel]domain.ModelKind, error) {
	if s.Pool == nil || len(pairs) == 0 {
		return nil, nil
	}
	vendorSet := map[string]bool{}
	modelSet := map[string]bool{}
	for _, p := range pairs {
		if p.Vendor != "" {
			vendorSet[p.Vendor] = true
		}
		if p.Model != "" {
			modelSet[p.Model] = true
		}
	}
	models := make([]string, 0, len(modelSet))
	for m := range modelSet {
		models = append(models, m)
	}
	vendors := make([]string, 0, len(vendorSet)+1)
	vendors = append(vendors, "")
	for v := range vendorSet {
		vendors = append(vendors, v)
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT vendor, model, kind
		FROM gateway.prices
		WHERE model = ANY($1::text[])
		  AND vendor = ANY($2::text[])`, models, vendors)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	raw := map[domain.VendorModel]domain.ModelKind{}
	for rows.Next() {
		var v, m, k string
		if err := rows.Scan(&v, &m, &k); err != nil {
			return nil, err
		}
		raw[domain.VendorModel{Vendor: v, Model: m}] = domain.ModelKind(k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make(map[domain.VendorModel]domain.ModelKind, len(pairs))
	for _, p := range pairs {
		if k, ok := raw[p]; ok {
			out[p] = k
		} else if k, ok := raw[domain.VendorModel{Vendor: "", Model: p.Model}]; ok {
			out[p] = k
		}
	}
	return out, nil
}

// ModelInputs returns the accepted input modalities for an account's model:
// the account's discovered provider list first, then the catalogue (vendor
// row then fallback row). ok is false when neither has a row.
func (s Store) ModelInputs(ctx context.Context, accountID, vendor, model string) ([]string, bool, error) {
	if s.Pool == nil {
		return nil, false, nil
	}
	if _, err := uuid.Parse(accountID); err == nil {
		var rawIn []byte
		err := s.Pool.QueryRow(ctx, `
			SELECT modalities->'input'
			FROM gateway.account_models_seen
			WHERE account_id = $1::uuid AND model_id = $2`, accountID, model).Scan(&rawIn)
		if err == nil && len(rawIn) > 0 {
			var list []string
			if uerr := json.Unmarshal(rawIn, &list); uerr == nil {
				if norm := domain.NormalizeInputs(list); len(norm) > 0 {
					return norm, true, nil
				}
			}
		} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, false, err
		}
	}
	var inputs []string
	err := s.Pool.QueryRow(ctx, `
		SELECT inputs
		FROM gateway.prices
		WHERE model = $2
		  AND vendor IN ($1, '')
		ORDER BY (vendor = $1) DESC
		LIMIT 1`, domain.VendorKey(vendor), model).Scan(&inputs)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return inputs, true, nil
}

// SuggestModelForInput returns the first usable chat model on accountID that
// accepts needInput, if any.
func (s Store) SuggestModelForInput(ctx context.Context, accountID, vendor, needInput string) (string, bool, error) {
	if s.Pool == nil {
		return "", false, nil
	}
	if _, err := uuid.Parse(accountID); err != nil {
		return "", false, nil
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT pm.model, s.modalities->'input'
		FROM gateway.provider_accounts AS a
		JOIN gateway.provider_models AS pm ON pm.account_id = a.id
		LEFT JOIN gateway.account_models_seen AS s ON s.account_id = a.id AND s.model_id = pm.model
		WHERE a.id = $1::uuid
		  AND a.status = $2
		  AND a.enabled
		  AND NOT pm.retired
		ORDER BY pm.position`, accountID, domain.StatusConnected)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	type candidate struct {
		model      string
		seenInputs []string
	}
	var candidates []candidate
	var modelIDs []string
	for rows.Next() {
		var m string
		var rawIn []byte
		if err := rows.Scan(&m, &rawIn); err != nil {
			return "", false, err
		}
		var seen []string
		if len(rawIn) > 0 {
			var list []string
			if uerr := json.Unmarshal(rawIn, &list); uerr == nil {
				seen = domain.NormalizeInputs(list)
			}
		}
		candidates = append(candidates, candidate{model: m, seenInputs: seen})
		modelIDs = append(modelIDs, m)
	}
	if err := rows.Err(); err != nil {
		return "", false, err
	}
	if len(candidates) == 0 {
		return "", false, nil
	}
	vendor = domain.VendorKey(vendor)
	catRows, err := s.Pool.Query(ctx, `
		SELECT vendor, model, kind, inputs
		FROM gateway.prices
		WHERE model = ANY($1::text[])
		  AND vendor IN ($2, '')`, modelIDs, vendor)
	if err != nil {
		return "", false, err
	}
	defer catRows.Close()
	type catInfo struct {
		kind   domain.ModelKind
		inputs []string
	}
	catByKey := map[domain.VendorModel]catInfo{}
	for catRows.Next() {
		var v, m, k string
		var in []string
		if err := catRows.Scan(&v, &m, &k, &in); err != nil {
			return "", false, err
		}
		catByKey[domain.VendorModel{Vendor: v, Model: m}] = catInfo{kind: domain.ModelKind(k), inputs: in}
	}
	if err := catRows.Err(); err != nil {
		return "", false, err
	}
	for _, c := range candidates {
		info, ok := catByKey[domain.VendorModel{Vendor: vendor, Model: c.model}]
		if !ok {
			info, ok = catByKey[domain.VendorModel{Vendor: "", Model: c.model}]
		}
		// Uncatalogued models count as chat with text input.
		kind := domain.KindChat
		if ok && info.kind != "" {
			kind = info.kind
		}
		if kind != domain.KindChat {
			continue
		}
		inputs := c.seenInputs
		if len(inputs) == 0 && ok && len(info.inputs) > 0 {
			inputs = info.inputs
		}
		if len(inputs) == 0 {
			inputs = []string{domain.InputText}
		}
		if slices.Contains(inputs, needInput) {
			return c.model, true, nil
		}
	}
	return "", false, nil
}

func scanCatalogRow(scanner interface{ Scan(dest ...any) error }) (domain.CatalogRow, error) {
	var (
		row     domain.CatalogRow
		kind    string
		inRate  pgtype.Numeric
		outRate pgtype.Numeric
		crRate  pgtype.Numeric
		cwRate  pgtype.Numeric
		maxOut  *int
		retires pgtype.Date
	)
	err := scanner.Scan(
		&row.Vendor, &row.Model, &row.Version, &kind, &row.Inputs, &row.Outputs,
		&inRate, &outRate, &crRate, &cwRate,
		&row.Caps, &row.ContextWindow, &maxOut, &row.Reasoning, &row.Lifecycle, &retires,
	)
	if err != nil {
		return domain.CatalogRow{}, err
	}
	row.Kind = domain.ModelKind(kind)
	if maxOut != nil {
		row.MaxOutput = *maxOut
	}
	if retires.Valid {
		t := time.Date(retires.Time.Year(), retires.Time.Month(), retires.Time.Day(), 0, 0, 0, 0, time.UTC)
		row.RetiresAt = &t
	}
	inMicro := numericMicroPtr(inRate)
	outMicro := numericMicroPtr(outRate)
	crMicro := numericMicroPtr(crRate)
	cwMicro := numericMicroPtr(cwRate)
	row.Price = domain.Price{
		Version:                   row.Version,
		InputPerMTok:              domain.USDFromMicro(inMicro),
		OutputPerMTok:             domain.USDFromMicro(outMicro),
		CacheReadMTok:             domain.USDFromMicro(crMicro),
		CacheWriteMTok:            domain.USDFromMicro(cwMicro),
		InputMicroUSDPerMTok:      inMicro,
		OutputMicroUSDPerMTok:     outMicro,
		CacheReadMicroUSDPerMTok:  crMicro,
		CacheWriteMicroUSDPerMTok: cwMicro,
		ContextWindow:             row.ContextWindow,
	}
	return row, nil
}

func numericMicroPtr(n pgtype.Numeric) *int64 {
	if !n.Valid || n.Int == nil || n.NaN || n.InfinityModifier != pgtype.Finite {
		return nil
	}
	exp := int(n.Exp) + 6
	num := new(big.Int).Set(n.Int)
	if exp >= 0 {
		pow := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exp)), nil)
		num.Mul(num, pow)
	} else {
		den := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-exp)), nil)
		half := new(big.Int).Rsh(den, 1)
		if num.Sign() >= 0 {
			num.Add(num, half)
		} else {
			num.Sub(num, half)
		}
		num.Quo(num, den)
	}
	if !num.IsInt64() {
		return nil
	}
	v := num.Int64()
	return &v
}
