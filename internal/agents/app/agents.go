package app

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/agents/domain"
)

var (
	ErrAccess          = errors.New("agents: access")
	ErrAgentNotFound   = errors.New("agents: no such agent")
	ErrAgentManaged    = errors.New("agents: agent managed")
	ErrAgentCreate     = errors.New("agents: agent create")
	ErrAgentDuplicate  = errors.New("agents: agent name or slug in use")
	ErrVersionNotFound = errors.New("agents: no such version")
)

const (
	permAgentCreate = "agent:create"
	permManage      = "settings:manage"
)

// Access identifies the caller and their effective permissions and teams.
type Access struct {
	UserID string
	Allow  string
	Teams  string
}

// AgentShare is how an agent is shared, as a person sees it.
type AgentShare struct {
	Visibility string
	TeamIDs    []string
	TeamNames  []string
	CanManage  bool
	CanUse     bool
}

// Agent is one saved agent profile as a person sees it.
type Agent struct {
	ID                   string
	Name                 string
	Slug                 string
	Role                 string
	Description          string
	Instructions         string
	DefaultRoute         string
	DefaultModelProvider string
	DefaultModelID       string
	ToolClasses          []string
	ConnectorIDs         []string
	KnowledgeFolderIDs   []string
	PlanApproval         string
	CheckCriteria        []json.RawMessage
	Enabled              bool
	Installed            bool
	Version              int
	Share                AgentShare
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// Version is one immutable snapshot of an agent.
type Version struct {
	AgentID   string
	Version   int
	Digest    string
	Snapshot  json.RawMessage
	CreatedBy string
	CreatedAt time.Time
}

// AgentFields are the fields a person sets on an agent.
type AgentFields struct {
	Name                 string
	Slug                 string
	Role                 string
	Description          string
	Instructions         string
	DefaultRoute         string
	DefaultModelProvider string
	DefaultModelID       string
	ToolClasses          []string
	ConnectorIDs         []string
	KnowledgeFolderIDs   []string
	PlanApproval         string
	CheckCriteria        []json.RawMessage
	Enabled              bool
	Installed            bool
	Visibility           string
	TeamIDs              []string
}

// AgentChange is what a person sends to change an agent.
type AgentChange struct {
	Name                 *string
	Slug                 *string
	Role                 *string
	Description          *string
	Instructions         *string
	DefaultRoute         *string
	DefaultModelProvider *string
	DefaultModelID       *string
	ToolClasses          *[]string
	ConnectorIDs         *[]string
	KnowledgeFolderIDs   *[]string
	PlanApproval         *string
	CheckCriteria        *[]json.RawMessage
	Enabled              *bool
	Visibility           *string
	TeamIDs              *[]string
}

// TeamNames resolves team ids to names. Nil skips team validation.
type TeamNames interface {
	TeamNames(ctx context.Context, teamIDs []string) (map[string]string, error)
}

// Book keeps agents and their immutable versions.
type Book interface {
	ListAgents(ctx context.Context, access Access) ([]Agent, error)
	GetAgent(ctx context.Context, access Access, id string) (Agent, error)
	AgentForUse(ctx context.Context, access Access, id string) (Agent, []domain.Lesson, error)
	CreateAgent(ctx context.Context, access Access, fields AgentFields) (Agent, error)
	UpdateAgent(ctx context.Context, access Access, id string, fields AgentFields) (Agent, error)
	DeleteAgent(ctx context.Context, access Access, id string) error
	ListVersions(ctx context.Context, access Access, agentID string) ([]Version, error)
	GetVersion(ctx context.Context, access Access, agentID string, version int) (Version, error)
}

func agentCaller(access Access) error {
	if _, err := uuid.Parse(access.UserID); err != nil {
		return ErrAccess
	}
	return nil
}

func allows(access Access, want string) bool {
	return slices.Contains(strings.Split(access.Allow, ","), want)
}

func knownTeams(ctx context.Context, teams TeamNames, ids []string) error {
	if teams == nil || len(ids) == 0 {
		return nil
	}
	names, err := teams.TeamNames(ctx, ids)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, ok := names[id]; !ok {
			return domain.Invalid("Choose teams from the list.")
		}
	}
	return nil
}

