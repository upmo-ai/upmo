package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/chat/domain"
	"github.com/upmo-ai/upmo/internal/gateway"
)

const maxAgentBody = 64 << 10

type AccountList interface {
	List(ctx context.Context, user string) ([]gateway.Provider, error)
}

// AgentTeams validates team ids against the workspace.
type AgentTeams interface {
	TeamNames(ctx context.Context, teamIDs []string) (map[string]string, error)
}

type AgentsHandler struct {
	Agents       app.AgentBook
	Teams        AgentTeams
	Access       func(r *http.Request) (app.Access, bool)
	Log          *slog.Logger
	Routes       gateway.RouteBook
	Accounts     AccountList
	DefaultRoute string
}

func (h AgentsHandler) Mount(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	wrap := func(next http.HandlerFunc) http.Handler {
		if guard == nil {
			return next
		}
		return guard(next)
	}
	mux.Handle("GET /api/agents", wrap(h.list))
	mux.Handle("POST /api/agents", wrap(h.create))
	mux.Handle("PATCH /api/agents/{id}", wrap(h.change))
	mux.Handle("DELETE /api/agents/{id}", wrap(h.remove))
}

type agentJSON struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	PromptOverlay string   `json:"prompt_overlay"`
	Route         string   `json:"route"`
	ModelProvider string   `json:"model_provider"`
	ModelID       string   `json:"model_id"`
	Enabled       bool     `json:"enabled"`
	Usable        bool     `json:"usable"`
	Visibility    string   `json:"visibility"`
	TeamIDs       []string `json:"team_ids"`
	TeamNames     []string `json:"team_names"`
	CanUse        bool     `json:"can_use"`
	CanManage     bool     `json:"can_manage"`
}

func agentBody(a app.Agent, usable bool) agentJSON {
	teamIDs, teamNames := a.Share.TeamIDs, a.Share.TeamNames
	if teamIDs == nil {
		teamIDs = []string{}
	}
	if teamNames == nil {
		teamNames = []string{}
	}
	visibility := a.Share.Visibility
	if visibility == "" {
		visibility = domain.VisibilityPrivate
	}
	return agentJSON{
		ID:            a.ID,
		Name:          a.Name,
		Description:   a.Description,
		PromptOverlay: a.PromptOverlay,
		Route:         a.Route,
		ModelProvider: a.ModelProvider,
		ModelID:       a.ModelID,
		Enabled:       a.Enabled,
		Usable:        usable,
		Visibility:    visibility,
		TeamIDs:       teamIDs,
		TeamNames:     teamNames,
		CanUse:        a.Share.CanUse,
		CanManage:     a.Share.CanManage,
	}
}

func (h AgentsHandler) person(w http.ResponseWriter, r *http.Request) (app.Access, bool) {
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		writeError(w, http.StatusForbidden, "needs a signed-in person")
		return app.Access{}, false
	}
	w.Header().Set("Cache-Control", "no-store")
	if h.Access == nil {
		writeError(w, http.StatusUnauthorized, "unauthorised")
		return app.Access{}, false
	}
	access, ok := h.Access(r)
	if !ok || access.UserID == "" {
		writeError(w, http.StatusUnauthorized, "unauthorised")
		return app.Access{}, false
	}
	return access, true
}

func decodeAgent(w http.ResponseWriter, r *http.Request, into any) bool {
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAgentBody)).Decode(into)
	if err == nil {
		return true
	}
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeError(w, http.StatusRequestEntityTooLarge, "That request is too large.")
		return false
	}
	writeError(w, http.StatusBadRequest, "bad request")
	return false
}

