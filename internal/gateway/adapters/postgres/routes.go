package postgres

import (
	"context"
	"errors"
	"fmt"
	"slices"

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

var (
	_ app.Teams      = Store{}
	_ app.UserRoutes = Store{}
)

// accountTeams is the ids of the teams an account is shared with, as text. It
// is empty for a private account and for one shared with everyone.
const accountTeams = `COALESCE((
	SELECT array_agg(tid::text ORDER BY tid)
	FROM unnest(%s.team_ids) AS tid), '{}'::text[])`

// accountReach is the owner, the visibility and the teams. Routing branches
// on visibility and house. The owner is reported for every non-house account.
func accountReach(alias string) string {
	return fmt.Sprintf(`COALESCE(%s.owner_user_id::text, ''), %s.visibility, `+accountTeams, alias, alias, alias)
}

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

// ByName loads one shared or installed route and its links in position order.
// It never resolves a user's private route.
func (s Store) ByName(ctx context.Context, name string) (domain.Route, error) {
	var routeID, cap, decision string
	var r domain.Route
	err := s.Pool.QueryRow(ctx, `
		SELECT id::text, name, capability, decision
		FROM gateway.routes
		WHERE name = $1
		  AND (installed OR visibility <> 'private')
		ORDER BY installed DESC, (visibility <> 'private') DESC, id
		LIMIT 1`, name).Scan(&routeID, &r.Name, &cap, &decision)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Route{}, fmt.Errorf("gateway: no route %q", name)
		}
		return domain.Route{}, err
	}
	r.Capability = domain.Capability(cap)
	r.Decision = domain.Decision(decision)
	return s.loadRouteLinks(ctx, routeID, r)
}

// ByNameFor loads one route by name for the calling principal. System workers
// (who.User == "") only resolve installed or shared routes; user requests
// resolve routes the user can use via identity.can_use, narrowed to who.TeamID
// when set.
func (s Store) ByNameFor(ctx context.Context, name string, who domain.Principal) (domain.Route, error) {
	if who.User == "" {
		return s.ByName(ctx, name)
	}
	var routeID, cap, decision string
	var r domain.Route
	var err error
	if _, parseErr := uuid.Parse(who.User); parseErr != nil {
		// Synthetic test/debug callers (e.g. chatstream_integration_test using
		// UserID: "u") pass non-UUID user IDs; resolve only installed or
		// everyone routes rather than failing uuid casts.
		err = s.Pool.QueryRow(ctx, `
			SELECT id::text, name, capability, decision
			FROM gateway.routes
			WHERE name = $1
			  AND (installed OR visibility = 'everyone')
			ORDER BY installed DESC, id
			LIMIT 1`, name).Scan(&routeID, &r.Name, &cap, &decision)
	} else {
		teams, terr := s.TeamsOf(ctx, who.User)
		if terr != nil {
			return domain.Route{}, terr
		}
		if who.TeamID != "" {
			if slices.Contains(teams, who.TeamID) {
				teams = []string{who.TeamID}
			} else {
				teams = []string{}
			}
		}
		if teams == nil {
			teams = []string{}
		}
		err = s.Pool.QueryRow(ctx, `
			SELECT id::text, name, capability, decision
			FROM gateway.routes
			WHERE name = $1
			  AND (installed OR identity.can_use($2::uuid, COALESCE($3::uuid[], '{}'), owner_user_id, visibility, team_ids))
			ORDER BY installed DESC, (visibility <> 'private') DESC, id
			LIMIT 1`, name, who.User, teams).Scan(&routeID, &r.Name, &cap, &decision)
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Route{}, fmt.Errorf("gateway: no route %q", name)
		}
		return domain.Route{}, err
	}
	r.Capability = domain.Capability(cap)
	r.Decision = domain.Decision(decision)
	return s.loadRouteLinks(ctx, routeID, r)
}

func (s Store) loadRouteLinks(ctx context.Context, routeID string, r domain.Route) (domain.Route, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT link.position, link.model, link.weight, link.retry_budget, link.required_capabilities,
			account.id::text, COALESCE(NULLIF(account.provider, ''), account.adapter), account.adapter, account.credential_ref, account.plan_kind, account.house, account.base_url,
			`+accountReach("account")+`
		FROM gateway.route_links AS link
		JOIN gateway.provider_accounts AS account ON account.id = link.provider_account_id
		WHERE link.route_id = $1::uuid
		ORDER BY link.position`, routeID)
	if err != nil {
		return domain.Route{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var link domain.Link
		if err := rows.Scan(
			&link.Position, &link.Model, &link.Weight, &link.RetryBudget, &link.Need,
			&link.Account.ID, &link.Account.Provider, &link.Account.Adapter, &link.Account.CredentialRef,
			&link.Account.PlanKind, &link.Account.House, &link.Account.BaseURL, &link.Account.OwnerUserID,
			&link.Account.Visibility, &link.Account.TeamIDs,
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
		SELECT a.id::text, COALESCE(NULLIF(a.provider, ''), a.adapter), a.adapter, a.credential_ref, a.plan_kind, a.house, a.base_url, `+accountReach("a")+`
		FROM gateway.provider_accounts AS a
		WHERE a.id = $1::uuid
		  AND EXISTS (SELECT 1 FROM gateway.provider_models AS m WHERE m.account_id = a.id AND m.model = $2)`, id, model).Scan(&a.ID, &a.Provider, &a.Adapter, &a.CredentialRef, &a.PlanKind, &a.House, &a.BaseURL, &a.OwnerUserID, &a.Visibility, &a.TeamIDs)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Account{}, false, nil
	}
	if err != nil {
		return domain.Account{}, false, err
	}
	return a, true, nil
}

