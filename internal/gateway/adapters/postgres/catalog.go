package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

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

	if _, err := tx.Exec(ctx, `DELETE FROM gateway.prices`); err != nil {
		return err
	}
	for _, row := range rows {
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
		var retiresAt any
		if row.RetiresAt != nil {
			retiresAt = row.RetiresAt.UTC().Format("2006-01-02")
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO gateway.prices (
				vendor, model, version, kind, inputs, outputs,
				input_usd_per_mtok, output_usd_per_mtok,
				cache_read_usd_per_mtok, cache_write_usd_per_mtok,
				capabilities, context_window, max_output, reasoning, lifecycle, retires_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16::date)`,
			domain.VendorKey(row.Vendor), row.Model, row.Version, string(kind), inputs, outputs,
			numericOrNil(row.Price.InputPerMTok),
			numericOrNil(row.Price.OutputPerMTok),
			numericOrNil(row.Price.CacheReadMTok),
			numericOrNil(row.Price.CacheWriteMTok),
			caps,
			ctxWindow,
			maxOut,
			row.Reasoning,
			row.Lifecycle,
			retiresAt,
		)
		if err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO gateway.catalog_meta (id, version) VALUES (1, $1)
		ON CONFLICT (id) DO UPDATE SET version = EXCLUDED.version`, version); err != nil {
		return err
	}
	return tx.Commit(ctx)
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
	row.Price = domain.Price{
		Version:        row.Version,
		InputPerMTok:   numericPtr(inRate),
		OutputPerMTok:  numericPtr(outRate),
		CacheReadMTok:  numericPtr(crRate),
		CacheWriteMTok: numericPtr(cwRate),
		ContextWindow:  row.ContextWindow,
	}
	return row, nil
}

func numericPtr(n pgtype.Numeric) *float64 {
	if !n.Valid {
		return nil
	}
	f, err := n.Float64Value()
	if err != nil || !f.Valid {
		return nil
	}
	v := f.Float64
	return &v
}
