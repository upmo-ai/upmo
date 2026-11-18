package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/upmo-ai/upmo/internal/agents/app"
	"github.com/upmo-ai/upmo/internal/agents/domain"
	"github.com/upmo-ai/upmo/internal/gateway"
)

const maxAgentBody = 64 << 10

// AccountList reads provider accounts visible to a user.
type AccountList interface {
	List(ctx context.Context, user string) ([]gateway.Provider, error)
}

// RouteList lists the routes a person sees, with the hops they may see.
type RouteList interface {
	List(ctx context.Context, user string) ([]gateway.ListedRoute, error)
}

// AgentTeams validates team ids against the workspace.
type AgentTeams interface {
	TeamNames(ctx context.Context, teamIDs []string) (map[string]string, error)
}

// Handler serves /api/agents backed by internal/agents.
type Handler struct {
	Agents       app.Book
	Teams        AgentTeams
	Access       func(r *http.Request) (app.Access, bool)
	Log          *slog.Logger
	Routes       RouteList
	Accounts     AccountList
	DefaultRoute string
}

// Mount registers the agent routes on mux.
func (h Handler) Mount(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	wrap := func(next http.HandlerFunc) http.Handler {
		if guard == nil {
			return next
		}
		return guard(next)
	}
	mux.Handle("GET /api/agents", wrap(h.list))
	mux.Handle("POST /api/agents", wrap(h.create))
	mux.Handle("GET /api/agents/{id}", wrap(h.get))
	mux.Handle("PATCH /api/agents/{id}", wrap(h.change))
	mux.Handle("DELETE /api/agents/{id}", wrap(h.remove))
	mux.Handle("GET /api/agents/{id}/versions", wrap(h.versions))
}

type agentJSON struct {
	ID                   string            `json:"id"`
	Name                 string            `json:"name"`
	Slug                 string            `json:"slug"`
	Role                 string            `json:"role"`
	Description          string            `json:"description"`
	PromptOverlay        string            `json:"prompt_overlay"`
	Instructions         string            `json:"instructions"`
	Route                string            `json:"route"`
	DefaultRoute         string            `json:"default_route"`
	ModelProvider        string            `json:"model_provider"`
	DefaultModelProvider string            `json:"default_model_provider"`
	ModelID              string            `json:"model_id"`
	DefaultModelID       string            `json:"default_model_id"`
	ToolClasses          []string          `json:"tool_classes"`
	ConnectorIDs         []string          `json:"connector_ids"`
	KnowledgeFolderIDs   []string          `json:"knowledge_folder_ids"`
	PlanApproval         string            `json:"plan_approval"`
	CheckCriteria        []json.RawMessage `json:"check_criteria"`
	Enabled              bool              `json:"enabled"`
	Installed            bool              `json:"installed"`
	Version              int               `json:"version"`
	Usable               bool              `json:"usable"`
	Visibility           string            `json:"visibility"`
	TeamIDs              []string          `json:"team_ids"`
	TeamNames            []string          `json:"team_names"`
	CanUse               bool              `json:"can_use"`
	CanManage            bool              `json:"can_manage"`
}

type versionJSON struct {
	AgentID   string          `json:"agent_id"`
	Version   int             `json:"version"`
	Digest    string          `json:"digest"`
	Snapshot  json.RawMessage `json:"snapshot"`
	CreatedBy string          `json:"created_by,omitempty"`
	CreatedAt string          `json:"created_at"`
}

func nonNilSlice[T any](in []T) []T {
	if in == nil {
		return []T{}
	}
	return in
}

