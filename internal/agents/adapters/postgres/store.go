package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/agents/app"
	"github.com/upmo-ai/upmo/internal/agents/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
)

// Store persists agents, immutable versions, and two-tier house rules in Postgres.
type Store struct {
	Pool *pgxpool.Pool
}

var (
	_ app.Book       = Store{}
	_ app.LessonBook = Store{}
	_ app.Playbook   = Store{}
)

const agentColumns = `a.id::text, a.name, a.slug, a.role, a.description, a.instructions,
	COALESCE(a.default_route, ''), COALESCE(a.default_model_provider, ''), COALESCE(a.default_model_id, ''),
	a.tool_classes, a.connector_ids,
	COALESCE((SELECT array_agg(kid::text ORDER BY idx)
	  FROM unnest(a.knowledge_folder_ids) WITH ORDINALITY AS t(kid, idx)), '{}'),
	a.plan_approval, a.check_criteria, a.enabled, a.installed, a.version,
	a.visibility,
	COALESCE((SELECT array_agg(tid::text ORDER BY tid)
	  FROM unnest(a.team_ids) AS tid), '{}'),
	identity.may_use(a.owner_user_id, a.visibility, a.team_ids),
	identity.may_manage(a.owner_user_id, a.visibility),
	a.created_at, a.updated_at`