func (h AgentsHandler) list(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	rows, err := app.ListAgents(r.Context(), h.Agents, access)
	if h.refuse(w, err) {
		return
	}
	var (
		routes   []gateway.ListedRoute
		accounts []gateway.Provider
	)
	if h.Routes != nil && h.Accounts != nil {
		if routes, err = h.Routes.List(r.Context()); err != nil {
			h.sourceFailed(w, err)
			return
		}
		if accounts, err = h.Accounts.List(r.Context(), access.UserID); err != nil {
			h.sourceFailed(w, err)
			return
		}
	}
	out := make([]agentJSON, 0, len(rows))
	for _, row := range rows {
		usable := gateway.UsableAgent(row.Enabled, row.Route, row.ModelProvider, row.ModelID, routes, accounts, h.DefaultRoute)
		out = append(out, agentBody(row, usable))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h AgentsHandler) usableNow(ctx context.Context, user string, a app.Agent) bool {
	if h.Routes == nil || h.Accounts == nil {
		return false
	}
	routes, err := h.Routes.List(ctx)
	if err != nil {
		if h.Log != nil {
			h.Log.Warn("agents routes failed", "err", err)
		}
		return false
	}
	accounts, err := h.Accounts.List(ctx, user)
	if err != nil {
		if h.Log != nil {
			h.Log.Warn("agents accounts failed", "err", err)
		}
		return false
	}
	return gateway.UsableAgent(a.Enabled, a.Route, a.ModelProvider, a.ModelID, routes, accounts, h.DefaultRoute)
}

func (h AgentsHandler) sourceFailed(w http.ResponseWriter, err error) {
	if h.Log != nil {
		h.Log.Error("agents usable sources failed", "err", err)
	}
	writeError(w, http.StatusInternalServerError, "agents")
}

func (h AgentsHandler) create(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	var body struct {
		Name          string   `json:"name"`
		Description   string   `json:"description"`
		PromptOverlay string   `json:"prompt_overlay"`
		Route         string   `json:"route"`
		ModelProvider string   `json:"model_provider"`
		ModelID       string   `json:"model_id"`
		Enabled       *bool    `json:"enabled"`
		Visibility    string   `json:"visibility"`
		TeamIDs       []string `json:"team_ids"`
	}
	if !decodeAgent(w, r, &body) {
		return
	}
	made, err := app.CreateAgent(r.Context(), h.Agents, h.Teams, access, app.AgentFields{
		Name:          body.Name,
		Description:   body.Description,
		PromptOverlay: body.PromptOverlay,
		Route:         body.Route,
		ModelProvider: body.ModelProvider,
		ModelID:       body.ModelID,
		Enabled:       body.Enabled == nil || *body.Enabled,
		Visibility:    body.Visibility,
		TeamIDs:       body.TeamIDs,
	})
	if h.refuse(w, err) {
		return
	}
	writeJSON(w, http.StatusCreated, agentBody(made, h.usableNow(r.Context(), access.UserID, made)))
}

func (h AgentsHandler) change(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	var body struct {
		Name          *string   `json:"name"`
		Description   *string   `json:"description"`
		PromptOverlay *string   `json:"prompt_overlay"`
		Route         *string   `json:"route"`
		ModelProvider *string   `json:"model_provider"`
		ModelID       *string   `json:"model_id"`
		Enabled       *bool     `json:"enabled"`
		Visibility    *string   `json:"visibility"`
		TeamIDs       *[]string `json:"team_ids"`
	}
	if !decodeAgent(w, r, &body) {
		return
	}
	made, err := app.ChangeAgent(r.Context(), h.Agents, h.Teams, access, r.PathValue("id"), app.AgentChange{
		Name:          body.Name,
		Description:   body.Description,
		PromptOverlay: body.PromptOverlay,
		Route:         body.Route,
		ModelProvider: body.ModelProvider,
		ModelID:       body.ModelID,
		Enabled:       body.Enabled,
		Visibility:    body.Visibility,
		TeamIDs:       body.TeamIDs,
	})
	if h.refuse(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, agentBody(made, h.usableNow(r.Context(), access.UserID, made)))
}

func (h AgentsHandler) remove(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	if h.refuse(w, app.DeleteAgent(r.Context(), h.Agents, access, r.PathValue("id"))) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h AgentsHandler) refuse(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	var invalid domain.Invalid
	switch {
	case errors.As(err, &invalid):
		writeError(w, http.StatusUnprocessableEntity, string(invalid))
	case errors.Is(err, app.ErrAccess):
		writeError(w, http.StatusForbidden, "unauthorised")
	case errors.Is(err, app.ErrAgentCreate):
		writeError(w, http.StatusForbidden, "You cannot create an agent.")
	case errors.Is(err, app.ErrAgentNotFound):
		writeError(w, http.StatusNotFound, "Not found.")
	case errors.Is(err, app.ErrAgentManaged):
		writeError(w, http.StatusForbidden, "This agent is managed by your workspace.")
	case errors.Is(err, app.ErrAgentDuplicate):
		writeError(w, http.StatusConflict, "An agent with that name already exists.")
	default:
		if h.Log != nil {
			h.Log.Error("agents failed", "err", err)
		}
		writeError(w, http.StatusInternalServerError, "agents")
	}
	return true
}
