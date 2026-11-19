package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
)

const workspaceColumns = `
	name, logo_url, time_zone, locale, default_route, default_agent_id,
	allow_member_providers, allow_member_routes, allow_member_connectors,
	allow_member_agents, sso_required, updated_at`

func scanWorkspace(row pgx.Row) (domain.Workspace, error) {
	var w domain.Workspace
	err := row.Scan(
		&w.Name, &w.LogoURL, &w.TimeZone, &w.Locale, &w.DefaultRoute, &w.DefaultAgentID,
		&w.AllowMemberProviders, &w.AllowMemberRoutes, &w.AllowMemberConnectors,
		&w.AllowMemberAgents, &w.SSORequired, &w.UpdatedAt,
	)
	return w, err
}

// WorkspaceTimeZone reads the workspace's configured time zone on the owner pool.
func WorkspaceTimeZone(ctx context.Context, owner *pgxpool.Pool) (string, error) {
	if owner == nil {
		return "UTC", nil
	}
	var tz string
	err := owner.QueryRow(ctx, `SELECT time_zone FROM identity.workspace WHERE id = true`).Scan(&tz)
	if errors.Is(err, pgx.ErrNoRows) {
		return "UTC", nil
	}
	if err != nil {
		return "", err
	}
	if tz == "" {
		return "UTC", nil
	}
	return tz, nil
}

// GetWorkspace reads the single-row workspace settings under row security for actor.
func (p Passkeys) GetWorkspace(ctx context.Context, actor domain.UserID) (domain.Workspace, error) {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return domain.Workspace{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT set_config('upmo.user_id', $1, true)`, actor.String()); err != nil {
		return domain.Workspace{}, err
	}
	w, err := scanWorkspace(tx.QueryRow(ctx, `SELECT `+workspaceColumns+` FROM identity.workspace WHERE id = true`))
	if err != nil {
		return domain.Workspace{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Workspace{}, err
	}
	return w, nil
}

// UpdateWorkspace updates the single-row workspace settings and seals the
// workspace.update event in the same transaction.
func (p Passkeys) UpdateWorkspace(ctx context.Context, actor domain.UserID, allow string, next domain.WorkspaceUpdate, now time.Time) (domain.Workspace, error) {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return domain.Workspace{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		SELECT set_config('upmo.user_id', $1, true),
		       set_config('upmo.allow', $2, true)`, actor.String(), allow); err != nil {
		return domain.Workspace{}, err
	}

	before, err := scanWorkspace(tx.QueryRow(ctx, `SELECT `+workspaceColumns+` FROM identity.workspace WHERE id = true FOR UPDATE`))
	if err != nil {
		return domain.Workspace{}, mapWorkspaceWrite(err)
	}

	fields, values := workspaceChanges(before, next)
	if len(fields) == 0 && len(values) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return domain.Workspace{}, err
		}
		audit.NoChange(ctx)
		return before, nil
	}

	var updatedAt time.Time
	err = tx.QueryRow(ctx, `
		UPDATE identity.workspace
		SET name = $1,
		    logo_url = $2,
		    time_zone = $3,
		    locale = $4,
		    default_route = $5,
		    default_agent_id = $6,
		    allow_member_providers = $7,
		    allow_member_routes = $8,
		    allow_member_connectors = $9,
		    allow_member_agents = $10,
		    sso_required = $11,
		    updated_at = $12
		WHERE id = true
		RETURNING updated_at`,
		next.Name, next.LogoURL, next.TimeZone, next.Locale, next.DefaultRoute, next.DefaultAgentID,
		next.AllowMemberProviders, next.AllowMemberRoutes, next.AllowMemberConnectors,
		next.AllowMemberAgents, next.SSORequired, now,
	).Scan(&updatedAt)
	if err != nil {
		return domain.Workspace{}, mapWorkspaceWrite(err)
	}

	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.WorkspaceUpdate,
		Outcome:  audit.Success,
		TargetID: "workspace",
		Changes: audit.Changes{
			Fields: fields,
			Values: values,
		},
	}); err != nil {
		return domain.Workspace{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Workspace{}, err
	}

	return domain.Workspace{
		Name:                  next.Name,
		LogoURL:               next.LogoURL,
		TimeZone:              next.TimeZone,
		Locale:                next.Locale,
		DefaultRoute:          next.DefaultRoute,
		DefaultAgentID:        next.DefaultAgentID,
		AllowMemberProviders:  next.AllowMemberProviders,
		AllowMemberRoutes:     next.AllowMemberRoutes,
		AllowMemberConnectors: next.AllowMemberConnectors,
		AllowMemberAgents:     next.AllowMemberAgents,
		SSORequired:           next.SSORequired,
		UpdatedAt:             updatedAt,
	}, nil
}

func mapWorkspaceWrite(err error) error {
	var pgErr *pgconn.PgError
	if errors.Is(err, pgx.ErrNoRows) || (errors.As(err, &pgErr) && pgErr.Code == "42501") {
		return domain.ErrSettingsOnly
	}
	return err
}

func workspaceChanges(before domain.Workspace, next domain.WorkspaceUpdate) ([]string, map[string]audit.Change) {
	var fields []string
	values := map[string]audit.Change{}
	if before.Name != next.Name {
		fields = append(fields, "name")
	}
	if before.LogoURL != next.LogoURL {
		fields = append(fields, "logo_url")
	}
	if before.TimeZone != next.TimeZone {
		values["time_zone"] = audit.Change{From: before.TimeZone, To: next.TimeZone}
	}
	if before.Locale != next.Locale {
		values["locale"] = audit.Change{From: before.Locale, To: next.Locale}
	}
	if before.DefaultRoute != next.DefaultRoute {
		values["default_route"] = audit.Change{From: before.DefaultRoute, To: next.DefaultRoute}
	}
	if !uuidPtrEqual(before.DefaultAgentID, next.DefaultAgentID) {
		values["default_agent_id"] = audit.Change{From: uuidString(before.DefaultAgentID), To: uuidString(next.DefaultAgentID)}
	}
	if before.AllowMemberProviders != next.AllowMemberProviders {
		values["allow_member_providers"] = audit.Change{From: before.AllowMemberProviders, To: next.AllowMemberProviders}
	}
	if before.AllowMemberRoutes != next.AllowMemberRoutes {
		values["allow_member_routes"] = audit.Change{From: before.AllowMemberRoutes, To: next.AllowMemberRoutes}
	}
	if before.AllowMemberConnectors != next.AllowMemberConnectors {
		values["allow_member_connectors"] = audit.Change{From: before.AllowMemberConnectors, To: next.AllowMemberConnectors}
	}
	if before.AllowMemberAgents != next.AllowMemberAgents {
		values["allow_member_agents"] = audit.Change{From: before.AllowMemberAgents, To: next.AllowMemberAgents}
	}
	if before.SSORequired != next.SSORequired {
		values["sso_required"] = audit.Change{From: before.SSORequired, To: next.SSORequired}
	}
	return fields, values
}

func uuidPtrEqual(a, b *uuid.UUID) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func uuidString(id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	return id.String()
}