// CreateAgent validates and stores a new agent and its v1 version snapshot.
func CreateAgent(ctx context.Context, book Book, teams TeamNames, access Access, in AgentFields) (Agent, error) {
	if err := agentCaller(access); err != nil {
		return Agent{}, err
	}
	if !allows(access, permAgentCreate) {
		return Agent{}, ErrAgentCreate
	}
	visibility, teamIDs, err := domain.CleanVisibility(in.Visibility, in.TeamIDs)
	if err != nil {
		return Agent{}, err
	}
	if visibility != domain.VisibilityPrivate && !allows(access, permManage) {
		return Agent{}, ErrAgentManaged
	}
	if err := knownTeams(ctx, teams, teamIDs); err != nil {
		return Agent{}, err
	}
	in.Visibility, in.TeamIDs = visibility, teamIDs
	in, err = CleanAgent(in)
	if err != nil {
		return Agent{}, err
	}
	return book.CreateAgent(ctx, access, in)
}

// ChangeAgent applies a partial update to an agent and stamps a new version if changed.
func ChangeAgent(ctx context.Context, book Book, teams TeamNames, access Access, id string, change AgentChange) (Agent, error) {
	if err := agentCaller(access); err != nil {
		return Agent{}, err
	}
	if _, err := uuid.Parse(id); err != nil {
		return Agent{}, ErrAgentNotFound
	}
	current, err := book.GetAgent(ctx, access, id)
	if err != nil {
		return Agent{}, err
	}
	if !current.Share.CanManage {
		return Agent{}, ErrAgentManaged
	}
	fields := AgentFields{
		Name:                 current.Name,
		Slug:                 current.Slug,
		Role:                 current.Role,
		Description:          current.Description,
		Instructions:         current.Instructions,
		DefaultRoute:         current.DefaultRoute,
		DefaultModelProvider: current.DefaultModelProvider,
		DefaultModelID:       current.DefaultModelID,
		ToolClasses:          append([]string{}, current.ToolClasses...),
		ConnectorIDs:         append([]string{}, current.ConnectorIDs...),
		KnowledgeFolderIDs:   append([]string{}, current.KnowledgeFolderIDs...),
		PlanApproval:         current.PlanApproval,
		CheckCriteria:        append([]json.RawMessage{}, current.CheckCriteria...),
		Enabled:              current.Enabled,
		Installed:            current.Installed,
		Visibility:           current.Share.Visibility,
		TeamIDs:              append([]string{}, current.Share.TeamIDs...),
	}
	if change.Name != nil {
		fields.Name = *change.Name
	}
	if change.Slug != nil {
		fields.Slug = *change.Slug
	}
	if change.Role != nil {
		fields.Role = *change.Role
	}
	if change.Description != nil {
		fields.Description = *change.Description
	}
	if change.Instructions != nil {
		fields.Instructions = *change.Instructions
	}
	if change.DefaultRoute != nil {
		fields.DefaultRoute = *change.DefaultRoute
	}
	if change.DefaultModelProvider != nil {
		fields.DefaultModelProvider = *change.DefaultModelProvider
	}
	if change.DefaultModelID != nil {
		fields.DefaultModelID = *change.DefaultModelID
	}
	if change.ToolClasses != nil {
		fields.ToolClasses = append([]string{}, *change.ToolClasses...)
	}
	if change.ConnectorIDs != nil {
		fields.ConnectorIDs = append([]string{}, *change.ConnectorIDs...)
	}
	if change.KnowledgeFolderIDs != nil {
		fields.KnowledgeFolderIDs = append([]string{}, *change.KnowledgeFolderIDs...)
	}
	if change.PlanApproval != nil {
		fields.PlanApproval = *change.PlanApproval
	}
	if change.CheckCriteria != nil {
		fields.CheckCriteria = append([]json.RawMessage{}, *change.CheckCriteria...)
	}
	if change.Enabled != nil {
		fields.Enabled = *change.Enabled
	}
	if change.Visibility != nil || change.TeamIDs != nil {
		visibility := fields.Visibility
		teamIDs := fields.TeamIDs
		if change.Visibility != nil {
			visibility = *change.Visibility
		}
		switch {
		case change.TeamIDs != nil:
			teamIDs = append([]string{}, *change.TeamIDs...)
		case visibility != domain.VisibilityTeam:
			teamIDs = nil
		}
		fields.Visibility, fields.TeamIDs, err = domain.CleanVisibility(visibility, teamIDs)
		if err != nil {
			return Agent{}, err
		}
		if fields.Visibility != domain.VisibilityPrivate && !allows(access, permManage) {
			return Agent{}, ErrAgentManaged
		}
		if err := knownTeams(ctx, teams, fields.TeamIDs); err != nil {
			return Agent{}, err
		}
	}
	fields, err = CleanAgent(fields)
	if err != nil {
		return Agent{}, err
	}
	return book.UpdateAgent(ctx, access, id, fields)
}

