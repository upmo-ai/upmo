package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
)

// ConnectorGrants serves per-user connector grants.
type ConnectorGrants struct {
	Grants   app.ConnectorGrants
	Sessions app.Service
}

// Mount registers the grant routes. They require a session cookie.
func (h ConnectorGrants) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/connector-grants", h.list)
	mux.HandleFunc("PUT /api/connector-grants/{connector}", h.put)
	mux.HandleFunc("DELETE /api/connector-grants/{connector}", h.remove)
}

func (h ConnectorGrants) list(w http.ResponseWriter, r *http.Request) {
	session, ok := h.session(w, r)
	if !ok {
		return
	}
	rows, err := h.Grants.List(r.Context(), session.UserID)
	if err != nil {
		http.Error(w, "connector grants", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		ids := row.Resources
		if ids == nil {
			ids = []string{}
		}
		out = append(out, map[string]any{
			"connector":      row.Connector,
			"resource_scope": string(row.Scope),
			"resource_ids":   ids,
			"changes":        string(row.Changes),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h ConnectorGrants) put(w http.ResponseWriter, r *http.Request) {
	session, ok := h.session(w, r)
	if !ok {
		return
	}
	var body struct {
		ResourceScope string   `json:"resource_scope"`
		ResourceIDs   []string `json:"resource_ids"`
		Changes       string   `json:"changes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeRaw(w, http.StatusBadRequest, memberError("choose all or only these"))
		return
	}
	grant, err := h.Grants.Put(r.Context(), session.UserID, r.PathValue("connector"), body.ResourceScope, body.ResourceIDs, body.Changes)
	if err != nil {
		writeGrantError(w, err)
		return
	}
	ids := grant.Resources
	if ids == nil {
		ids = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"connector":      grant.Connector,
		"resource_scope": string(grant.Scope),
		"resource_ids":   ids,
		"changes":        string(grant.Changes),
	})
}

func (h ConnectorGrants) remove(w http.ResponseWriter, r *http.Request) {
	session, ok := h.session(w, r)
	if !ok {
		return
	}
	if err := h.Grants.Remove(r.Context(), session.UserID, r.PathValue("connector")); err != nil {
		writeGrantError(w, err)
		return
	}
	noContent(w)
}

func (h ConnectorGrants) session(w http.ResponseWriter, r *http.Request) (app.Session, bool) {
	if !allowPerson(w, r) {
		return app.Session{}, false
	}
	w.Header().Set("Cache-Control", "no-store")
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		writeRaw(w, http.StatusUnauthorized, unauthorised)
		return app.Session{}, false
	}
	session, err := h.Sessions.Lookup(r.Context(), cookie.Value)
	if err != nil {
		ClearCookie(w)
		writeRaw(w, http.StatusUnauthorized, unauthorised)
		return app.Session{}, false
	}
	return session, true
}

func writeGrantError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrConnector):
		writeRaw(w, http.StatusBadRequest, memberError("unknown connector"))
	case errors.Is(err, domain.ErrResourceScope):
		writeRaw(w, http.StatusBadRequest, memberError("choose all or only these"))
	case errors.Is(err, domain.ErrChanges):
		writeRaw(w, http.StatusBadRequest, memberError("choose allow, ask or block"))
	default:
		http.Error(w, "connector grants", http.StatusInternalServerError)
	}
}
