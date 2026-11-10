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
	// ErrAgentNotFound is an agent that does not exist, that this person
	// cannot see, or that is switched off for a reply.
	ErrAgentNotFound = errors.New("chat: no such agent")
	// ErrAgentGlobal is a change to a global agent by someone who may not
	// manage global settings.
	ErrAgentGlobal = errors.New("chat: agent is global")
	// ErrAgentDuplicate is a name the owner already uses.
	ErrAgentDuplicate = errors.New("chat: agent name in use")
)

// ManageGlobal is the permission that lets a person create, change and delete
// the global agents.
const ManageGlobal = "settings:manage"

// Agent is one saved profile as a person sees it. Route, ModelProvider and
// ModelID are empty when the agent has none. Global says it has no owner;
// Editable says this person may change or delete it.
type Agent struct {
	ID            string
	Name          string
	Description   string
	PromptOverlay string
	Route         string
	ModelProvider string
	ModelID       string
	Enabled       bool
	Global        bool
	Editable      bool
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
}

// AgentChange is what a person sends to change an agent. A nil field is left
// as it is. An empty route or model clears it.
type AgentChange struct {
	Name          *string
	Description   *string
	PromptOverlay *string
	Route         *string
	ModelProvider *string
	ModelID       *string
	Enabled       *bool
}

// AgentBook keeps agents. Every call is for one person; an implementation
// shows them their own agents and the enabled global ones, and lets them
// write their own, and the global ones only with ManageGlobal.
type AgentBook interface {
	// ListAgents returns own agents first, then global ones, each by name.
	ListAgents(ctx context.Context, access Access) ([]Agent, error)
	// GetAgent returns an agent the person can see, or ErrAgentNotFound.
	GetAgent(ctx context.Context, access Access, id string) (Agent, error)
	// CreateAgent stores an agent, global or the person's own. It returns
	// ErrAgentDuplicate, or ErrAgentGlobal for a global one the person may
	// not create.
	CreateAgent(ctx context.Context, access Access, fields AgentFields, global bool) (Agent, error)
	// UpdateAgent writes all the fields of an agent. It returns
	// ErrAgentNotFound, ErrAgentGlobal or ErrAgentDuplicate.
	UpdateAgent(ctx context.Context, access Access, id string, fields AgentFields) (Agent, error)
	// DeleteAgent removes an agent. It returns ErrAgentNotFound or
	// ErrAgentGlobal.
	DeleteAgent(ctx context.Context, access Access, id string) error
}

// agentCaller checks that a person is signed in. Agents need no chat
// permission: any signed-in person uses their own agents and reads the global
// ones, and Allow carries only settings:manage, when they hold it.
func agentCaller(access Access) error {
	if _, err := uuid.Parse(access.UserID); err != nil {
		return ErrAccess
	}
	return nil
}

// CanManageGlobal reports whether the person may write global agents.
func (a Access) CanManageGlobal() bool {
	return slices.Contains(strings.Split(a.Allow, ","), ManageGlobal)
}

// ListAgents returns the person's own agents and the global ones.
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

// CleanAgent checks every field of an agent.
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

// CreateAgent validates and stores a new agent. A global one needs
// ManageGlobal.
func CreateAgent(ctx context.Context, book AgentBook, access Access, in AgentFields, global bool) (Agent, error) {
	if err := agentCaller(access); err != nil {
		return Agent{}, err
	}
	if global && !access.CanManageGlobal() {
		return Agent{}, ErrAgentGlobal
	}
	in, err := CleanAgent(in)
	if err != nil {
		return Agent{}, err
	}
	return book.CreateAgent(ctx, access, in, global)
}

// ChangeAgent applies the change to the agent and stores it. Another person's
// agent is not found, and a global one needs ManageGlobal.
func ChangeAgent(ctx context.Context, book AgentBook, access Access, id string, change AgentChange) (Agent, error) {
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
	if !current.Editable {
		return Agent{}, ErrAgentGlobal
	}
	fields := AgentFields{
		Name:          current.Name,
		Description:   current.Description,
		PromptOverlay: current.PromptOverlay,
		Route:         current.Route,
		ModelProvider: current.ModelProvider,
		ModelID:       current.ModelID,
		Enabled:       current.Enabled,
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
	fields, err = CleanAgent(fields)
	if err != nil {
		return Agent{}, err
	}
	return book.UpdateAgent(ctx, access, id, fields)
}

// DeleteAgent removes an agent. Another person's agent is not found, and a
// global one needs ManageGlobal.
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
	if !current.Editable {
		return ErrAgentGlobal
	}
	return book.DeleteAgent(ctx, access, id)
}