// PinProvider resolves a provider name and a model for one person, who is on
// teams, as the table owner like PinAccount: their private account of that
// provider that lists the model comes first, then one shared with a team of
// theirs, then one shared with everyone, then a house account, each by name.
// The provider matches without regard to case, and an account with no
// provider label counts as its adapter. ok is false when no account the
// person may use lists the model.
func (s Store) PinProvider(ctx context.Context, user string, teams []string, provider, model string) (domain.Account, bool, error) {
	if teams == nil {
		teams = []string{}
	}
	var a domain.Account
	err := s.Pool.QueryRow(ctx, `
		SELECT a.id::text, COALESCE(NULLIF(a.provider, ''), a.adapter), a.adapter, a.credential_ref, a.plan_kind, a.house, a.base_url, `+accountReach("a")+`
		FROM gateway.provider_accounts AS a
		WHERE lower(COALESCE(NULLIF(a.provider, ''), a.adapter)) = lower($2)
		  AND identity.can_use($1::uuid, COALESCE($4::uuid[], '{}'), a.owner_user_id, a.visibility, a.team_ids)
		  AND EXISTS (SELECT 1 FROM gateway.provider_models AS m WHERE m.account_id = a.id AND m.model = $3)
		ORDER BY
			CASE
				WHEN a.visibility = 'private' THEN 0
				WHEN a.visibility = 'team' THEN 1
				WHEN NOT a.house THEN 2
				ELSE 3
			END,
			a.name
		LIMIT 1`, user, provider, model, teams).Scan(&a.ID, &a.Provider, &a.Adapter, &a.CredentialRef, &a.PlanKind, &a.House, &a.BaseURL, &a.OwnerUserID, &a.Visibility, &a.TeamIDs)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Account{}, false, nil
	}
	if err != nil {
		return domain.Account{}, false, err
	}
	return a, true, nil
}

// Names lists shared and installed route names for the Models endpoint.
func (s Store) Names(ctx context.Context) ([]string, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT DISTINCT name
		FROM gateway.routes
		WHERE installed OR visibility <> 'private'
		ORDER BY name`)
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

// List returns routes visible to the caller with hops in position order.
// Credential refs stay out; each hop names the account it calls, so the HTTP
// layer can compute what the calling person can use.
func (s Store) List(ctx context.Context) ([]app.ListedRoute, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT r.id::text, r.name, r.capability, r.decision, r.installed, r.visibility,
			l.position, account.adapter, l.model, l.weight, l.retry_budget, account.id::text, l.required_capabilities
		FROM gateway.routes AS r
		LEFT JOIN gateway.route_links AS l ON l.route_id = r.id
		LEFT JOIN gateway.provider_accounts AS account ON account.id = l.provider_account_id
		ORDER BY r.name, r.id, l.position`)
	if err != nil {
		return nil, fmt.Errorf("gateway: list routes: %w", err)
	}
	defer rows.Close()

	out := make([]app.ListedRoute, 0)
	var cur *app.ListedRoute
	for rows.Next() {
		var id, name, capability, decision, visibility string
		var installed bool
		var position *int
		var adapter, model, accountID *string
		var weight, retry *int
		var need []string
		if err := rows.Scan(&id, &name, &capability, &decision, &installed, &visibility, &position, &adapter, &model, &weight, &retry, &accountID, &need); err != nil {
			return nil, fmt.Errorf("gateway: list routes: %w", err)
		}
		if cur == nil || cur.ID != id {
			out = append(out, app.ListedRoute{
				ID:         id,
				Name:       name,
				Capability: capability,
				Decision:   decision,
				Hops:       []app.ListedHop{},
				Installed:  installed,
				Visibility: visibility,
				TeamIDs:    []string{},
				TeamNames:  []string{},
				CanUse:     installed || visibility == domain.VisibilityEveryone,
			})
			cur = &out[len(out)-1]
		}
		if position == nil || adapter == nil || model == nil || weight == nil || retry == nil || accountID == nil {
			continue
		}
		cur.Hops = append(cur.Hops, app.ListedHop{
			Position:             *position,
			Adapter:              *adapter,
			Model:                *model,
			Weight:               *weight,
			Retry:                *retry,
			AccountID:            *accountID,
			RequiredCapabilities: need,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("gateway: list routes: %w", err)
	}
	return out, nil
}
