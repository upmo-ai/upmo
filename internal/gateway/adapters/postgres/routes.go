package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

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
