package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
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
	a.enabled,
	a.visibility, COALESCE(a.owner_user_id::text, ''),
	CASE WHEN NOT a.house AND identity.may_manage_provider(a.owner_user_id, a.visibility) THEN a.credential_ref ELSE '' END,
	EXISTS (SELECT 1 FROM gateway.credentials AS c WHERE c.ref = a.credential_ref),
	a.key_hint, a.key_changed_at, COALESCE(a.key_changed_by::text, ''),
	a.house`

// visibleTo is the accounts a person is shown: one they can use, and a shared
// one they may manage. Row security says the same.
func visibleTo() string {
	return `(identity.may_use(a.owner_user_id, a.visibility, a.team_ids)
		OR identity.may_manage(a.owner_user_id, a.visibility))`
}

func scanAccount(row pgx.Row) (domain.Provider, error) {
	var a domain.Provider
	err := row.Scan(&a.ID, &a.Provider, &a.Name, &a.Adapter, &a.PlanKind, &a.BaseURL, &a.Status, &a.Enabled,
		&a.Share.Visibility, &a.OwnerUserID, &a.CredentialRef, &a.HasKey, &a.KeyHint, &a.KeyChangedAt, &a.KeyChangedBy, &a.Share.House)
	if a.Provider == "" {
		a.Provider = a.Adapter
	}
	a.Own = a.Share.Visibility == domain.VisibilityPrivate
	a.Disabled = !a.Enabled
	a.Models = []string{}
	a.EnabledModels = []domain.EnabledModel{}
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
		entries, err := modelEntriesOf(ctx, tx, ids)
		if err != nil {
			return nil, err
		}
		for id, list := range entries {
			out[byID[id]].EnabledModels = list
			out[byID[id]].Models = modelNames(list)
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
	entries, err := modelEntriesOf(ctx, q, []string{a.ID})
	if err != nil {
		return domain.Provider{}, err
	}
	if list := entries[a.ID]; list != nil {
		a.EnabledModels = list
		a.Models = modelNames(list)
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

func modelEntriesOf(ctx context.Context, q querier, ids []string) (map[string][]domain.EnabledModel, error) {
	rows, err := q.Query(ctx, `
		SELECT account_id::text, model, listed, retired
		FROM gateway.provider_models
		WHERE account_id = ANY($1::uuid[])
		ORDER BY account_id, position`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]domain.EnabledModel{}
	for rows.Next() {
		var id, model string
		var listed, retired bool
		if err := rows.Scan(&id, &model, &listed, &retired); err != nil {
			return nil, err
		}
		out[id] = append(out[id], domain.EnabledModel{ID: model, Listed: listed, Retired: retired})
	}
	return out, rows.Err()
}

type modelMissState struct {
	misses  int
	retired bool
}

func modelMissStatesOf(ctx context.Context, q querier, id string) (map[string]modelMissState, error) {
	rows, err := q.Query(ctx, `
		SELECT model, misses, retired
		FROM gateway.provider_models
		WHERE account_id = $1::uuid`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]modelMissState{}
	for rows.Next() {
		var model string
		var state modelMissState
		if err := rows.Scan(&model, &state.misses, &state.retired); err != nil {
			return nil, err
		}
		out[model] = state
	}
	return out, rows.Err()
}

func modelNames(entries []domain.EnabledModel) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.ID
	}
	return out
}

func modelsOf(ctx context.Context, q querier, ids []string) (map[string][]string, error) {
	entries, err := modelEntriesOf(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]string, len(entries))
	for id, list := range entries {
		out[id] = modelNames(list)
	}
	return out, nil
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
			(owner_user_id, provider, name, adapter, credential_ref, plan_kind, house, base_url, status, visibility, team_ids, key_hint, key_changed_at, key_changed_by)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, false, $7, $8, $9, $10::uuid[], $11,
			CASE WHEN $12 THEN now() ELSE NULL END, CASE WHEN $12 THEN $1::uuid ELSE NULL END)
		RETURNING id::text`,
		who.User, a.Provider, a.Name, a.Adapter, a.CredentialRef, a.PlanKind, a.BaseURL, domain.StatusNotConnected, visibility, teamIDs, a.KeyHint, sealed != nil).Scan(&id)
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
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.ProviderCreate,
		Outcome:  audit.Success,
		TargetID: id,
		Changes: audit.Changes{
			Fields: []string{"plan_kind", "provider", "team_ids", "visibility"},
			Values: map[string]audit.Change{
				"provider":   {To: made.Provider},
				"plan_kind":  {To: made.PlanKind},
				"visibility": {To: made.Share.Visibility},
				"team_ids":   {To: made.Share.TeamIDs},
			},
		},
	}); err != nil {
		return domain.Provider{}, err
	}
	return made, tx.Commit(ctx)
}

