package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

// Store reads routing rows. It does not resolve secrets.
type Store struct {
	Pool *pgxpool.Pool
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
			account.id::text, account.adapter, account.credential_ref, account.plan_kind, account.house, account.base_url
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
			&link.Account.PlanKind, &link.Account.House, &link.Account.BaseURL,
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

// List returns every route with hops in position order. Credential refs stay out.
func (s Store) List(ctx context.Context) ([]app.ListedRoute, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT r.name, r.capability, r.decision,
			l.position, account.adapter, l.model, l.weight, l.retry_budget
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
		var adapter, model *string
		var weight, retry *int
		if err := rows.Scan(&name, &capability, &decision, &position, &adapter, &model, &weight, &retry); err != nil {
			return nil, fmt.Errorf("gateway: list routes: %w", err)
		}
		if cur == nil || cur.Name != name {
			out = append(out, app.ListedRoute{Name: name, Capability: capability, Decision: decision, Hops: []app.ListedHop{}})
			cur = &out[len(out)-1]
		}
		if position == nil || adapter == nil || model == nil || weight == nil || retry == nil {
			continue
		}
		cur.Hops = append(cur.Hops, app.ListedHop{
			Position: *position,
			Adapter:  *adapter,
			Model:    *model,
			Weight:   *weight,
			Retry:    *retry,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("gateway: list routes: %w", err)
	}
	return out, nil
}