func (s Store) begin(ctx context.Context, access app.Access) (pgx.Tx, error) {
	if s.Pool == nil {
		return nil, fmt.Errorf("agents: no database")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	teams := access.Teams
	if teams == "" {
		teams = "{}"
	}
	if _, err := tx.Exec(ctx, `
		SELECT set_config('upmo.user_id', $1, true),
		       set_config('upmo.allow', $2, true),
		       set_config('upmo.teams', $3, true)`,
		access.UserID, access.Allow, teams); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func scanAgent(row pgx.Row) (app.Agent, error) {
	var a app.Agent
	var criteriaRaw []byte
	var teamIDs, toolClasses, connectorIDs, folderIDs []string
	err := row.Scan(
		&a.ID, &a.Name, &a.Slug, &a.Role, &a.Description, &a.Instructions,
		&a.DefaultRoute, &a.DefaultModelProvider, &a.DefaultModelID,
		&toolClasses, &connectorIDs, &folderIDs,
		&a.PlanApproval, &criteriaRaw, &a.Enabled, &a.Installed, &a.Version,
		&a.Share.Visibility, &teamIDs,
		&a.Share.CanUse, &a.Share.CanManage, &a.CreatedAt, &a.UpdatedAt,
	)
	if err != nil {
		return app.Agent{}, err
	}
	a.ToolClasses = nonNilStrings(toolClasses)
	a.ConnectorIDs = nonNilStrings(connectorIDs)
	a.KnowledgeFolderIDs = nonNilStrings(folderIDs)
	a.Share.TeamIDs = nonNilStrings(teamIDs)
	a.CheckCriteria = parseCriteria(criteriaRaw)
	a.CreatedAt, a.UpdatedAt = a.CreatedAt.UTC(), a.UpdatedAt.UTC()
	return a, nil
}

func nonNilStrings(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

func parseCriteria(raw []byte) []json.RawMessage {
	if len(raw) == 0 {
		return []json.RawMessage{}
	}
	var out []json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return []json.RawMessage{}
	}
	return out
}

func marshalCriteria(in []json.RawMessage) ([]byte, error) {
	if in == nil {
		in = []json.RawMessage{}
	}
	return json.Marshal(in)
}

func (s Store) ListAgents(ctx context.Context, access app.Access) ([]app.Agent, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
		SELECT `+agentColumns+`
		FROM agents.agents AS a
		ORDER BY CASE
			WHEN a.visibility = 'private' THEN 0
			WHEN a.visibility = 'team' THEN 1
			ELSE 2
		END, lower(a.name), a.id`)
	if err != nil {
		return nil, fmt.Errorf("agents: list: %w", err)
	}
	defer rows.Close()
	out := []app.Agent{}
	var ids []string
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, fmt.Errorf("agents: list: %w", err)
		}
		ids = append(ids, a.ID)
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("agents: list: %w", err)
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

// ProviderUses lists agents that name one of a provider account's models. It
// is a narrow composition port; gateway never imports the agents package.
func (s Store) ProviderUses(ctx context.Context, access app.Access, provider string, models []string) ([]string, error) {
	if len(models) == 0 {
		return []string{}, nil
	}
	tx, err := s.begin(ctx, access)
	if err != nil {
		return nil, fmt.Errorf("agents: provider uses: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	rows, err := tx.Query(ctx, `
		SELECT name
		FROM agents.agents
		WHERE lower(COALESCE(default_model_provider, '')) = lower($1)
		  AND default_model_id = ANY($2::text[])
		ORDER BY lower(name), id`, provider, models)
	if err != nil {
		return nil, fmt.Errorf("agents: provider uses: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("agents: provider uses: %w", err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("agents: provider uses: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("agents: provider uses: %w", err)
	}
	return out, nil
}

// SystemProviderModelUses returns which models of provider are named by any
// agent in agents.agents, read on the owner pool for background checks.
func SystemProviderModelUses(ctx context.Context, owner *pgxpool.Pool, provider string, models []string) ([]string, error) {
	if owner == nil || provider == "" || len(models) == 0 {
		return []string{}, nil
	}
	rows, err := owner.Query(ctx, `
		SELECT DISTINCT default_model_id
		FROM agents.agents
		WHERE lower(COALESCE(default_model_provider, '')) = lower($1)
		  AND default_model_id = ANY($2::text[])
		ORDER BY default_model_id`, provider, models)
	if err != nil {
		return nil, fmt.Errorf("agents: system provider model uses: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, fmt.Errorf("agents: system provider model uses: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s Store) GetAgent(ctx context.Context, access app.Access, id string) (app.Agent, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return app.Agent{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	a, err := getAgent(ctx, tx, id, false)
	if err != nil {
		return app.Agent{}, err
	}
	return a, tx.Commit(ctx)
}

// AgentForUse returns an enabled agent that access may use, along with its
// always_inline house rules for the cached system prefix.
func (s Store) AgentForUse(ctx context.Context, access app.Access, id string) (app.Agent, []domain.Lesson, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return app.Agent{}, nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	a, err := getAgent(ctx, tx, id, true)
	if err != nil {
		return app.Agent{}, nil, err
	}
	inline, err := listInlineLessonsTx(ctx, tx, id)
	if err != nil {
		return app.Agent{}, nil, err
	}
	return a, inline, tx.Commit(ctx)
}

func getAgent(ctx context.Context, tx pgx.Tx, id string, forUse bool) (app.Agent, error) {
	a, err := scanAgent(tx.QueryRow(ctx, `
		SELECT `+agentColumns+`
		FROM agents.agents AS a
		WHERE a.id = $1::uuid AND (($2::boolean AND a.enabled
			AND identity.may_use(a.owner_user_id, a.visibility, a.team_ids))
			OR NOT $2::boolean)`, id, forUse))
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Agent{}, app.ErrAgentNotFound
	}
	if err != nil {
		return app.Agent{}, fmt.Errorf("agents: get: %w", err)
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
	visibility := in.Visibility
	if visibility == "" {
		visibility = domain.VisibilityPrivate
	}
	slug := in.Slug
	if slug == "" {
		slug = domain.DeriveSlug(in.Name)
	}
	planApproval := in.PlanApproval
	if planApproval == "" {
		planApproval = domain.PlanApprovalRisky
	}
	criteriaJSON, err := marshalCriteria(in.CheckCriteria)
	if err != nil {
		return app.Agent{}, err
	}
	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO agents.agents (
			owner_user_id, visibility, team_ids, installed, name, slug, role,
			description, instructions, default_route, default_model_provider,
			default_model_id, tool_classes, connector_ids, knowledge_folder_ids,
			plan_approval, check_criteria, enabled, version
		)
		VALUES (
			$1::uuid, $2, $3::uuid[], $4, $5, $6, $7,
			$8, $9, $10, $11,
			$12, $13::text[], $14::text[], $15::uuid[],
			$16, $17::jsonb, $18, 1
		)
		RETURNING id::text`,
		access.UserID, visibility, uuidArray(in.TeamIDs), in.Installed, in.Name, slug, in.Role,
		in.Description, in.Instructions, orNull(in.DefaultRoute), orNull(in.DefaultModelProvider),
		orNull(in.DefaultModelID), nonNilStrings(in.ToolClasses), nonNilStrings(in.ConnectorIDs), uuidArray(in.KnowledgeFolderIDs),
		planApproval, string(criteriaJSON), in.Enabled).Scan(&id)
	if err != nil {
		return app.Agent{}, agentWriteErr(err)
	}
	a, err := getAgent(ctx, tx, id, false)
	if err != nil {
		return app.Agent{}, err
	}
	if err := stampVersionTx(ctx, tx, access.UserID, a, nil); err != nil {
		return app.Agent{}, err
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.AgentCreate,
		Outcome:  audit.Success,
		TargetID: id,
		Changes: audit.Changes{
			Fields: []string{"enabled", "model_id", "model_provider", "route", "team_ids", "visibility"},
			Values: map[string]audit.Change{
				"enabled":        {To: a.Enabled},
				"route":          {To: a.DefaultRoute},
				"model_provider": {To: a.DefaultModelProvider},
				"model_id":       {To: a.DefaultModelID},
				"visibility":     {To: a.Share.Visibility},
				"team_ids":       {To: a.Share.TeamIDs},
			},
		},
	}); err != nil {
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
	current, err := getAgent(ctx, tx, id, false)
	if err != nil {
		return app.Agent{}, err
	}
	visibility := in.Visibility
	if visibility == "" {
		visibility = domain.VisibilityPrivate
	}
	slug := in.Slug
	if slug == "" {
		slug = current.Slug
	}
	planApproval := in.PlanApproval
	if planApproval == "" {
		planApproval = current.PlanApproval
	}
	criteriaJSON, err := marshalCriteria(in.CheckCriteria)
	if err != nil {
		return app.Agent{}, err
	}
	candidate := app.Agent{
		ID:                   current.ID,
		Name:                 in.Name,
		Slug:                 slug,
		Role:                 in.Role,
		Description:          in.Description,
		Instructions:         in.Instructions,
		DefaultRoute:         in.DefaultRoute,
		DefaultModelProvider: in.DefaultModelProvider,
		DefaultModelID:       in.DefaultModelID,
		ToolClasses:          nonNilStrings(in.ToolClasses),
		ConnectorIDs:         nonNilStrings(in.ConnectorIDs),
		KnowledgeFolderIDs:   nonNilStrings(in.KnowledgeFolderIDs),
		PlanApproval:         planApproval,
		CheckCriteria:        parseCriteria(criteriaJSON),
		Enabled:              in.Enabled,
		Installed:            current.Installed,
		Share: app.AgentShare{
			Visibility: visibility,
			TeamIDs:    nonNilStrings(in.TeamIDs),
		},
	}
	changes, share := agentChanges(current, candidate)
	if changes.Fields == nil && share.Fields == nil {
		audit.NoChange(ctx)
		return current, tx.Commit(ctx)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE agents.agents
		SET name = $2, slug = $3, role = $4, description = $5, instructions = $6,
		    default_route = $7, default_model_provider = $8, default_model_id = $9,
		    tool_classes = $10::text[], connector_ids = $11::text[],
		    knowledge_folder_ids = $12::uuid[], plan_approval = $13,
		    check_criteria = $14::jsonb, enabled = $15,
		    visibility = $16, team_ids = $17::uuid[],
		    version = version + 1, updated_at = now()
		WHERE id = $1::uuid`,
		id, in.Name, slug, in.Role, in.Description, in.Instructions,
		orNull(in.DefaultRoute), orNull(in.DefaultModelProvider), orNull(in.DefaultModelID),
		nonNilStrings(in.ToolClasses), nonNilStrings(in.ConnectorIDs),
		uuidArray(in.KnowledgeFolderIDs), planApproval,
		string(criteriaJSON), in.Enabled,
		visibility, uuidArray(in.TeamIDs))
	if err != nil {
		return app.Agent{}, agentWriteErr(err)
	}
	if tag.RowsAffected() == 0 {
		return app.Agent{}, refusedAgent(ctx, tx, id)
	}
	a, err := getAgent(ctx, tx, id, false)
	if err != nil {
		return app.Agent{}, err
	}
	inline, err := listInlineLessonsTx(ctx, tx, id)
	if err != nil {
		return app.Agent{}, err
	}
	if err := stampVersionTx(ctx, tx, access.UserID, a, inline); err != nil {
		return app.Agent{}, err
	}
	if changes.Fields != nil {
		if err := audit.Record(ctx, tx, audit.Event{
			Action:   audit.AgentUpdate,
			Outcome:  audit.Success,
			TargetID: id,
			Changes:  changes,
		}); err != nil {
			return app.Agent{}, err
		}
	}
	if share.Fields != nil {
		if err := audit.Record(ctx, tx, audit.Event{
			Action:   audit.AgentShare,
			Outcome:  audit.Success,
			TargetID: id,
			Changes:  share,
		}); err != nil {
			return app.Agent{}, err
		}
	}
	return a, tx.Commit(ctx)
}

func (s Store) DeleteAgent(ctx context.Context, access app.Access, id string) error {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `DELETE FROM agents.agents WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("agents: delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return refusedAgent(ctx, tx, id)
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.AgentDelete,
		Outcome:  audit.Success,
		TargetID: id,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s Store) ListVersions(ctx context.Context, access app.Access, agentID string) ([]app.Version, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := getAgent(ctx, tx, agentID, false); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
		SELECT agent_id::text, version, digest, snapshot, COALESCE(created_by::text, ''), created_at
		FROM agents.versions
		WHERE agent_id = $1::uuid
		ORDER BY version DESC`, agentID)
	if err != nil {
		return nil, fmt.Errorf("agents: list versions: %w", err)
	}
	defer rows.Close()
	out := []app.Version{}
	for rows.Next() {
		var v app.Version
		var snap []byte
		if err := rows.Scan(&v.AgentID, &v.Version, &v.Digest, &snap, &v.CreatedBy, &v.CreatedAt); err != nil {
			return nil, fmt.Errorf("agents: list versions: %w", err)
		}
		v.Snapshot = json.RawMessage(snap)
		v.CreatedAt = v.CreatedAt.UTC()
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("agents: list versions: %w", err)
	}
	return out, tx.Commit(ctx)
}

func (s Store) GetVersion(ctx context.Context, access app.Access, agentID string, version int) (app.Version, error) {
	tx, err := s.begin(ctx, access)
	if err != nil {
		return app.Version{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var v app.Version
	var snap []byte
	err = tx.QueryRow(ctx, `
		SELECT agent_id::text, version, digest, snapshot, COALESCE(created_by::text, ''), created_at
		FROM agents.versions
		WHERE agent_id = $1::uuid AND version = $2`, agentID, version).Scan(
		&v.AgentID, &v.Version, &v.Digest, &snap, &v.CreatedBy, &v.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return app.Version{}, app.ErrVersionNotFound
	}
	if err != nil {
		return app.Version{}, fmt.Errorf("agents: get version: %w", err)
	}
	v.Snapshot = json.RawMessage(snap)
	v.CreatedAt = v.CreatedAt.UTC()
	return v, tx.Commit(ctx)
}

func stampVersionTx(ctx context.Context, tx pgx.Tx, createdBy string, a app.Agent, inline []domain.Lesson) error {
	inlineSnap := make([]domain.SnapshotLesson, 0, len(inline))
	for _, l := range inline {
		inlineSnap = append(inlineSnap, domain.SnapshotLesson{
			Title:        l.Title,
			Cue:          l.Cue,
			Body:         l.Body,
			AlwaysInline: l.AlwaysInline,
		})
	}
	snap := domain.Snapshot{
		Name:                 a.Name,
		Slug:                 a.Slug,
		Role:                 a.Role,
		Description:          a.Description,
		Instructions:         a.Instructions,
		DefaultRoute:         a.DefaultRoute,
		DefaultModelProvider: a.DefaultModelProvider,
		DefaultModelID:       a.DefaultModelID,
		ToolClasses:          a.ToolClasses,
		ConnectorIDs:         a.ConnectorIDs,
		KnowledgeFolderIDs:   a.KnowledgeFolderIDs,
		PlanApproval:         a.PlanApproval,
		CheckCriteria:        a.CheckCriteria,
		Enabled:              a.Enabled,
		Visibility:           a.Share.Visibility,
		TeamIDs:              a.Share.TeamIDs,
		Installed:            a.Installed,
		InlineLessons:        inlineSnap,
	}
	raw, digest, err := domain.CanonicalSnapshot(snap)
	if err != nil {
		return fmt.Errorf("agents: snapshot: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO agents.versions (agent_id, version, digest, snapshot, created_by)
		VALUES ($1::uuid, $2, $3, $4::jsonb, NULLIF($5, '')::uuid)`,
		a.ID, a.Version, digest, string(raw), createdBy); err != nil {
		return fmt.Errorf("agents: insert version: %w", err)
	}
	return nil
}

func bumpAgentVersionTx(ctx context.Context, tx pgx.Tx, createdBy, agentID string) error {
	var nextVersion int
	err := tx.QueryRow(ctx, `
		UPDATE agents.agents
		SET version = version + 1, updated_at = now()
		WHERE id = $1::uuid
		RETURNING version`, agentID).Scan(&nextVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return refusedAgent(ctx, tx, agentID)
	}
	if err != nil {
		return agentWriteErr(err)
	}
	a, err := getAgent(ctx, tx, agentID, false)
	if err != nil {
		return err
	}
	inline, err := listInlineLessonsTx(ctx, tx, agentID)
	if err != nil {
		return err
	}
	return stampVersionTx(ctx, tx, createdBy, a, inline)
}

func agentChanges(before, after app.Agent) (changes, share audit.Changes) {
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
	if before.Slug != after.Slug {
		named(&changes, "slug")
	}
	if before.Role != after.Role {
		named(&changes, "role")
	}
	if before.Description != after.Description {
		named(&changes, "description")
	}
	if before.Instructions != after.Instructions {
		named(&changes, "instructions")
	}
	if before.DefaultRoute != after.DefaultRoute {
		put(&changes, "route", before.DefaultRoute, after.DefaultRoute)
	}
	if before.DefaultModelProvider != after.DefaultModelProvider {
		put(&changes, "model_provider", before.DefaultModelProvider, after.DefaultModelProvider)
	}
	if before.DefaultModelID != after.DefaultModelID {
		put(&changes, "model_id", before.DefaultModelID, after.DefaultModelID)
	}
	if !slices.Equal(before.ToolClasses, after.ToolClasses) {
		named(&changes, "tool_classes")
	}
	if !slices.Equal(before.ConnectorIDs, after.ConnectorIDs) {
		named(&changes, "connector_ids")
	}
	if !slices.Equal(before.KnowledgeFolderIDs, after.KnowledgeFolderIDs) {
		named(&changes, "knowledge_folder_ids")
	}
	if before.PlanApproval != after.PlanApproval {
		named(&changes, "plan_approval")
	}
	if !criteriaEqual(before.CheckCriteria, after.CheckCriteria) {
		named(&changes, "check_criteria")
	}
	if before.Enabled != after.Enabled {
		put(&changes, "enabled", before.Enabled, after.Enabled)
	}
	if before.Share.Visibility != after.Share.Visibility || !slices.Equal(before.Share.TeamIDs, after.Share.TeamIDs) {
		put(&changes, "visibility", before.Share.Visibility, after.Share.Visibility)
		put(&changes, "team_ids", before.Share.TeamIDs, after.Share.TeamIDs)
		put(&share, "visibility", before.Share.Visibility, after.Share.Visibility)
		put(&share, "team_ids", before.Share.TeamIDs, after.Share.TeamIDs)
	}
	sort.Strings(changes.Fields)
	sort.Strings(share.Fields)
	return changes, share
}

func criteriaEqual(a, b []json.RawMessage) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		ca, errA := domain.CanonicalJSON(a[i])
		cb, errB := domain.CanonicalJSON(b[i])
		if errA != nil || errB != nil {
			if string(a[i]) != string(b[i]) {
				return false
			}
			continue
		}
		if string(ca) != string(cb) {
			return false
		}
	}
	return true
}

func refusedAgent(ctx context.Context, tx pgx.Tx, id string) error {
	a, err := getAgent(ctx, tx, id, false)
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
	return fmt.Errorf("agents: write: %w", err)
}

func orNull(s string) *string {
	if s == "" {
		return nil
	}
	return &s
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
		FROM agents.agents AS a
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