// changeable is an account the person may manage, and not a house account.
const changeable = `(NOT house AND identity.may_manage_provider(owner_user_id, visibility))`

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
	var cur domain.Provider
	var curTeams []string
	err = tx.QueryRow(ctx, `
		SELECT name, plan_kind, base_url, status, enabled, visibility,
		       COALESCE((SELECT array_agg(tid::text ORDER BY tid) FROM unnest(team_ids) AS tid), '{}'),
		       COALESCE(owner_user_id::text, '')
		FROM gateway.provider_accounts
		WHERE id = $1::uuid AND `+changeable+`
		FOR UPDATE`, a.ID).
		Scan(&cur.Name, &cur.PlanKind, &cur.BaseURL, &cur.Status, &cur.Enabled, &cur.Share.Visibility, &curTeams, &cur.OwnerUserID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Provider{}, app.ErrNotFound
	}
	if err != nil {
		return domain.Provider{}, err
	}
	// A shared account turned private counts against the owner's personal cap,
	// under the same lock as a new private account, and cannot still be linked
	// by a shared or installed route.
	if visibility == domain.VisibilityPrivate && cur.Share.Visibility != domain.VisibilityPrivate {
		var sharedUse bool
		if err := tx.QueryRow(ctx, `SELECT gateway.provider_account_used_by_shared_route($1::uuid)`, a.ID).Scan(&sharedUse); err != nil {
			return domain.Provider{}, err
		}
		if sharedUse {
			return domain.Provider{}, domain.Invalid("A provider used by a shared route cannot be made private.")
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('gateway.providers:' || $1::text, 0))`, cur.OwnerUserID); err != nil {
			return domain.Provider{}, err
		}
		var count int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM gateway.provider_accounts
			WHERE owner_user_id = $1::uuid AND visibility = 'private'`, cur.OwnerUserID).Scan(&count); err != nil {
			return domain.Provider{}, err
		}
		if count >= domain.MaxOwnAccounts {
			return domain.Provider{}, app.ErrLimit
		}
	}
	curModels, err := modelsOf(ctx, tx, []string{a.ID})
	if err != nil {
		return domain.Provider{}, err
	}
	prevStates, err := modelMissStatesOf(ctx, tx, a.ID)
	if err != nil {
		return domain.Provider{}, err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE gateway.provider_accounts
		SET name = $2, plan_kind = $3, base_url = $4, enabled = $5, updated_at = now(),
			status = CASE WHEN $6 THEN 'Not connected' ELSE status END,
			checked_at = CASE WHEN $6 THEN NULL ELSE checked_at END,
			visibility = $7, team_ids = $8::uuid[],
			key_hint = CASE WHEN $9 THEN $10 ELSE key_hint END,
			key_changed_at = CASE WHEN $9 THEN now() ELSE key_changed_at END,
			key_changed_by = CASE WHEN $9 THEN $11::uuid ELSE key_changed_by END
		WHERE id = $1::uuid AND `+changeable,
		a.ID, a.Name, a.PlanKind, a.BaseURL, a.Enabled, reset, visibility, teamIDs, sealed != nil, a.KeyHint, who.User)
	if err != nil {
		return domain.Provider{}, mapWrite(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.Provider{}, app.ErrNotFound
	}
	if cur.BaseURL != a.BaseURL {
		if _, err := tx.Exec(ctx, `DELETE FROM gateway.account_models_seen WHERE account_id = $1::uuid`, a.ID); err != nil {
			return domain.Provider{}, err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM gateway.provider_models WHERE account_id = $1::uuid`, a.ID); err != nil {
		return domain.Provider{}, err
	}
	if err := putModelsWithState(ctx, tx, a.ID, a.Models, prevStates); err != nil {
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
	changes, share := providerChanges(cur, curTeams, curModels[a.ID], made, sealed != nil)
	if changes.Fields != nil {
		if err := audit.Record(ctx, tx, audit.Event{
			Action:   audit.ProviderUpdate,
			Outcome:  audit.Success,
			TargetID: a.ID,
			Changes:  changes,
		}); err != nil {
			return domain.Provider{}, err
		}
	}
	if share.Fields != nil {
		if err := audit.Record(ctx, tx, audit.Event{
			Action:   audit.ProviderShare,
			Outcome:  audit.Success,
			TargetID: a.ID,
			Changes:  share,
		}); err != nil {
			return domain.Provider{}, err
		}
	}
	if cur.Enabled != made.Enabled {
		action := audit.ProviderDisable
		if made.Enabled {
			action = audit.ProviderEnable
		}
		if err := audit.Record(ctx, tx, audit.Event{
			Action: action, Outcome: audit.Success, TargetID: a.ID,
			Changes: audit.Changes{Fields: []string{"enabled"}, Values: map[string]audit.Change{
				"enabled": {From: cur.Enabled, To: made.Enabled},
			}},
		}); err != nil {
			return domain.Provider{}, err
		}
	}
	if changes.Fields == nil && share.Fields == nil && cur.Enabled == made.Enabled {
		// The change repeated the stored account, so nothing happened.
		audit.NoChange(ctx)
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
	if _, err := tx.Exec(ctx, `DELETE FROM gateway.account_models_seen WHERE account_id = $1::uuid`, id); err != nil {
		return err
	}
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
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.ProviderDelete,
		Outcome:  audit.Success,
		TargetID: id,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RouteUses lists every route linked to an account. The preceding account
// lookup is subject to row security, so a caller cannot use this read to learn
// about an invisible account.
func (p Providers) RouteUses(ctx context.Context, who app.Caller, id string) ([]string, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, app.ErrNotFound
	}
	tx, err := p.begin(ctx, who)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var one int
	if err := tx.QueryRow(ctx, `SELECT 1 FROM gateway.provider_accounts AS a WHERE a.id = $1::uuid AND `+visibleTo(), id).Scan(&one); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, app.ErrNotFound
		}
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT r.name
		FROM gateway.route_links AS l
		JOIN gateway.routes AS r ON r.id = l.route_id
		WHERE l.provider_account_id = $1::uuid
		ORDER BY r.name`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.ProviderUsesRead,
		Outcome:  audit.Success,
		TargetID: id,
	}); err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
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

// PutSeenModels replaces the discovered models stored for an account.
// Lists exceeding domain.MaxSeenModels (2,000) are refused rather than
// truncated so callers never store a partial list.
func (p Providers) PutSeenModels(ctx context.Context, who app.Caller, id string, models []domain.SeenModel) error {
	return p.putSeenModels(ctx, who, id, "", "", false, models)
}

// PutSeenModelsForEndpoint replaces the discovered models stored for an
// account only if its saved adapter and base URL still match the probed
// endpoint under the row lock.
func (p Providers) PutSeenModelsForEndpoint(ctx context.Context, who app.Caller, id, adapter, baseURL string, models []domain.SeenModel) error {
	return p.putSeenModels(ctx, who, id, adapter, baseURL, true, models)
}

func (p Providers) putSeenModels(ctx context.Context, who app.Caller, id, adapter, baseURL string, checkEndpoint bool, models []domain.SeenModel) error {
	if _, err := uuid.Parse(id); err != nil {
		return app.ErrNotFound
	}
	if len(models) > domain.MaxSeenModels {
		return domain.Invalid(fmt.Sprintf("At most %d discovered models may be stored per account.", domain.MaxSeenModels))
	}
	tx, err := p.begin(ctx, who)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var curAdapter, curBaseURL string
	err = tx.QueryRow(ctx, `
		SELECT adapter, base_url FROM gateway.provider_accounts
		WHERE id = $1::uuid AND `+changeable+`
		FOR UPDATE`, id).Scan(&curAdapter, &curBaseURL)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.ErrNotFound
	}
	if err != nil {
		return err
	}
	if checkEndpoint && (curAdapter != adapter || curBaseURL != baseURL) {
		return nil
	}
	seenSet, err := replaceSeenModels(ctx, tx, id, models)
	if err != nil {
		return err
	}
	// Keep existing enabled models' listed flags aligned with the newly seen list.
	entries, err := modelEntriesOf(ctx, tx, []string{id})
	if err != nil {
		return err
	}
	if list := entries[id]; len(list) > 0 {
		changed := false
		names := make([]string, len(list))
		for i, e := range list {
			names[i] = e.ID
			if e.Listed != seenSet[e.ID] {
				changed = true
			}
		}
		if changed {
			prevStates, err := modelMissStatesOf(ctx, tx, id)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM gateway.provider_models WHERE account_id = $1::uuid`, id); err != nil {
				return err
			}
			if err := putModelsWithState(ctx, tx, id, names, prevStates); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

