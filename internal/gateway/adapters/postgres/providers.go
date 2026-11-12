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
// sealed keys. Every call runs in a transaction that names the person, their
// teams, and whether they hold settings:manage. Row security shows them the
// accounts they can use and the shared ones they may manage, and lets them
// write their own private account, or a shared one when they may manage it.
type Providers struct {
	Pool *pgxpool.Pool
}

var _ app.Accounts = Providers{}

// querier is what a pool and a transaction both do.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (p Providers) begin(ctx context.Context, who app.Caller) (pgx.Tx, error) {
	if p.Pool == nil {
		return nil, fmt.Errorf("gateway: no database")
	}
	if _, err := uuid.Parse(who.User); err != nil {
		return nil, fmt.Errorf("gateway: provider owner")
	}
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	// The caller read allow and teams for this request. The next request
	// reads them again, so a team change takes effect then.
	if _, err := tx.Exec(ctx, `
		SELECT set_config('upmo.user_id', $1, true),
			set_config('upmo.allow', $2, true),
			set_config('upmo.teams', $3, true)`, who.User, who.Allow, uuidList(who.Teams)); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

// accountColumns reads an account as a person sees it. The credential ref is
// shown only for an account the person may manage, and never for a house key
// or to someone who can only use a shared account.
const accountColumns = `a.id::text, a.provider, a.name, a.adapter, a.plan_kind, a.base_url, a.status,
	a.visibility, COALESCE(a.owner_user_id::text, ''),
	CASE WHEN NOT a.house AND identity.may_manage(a.owner_user_id, a.visibility) THEN a.credential_ref ELSE '' END,
	EXISTS (SELECT 1 FROM gateway.credentials AS c WHERE c.ref = a.credential_ref),
	a.house`

// visibleTo is the accounts a person is shown: one they can use, and a shared
// one they may manage. Row security says the same.
func visibleTo() string {
	return `(identity.may_use(a.owner_user_id, a.visibility, a.team_ids)
		OR identity.may_manage(a.owner_user_id, a.visibility))`
}

func scanAccount(row pgx.Row) (domain.Provider, error) {
	var a domain.Provider
	err := row.Scan(&a.ID, &a.Provider, &a.Name, &a.Adapter, &a.PlanKind, &a.BaseURL, &a.Status,
		&a.Share.Visibility, &a.OwnerUserID, &a.CredentialRef, &a.HasKey, &a.Share.House)
	if a.Provider == "" {
		a.Provider = a.Adapter
	}
	a.Own = a.Share.Visibility == domain.VisibilityPrivate
	a.Models = []string{}
	a.Share.TeamIDs = []string{}
	a.Share.TeamNames = []string{}
	return a, err
}

