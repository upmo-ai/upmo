package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/upmo-ai/upmo/internal/chat/app"
)

var _ app.AgentBook = Store{}

// agentColumns are the columns scanAgent reads, from a chat.agents row named
// a. The person is $1: an agent is editable when it is theirs, or global and
// they hold settings:manage, which the transaction records in upmo.allow.
const agentColumns = `a.id::text, a.name, a.description, a.prompt_overlay,
	COALESCE(a.route, ''), COALESCE(a.model_provider, ''), COALESCE(a.model_id, ''), a.enabled,
	a.owner_user_id IS NULL,
	COALESCE(a.owner_user_id = $1::uuid
	  OR (a.owner_user_id IS NULL AND 'settings:manage' = ANY (string_to_array(NULLIF(current_setting('upmo.allow', true), ''), ','))), false),
	a.created_at, a.updated_at`

func scanAgent(row pgx.Row) (app.Agent, error) {
	var a app.Agent
	err := row.Scan(&a.ID, &a.Name, &a.Description, &a.PromptOverlay, &a.Route, &a.ModelProvider, &a.ModelID,
		&a.Enabled, &a.Global, &a.Editable, &a.CreatedAt, &a.UpdatedAt)
	a.CreatedAt, a.UpdatedAt = a.CreatedAt.UTC(), a.UpdatedAt.UTC()
	return a, err
}

// ListAgents returns the person's own agents, then the global ones, each by
// name. Row security decides which are visible.
func (s Store) ListAgents(ctx context.Context, access app.Access) ([]app.Agent, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
		SELECT `+agentColumns+`
		FROM chat.agents AS a
		ORDER BY (a.owner_user_id IS NULL), lower(a.name), a.id`, access.UserID)
	if err != nil {
		return nil, fmt.Errorf("chat: list agents: %w", err)
	}
	defer rows.Close()
	out := []app.Agent{}
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, fmt.Errorf("chat: list agents: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("chat: list agents: %w", err)
	}
	return out, tx.Commit(ctx)
}

// GetAgent returns an agent this person can see.
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

// Agent returns an agent for a reply: one this person can see that is switched
// on. A missing, foreign or disabled agent is app.ErrAgentNotFound.
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
		WHERE a.id = $2::uuid AND (a.enabled OR NOT $3::boolean)`, access.UserID, id, enabledOnly))
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Agent{}, app.ErrAgentNotFound
	}
	if err != nil {
		return app.Agent{}, fmt.Errorf("chat: agent: %w", err)
	}
	return a, nil
}

// CreateAgent stores an agent, the person's own or a global one. Row security
// refuses a global agent to anyone without settings:manage.
func (s Store) CreateAgent(ctx context.Context, access app.Access, in app.AgentFields, global bool) (app.Agent, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return app.Agent{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO chat.agents (owner_user_id, name, description, prompt_overlay, route, model_provider, model_id, enabled)
		VALUES (CASE WHEN $1::boolean THEN NULL ELSE $2::uuid END, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id::text`,
		global, access.UserID, in.Name, in.Description, in.PromptOverlay,
		orNull(in.Route), orNull(in.ModelProvider), orNull(in.ModelID), in.Enabled).Scan(&id)
	if err != nil {
		return app.Agent{}, agentWriteErr(err)
	}
	a, err := getAgent(ctx, tx, access, id, false)
	if err != nil {
		return app.Agent{}, err
	}
	return a, tx.Commit(ctx)
}

// UpdateAgent writes every field of an agent the person may change.
func (s Store) UpdateAgent(ctx context.Context, access app.Access, id string, in app.AgentFields) (app.Agent, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return app.Agent{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		UPDATE chat.agents
		SET name = $2, description = $3, prompt_overlay = $4, route = $5,
		    model_provider = $6, model_id = $7, enabled = $8, updated_at = now()
		WHERE id = $1::uuid`,
		id, in.Name, in.Description, in.PromptOverlay,
		orNull(in.Route), orNull(in.ModelProvider), orNull(in.ModelID), in.Enabled)
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

// DeleteAgent removes an agent the person may change.
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

// refusedAgent says why a write matched no row: the agent is global and the
// person may not change it, or they cannot see it at all.
func refusedAgent(ctx context.Context, tx pgx.Tx, access app.Access, id string) error {
	a, err := getAgent(ctx, tx, access, id, false)
	if err != nil {
		return err
	}
	if a.Global {
		return app.ErrAgentGlobal
	}
	return app.ErrAgentNotFound
}

// agentWriteErr maps a refused insert or update: a name already in use, or a
// global agent that row security does not let this person write.
func agentWriteErr(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23505":
			return app.ErrAgentDuplicate
		case "42501":
			return app.ErrAgentGlobal
		}
	}
	return fmt.Errorf("chat: agent: %w", err)
}
