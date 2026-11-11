package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

// Store reads routing rows. It does not resolve secrets.
type Store struct {
	Pool *pgxpool.Pool
}

var _ app.Teams = Store{}

// accountTeams is the ids of the teams an account is shared with, as text. It
// is empty for a personal account and for a workspace one.
const accountTeams = `COALESCE((
	SELECT array_agg(t.team_id::text ORDER BY t.team_id)
	FROM gateway.provider_account_teams AS t
	WHERE t.account_id = %s.id), '{}'::text[])`

func teamsOfAccount(alias string) string { return fmt.Sprintf(accountTeams, alias) }

// TeamsOf lists the ids of the teams a person is on, by id. It reads the
// identity tables as the table owner, like the rest of Store, so the caller
// settles what the person may do with them. A person that is not a UUID is on
// no team.
func (s Store) TeamsOf(ctx context.Context, user string) ([]string, error) {
	if _, err := uuid.Parse(user); err != nil {
		return nil, nil
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT team_id::text FROM identity.team_members WHERE user_id = $1::uuid ORDER BY team_id`, user)
	if err != nil {
		return nil, fmt.Errorf("gateway: teams of a person: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("gateway: teams of a person: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("gateway: teams of a person: %w", err)
	}
	return out, nil
}

// ByName loads one route and its links in position order.
func (s Store) ByName(ctx context.Context, name string) (domain.Route, error) {
	var r domain.Route
	var cap, decision string
	err := s.Pool.QueryRow(ctx, `
		SELECT name, capability, decision
		FROM gateway.routes
		WHERE name = $1`, name).Scan(&r.Name, &cap, &decision)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Route{}, fmt.Errorf("gateway: no route %q", name)
		}
		return domain.Route{}, err
	}
	r.Capability = domain.Capability(cap)
	r.Decision = domain.Decision(decision)
	rows, err := s.Pool.Query(ctx, `
		SELECT link.position, link.model, link.weight, link.retry_budget, link.required_capabilities,
			account.id::text, account.adapter, account.credential_ref, account.plan_kind, account.house, account.base_url,
			COALESCE(account.owner_user_id::text, ''), `+teamsOfAccount("account")+`
		FROM gateway.route_links AS link
		JOIN gateway.provider_accounts AS account ON account.id = link.provider_account_id
		WHERE link.route_id = (SELECT id FROM gateway.routes WHERE name = $1)
		ORDER BY link.position`, name)
	if err != nil {
		return domain.Route{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var link domain.Link
		if err := rows.Scan(
			&link.Position, &link.Model, &link.Weight, &link.RetryBudget, &link.Need,
			&link.Account.ID, &link.Account.Adapter, &link.Account.CredentialRef,
			&link.Account.PlanKind, &link.Account.House, &link.Account.BaseURL, &link.Account.OwnerUserID,
			&link.Account.TeamIDs,
		); err != nil {
			return domain.Route{}, err
		}
		r.Links = append(r.Links, link)
	}
	if err := rows.Err(); err != nil {
		return domain.Route{}, err
	}
	return r, r.Check()
}

// PinAccount loads the provider account a pin names. It reads as the table
// owner like ByName, so the caller decides whether the person may use the
// account, from the teams it is shared with. ok is false when there is no
// such account, or it does not list the model, as PinProvider requires.
func (s Store) PinAccount(ctx context.Context, id, model string) (domain.Account, bool, error) {
	var a domain.Account
	err := s.Pool.QueryRow(ctx, `
		SELECT a.id::text, a.adapter, a.credential_ref, a.plan_kind, a.house, a.base_url, COALESCE(a.owner_user_id::text, ''),
			`+teamsOfAccount("a")+`
		FROM gateway.provider_accounts AS a
		WHERE a.id = $1::uuid
		  AND EXISTS (SELECT 1 FROM gateway.provider_models AS m WHERE m.account_id = a.id AND m.model = $2)`, id, model).Scan(&a.ID, &a.Adapter, &a.CredentialRef, &a.PlanKind, &a.House, &a.BaseURL, &a.OwnerUserID, &a.TeamIDs)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Account{}, false, nil
	}
	if err != nil {
		return domain.Account{}, false, err
	}
	return a, true, nil
}

// PinProvider resolves a provider name and a model for one person, who is on
// teams, as the table owner like PinAccount: their own account of that
// provider that lists the model comes first, then one shared with a team of
// theirs, then a workspace one (the house account before the others), each by
// name. The provider matches without regard to case, and an account with no
// provider label counts as its adapter. ok is false when no account the
// person may use lists the model.
func (s Store) PinProvider(ctx context.Context, user string, teams []string, provider, model string) (domain.Account, bool, error) {
	if teams == nil {
		teams = []string{}
	}
	var a domain.Account
	err := s.Pool.QueryRow(ctx, `
		SELECT a.id::text, a.adapter, a.credential_ref, a.plan_kind, a.house, a.base_url, COALESCE(a.owner_user_id::text, ''),
			`+teamsOfAccount("a")+`
		FROM gateway.provider_accounts AS a
		WHERE lower(COALESCE(NULLIF(a.provider, ''), a.adapter)) = lower($2)
		  AND (
			a.owner_user_id = $1::uuid
			OR (a.owner_user_id IS NULL AND (
				NOT EXISTS (SELECT 1 FROM gateway.provider_account_teams AS t WHERE t.account_id = a.id)
				OR EXISTS (SELECT 1 FROM gateway.provider_account_teams AS t WHERE t.account_id = a.id AND t.team_id = ANY ($4::uuid[]))
			))
		  )
		  AND EXISTS (SELECT 1 FROM gateway.provider_models AS m WHERE m.account_id = a.id AND m.model = $3)
		ORDER BY
			CASE
				WHEN a.owner_user_id IS NOT NULL THEN 0
				WHEN EXISTS (SELECT 1 FROM gateway.provider_account_teams AS t WHERE t.account_id = a.id) THEN 1
				ELSE 2
			END,
			a.house DESC, a.name
		LIMIT 1`, user, provider, model, teams).Scan(&a.ID, &a.Adapter, &a.CredentialRef, &a.PlanKind, &a.House, &a.BaseURL, &a.OwnerUserID, &a.TeamIDs)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Account{}, false, nil
	}
	if err != nil {
		return domain.Account{}, false, err
	}
	return a, true, nil
}

// Names lists route names for the Models endpoint.
func (s Store) Names(ctx context.Context) ([]string, error) {
	rows, err := s.Pool.Query(ctx, `SELECT name FROM gateway.routes ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// List returns every route with hops in position order. Credential refs stay
// out; each hop names the account it calls, so the HTTP layer can compute
// what the calling person can use.
func (s Store) List(ctx context.Context) ([]app.ListedRoute, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT r.name, r.capability, r.decision,
			l.position, account.adapter, l.model, l.weight, l.retry_budget, account.id::text
		FROM gateway.routes AS r
		LEFT JOIN gateway.route_links AS l ON l.route_id = r.id
		LEFT JOIN gateway.provider_accounts AS account ON account.id = l.provider_account_id
		ORDER BY r.name, l.position`)
	if err != nil {
		return nil, fmt.Errorf("gateway: list routes: %w", err)
	}
	defer rows.Close()

	out := make([]app.ListedRoute, 0)
	var cur *app.ListedRoute
	for rows.Next() {
		var name, capability, decision string
		var position *int
		var adapter, model, accountID *string
		var weight, retry *int
		if err := rows.Scan(&name, &capability, &decision, &position, &adapter, &model, &weight, &retry, &accountID); err != nil {
			return nil, fmt.Errorf("gateway: list routes: %w", err)
		}
		if cur == nil || cur.Name != name {
			out = append(out, app.ListedRoute{Name: name, Capability: capability, Decision: decision, Hops: []app.ListedHop{}})
			cur = &out[len(out)-1]
		}
		if position == nil || adapter == nil || model == nil || weight == nil || retry == nil || accountID == nil {
			continue
		}
		cur.Hops = append(cur.Hops, app.ListedHop{
			Position:  *position,
			Adapter:   *adapter,
			Model:     *model,
			Weight:    *weight,
			Retry:     *retry,
			AccountID: *accountID,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("gateway: list routes: %w", err)
	}
	return out, nil
}
