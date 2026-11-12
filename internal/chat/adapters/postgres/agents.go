package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/chat/domain"
)

var _ app.AgentBook = Store{}

const agentColumns = `a.id::text, a.name, a.description, a.prompt_overlay,
	COALESCE(a.route, ''), COALESCE(a.model_provider, ''), COALESCE(a.model_id, ''), a.enabled,
	a.visibility,
	COALESCE((SELECT array_agg(tid::text ORDER BY tid)
	  FROM unnest(a.team_ids) AS tid), '{}'),
	identity.may_use(a.owner_user_id, a.visibility, a.team_ids),
	identity.may_manage(a.owner_user_id, a.visibility),
	a.created_at, a.updated_at`

func scanAgent(row pgx.Row) (app.Agent, error) {
	var a app.Agent
	var teamIDs []string
	err := row.Scan(&a.ID, &a.Name, &a.Description, &a.PromptOverlay, &a.Route, &a.ModelProvider, &a.ModelID,
		&a.Enabled, &a.Share.Visibility, &teamIDs,
		&a.Share.CanUse, &a.Share.CanManage, &a.CreatedAt, &a.UpdatedAt)
	if teamIDs == nil {
		teamIDs = []string{}
	}
	a.Share.TeamIDs = teamIDs
	a.CreatedAt, a.UpdatedAt = a.CreatedAt.UTC(), a.UpdatedAt.UTC()
	return a, err
}

func (s Store) ListAgents(ctx context.Context, access app.Access) ([]app.Agent, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
		SELECT `+agentColumns+`
		FROM chat.agents AS a
		ORDER BY CASE
			WHEN a.visibility = 'private' THEN 0
			WHEN a.visibility = 'team' THEN 1
			ELSE 2
		END, lower(a.name), a.id`)
	if err != nil {
		return nil, fmt.Errorf("chat: list agents: %w", err)
	}
	defer rows.Close()
	out := []app.Agent{}
	var ids []string
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, fmt.Errorf("chat: list agents: %w", err)
		}
		ids = append(ids, a.ID)
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("chat: list agents: %w", err)
	}
	if len(ids) > 0 {
		names, err := agentTeamNames(ctx, tx, ids)
		if err != nil {
			return nil, err
		}
		for i := range out {
			if list := names[out[i].ID]; list != nil {
				out[i].Share.TeamNames = list
			}
		}
	}
	return out, tx.Commit(ctx)
}

func (s Store) GetAgent(ctx context.Context, access app.Access, id string) (app.Agent, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return app.Agent{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	a, err := getAgent(ctx, tx, access, id, false)
	if err != nil {
		return app.Agent{}, err
	}
	return a, tx.Commit(ctx)
}

func (s Store) Agent(ctx context.Context, access app.Access, id string) (app.Agent, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return app.Agent{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	a, err := getAgent(ctx, tx, access, id, true)
	if err != nil {
		return app.Agent{}, err
	}
	return a, tx.Commit(ctx)
}

func getAgent(ctx context.Context, tx pgx.Tx, access app.Access, id string, enabledOnly bool) (app.Agent, error) {
	a, err := scanAgent(tx.QueryRow(ctx, `
		SELECT `+agentColumns+`
		FROM chat.agents AS a
		WHERE a.id = $1::uuid AND (a.enabled OR NOT $2::boolean)`, id, enabledOnly))
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Agent{}, app.ErrAgentNotFound
	}
	if err != nil {
		return app.Agent{}, fmt.Errorf("chat: agent: %w", err)
	}
	names, err := agentTeamNames(ctx, tx, []string{a.ID})
	if err != nil {
		return app.Agent{}, err
	}
	a.Share.TeamNames = names[a.ID]
	if a.Share.TeamNames == nil {
		a.Share.TeamNames = []string{}
	}
	return a, nil
}

func (s Store) CreateAgent(ctx context.Context, access app.Access, in app.AgentFields) (app.Agent, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return app.Agent{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := knownTeams(ctx, tx, in.TeamIDs); err != nil {
		return app.Agent{}, err
	}
	var id string
	visibility := in.Visibility
	if visibility == "" {
		visibility = domain.VisibilityPrivate
	}
	err = tx.QueryRow(ctx, `
		INSERT INTO chat.agents (owner_user_id, name, description, prompt_overlay, route, model_provider, model_id, enabled, visibility, team_ids)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10::uuid[])
		RETURNING id::text`,
		access.UserID, in.Name, in.Description, in.PromptOverlay,
		orNull(in.Route), orNull(in.ModelProvider), orNull(in.ModelID), in.Enabled,
		visibility, uuidArray(in.TeamIDs)).Scan(&id)
	if err != nil {
		return app.Agent{}, agentWriteErr(err)
	}
	a, err := getAgent(ctx, tx, access, id, false)
	if err != nil {
		return app.Agent{}, err
	}
	return a, tx.Commit(ctx)
}

func (s Store) UpdateAgent(ctx context.Context, access app.Access, id string, in app.AgentFields) (app.Agent, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return app.Agent{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := knownTeams(ctx, tx, in.TeamIDs); err != nil {
		return app.Agent{}, err
	}
	visibility := in.Visibility
	if visibility == "" {
		visibility = domain.VisibilityPrivate
	}
	tag, err := tx.Exec(ctx, `
		UPDATE chat.agents
		SET name = $2, description = $3, prompt_overlay = $4, route = $5,
		    model_provider = $6, model_id = $7, enabled = $8,
		    visibility = $9, team_ids = $10::uuid[], updated_at = now()
		WHERE id = $1::uuid`,
		id, in.Name, in.Description, in.PromptOverlay,
		orNull(in.Route), orNull(in.ModelProvider), orNull(in.ModelID), in.Enabled,
		visibility, uuidArray(in.TeamIDs))
	if err != nil {
		return app.Agent{}, agentWriteErr(err)
	}
	if tag.RowsAffected() == 0 {
		return app.Agent{}, refusedAgent(ctx, tx, access, id)
	}
	a, err := getAgent(ctx, tx, access, id, false)
	if err != nil {
		return app.Agent{}, err
	}
	return a, tx.Commit(ctx)
}

func (s Store) DeleteAgent(ctx context.Context, access app.Access, id string) error {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `DELETE FROM chat.agents WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("chat: delete agent: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return refusedAgent(ctx, tx, access, id)
	}
	return tx.Commit(ctx)
}