// List returns own accounts first, then team ones, then workspace ones, each
// by name.
func (p Providers) List(ctx context.Context, who app.Caller) ([]domain.Provider, error) {
	tx, err := p.begin(ctx, who)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
		SELECT `+accountColumns+`
		FROM gateway.provider_accounts AS a
		WHERE `+visibleTo()+`
		ORDER BY CASE
			WHEN a.house THEN 3
			WHEN a.visibility = 'private' THEN 0
			WHEN a.visibility = 'team' THEN 1
			ELSE 2
		END, a.name`)
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
		teams, err := teamsOf(ctx, tx, ids)
		if err != nil {
			return nil, err
		}
		for id, list := range teams {
			out[byID[id]].Share.TeamIDs, out[byID[id]].Share.TeamNames = list.ids, list.names
		}
	}
	return out, tx.Commit(ctx)
}

// Get returns an account the person can see, or app.ErrNotFound.
func (p Providers) Get(ctx context.Context, who app.Caller, id string) (domain.Provider, error) {
	tx, err := p.begin(ctx, who)
	if err != nil {
		return domain.Provider{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	a, err := getAccount(ctx, tx, id)
	if err != nil {
		return domain.Provider{}, err
	}
	return a, tx.Commit(ctx)
}

func getAccount(ctx context.Context, q querier, id string) (domain.Provider, error) {
	if _, err := uuid.Parse(id); err != nil {
		return domain.Provider{}, app.ErrNotFound
	}
	a, err := scanAccount(q.QueryRow(ctx, `
		SELECT `+accountColumns+`
		FROM gateway.provider_accounts AS a
		WHERE a.id = $1::uuid AND `+visibleTo(), id))
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
	teams, err := teamsOf(ctx, q, []string{a.ID})
	if err != nil {
		return domain.Provider{}, err
	}
	if list, ok := teams[a.ID]; ok {
		a.Share.TeamIDs, a.Share.TeamNames = list.ids, list.names
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

// sharedWith is the teams one account is shared with, by name.
type sharedWith struct{ ids, names []string }

// teamsOf reads the teams each account is shared with.
func teamsOf(ctx context.Context, q querier, ids []string) (map[string]sharedWith, error) {
	rows, err := q.Query(ctx, `
		SELECT a.id::text, tid::text, team.name
		FROM gateway.provider_accounts AS a
		CROSS JOIN LATERAL unnest(a.team_ids) AS tid
		JOIN identity.teams AS team ON team.id = tid
		WHERE a.id = ANY($1::uuid[])
		ORDER BY a.id, team.name, tid`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]sharedWith{}
	for rows.Next() {
		var account, id, name string
		if err := rows.Scan(&account, &id, &name); err != nil {
			return nil, err
		}
		got := out[account]
		got.ids, got.names = append(got.ids, id), append(got.names, name)
		out[account] = got
	}
	return out, rows.Err()
}

// Create stores a new account, its models and its sealed key in one
// transaction. A private account is counted under a lock for its person, so
// two requests cannot both take the last place. A shared account has no cap.
func (p Providers) Create(ctx context.Context, who app.Caller, a domain.Provider, sealed *domain.Sealed) (domain.Provider, error) {
	tx, err := p.begin(ctx, who)
	if err != nil {
		return domain.Provider{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	visibility, teamIDs := shareOf(a)
	if err := knownTeams(ctx, tx, teamIDs); err != nil {
		return domain.Provider{}, err
	}
	if visibility == domain.VisibilityPrivate {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('gateway.providers:' || $1::text, 0))`, who.User); err != nil {
			return domain.Provider{}, err
		}
		var count int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM gateway.provider_accounts
			WHERE owner_user_id = $1::uuid AND visibility = 'private'`, who.User).Scan(&count); err != nil {
			return domain.Provider{}, err
		}
		if count >= domain.MaxOwnAccounts {
			return domain.Provider{}, app.ErrLimit
		}
	}
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO gateway.provider_accounts
			(owner_user_id, provider, name, adapter, credential_ref, plan_kind, house, base_url, status, visibility, team_ids)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, false, $7, $8, $9, $10::uuid[])
		RETURNING id::text`,
		who.User, a.Provider, a.Name, a.Adapter, a.CredentialRef, a.PlanKind, a.BaseURL, domain.StatusNotConnected, visibility, teamIDs).Scan(&id)
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
	made, err := getAccount(ctx, tx, id)
	if err != nil {
		return domain.Provider{}, err
	}
	return made, tx.Commit(ctx)
}

// changeable is an account the person may manage, and not a house account.
const changeable = `(NOT house AND identity.may_manage(owner_user_id, visibility))`