func agentBody(a app.Agent, usable bool) agentJSON {
	visibility := a.Share.Visibility
	if visibility == "" {
		visibility = domain.VisibilityPrivate
	}
	planApproval := a.PlanApproval
	if planApproval == "" {
		planApproval = domain.PlanApprovalRisky
	}
	return agentJSON{
		ID:                   a.ID,
		Name:                 a.Name,
		Slug:                 a.Slug,
		Role:                 a.Role,
		Description:          a.Description,
		PromptOverlay:        a.Instructions,
		Instructions:         a.Instructions,
		Route:                a.DefaultRoute,
		DefaultRoute:         a.DefaultRoute,
		ModelProvider:        a.DefaultModelProvider,
		DefaultModelProvider: a.DefaultModelProvider,
		ModelID:              a.DefaultModelID,
		DefaultModelID:       a.DefaultModelID,
		ToolClasses:          nonNilSlice(a.ToolClasses),
		ConnectorIDs:         nonNilSlice(a.ConnectorIDs),
		KnowledgeFolderIDs:   nonNilSlice(a.KnowledgeFolderIDs),
		PlanApproval:         planApproval,
		CheckCriteria:        nonNilSlice(a.CheckCriteria),
		Enabled:              a.Enabled,
		Installed:            a.Installed,
		Version:              a.Version,
		Usable:               usable,
		Visibility:           visibility,
		TeamIDs:              nonNilSlice(a.Share.TeamIDs),
		TeamNames:            nonNilSlice(a.Share.TeamNames),
		CanUse:               a.Share.CanUse,
		CanManage:            a.Share.CanManage,
	}
}

