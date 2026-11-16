package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/agents/app"
	"github.com/upmo-ai/upmo/internal/agents/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
)

// InstalledPack summarizes an idempotent signed pack installation.
type InstalledPack struct {
	PackID       string    `json:"pack_id"`
	PackVersion  string    `json:"pack_version"`
	Agent        app.Agent `json:"agent"`
	LessonsCount int       `json:"lessons_count"`
}

type cleanPackLesson struct {
	title        string
	cue          string
	body         string
	alwaysInline bool
}

// InstallPack installs or updates a verified upmo.pack/v1 bundle idempotently
// on the schema owner pool and seals the provision.pack_install audit event.
//
// In v1.0.0, Pack.Templates, Pack.ConnectorRules, and Pack.Schedules are
// reserved and ignored during installation; only Agent and Lessons are applied.
// Updating an existing installed agent preserves admin-configured enabled,
// visibility, and team_ids settings and refuses pack version downgrades.
func InstallPack(ctx context.Context, owner *pgxpool.Pool, pack domain.Pack, keyID string, now time.Time) (InstalledPack, error) {
	if owner == nil {
		return InstalledPack{}, fmt.Errorf("agents: no database")
	}
	name, err := domain.CleanName(pack.Agent.Name)
	if err != nil {
		return InstalledPack{}, err
	}
	slug, err := domain.CleanSlug(pack.Agent.Slug, name)
	if err != nil {
		return InstalledPack{}, err
	}
	role, err := domain.CleanRole(pack.Agent.Role)
	if err != nil {
		return InstalledPack{}, err
	}
	desc, err := domain.CleanDescription(pack.Description)
	if err != nil {
		return InstalledPack{}, err
	}
	instr, err := domain.CleanInstructions(pack.Agent.Instructions)
	if err != nil {
		return InstalledPack{}, err
	}
	route, err := domain.CleanRoute(pack.Agent.DefaultRoute)
	if err != nil {
		return InstalledPack{}, err
	}
	modelProv, modelID, err := domain.CleanModel(pack.Agent.DefaultModelProvider, pack.Agent.DefaultModelID)
	if err != nil {
		return InstalledPack{}, err
	}
	toolClasses, err := domain.CleanToolClasses(pack.Agent.ToolClasses)
	if err != nil {
		return InstalledPack{}, err
	}
	connectorIDs, err := domain.CleanConnectorIDs(pack.Agent.ConnectorIDs)
	if err != nil {
		return InstalledPack{}, err
	}
	planApproval, err := domain.CleanPlanApproval(pack.Agent.PlanApproval)
	if err != nil {
		return InstalledPack{}, err
	}
	criteria, err := domain.CleanCheckCriteria(pack.Agent.CheckCriteria)
	if err != nil {
		return InstalledPack{}, err
	}
	criteriaJSON, err := marshalCriteria(criteria)
	if err != nil {
		return InstalledPack{}, err
	}

	cleanedLessons := make([]cleanPackLesson, 0, len(pack.Lessons))
	seenTitles := make(map[string]bool, len(pack.Lessons))
	indexCount := 0
	for _, l := range pack.Lessons {
		title, cue, body, err := domain.CleanLesson(l.Title, l.Cue, l.Body)
		if err != nil {
			return InstalledPack{}, err
		}
		if seenTitles[title] {
			return InstalledPack{}, domain.ErrLessonDuplicate
		}
		seenTitles[title] = true
		if !l.AlwaysInline {
			indexCount++
			if indexCount > domain.LessonIndexCap {
				return InstalledPack{}, domain.ErrLessonFull
			}
		}
		cleanedLessons = append(cleanedLessons, cleanPackLesson{
			title:        title,
			cue:          cue,
			body:         body,
			alwaysInline: l.AlwaysInline,
		})
	}

	tx, err := owner.Begin(ctx)
	if err != nil {
		return InstalledPack{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var existingID string
	var existingInstalled bool
	var existingPackVersion string
	err = tx.QueryRow(ctx, `
		SELECT id::text, installed, pack_version
		FROM agents.agents
		WHERE lower(slug) = lower($1) AND visibility <> 'private'
		FOR UPDATE`, slug).Scan(&existingID, &existingInstalled, &existingPackVersion)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return InstalledPack{}, err
	}

	nowUTC := now.UTC()
	var agentID string
	var prevDigest string
	isNew := errors.Is(err, pgx.ErrNoRows)

	if isNew {
		err = tx.QueryRow(ctx, `
			INSERT INTO agents.agents (
				owner_user_id, visibility, team_ids, installed, pack_version, name, slug, role,
				description, instructions, default_route, default_model_provider,
				default_model_id, tool_classes, connector_ids, knowledge_folder_ids,
				plan_approval, check_criteria, enabled, version, created_at, updated_at
			) VALUES (
				$1::uuid, 'everyone', '{}'::uuid[], true, $2, $3, $4, $5,
				$6, $7, $8, $9,
				$10, $11::text[], $12::text[], '{}'::uuid[],
				$13, $14::jsonb, true, 1, $15, $15
			)
			RETURNING id::text`,
			uuid.Nil.String(), pack.Version, name, slug, role,
			desc, instr, orNull(route), orNull(modelProv),
			orNull(modelID), nonNilStrings(toolClasses), nonNilStrings(connectorIDs),
			planApproval, string(criteriaJSON), nowUTC).Scan(&agentID)
		if err != nil {
			return InstalledPack{}, agentWriteErr(err)
		}
	} else {
		if !existingInstalled {
			return InstalledPack{}, app.ErrAgentDuplicate
		}
		if existingPackVersion != "" {
			cmp, cmpErr := domain.ComparePackVersion(pack.Version, existingPackVersion)
			if cmpErr != nil {
				return InstalledPack{}, fmt.Errorf("%w: %v", domain.ErrMalformedPack, cmpErr)
			}
			if cmp < 0 {
				return InstalledPack{}, domain.ErrPackDowngrade
			}
		}
		agentID = existingID
		prevAgent, err := getAgent(ctx, tx, agentID, false)
		if err != nil {
			return InstalledPack{}, err
		}
		prevInline, err := listInlineLessonsTx(ctx, tx, agentID)
		if err != nil {
			return InstalledPack{}, err
		}
		prevDigest, err = snapshotDigest(prevAgent, prevInline)
		if err != nil {
			return InstalledPack{}, err
		}

		// Preserve admin-configured enabled, visibility, and team_ids across updates.
		if _, err := tx.Exec(ctx, `
			UPDATE agents.agents
			SET name = $2,
			    slug = $3,
			    role = $4,
			    description = $5,
			    instructions = $6,
			    default_route = $7,
			    default_model_provider = $8,
			    default_model_id = $9,
			    tool_classes = $10::text[],
			    connector_ids = $11::text[],
			    plan_approval = $12,
			    check_criteria = $13::jsonb,
			    pack_version = $14,
			    installed = true
			WHERE id = $1::uuid`,
			agentID, name, slug, role, desc, instr,
			orNull(route), orNull(modelProv), orNull(modelID),
			nonNilStrings(toolClasses), nonNilStrings(connectorIDs),
			planApproval, string(criteriaJSON), pack.Version); err != nil {
			return InstalledPack{}, agentWriteErr(err)
		}
	}

	titles := make([]string, 0, len(cleanedLessons))
	for _, l := range cleanedLessons {
		titles = append(titles, l.title)
		if _, err := tx.Exec(ctx, `
			INSERT INTO agents.lessons (agent_id, title, cue, body, always_inline, updated_at)
			VALUES ($1::uuid, $2, $3, $4, $5, $6)
			ON CONFLICT (agent_id, title) DO UPDATE
			SET cue = EXCLUDED.cue,
			    body = EXCLUDED.body,
			    always_inline = EXCLUDED.always_inline,
			    updated_at = EXCLUDED.updated_at`,
			agentID, l.title, l.cue, l.body, l.alwaysInline, nowUTC); err != nil {
			return InstalledPack{}, lessonWriteErr(err)
		}
	}
	if len(titles) == 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM agents.lessons WHERE agent_id = $1::uuid`, agentID); err != nil {
			return InstalledPack{}, err
		}
	} else {
		if _, err := tx.Exec(ctx, `
			DELETE FROM agents.lessons
			WHERE agent_id = $1::uuid AND NOT (title = ANY($2::text[]))`,
			agentID, titles); err != nil {
			return InstalledPack{}, err
		}
	}

	agent, err := getAgent(ctx, tx, agentID, false)
	if err != nil {
		return InstalledPack{}, err
	}
	inline, err := listInlineLessonsTx(ctx, tx, agentID)
	if err != nil {
		return InstalledPack{}, err
	}
	if isNew {
		if err := stampVersionTx(ctx, tx, "", agent, inline); err != nil {
			return InstalledPack{}, err
		}
	} else {
		nextDigest, err := snapshotDigest(agent, inline)
		if err != nil {
			return InstalledPack{}, err
		}
		if nextDigest != prevDigest {
			if err := tx.QueryRow(ctx, `
				UPDATE agents.agents
				SET version = version + 1, updated_at = $2
				WHERE id = $1::uuid
				RETURNING version`, agentID, nowUTC).Scan(&agent.Version); err != nil {
				return InstalledPack{}, agentWriteErr(err)
			}
			agent.UpdatedAt = nowUTC
			if err := stampVersionTx(ctx, tx, "", agent, inline); err != nil {
				return InstalledPack{}, err
			}
		}
	}

	if err := audit.Record(ctx, tx, audit.Event{
		Action:     audit.ProvisionPackInstall,
		Outcome:    audit.Success,
		TargetType: "pack",
		TargetID:   agentID,
		Changes: audit.Changes{
			Fields: []string{"agent_slug", "key_id", "lessons", "pack_id", "pack_version"},
			Values: map[string]audit.Change{
				"key_id":       {To: keyID},
				"pack_id":      {To: pack.ID},
				"pack_version": {To: pack.Version},
				"agent_slug":   {To: agent.Slug},
				"lessons":      {To: len(cleanedLessons)},
			},
		},
	}); err != nil {
		return InstalledPack{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return InstalledPack{}, agentWriteErr(err)
	}
	return InstalledPack{
		PackID:       pack.ID,
		PackVersion:  pack.Version,
		Agent:        agent,
		LessonsCount: len(cleanedLessons),
	}, nil
}

func snapshotDigest(a app.Agent, inline []domain.Lesson) (string, error) {
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
	_, digest, err := domain.CanonicalSnapshot(snap)
	return digest, err
}
