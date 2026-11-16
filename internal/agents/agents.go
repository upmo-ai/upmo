// Package agents is the agents bounded context: profiles, immutable version
// snapshots, two-tier house rules, and signed upmo.pack/v1 manifests.
package agents

import (
	"context"
	"embed"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	httpapi "github.com/upmo-ai/upmo/internal/agents/adapters/http"
	"github.com/upmo-ai/upmo/internal/agents/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/agents/app"
	"github.com/upmo-ai/upmo/internal/agents/domain"
)

// Schema is the Postgres schema this context owns.
const Schema = "agents"

// EnvPackTrustKeys is the environment variable carrying additional trusted
// Ed25519 public keys for upmo.pack/v1 verification.
const EnvPackTrustKeys = "UPMO_PACK_TRUST_KEYS"

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations returns this context's migration files.
func Migrations() fs.FS {
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic("agents: embed migrations: " + err.Error())
	}
	return files
}

// Exported types from the agents context.
type (
	Handler      = httpapi.Handler
	Store        = postgres.Store
	Access       = app.Access
	Agent        = app.Agent
	AgentFields  = app.AgentFields
	AgentChange  = app.AgentChange
	AgentShare   = app.AgentShare
	Version      = app.Version
	Lesson       = domain.Lesson
	LessonFields = app.LessonFields
	LessonChange = app.LessonChange
	Card         = domain.Card
	Pack         = domain.Pack
	TrustKeys    = domain.TrustKeys
)

// Visibility and plan-approval constants.
const (
	VisibilityPrivate  = domain.VisibilityPrivate
	VisibilityTeam     = domain.VisibilityTeam
	VisibilityEveryone = domain.VisibilityEveryone

	PlanApprovalAlways = domain.PlanApprovalAlways
	PlanApprovalRisky  = domain.PlanApprovalRisky
	PlanApprovalNever  = domain.PlanApprovalNever

	LessonTitleRunes = domain.LessonTitleRunes
	LessonCueRunes   = domain.LessonCueRunes
	LessonBodyRunes  = domain.LessonBodyRunes
	LessonIndexCap   = domain.LessonIndexCap
)

// Exported sentinel errors.
var (
	ErrAccess           = app.ErrAccess
	ErrAgentNotFound    = app.ErrAgentNotFound
	ErrAgentManaged     = app.ErrAgentManaged
	ErrAgentCreate      = app.ErrAgentCreate
	ErrAgentDuplicate   = app.ErrAgentDuplicate
	ErrVersionNotFound  = app.ErrVersionNotFound
	ErrLesson           = domain.ErrLesson
	ErrLessonTitle      = domain.ErrLessonTitle
	ErrLessonCue        = domain.ErrLessonCue
	ErrLessonBody       = domain.ErrLessonBody
	ErrLessonDuplicate  = domain.ErrLessonDuplicate
	ErrLessonGone       = domain.ErrLessonGone
	ErrLessonFull       = domain.ErrLessonFull
	ErrMalformedPack    = domain.ErrMalformedPack
	ErrUntrustedKey     = domain.ErrUntrustedKey
	ErrInvalidSignature = domain.ErrInvalidSignature
)

// NewStore returns the Postgres-backed Store for agents, versions, and house rules.
func NewStore(pool *pgxpool.Pool) Store {
	return postgres.Store{Pool: pool}
}

// CreateAgent validates, stores, and versions a new agent.
func CreateAgent(ctx context.Context, store Store, teams app.TeamNames, access Access, in AgentFields) (Agent, error) {
	return app.CreateAgent(ctx, store, teams, access, in)
}

// ChangeAgent validates, updates, and versions an existing agent.
func ChangeAgent(ctx context.Context, store Store, teams app.TeamNames, access Access, id string, change AgentChange) (Agent, error) {
	return app.ChangeAgent(ctx, store, teams, access, id, change)
}

// DeleteAgent deletes an agent.
func DeleteAgent(ctx context.Context, store Store, access Access, id string) error {
	return app.DeleteAgent(ctx, store, access, id)
}

// ListAgents lists the agents visible to access.
func ListAgents(ctx context.Context, store Store, access Access) ([]Agent, error) {
	return app.ListAgents(ctx, store, access)
}

// ListVersions returns immutable snapshots for agentID, newest first.
func ListVersions(ctx context.Context, store Store, access Access, agentID string) ([]Version, error) {
	return app.ListVersions(ctx, store, access, agentID)
}

// AppendInlineLessons composes instructions and any always_inline house rules
// into a prompt overlay.
func AppendInlineLessons(instructions string, inline []Lesson) string {
	return domain.AppendInlineLessons(instructions, inline)
}

// AgentSystem builds the cached system prefix text for an agent and its always_inline lessons.
func AgentSystem(name, instructions string, inline []Lesson) string {
	return domain.AgentSystem(name, instructions, inline)
}

// SaveLesson stores a house rule and bumps the agent version if always_inline is true.
func SaveLesson(ctx context.Context, store Store, access Access, agentID string, in LessonFields, at time.Time) (string, error) {
	return app.SaveLesson(ctx, store, access, agentID, in, at)
}

// ChangeLesson edits a house rule and bumps the agent version when inline status or content changes.
func ChangeLesson(ctx context.Context, store Store, access Access, agentID, lessonID string, ch LessonChange, at time.Time) error {
	return app.ChangeLesson(ctx, store, access, agentID, lessonID, ch, at)
}

// RemoveLesson deletes a house rule and bumps the agent version if it was always_inline.
func RemoveLesson(ctx context.Context, store Store, access Access, agentID, lessonID string, at time.Time) error {
	return app.RemoveLesson(ctx, store, access, agentID, lessonID, at)
}

// InlineLessons returns full always_inline lessons for agentID.
func InlineLessons(ctx context.Context, store Store, access Access, agentID string) ([]Lesson, error) {
	return app.InlineLessons(ctx, store, access, agentID)
}

// Index returns lightweight [Title -> Cue] cards for non-inline lessons.
func Index(ctx context.Context, store Store, access Access, agentID string) ([]Card, error) {
	return app.Index(ctx, store, access, agentID)
}

// Open returns one full house rule on demand.
func Open(ctx context.Context, store Store, access Access, agentID, lessonID string) (Lesson, error) {
	return app.Open(ctx, store, access, agentID, lessonID)
}

// PackTrustKeysFromEnv returns the bundled marketplace public keys merged with
// UPMO_PACK_TRUST_KEYS when configured.
func PackTrustKeysFromEnv(lookup func(string) (string, bool)) (TrustKeys, error) {
	base := domain.DefaultPackKeys()
	if lookup == nil {
		return base, nil
	}
	raw, ok := lookup(EnvPackTrustKeys)
	if !ok {
		return base, nil
	}
	return domain.ParseTrustKeys(raw, base)
}

// VerifyPack parses and verifies an upmo.pack/v1 manifest against keys.
func VerifyPack(raw []byte, keys TrustKeys) (Pack, error) {
	return domain.VerifyPack(raw, keys)
}
