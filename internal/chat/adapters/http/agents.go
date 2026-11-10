package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/upmo-ai/upmo/internal/chat/app"
	"github.com/upmo-ai/upmo/internal/chat/domain"
)

// maxAgentBody bounds an agent request: the instructions are at most 4000
// characters, so this is generous.
const maxAgentBody = 64 << 10

// AgentsHandler serves a signed-in person's own agents and the global ones
// beside them. Any signed-in person may use it; Access carries only
// settings:manage in Allow, for a person who may write the global agents.
type AgentsHandler struct {
	Agents app.AgentBook
	Access func(r *http.Request) (app.Access, bool)
	// Log records unexpected failures.
	Log *slog.Logger
}

// Mount registers the agent routes. guard runs first.
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
	ID            string `json:"id"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	PromptOverlay string `json:"prompt_overlay"`
	Route         string `json:"route"`
	ModelProvider string `json:"model_provider"`
	ModelID       string `json:"model_id"`
	Enabled       bool   `json:"enabled"`
	Scope         string `json:"scope"`
	Editable      bool   `json:"editable"`
}

func agentBody(a app.Agent) agentJSON {
	scope := "own"
	if a.Global {
		scope = "global"
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
		Scope:         scope,
		Editable:      a.Editable,
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

// decode reads a bounded JSON body. It answers 400 or 413 itself on failure.
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
	out := make([]agentJSON, 0, len(rows))
	for _, row := range rows {
		out = append(out, agentBody(row))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h AgentsHandler) create(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	var body struct {
		Name          string `json:"name"`
		Description   string `json:"description"`
		PromptOverlay string `json:"prompt_overlay"`
		Route         string `json:"route"`
		ModelProvider string `json:"model_provider"`
		ModelID       string `json:"model_id"`
		Enabled       *bool  `json:"enabled"`
		Scope         string `json:"scope"`
	}
	if !decodeAgent(w, r, &body) {
		return
	}
	if body.Scope != "" && body.Scope != "own" && body.Scope != "global" {
		writeError(w, http.StatusUnprocessableEntity, "The scope is own or global.")
		return
	}
	made, err := app.CreateAgent(r.Context(), h.Agents, access, app.AgentFields{
		Name:          body.Name,
		Description:   body.Description,
		PromptOverlay: body.PromptOverlay,
		Route:         body.Route,
		ModelProvider: body.ModelProvider,
		ModelID:       body.ModelID,
		Enabled:       body.Enabled == nil || *body.Enabled,
	}, body.Scope == "global")
	if h.refuse(w, err) {
		return
	}
	writeJSON(w, http.StatusCreated, agentBody(made))
}

func (h AgentsHandler) change(w http.ResponseWriter, r *http.Request) {
	access, ok := h.person(w, r)
	if !ok {
		return
	}
	var body struct {
		Name          *string `json:"name"`
		Description   *string `json:"description"`
		PromptOverlay *string `json:"prompt_overlay"`
		Route         *string `json:"route"`
		ModelProvider *string `json:"model_provider"`
		ModelID       *string `json:"model_id"`
		Enabled       *bool   `json:"enabled"`
	}
	if !decodeAgent(w, r, &body) {
		return
	}
	made, err := app.ChangeAgent(r.Context(), h.Agents, access, r.PathValue("id"), app.AgentChange{
		Name:          body.Name,
		Description:   body.Description,
		PromptOverlay: body.PromptOverlay,
		Route:         body.Route,
		ModelProvider: body.ModelProvider,
		ModelID:       body.ModelID,
		Enabled:       body.Enabled,
	})
	if h.refuse(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, agentBody(made))
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

// refuse answers a failed agent request with a fixed plain message. Nothing
// from the error text reaches the caller except a validation message, which
// is written by this context's domain and never carries input back.
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
	case errors.Is(err, app.ErrAgentNotFound):
		writeError(w, http.StatusNotFound, "Not found.")
	case errors.Is(err, app.ErrAgentGlobal):
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
