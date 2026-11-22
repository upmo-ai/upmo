package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
)

// Workspace serves the install workspace settings.
type Workspace struct {
	Workspace app.Workspace
	Sessions  app.Service
}

// Mount registers the workspace routes.
func (h Workspace) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/workspace", h.get)
	mux.HandleFunc("PUT /api/workspace", h.put)
}

type workspaceJSON struct {
	Name                  string    `json:"name"`
	LogoURL               string    `json:"logo_url"`
	TimeZone              string    `json:"time_zone"`
	Locale                string    `json:"locale"`
	DefaultRoute          string    `json:"default_route"`
	DefaultAgentID        *string   `json:"default_agent_id"`
	AllowMemberProviders  bool      `json:"allow_member_providers"`
	AllowMemberRoutes     bool      `json:"allow_member_routes"`
	AllowMemberConnectors bool      `json:"allow_member_connectors"`
	AllowMemberAgents     bool      `json:"allow_member_agents"`
	SSORequired           bool      `json:"sso_required"`
	UpdatedAt             time.Time `json:"updated_at"`
}

func toWorkspaceJSON(w domain.Workspace) workspaceJSON {
	var agentID *string
	if w.DefaultAgentID != nil {
		s := w.DefaultAgentID.String()
		agentID = &s
	}
	return workspaceJSON{
		Name:                  w.Name,
		LogoURL:               w.LogoURL,
		TimeZone:              w.TimeZone,
		Locale:                w.Locale,
		DefaultRoute:          w.DefaultRoute,
		DefaultAgentID:        agentID,
		AllowMemberProviders:  w.AllowMemberProviders,
		AllowMemberRoutes:     w.AllowMemberRoutes,
		AllowMemberConnectors: w.AllowMemberConnectors,
		AllowMemberAgents:     w.AllowMemberAgents,
		SSORequired:           w.SSORequired,
		UpdatedAt:             w.UpdatedAt,
	}
}

func (h Workspace) get(w http.ResponseWriter, r *http.Request) {
	session, ok := h.session(w, r)
	if !ok {
		return
	}
	got, err := h.Workspace.Get(r.Context(), session)
	if refuseWorkspace(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, toWorkspaceJSON(got))
}

func (h Workspace) put(w http.ResponseWriter, r *http.Request) {
	session, ok := h.session(w, r)
	if !ok {
		return
	}
	var input struct {
		Name                  string  `json:"name"`
		LogoURL               string  `json:"logo_url"`
		TimeZone              string  `json:"time_zone"`
		Locale                string  `json:"locale"`
		DefaultRoute          string  `json:"default_route"`
		DefaultAgentID        *string `json:"default_agent_id"`
		AllowMemberProviders  *bool   `json:"allow_member_providers"`
		AllowMemberRoutes     *bool   `json:"allow_member_routes"`
		AllowMemberConnectors *bool   `json:"allow_member_connectors"`
		AllowMemberAgents     *bool   `json:"allow_member_agents"`
		SSORequired           *bool   `json:"sso_required"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if dec.Decode(&input) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid workspace settings"})
		return
	}
	var extra any
	if !errors.Is(dec.Decode(&extra), io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid workspace settings"})
		return
	}
	if input.AllowMemberProviders == nil ||
		input.AllowMemberRoutes == nil ||
		input.AllowMemberConnectors == nil ||
		input.AllowMemberAgents == nil ||
		input.SSORequired == nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "missing workspace setting"})
		return
	}
	var agentID *uuid.UUID
	if input.DefaultAgentID != nil {
		raw := strings.TrimSpace(*input.DefaultAgentID)
		if raw != "" {
			parsed, err := uuid.Parse(raw)
			if err != nil {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid default_agent_id"})
				return
			}
			agentID = &parsed
		}
	}
	updated, err := h.Workspace.Update(r.Context(), session, domain.WorkspaceUpdate{
		Name:                  input.Name,
		LogoURL:               input.LogoURL,
		TimeZone:              input.TimeZone,
		Locale:                input.Locale,
		DefaultRoute:          input.DefaultRoute,
		DefaultAgentID:        agentID,
		AllowMemberProviders:  *input.AllowMemberProviders,
		AllowMemberRoutes:     *input.AllowMemberRoutes,
		AllowMemberConnectors: *input.AllowMemberConnectors,
		AllowMemberAgents:     *input.AllowMemberAgents,
		SSORequired:           *input.SSORequired,
	})
	if refuseWorkspace(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, toWorkspaceJSON(updated))
}

func (h Workspace) session(w http.ResponseWriter, r *http.Request) (app.Session, bool) {
	if !allowPerson(w, r) {
		return app.Session{}, false
	}
	w.Header().Set("Cache-Control", "no-store")
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		writeRaw(w, http.StatusUnauthorized, unauthorized)
		return app.Session{}, false
	}
	session, err := h.Sessions.Lookup(r.Context(), cookie.Value)
	if err != nil {
		ClearCookie(w)
		writeRaw(w, http.StatusUnauthorized, unauthorized)
		return app.Session{}, false
	}
	markActor(r, session)
	return session, true
}

func refuseWorkspace(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, domain.ErrNoSuchMember):
		ClearCookie(w)
		writeRaw(w, http.StatusUnauthorized, unauthorized)
	case errors.Is(err, domain.ErrSettingsOnly):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "you cannot manage workspace settings"})
	case errors.Is(err, domain.ErrInvalidWorkspace):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
	default:
		http.Error(w, "workspace", http.StatusInternalServerError)
	}
	return true
}