// Update writes an account's fields, replaces its models and, for a shared
// account, the teams it is shared with and, when sealed is set, replaces its
// key, in one transaction.
func (p Providers) Update(ctx context.Context, who app.Caller, a domain.Provider, sealed *domain.Sealed, reset bool) (domain.Provider, error) {
	tx, err := p.begin(ctx, who)
	if err != nil {
		return domain.Provider{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	visibility, teamIDs := shareOf(a)
	if err := knownTeams(ctx, tx, teamIDs); err != nil {
		return domain.Provider{}, err
	}
	var current, owner string
	err = tx.QueryRow(ctx, `
		SELECT visibility, owner_user_id::text FROM gateway.provider_accounts
		WHERE id = $1::uuid AND `+changeable, a.ID).Scan(&current, &owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Provider{}, app.ErrNotFound
	}
	if err != nil {
		return domain.Provider{}, err
	}
	// A shared account turned private counts against the owner's personal cap,
	// under the same lock as a new private account.
	if visibility == domain.VisibilityPrivate && current != domain.VisibilityPrivate {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('gateway.providers:' || $1::text, 0))`, owner); err != nil {
			return domain.Provider{}, err
		}
		var count int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM gateway.provider_accounts
			WHERE owner_user_id = $1::uuid AND visibility = 'private'`, owner).Scan(&count); err != nil {
			return domain.Provider{}, err
		}
		if count >= domain.MaxOwnAccounts {
			return domain.Provider{}, app.ErrLimit
		}
	}
	tag, err := tx.Exec(ctx, `
		UPDATE gateway.provider_accounts
		SET name = $2, plan_kind = $3, base_url = $4, updated_at = now(),
			status = CASE WHEN $5 THEN 'Not connected' ELSE status END,
			checked_at = CASE WHEN $5 THEN NULL ELSE checked_at END,
			visibility = $6, team_ids = $7::uuid[]
		WHERE id = $1::uuid AND `+changeable,
		a.ID, a.Name, a.PlanKind, a.BaseURL, reset, visibility, teamIDs)
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
	made, err := getAccount(ctx, tx, a.ID)
	if err != nil {
		return domain.Provider{}, err
	}
	return made, tx.Commit(ctx)
}

// Remove deletes an account with its models, teams and key in one
// transaction, unless a route still links to it.
func (p Providers) Remove(ctx context.Context, who app.Caller, id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return app.ErrNotFound
	}
	tx, err := p.begin(ctx, who)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var ref string
	err = tx.QueryRow(ctx, `
		SELECT credential_ref FROM gateway.provider_accounts
		WHERE id = $1::uuid AND `+changeable+`
		FOR UPDATE`, id).Scan(&ref)
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
	if _, err := tx.Exec(ctx, `DELETE FROM gateway.provider_accounts WHERE id = $1::uuid AND `+changeable, id); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return app.InUseError{}
		}
		return err
	}
	return tx.Commit(ctx)
}

// Sealed returns the stored key for an account's ref, or app.ErrNotFound. Row
// security hands it only to the owner of a personal account and to a person
// who may share, for a shared one; never to a member of a team it is shared
// with.
func (p Providers) Sealed(ctx context.Context, who app.Caller, ref string) (domain.Sealed, error) {
	tx, err := p.begin(ctx, who)
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

// Note stores the status a test found for an account.
func (p Providers) Note(ctx context.Context, who app.Caller, id, status string) error {
	if _, err := uuid.Parse(id); err != nil {
		return app.ErrNotFound
	}
	tx, err := p.begin(ctx, who)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		UPDATE gateway.provider_accounts SET status = $2, checked_at = now(), updated_at = now()
		WHERE id = $1::uuid AND `+changeable, id, status)
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

// shareOf is the visibility and teams an account is stored with. A private
// account has no teams. A team account has the teams it was shared with. Any
// other shared account is for everyone.
func shareOf(a domain.Provider) (string, []string) {
	vis := a.Share.Visibility
	if vis == "" {
		if a.Own {
			vis = domain.VisibilityPrivate
		} else {
			vis = domain.VisibilityEveryone
		}
	}
	switch vis {
	case domain.VisibilityTeam:
		if a.Share.TeamIDs == nil {
			return domain.VisibilityTeam, []string{}
		}
		return domain.VisibilityTeam, a.Share.TeamIDs
	case domain.VisibilityPrivate:
		return domain.VisibilityPrivate, []string{}
	default:
		return domain.VisibilityEveryone, []string{}
	}
}

// uuidList is the Postgres uuid array literal the settings statement writes.
func uuidList(ids []string) string {
	if len(ids) == 0 {
		return "{}"
	}
	return "{" + strings.Join(ids, ",") + "}"
}

// knownTeams refuses a team id that is not a team. An empty list is fine.
func knownTeams(ctx context.Context, tx pgx.Tx, teams []string) error {
	if len(teams) == 0 {
		return nil
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM identity.teams WHERE id = ANY($1::uuid[])`, teams).Scan(&n); err != nil {
		return err
	}
	if n != len(teams) {
		return domain.Invalid("Choose teams from the list.")
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
