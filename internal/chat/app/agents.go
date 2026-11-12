package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/chat/domain"
)

var (
	ErrAgentNotFound  = errors.New("chat: no such agent")
	ErrAgentManaged   = errors.New("chat: agent managed")
	ErrAgentCreate    = errors.New("chat: agent create")
	ErrAgentDuplicate = errors.New("chat: agent name in use")
)

const permAgentCreate = "agent:create"

// AgentShare is how an agent is shared, as a person sees it.
type AgentShare struct {
	Visibility string
	TeamIDs    []string
	TeamNames  []string
	CanManage  bool
	CanUse     bool
}

// Agent is one saved profile as a person sees it.
type Agent struct {
	ID            string
	Name          string
	Description   string
	PromptOverlay string
	Route         string
	ModelProvider string
	ModelID       string
	Enabled       bool
	Share         AgentShare
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Defaults are what the agent contributes to the choice of route and model.
func (a Agent) Defaults() domain.Defaults {
	return domain.Defaults{Route: a.Route, ModelProvider: a.ModelProvider, ModelID: a.ModelID}
}

// AgentFields are the fields a person sets on an agent.
type AgentFields struct {
	Name          string
	Description   string
	PromptOverlay string
	Route         string
	ModelProvider string
	ModelID       string
	Enabled       bool
	Visibility    string
	TeamIDs       []string
}

// AgentChange is what a person sends to change an agent.
type AgentChange struct {
	Name          *string
	Description   *string
	PromptOverlay *string
	Route         *string
	ModelProvider *string
	ModelID       *string
	Enabled       *bool
	Visibility    *string
	TeamIDs       *[]string
}

// TeamNames resolves team ids to names. Nil returns no names.
type TeamNames interface {
	TeamNames(ctx context.Context, teamIDs []string) (map[string]string, error)
}

// AgentBook keeps agents.
type AgentBook interface {
	ListAgents(ctx context.Context, access Access) ([]Agent, error)
	GetAgent(ctx context.Context, access Access, id string) (Agent, error)
	CreateAgent(ctx context.Context, access Access, fields AgentFields) (Agent, error)
	UpdateAgent(ctx context.Context, access Access, id string, fields AgentFields) (Agent, error)
	DeleteAgent(ctx context.Context, access Access, id string) error
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

// CreateAgent validates and stores a new agent. Creating needs agent:create.
func CreateAgent(ctx context.Context, book AgentBook, teams TeamNames, access Access, in AgentFields) (Agent, error) {
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
	if visibility != domain.VisibilityPrivate && !allows(access, "settings:manage") {
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

// ChangeAgent applies the change to the agent and stores it.
func ChangeAgent(ctx context.Context, book AgentBook, teams TeamNames, access Access, id string, change AgentChange) (Agent, error) {
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
		Name:          current.Name,
		Description:   current.Description,
		PromptOverlay: current.PromptOverlay,
		Route:         current.Route,
		ModelProvider: current.ModelProvider,
		ModelID:       current.ModelID,
		Enabled:       current.Enabled,
		Visibility:    current.Share.Visibility,
		TeamIDs:       append([]string(nil), current.Share.TeamIDs...),
	}
	if change.Name != nil {
		fields.Name = *change.Name
	}
	if change.Description != nil {
		fields.Description = *change.Description
	}
	if change.PromptOverlay != nil {
		fields.PromptOverlay = *change.PromptOverlay
	}
	if change.Route != nil {
		fields.Route = *change.Route
	}
	if change.ModelProvider != nil {
		fields.ModelProvider = *change.ModelProvider
	}
	if change.ModelID != nil {
		fields.ModelID = *change.ModelID
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
			teamIDs = append([]string(nil), *change.TeamIDs...)
		case visibility != domain.VisibilityTeam:
			teamIDs = nil
		}
		var err error
		fields.Visibility, fields.TeamIDs, err = domain.CleanVisibility(visibility, teamIDs)
		if err != nil {
			return Agent{}, err
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
func DeleteAgent(ctx context.Context, book AgentBook, access Access, id string) error {
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
func ListAgents(ctx context.Context, book AgentBook, access Access) ([]Agent, error) {
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

// CleanAgent checks every field of an agent except sharing, which is cleaned
// separately.
func CleanAgent(in AgentFields) (AgentFields, error) {
	var err error
	if in.Name, err = domain.AgentName(in.Name); err != nil {
		return AgentFields{}, err
	}
	if in.Description, err = domain.AgentDescription(in.Description); err != nil {
		return AgentFields{}, err
	}
	if in.PromptOverlay, err = domain.AgentOverlay(in.PromptOverlay); err != nil {
		return AgentFields{}, err
	}
	if in.Route, err = domain.AgentRoute(in.Route); err != nil {
		return AgentFields{}, err
	}
	if in.ModelProvider, in.ModelID, err = domain.AgentModel(in.ModelProvider, in.ModelID); err != nil {
		return AgentFields{}, err
	}
	return in, nil
}

func knownTeams(ctx context.Context, teams TeamNames, ids []string) error {
	if len(ids) == 0 || teams == nil {
		return nil
	}
	names, err := teams.TeamNames(ctx, ids)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if names[id] == "" {
			return domain.Invalid("Choose teams from the list.")
		}
	}
	return nil
}