func refusedAgent(ctx context.Context, tx pgx.Tx, access app.Access, id string) error {
	a, err := getAgent(ctx, tx, access, id, false)
	if err != nil {
		return err
	}
	if !a.Share.CanManage {
		return app.ErrAgentManaged
	}
	return app.ErrAgentNotFound
}

func agentWriteErr(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23505":
			return app.ErrAgentDuplicate
		case "42501":
			return app.ErrAgentManaged
		}
	}
	return fmt.Errorf("chat: agent: %w", err)
}

func uuidArray(ids []string) string {
	if len(ids) == 0 {
		return "{}"
	}
	return "{" + strings.Join(ids, ",") + "}"
}

func knownTeams(ctx context.Context, tx pgx.Tx, teams []string) error {
	if len(teams) == 0 {
		return nil
	}
	var n int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM identity.teams
		WHERE id = ANY($1::uuid[])`, teams).Scan(&n); err != nil {
		return err
	}
	if n != len(teams) {
		return domain.Invalid("Choose teams from the list.")
	}
	return nil
}

func agentTeamNames(ctx context.Context, q pgx.Tx, ids []string) (map[string][]string, error) {
	rows, err := q.Query(ctx, `
		SELECT a.id::text, team.name
		FROM chat.agents AS a
		CROSS JOIN LATERAL unnest(a.team_ids) AS tid
		JOIN identity.teams AS team ON team.id = tid
		WHERE a.id = ANY($1::uuid[])
		ORDER BY a.id, team.name`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = append(out[id], name)
	}
	return out, rows.Err()
}