// DeleteAgent removes an agent.
func DeleteAgent(ctx context.Context, book Book, access Access, id string) error {
	if err := agentCaller(access); err != nil {
		return err
	}
	if _, err := uuid.Parse(id); err != nil {
		return ErrAgentNotFound
	}
	current, err := book.GetAgent(ctx, access, id)
	if err != nil {
		return err
	}
	if !current.Share.CanManage {
		return ErrAgentManaged
	}
	return book.DeleteAgent(ctx, access, id)
}

// ListAgents returns the agents this person can see.
func ListAgents(ctx context.Context, book Book, access Access) ([]Agent, error) {
	if err := agentCaller(access); err != nil {
		return nil, err
	}
	got, err := book.ListAgents(ctx, access)
	if err != nil {
		return nil, err
	}
	if got == nil {
		got = []Agent{}
	}
	return got, nil
}

// GetAgent returns one visible agent.
func GetAgent(ctx context.Context, book Book, access Access, id string) (Agent, error) {
	if err := agentCaller(access); err != nil {
		return Agent{}, err
	}
	if _, err := uuid.Parse(id); err != nil {
		return Agent{}, ErrAgentNotFound
	}
	return book.GetAgent(ctx, access, id)
}

// ListVersions returns all immutable version snapshots for an agent, newest first.
func ListVersions(ctx context.Context, book Book, access Access, agentID string) ([]Version, error) {
	if err := agentCaller(access); err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(agentID); err != nil {
		return nil, ErrAgentNotFound
	}
	got, err := book.ListVersions(ctx, access, agentID)
	if err != nil {
		return nil, err
	}
	if got == nil {
		got = []Version{}
	}
	return got, nil
}

// CleanAgent validates and normalizes every non-sharing field of an agent.
func CleanAgent(in AgentFields) (AgentFields, error) {
	var err error
	if in.Name, err = domain.CleanName(in.Name); err != nil {
		return AgentFields{}, err
	}
	if in.Slug, err = domain.CleanSlug(in.Slug, in.Name); err != nil {
		return AgentFields{}, err
	}
	if in.Role, err = domain.CleanRole(in.Role); err != nil {
		return AgentFields{}, err
	}
	if in.Description, err = domain.CleanDescription(in.Description); err != nil {
		return AgentFields{}, err
	}
	if in.Instructions, err = domain.CleanInstructions(in.Instructions); err != nil {
		return AgentFields{}, err
	}
	if in.DefaultRoute, err = domain.CleanRoute(in.DefaultRoute); err != nil {
		return AgentFields{}, err
	}
	if in.DefaultModelProvider, in.DefaultModelID, err = domain.CleanModel(in.DefaultModelProvider, in.DefaultModelID); err != nil {
		return AgentFields{}, err
	}
	if in.ToolClasses, err = domain.CleanToolClasses(in.ToolClasses); err != nil {
		return AgentFields{}, err
	}
	if in.ConnectorIDs, err = domain.CleanConnectorIDs(in.ConnectorIDs); err != nil {
		return AgentFields{}, err
	}
	if in.KnowledgeFolderIDs, err = domain.CleanKnowledgeFolderIDs(in.KnowledgeFolderIDs); err != nil {
		return AgentFields{}, err
	}
	if in.PlanApproval, err = domain.CleanPlanApproval(in.PlanApproval); err != nil {
		return AgentFields{}, err
	}
	if in.CheckCriteria, err = domain.CleanCheckCriteria(in.CheckCriteria); err != nil {
		return AgentFields{}, err
	}
	return in, nil
}
