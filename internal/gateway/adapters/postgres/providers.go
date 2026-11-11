package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

// Providers keeps the provider accounts people add, with their models and
// sealed keys. Every call runs in a transaction that names the person to row
// security, which shows them their own accounts and the global ones and lets
// them write only their own.
type Providers struct {
	Pool *pgxpool.Pool
}

var _ app.Accounts = Providers{}

// querier is what a pool and a transaction both do.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (p Providers) begin(ctx context.Context, user string) (pgx.Tx, error) {
	if p.Pool == nil {
		return nil, fmt.Errorf("gateway: no database")
	}
	if _, err := uuid.Parse(user); err != nil {
		return nil, fmt.Errorf("gateway: provider owner")
	}
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('upmo.user_id', $1, true)`, user); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

const accountColumns = `a.id::text, a.provider, a.name, a.adapter, a.plan_kind, a.base_url, a.status,
	a.owner_user_id IS NOT NULL,
	CASE WHEN a.owner_user_id IS NOT NULL THEN a.credential_ref ELSE '' END,
	EXISTS (SELECT 1 FROM gateway.credentials AS c WHERE c.ref = a.credential_ref)`

func scanAccount(row pgx.Row) (domain.Provider, error) {
	var a domain.Provider
	err := row.Scan(&a.ID, &a.Provider, &a.Name, &a.Adapter, &a.PlanKind, &a.BaseURL, &a.Status,
		&a.Own, &a.CredentialRef, &a.HasKey)
	if a.Provider == "" {
		a.Provider = a.Adapter
	}
	a.Models = []string{}
	return a, err
}

// List returns own accounts first, then global ones, each by name.
func (p Providers) List(ctx context.Context, user string) ([]domain.Provider, error) {
	tx, err := p.begin(ctx, user)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
		SELECT `+accountColumns+`
		FROM gateway.provider_accounts AS a
		WHERE a.owner_user_id IS NULL OR a.owner_user_id = $1::uuid
		ORDER BY (a.owner_user_id IS NULL), a.name`, user)
	if err != nil {
		return nil, err
	}
	out := []domain.Provider{}
	byID := map[string]int{}
	var ids []string
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		byID[a.ID] = len(out)
		ids = append(ids, a.ID)
		out = append(out, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		models, err := modelsOf(ctx, tx, ids)
		if err != nil {
			return nil, err
		}
		for id, list := range models {
			out[byID[id]].Models = list
		}
	}
	return out, tx.Commit(ctx)
}