func replaceSeenModels(ctx context.Context, tx pgx.Tx, id string, models []domain.SeenModel) (map[string]bool, error) {
	if _, err := tx.Exec(ctx, `DELETE FROM gateway.account_models_seen WHERE account_id = $1::uuid`, id); err != nil {
		return nil, err
	}
	seenSet := make(map[string]bool, len(models))
	if len(models) == 0 {
		return seenSet, nil
	}
	batch := &pgx.Batch{}
	for i, m := range models {
		seenSet[m.ID] = true
		var ctxLen *int
		if m.ContextLength > 0 {
			v := m.ContextLength
			ctxLen = &v
		}
		var pricingJSON []byte
		var err error
		if len(m.Pricing) > 0 {
			if pricingJSON, err = json.Marshal(m.Pricing); err != nil {
				return nil, err
			}
		}
		var modJSON []byte
		if m.Modalities != nil && (len(m.Modalities.Input) > 0 || len(m.Modalities.Output) > 0) {
			if modJSON, err = json.Marshal(m.Modalities); err != nil {
				return nil, err
			}
		}
		batch.Queue(`
			INSERT INTO gateway.account_models_seen
				(account_id, model_id, position, display_name, created, context_length, pricing, modalities)
			VALUES ($1::uuid, $2, $3, $4, $5, $6, $7::jsonb, $8::jsonb)`,
			id, m.ID, i+1, m.DisplayName, m.Created, ctxLen, nullableJSON(pricingJSON), nullableJSON(modJSON))
	}
	br := tx.SendBatch(ctx, batch)
	for range models {
		if _, err := br.Exec(); err != nil {
			_ = br.Close()
			return nil, err
		}
	}
	if err := br.Close(); err != nil {
		return nil, err
	}
	return seenSet, nil
}

