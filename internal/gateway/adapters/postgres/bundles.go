package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

// Bundle loads one named tool bundle in position order.
func (s Store) Bundle(ctx context.Context, name string) (domain.Bundle, error) {
	if name == "" {
		return domain.Bundle{}, fmt.Errorf("gateway: bundle name is empty")
	}
	var found string
	err := s.Pool.QueryRow(ctx, `SELECT name FROM gateway.tool_bundles WHERE name = $1`, name).Scan(&found)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Bundle{}, fmt.Errorf("gateway: no bundle %q", name)
		}
		return domain.Bundle{}, err
	}
	tools, err := s.bundleTools(ctx, found)
	if err != nil {
		return domain.Bundle{}, err
	}
	return domain.Bundle{Name: found, Tools: tools}, nil
}

// AgentBundle loads the bundle assigned to an agent.
func (s Store) AgentBundle(ctx context.Context, agentID string) (domain.Bundle, error) {
	if agentID == "" {
		return domain.Bundle{}, fmt.Errorf("gateway: agent has no tool bundle")
	}
	var name string
	err := s.Pool.QueryRow(ctx, `SELECT bundle FROM gateway.agent_bundles WHERE agent_id = $1`, agentID).Scan(&name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Bundle{}, fmt.Errorf("gateway: agent has no tool bundle")
		}
		return domain.Bundle{}, err
	}
	return s.Bundle(ctx, name)
}

func (s Store) bundleTools(ctx context.Context, name string) ([]string, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT tool_name FROM gateway.tool_bundle_tools
		WHERE bundle = $1
		ORDER BY position`, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tools []string
	for rows.Next() {
		var tool string
		if err := rows.Scan(&tool); err != nil {
			return nil, err
		}
		tools = append(tools, tool)
	}
	return tools, rows.Err()
}