// Get returns an account the person can see, or app.ErrNotFound.
func (p Providers) Get(ctx context.Context, user, id string) (domain.Provider, error) {
	tx, err := p.begin(ctx, user)
	if err != nil {
		return domain.Provider{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	a, err := getAccount(ctx, tx, user, id)
	if err != nil {
		return domain.Provider{}, err
	}
	return a, tx.Commit(ctx)
}

func getAccount(ctx context.Context, q querier, user, id string) (domain.Provider, error) {
	if _, err := uuid.Parse(id); err != nil {
		return domain.Provider{}, app.ErrNotFound
	}
	a, err := scanAccount(q.QueryRow(ctx, `
		SELECT `+accountColumns+`
		FROM gateway.provider_accounts AS a
		WHERE a.id = $1::uuid AND (a.owner_user_id IS NULL OR a.owner_user_id = $2::uuid)`, id, user))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Provider{}, app.ErrNotFound
	}
	if err != nil {
		return domain.Provider{}, err
	}
	models, err := modelsOf(ctx, q, []string{a.ID})
	if err != nil {
		return domain.Provider{}, err
	}
	if list := models[a.ID]; list != nil {
		a.Models = list
	}
	return a, nil
}

func modelsOf(ctx context.Context, q querier, ids []string) (map[string][]string, error) {
	rows, err := q.Query(ctx, `
		SELECT account_id::text, model
		FROM gateway.provider_models
		WHERE account_id = ANY($1::uuid[])
		ORDER BY account_id, position`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var id, model string
		if err := rows.Scan(&id, &model); err != nil {
			return nil, err
		}
		out[id] = append(out[id], model)
	}
	return out, rows.Err()
}

// Create stores a new own account, its models and its sealed key in one
// transaction. A person's accounts are counted under a lock for that person,
// so two requests cannot both take the last place.
func (p Providers) Create(ctx context.Context, user string, a domain.Provider, sealed *domain.Sealed) (domain.Provider, error) {
	tx, err := p.begin(ctx, user)
	if err != nil {
		return domain.Provider{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('gateway.providers:' || $1::text, 0))`, user); err != nil {
		return domain.Provider{}, err
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM gateway.provider_accounts WHERE owner_user_id = $1::uuid`, user).Scan(&count); err != nil {
		return domain.Provider{}, err
	}
	if count >= domain.MaxOwnAccounts {
		return domain.Provider{}, app.ErrLimit
	}
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO gateway.provider_accounts
			(owner_user_id, provider, name, adapter, credential_ref, plan_kind, house, base_url, status)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, false, $7, $8)
		RETURNING id::text`,
		user, a.Provider, a.Name, a.Adapter, a.CredentialRef, a.PlanKind, a.BaseURL, domain.StatusNotConnected).Scan(&id)
	if err != nil {
		return domain.Provider{}, mapWrite(err)
	}
	if err := putModels(ctx, tx, id, a.Models); err != nil {
		return domain.Provider{}, err
	}
	if sealed != nil {
		if err := putSealed(ctx, tx, a.CredentialRef, *sealed); err != nil {
			return domain.Provider{}, err
		}
	}
	made, err := getAccount(ctx, tx, user, id)
	if err != nil {
		return domain.Provider{}, err
	}
	return made, tx.Commit(ctx)
}

// Update writes an own account's fields, replaces its models and, when sealed
// is set, replaces its key, in one transaction.
func (p Providers) Update(ctx context.Context, user string, a domain.Provider, sealed *domain.Sealed, reset bool) (domain.Provider, error) {
	tx, err := p.begin(ctx, user)
	if err != nil {
		return domain.Provider{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		UPDATE gateway.provider_accounts
		SET name = $3, plan_kind = $4, base_url = $5, updated_at = now(),
			status = CASE WHEN $6 THEN 'Not connected' ELSE status END,
			checked_at = CASE WHEN $6 THEN NULL ELSE checked_at END
		WHERE id = $1::uuid AND owner_user_id = $2::uuid`,
		a.ID, user, a.Name, a.PlanKind, a.BaseURL, reset)
	if err != nil {
		return domain.Provider{}, mapWrite(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.Provider{}, app.ErrNotFound
	}
	if _, err := tx.Exec(ctx, `DELETE FROM gateway.provider_models WHERE account_id = $1::uuid`, a.ID); err != nil {
		return domain.Provider{}, err
	}
	if err := putModels(ctx, tx, a.ID, a.Models); err != nil {
		return domain.Provider{}, err
	}
	if sealed != nil {
		if err := putSealed(ctx, tx, a.CredentialRef, *sealed); err != nil {
			return domain.Provider{}, err
		}
	}
	made, err := getAccount(ctx, tx, user, a.ID)
	if err != nil {
		return domain.Provider{}, err
	}
	return made, tx.Commit(ctx)
}