func nullableJSON(b []byte) *string {
	if len(b) == 0 {
		return nil
	}
	s := string(b)
	return &s
}

// SeenModels returns the discovered models stored for an account the person
// can see, or app.ErrNotFound.
func (p Providers) SeenModels(ctx context.Context, who app.Caller, id string) ([]domain.SeenModel, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, app.ErrNotFound
	}
	tx, err := p.begin(ctx, who)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var ok int
	err = tx.QueryRow(ctx, `
		SELECT 1 FROM gateway.provider_accounts AS a
		WHERE a.id = $1::uuid AND `+visibleTo(), id).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, app.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT model_id, display_name, created, context_length, pricing, modalities, seen_at
		FROM gateway.account_models_seen
		WHERE account_id = $1::uuid
		ORDER BY position`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.SeenModel{}
	for rows.Next() {
		var m domain.SeenModel
		var ctxLen *int
		var pricingRaw, modRaw []byte
		var seenAt time.Time
		if err := rows.Scan(&m.ID, &m.DisplayName, &m.Created, &ctxLen, &pricingRaw, &modRaw, &seenAt); err != nil {
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
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.ProviderModelsRead,
		Outcome:  audit.Success,
		TargetID: id,
	}); err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}

func putModels(ctx context.Context, tx pgx.Tx, id string, models []string) error {
	return putModelsWithState(ctx, tx, id, models, nil)
}

func putModelsWithState(ctx context.Context, tx pgx.Tx, id string, models []string, prev map[string]modelMissState) error {
	for i, model := range models {
		state := prev[model]
		if _, err := tx.Exec(ctx, `
			INSERT INTO gateway.provider_models (account_id, model, position, listed, misses, retired)
			VALUES (
				$1::uuid,
				$2,
				$3,
				EXISTS (
					SELECT 1 FROM gateway.account_models_seen AS s
					WHERE s.account_id = $1::uuid AND s.model_id = $2
				),
				$4,
				$5
			)`, id, model, i+1, state.misses, state.retired); err != nil {
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

// SealTest records the outcome of one provider test in a transaction of its
// own: a test has no business transaction, so the event commits alone,
// before the answer goes out. A test that did not work, one refused before
// it probed included, is a failure.
func (p Providers) SealTest(ctx context.Context, id string, ok bool) error {
	if p.Pool == nil {
		return fmt.Errorf("gateway: no database")
	}
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	outcome := audit.Failure
	if ok {
		outcome = audit.Success
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.ProviderTest,
		Outcome:  outcome,
		TargetID: id,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// providerChanges compares an account before and after an update. A content
// field (name, base URL, models) keeps the field name alone, and so does a
// replaced key, as credential; plan kind, status, visibility and teams carry
// from and to. The second Changes holds the sharing move, and stays empty
// when who the account is for did not change.
func providerChanges(before domain.Provider, beforeTeams, beforeModels []string, after domain.Provider, rekeyed bool) (changes, share audit.Changes) {
	put := func(into *audit.Changes, field string, from, to any) {
		into.Fields = append(into.Fields, field)
		if into.Values == nil {
			into.Values = map[string]audit.Change{}
		}
		into.Values[field] = audit.Change{From: from, To: to}
	}
	named := func(into *audit.Changes, field string) {
		into.Fields = append(into.Fields, field)
	}
	if before.Name != after.Name {
		named(&changes, "name")
	}
	if before.PlanKind != after.PlanKind {
		put(&changes, "plan_kind", before.PlanKind, after.PlanKind)
	}
	if before.BaseURL != after.BaseURL {
		named(&changes, "base_url")
	}
	if !slices.Equal(beforeModels, after.Models) {
		named(&changes, "models")
	}
	if before.Status != after.Status {
		put(&changes, "status", before.Status, after.Status)
	}
	fromTeams, toTeams := sortedTeams(beforeTeams), sortedTeams(after.Share.TeamIDs)
	if before.Share.Visibility != after.Share.Visibility || !slices.Equal(fromTeams, toTeams) {
		put(&changes, "visibility", before.Share.Visibility, after.Share.Visibility)
		put(&changes, "team_ids", fromTeams, toTeams)
		put(&share, "visibility", before.Share.Visibility, after.Share.Visibility)
		put(&share, "team_ids", fromTeams, toTeams)
	}
	if rekeyed {
		named(&changes, "credential")
	}
	sort.Strings(changes.Fields)
	sort.Strings(share.Fields)
	return changes, share
}

// sortedTeams copies a team list in one order, so a set read two ways is
// equal to itself. An empty list comes back empty, never nil, so the stored
// value is an empty array.
func sortedTeams(ids []string) []string {
	out := make([]string, 0, len(ids))
	out = append(out, ids...)
	sort.Strings(out)
	return out
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
