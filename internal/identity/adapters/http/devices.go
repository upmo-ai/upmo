package httpapi

import (
	"net/http"

	"github.com/upmo-ai/upmo/internal/identity/app"
)

// Devices serves session listing and device revocation.
type Devices struct {
	Sessions app.Service
}

// Mount registers the devices routes.
func (d Devices) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sessions", d.list)
	mux.HandleFunc("DELETE /api/sessions/{id}", d.end)
}

func (d Devices) list(w http.ResponseWriter, r *http.Request) {
	session, ok := d.session(w, r)
	if !ok {
		return
	}
	rows, err := d.Sessions.Devices(r.Context(), session.UserID)
	if err != nil {
		http.Error(w, "sessions", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, map[string]any{
			"id":             row.ID,
			"sign_in_method": row.Method,
			"signed_in_at":   row.SignedIn,
			"last_seen":      row.LastSeen,
			"user_agent":     row.Agent,
			"current":        row.ID == session.ID,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (d Devices) end(w http.ResponseWriter, r *http.Request) {
	session, ok := d.session(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	rows, err := d.Sessions.Devices(r.Context(), session.UserID)
	if err != nil {
		http.Error(w, "sessions", http.StatusInternalServerError)
		return
	}
	owned := false
	for _, row := range rows {
		if row.ID == id {
			owned = true
		}
	}
	if !owned {
		writeRaw(w, http.StatusNotFound, unauthorised)
		return
	}
	if err := d.Sessions.End(r.Context(), id); err != nil {
		http.Error(w, "sessions", http.StatusInternalServerError)
		return
	}
	if id == session.ID {
		ClearCookie(w)
	}
	noContent(w)
}

func (d Devices) session(w http.ResponseWriter, r *http.Request) (app.Session, bool) {
	if !allowPerson(w, r) {
		return app.Session{}, false
	}
	w.Header().Set("Cache-Control", "no-store")
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		writeRaw(w, http.StatusUnauthorized, unauthorised)
		return app.Session{}, false
	}
	session, err := d.Sessions.Lookup(r.Context(), cookie.Value)
	if err != nil {
		ClearCookie(w)
		writeRaw(w, http.StatusUnauthorized, unauthorised)
		return app.Session{}, false
	}
	return session, true
}