func (h Handler) person(w http.ResponseWriter, r *http.Request) (app.Access, bool) {
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

func (h Handler) list(w http.ResponseWriter, r *http.Request) {
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
		if routes, err = h.usableRoutes(r.Context(), access.UserID); err != nil {
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
		usable := gateway.UsableAgent(row.Enabled, row.DefaultRoute, row.DefaultModelProvider, row.DefaultModelID, routes, accounts, h.DefaultRoute)
		out = append(out, agentBody(row, usable))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h Handler) get(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	got, err := app.GetAgent(r.Context(), h.Agents, access, r.PathValue("id"))
	if h.refuse(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, agentBody(got, h.usableNow(r.Context(), access.UserID, got)))
}

func (h Handler) versions(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	rows, err := app.ListVersions(r.Context(), h.Agents, access, r.PathValue("id"))
	if h.refuse(w, err) {
		return
	}
	out := make([]versionJSON, 0, len(rows))
	for _, v := range rows {
		out = append(out, versionJSON{
			AgentID:   v.AgentID,
			Version:   v.Version,
			Digest:    v.Digest,
			Snapshot:  v.Snapshot,
			CreatedBy: v.CreatedBy,
			CreatedAt: v.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// usableRoutes is the routes the person may call. The list also holds a shared
// route an admin sees only to manage it.
func (h Handler) usableRoutes(ctx context.Context, user string) ([]gateway.ListedRoute, error) {
	listed, err := h.Routes.List(ctx, user)
	if err != nil {
		return nil, err
	}
	routes := make([]gateway.ListedRoute, 0, len(listed))
	for _, route := range listed {
		if route.CanUse {
			routes = append(routes, route)
		}
	}
	return routes, nil
}

func (h Handler) usableNow(ctx context.Context, user string, a app.Agent) bool {
	if h.Routes == nil || h.Accounts == nil {
		return false
	}
	routes, err := h.usableRoutes(ctx, user)
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
	return gateway.UsableAgent(a.Enabled, a.DefaultRoute, a.DefaultModelProvider, a.DefaultModelID, routes, accounts, h.DefaultRoute)
}

func (h Handler) sourceFailed(w http.ResponseWriter, err error) {
	if h.Log != nil {
		h.Log.Error("agents usable sources failed", "err", err)
	}
	writeError(w, http.StatusInternalServerError, "agents")
}

func firstNonNil(a, b *string) *string {
	if a != nil {
		return a
	}
	return b
}

func (h Handler) create(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	var body struct {
		Name                 string            `json:"name"`
		Slug                 string            `json:"slug"`
		Role                 string            `json:"role"`
		Description          string            `json:"description"`
		PromptOverlay        *string           `json:"prompt_overlay"`
		Instructions         *string           `json:"instructions"`
		Route                *string           `json:"route"`
		DefaultRoute         *string           `json:"default_route"`
		ModelProvider        *string           `json:"model_provider"`
		DefaultModelProvider *string           `json:"default_model_provider"`
		ModelID              *string           `json:"model_id"`
		DefaultModelID       *string           `json:"default_model_id"`
		ToolClasses          []string          `json:"tool_classes"`
		ConnectorIDs         []string          `json:"connector_ids"`
		KnowledgeFolderIDs   []string          `json:"knowledge_folder_ids"`
		PlanApproval         string            `json:"plan_approval"`
		CheckCriteria        []json.RawMessage `json:"check_criteria"`
		Enabled              *bool             `json:"enabled"`
		Visibility           string            `json:"visibility"`
		TeamIDs              []string          `json:"team_ids"`
	}
	if !decodeAgent(w, r, &body) {
		return
	}
	fields := app.AgentFields{
		Name:               body.Name,
		Slug:               body.Slug,
		Role:               body.Role,
		Description:        body.Description,
		ToolClasses:        body.ToolClasses,
		ConnectorIDs:       body.ConnectorIDs,
		KnowledgeFolderIDs: body.KnowledgeFolderIDs,
		PlanApproval:       body.PlanApproval,
		CheckCriteria:      body.CheckCriteria,
		Enabled:            body.Enabled == nil || *body.Enabled,
		Visibility:         body.Visibility,
		TeamIDs:            body.TeamIDs,
	}
	if v := firstNonNil(body.Instructions, body.PromptOverlay); v != nil {
		fields.Instructions = *v
	}
	if v := firstNonNil(body.DefaultRoute, body.Route); v != nil {
		fields.DefaultRoute = *v
	}
	if v := firstNonNil(body.DefaultModelProvider, body.ModelProvider); v != nil {
		fields.DefaultModelProvider = *v
	}
	if v := firstNonNil(body.DefaultModelID, body.ModelID); v != nil {
		fields.DefaultModelID = *v
	}
	made, err := app.CreateAgent(r.Context(), h.Agents, h.Teams, access, fields)
	if h.refuse(w, err) {
		return
	}
	writeJSON(w, http.StatusCreated, agentBody(made, h.usableNow(r.Context(), access.UserID, made)))
}

func (h Handler) change(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	var body struct {
		Name                 *string            `json:"name"`
		Slug                 *string            `json:"slug"`
		Role                 *string            `json:"role"`
		Description          *string            `json:"description"`
		PromptOverlay        *string            `json:"prompt_overlay"`
		Instructions         *string            `json:"instructions"`
		Route                *string            `json:"route"`
		DefaultRoute         *string            `json:"default_route"`
		ModelProvider        *string            `json:"model_provider"`
		DefaultModelProvider *string            `json:"default_model_provider"`
		ModelID              *string            `json:"model_id"`
		DefaultModelID       *string            `json:"default_model_id"`
		ToolClasses          *[]string          `json:"tool_classes"`
		ConnectorIDs         *[]string          `json:"connector_ids"`
		KnowledgeFolderIDs   *[]string          `json:"knowledge_folder_ids"`
		PlanApproval         *string            `json:"plan_approval"`
		CheckCriteria        *[]json.RawMessage `json:"check_criteria"`
		Enabled              *bool              `json:"enabled"`
		Visibility           *string            `json:"visibility"`
		TeamIDs              *[]string          `json:"team_ids"`
	}
	if !decodeAgent(w, r, &body) {
		return
	}
	made, err := app.ChangeAgent(r.Context(), h.Agents, h.Teams, access, r.PathValue("id"), app.AgentChange{
		Name:                 body.Name,
		Slug:                 body.Slug,
		Role:                 body.Role,
		Description:          body.Description,
		Instructions:         firstNonNil(body.Instructions, body.PromptOverlay),
		DefaultRoute:         firstNonNil(body.DefaultRoute, body.Route),
		DefaultModelProvider: firstNonNil(body.DefaultModelProvider, body.ModelProvider),
		DefaultModelID:       firstNonNil(body.DefaultModelID, body.ModelID),
		ToolClasses:          body.ToolClasses,
		ConnectorIDs:         body.ConnectorIDs,
		KnowledgeFolderIDs:   body.KnowledgeFolderIDs,
		PlanApproval:         body.PlanApproval,
		CheckCriteria:        body.CheckCriteria,
		Enabled:              body.Enabled,
		Visibility:           body.Visibility,
		TeamIDs:              body.TeamIDs,
	})
	if h.refuse(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, agentBody(made, h.usableNow(r.Context(), access.UserID, made)))
}

func (h Handler) remove(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	if h.refuse(w, app.DeleteAgent(r.Context(), h.Agents, access, r.PathValue("id"))) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) refuse(w http.ResponseWriter, err error) bool {
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
	case errors.Is(err, app.ErrAgentNotFound), errors.Is(err, app.ErrVersionNotFound):
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

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, text string) {
	writeJSON(w, status, map[string]string{"error": text})
}
