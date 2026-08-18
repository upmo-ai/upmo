package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/identity/domain"
)

// ListConnectorGrants returns the person's saved grants, connector name first.
func (p Passkeys) ListConnectorGrants(ctx context.Context, user domain.UserID) ([]domain.ConnectorGrant, error) {
	rows, err := p.Pool.Query(ctx, `
		SELECT connector, resource_scope, resource_ids, changes
		FROM identity.connector_grants
		WHERE user_id = $1
		ORDER BY connector`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.ConnectorGrant
	for rows.Next() {
		grant, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, grant)
	}
	return out, rows.Err()
}

// SaveConnectorGrant upserts one grant.
func (p Passkeys) SaveConnectorGrant(ctx context.Context, user domain.UserID, grant domain.ConnectorGrant) error {
	ids := grant.Resources
	if ids == nil {
		ids = []string{}
	}
	_, err := p.Pool.Exec(ctx, `
		INSERT INTO identity.connector_grants (user_id, connector, resource_scope, resource_ids, changes)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (user_id, connector) DO UPDATE
		SET resource_scope = EXCLUDED.resource_scope,
		    resource_ids = EXCLUDED.resource_ids,
		    changes = EXCLUDED.changes`,
		user, grant.Connector, string(grant.Scope), ids, string(grant.Changes))
	return err
}

// DeleteConnectorGrant removes a saved grant. Missing is not an error.
func (p Passkeys) DeleteConnectorGrant(ctx context.Context, user domain.UserID, connector string) error {
	_, err := p.Pool.Exec(ctx, `DELETE FROM identity.connector_grants WHERE user_id = $1 AND connector = $2`, user, connector)
	return err
}

// LoadConnectorGrant returns a saved grant, or ok false when the default applies.
func (p Passkeys) LoadConnectorGrant(ctx context.Context, user domain.UserID, connector string) (domain.ConnectorGrant, bool, error) {
	row := p.Pool.QueryRow(ctx, `
		SELECT connector, resource_scope, resource_ids, changes
		FROM identity.connector_grants
		WHERE user_id = $1 AND connector = $2`, user, connector)
	grant, err := scanGrant(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ConnectorGrant{}, false, nil
	}
	if err != nil {
		return domain.ConnectorGrant{}, false, err
	}
	return grant, true, nil
}

type grantRow interface {
	Scan(dest ...any) error
}

func scanGrant(row grantRow) (domain.ConnectorGrant, error) {
	var grant domain.ConnectorGrant
	var scope, changes string
	var ids []string
	if err := row.Scan(&grant.Connector, &scope, &ids, &changes); err != nil {
		return domain.ConnectorGrant{}, err
	}
	if ids == nil {
		ids = []string{}
	}
	grant.Scope = domain.ResourceScope(scope)
	grant.Resources = ids
	grant.Changes = domain.ChangePolicy(changes)
	return grant, nil
}