// Remove deletes an own account with its models and key in one transaction,
// unless a route still links to it.
func (p Providers) Remove(ctx context.Context, user, id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return app.ErrNotFound
	}
	tx, err := p.begin(ctx, user)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var ref string
	err = tx.QueryRow(ctx, `
		SELECT credential_ref FROM gateway.provider_accounts
		WHERE id = $1::uuid AND owner_user_id = $2::uuid
		FOR UPDATE`, id, user).Scan(&ref)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrNotFound
	}
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT r.name
		FROM gateway.route_links AS l
		JOIN gateway.routes AS r ON r.id = l.route_id
		WHERE l.provider_account_id = $1::uuid
		ORDER BY r.name`, id)
	if err != nil {
		return err
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		names = append(names, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(names) > 0 {
		return app.InUseError{Routes: names}
	}
	// Models and the key go first: their row security looks the account up.
	if _, err := tx.Exec(ctx, `DELETE FROM gateway.provider_models WHERE account_id = $1::uuid`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM gateway.credentials WHERE ref = $1`, ref); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM gateway.provider_accounts WHERE id = $1::uuid AND owner_user_id = $2::uuid`, id, user); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return app.InUseError{}
		}
		return err
	}
	return tx.Commit(ctx)
}

// Sealed returns the stored key for an own account's ref, or app.ErrNotFound.
func (p Providers) Sealed(ctx context.Context, user, ref string) (domain.Sealed, error) {
	tx, err := p.begin(ctx, user)
	if err != nil {
		return domain.Sealed{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var s domain.Sealed
	err = tx.QueryRow(ctx, `SELECT ciphertext, nonce, key_id FROM gateway.credentials WHERE ref = $1`, ref).
		Scan(&s.Ciphertext, &s.Nonce, &s.KeyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Sealed{}, app.ErrNotFound
	}
	if err != nil {
		return domain.Sealed{}, err
	}
	return s, tx.Commit(ctx)
}

// Note stores the status a test found for an own account.
func (p Providers) Note(ctx context.Context, user, id, status string) error {
	if _, err := uuid.Parse(id); err != nil {
		return app.ErrNotFound
	}
	tx, err := p.begin(ctx, user)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		UPDATE gateway.provider_accounts SET status = $3, checked_at = now(), updated_at = now()
		WHERE id = $1::uuid AND owner_user_id = $2::uuid`, id, user, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return app.ErrNotFound
	}
	return tx.Commit(ctx)
}

func putModels(ctx context.Context, tx pgx.Tx, id string, models []string) error {
	for i, model := range models {
		if _, err := tx.Exec(ctx, `
			INSERT INTO gateway.provider_models (account_id, model, position)
			VALUES ($1::uuid, $2, $3)`, id, model, i+1); err != nil {
			return err
		}
	}
	return nil
}

func putSealed(ctx context.Context, tx pgx.Tx, ref string, s domain.Sealed) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO gateway.credentials (ref, ciphertext, nonce, key_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (ref) DO UPDATE
		SET ciphertext = EXCLUDED.ciphertext, nonce = EXCLUDED.nonce,
			key_id = EXCLUDED.key_id, created_at = now()`,
		ref, s.Ciphertext, s.Nonce, s.KeyID)
	return err
}

// mapWrite turns a name collision into app.ErrDuplicate. Other errors pass
// through; a database error carries no key, since a key is only ever bound as
// sealed bytes.
func mapWrite(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" && strings.Contains(pgErr.ConstraintName, "owner_name") {
		return app.ErrDuplicate
	}
	return err
}

// SealedCredential returns the stored key for ref without a person in the
// request. It is for the gateway process, which runs as the table owner and
// is let in by the service policy; a person's request never reaches it.
func (s Store) SealedCredential(ctx context.Context, ref string) (domain.Sealed, error) {
	var out domain.Sealed
	err := s.Pool.QueryRow(ctx, `SELECT ciphertext, nonce, key_id FROM gateway.credentials WHERE ref = $1`, ref).
		Scan(&out.Ciphertext, &out.Nonce, &out.KeyID)
	if err != nil {
		return domain.Sealed{}, err
	}
	return out, nil
}

// RecordStatus stores what a model call learned about the account with this
// credential ref. An outcome that started before the account last changed is
// stale — an edit reset the status while the attempt ran — and is dropped. An
// unchanged outcome is written again only after a minute, so a busy account
// costs a write a minute; the wait is kept in the row itself, so an edit that
// resets the account clears it at once and the next outcome is heard even
// when it repeats the last one. It is best-effort and called off the calling
// path.
func (s Store) RecordStatus(ctx context.Context, ref, status string, started time.Time) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE gateway.provider_accounts SET status = $2, checked_at = now(), updated_at = now()
		WHERE credential_ref = $1 AND updated_at <= $3
			AND (status IS DISTINCT FROM $2 OR checked_at IS NULL OR checked_at <= now() - interval '1 minute')`,
		ref, status, started)
	return err
}
