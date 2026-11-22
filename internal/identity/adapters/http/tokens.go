package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
)

// Tokens serves personal API tokens.
type Tokens struct {
	Tokens   app.APITokens
	Sessions app.Service
}

// Mount registers the token routes. They require a session cookie.
func (h Tokens) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/tokens", h.list)
	mux.HandleFunc("POST /api/tokens", h.create)
	mux.HandleFunc("DELETE /api/tokens/{id}", h.revoke)
}

func (h Tokens) list(w http.ResponseWriter, r *http.Request) {
	session, ok := h.session(w, r)
	if !ok {
		return
	}
	rows, err := h.Tokens.List(r.Context(), session.UserID)
	if err != nil {
		http.Error(w, "tokens", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		var used any
		if row.LastUsed != nil {
			used = row.LastUsed
		}
		out = append(out, map[string]any{
			"id": row.ID, "name": row.Name, "scope": row.Scope,
			"created_at": row.Created, "expires_at": row.Expires, "last_used_at": used,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h Tokens) create(w http.ResponseWriter, r *http.Request) {
	session, ok := h.session(w, r)
	if !ok {
		return
	}
	var body struct {
		Name    string   `json:"name"`
		Scope   []string `json:"scope"`
		Days    int      `json:"days"`
		Expires string   `json:"expires"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeRaw(w, http.StatusBadRequest, memberError("a token needs a name"))
		return
	}
	id, secret, err := h.Tokens.Create(r.Context(), session, body.Name, body.Scope, body.Days, body.Expires)
	if err != nil {
		writeTokenError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "secret": secret})
}

func (h Tokens) revoke(w http.ResponseWriter, r *http.Request) {
	session, ok := h.session(w, r)
	if !ok {
		return
	}
	if err := h.Tokens.Revoke(r.Context(), session.UserID, r.PathValue("id")); err != nil {
		writeTokenError(w, err)
		return
	}
	noContent(w)
}

func (h Tokens) session(w http.ResponseWriter, r *http.Request) (app.Session, bool) {
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

func writeTokenError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, app.ErrStale):
		writeRaw(w, http.StatusForbidden, memberError("sign in again"))
	case errors.Is(err, domain.ErrTokenName):
		writeRaw(w, http.StatusBadRequest, memberError("a token needs a name"))
	case errors.Is(err, domain.ErrTokenDays):
		writeRaw(w, http.StatusBadRequest, memberError("choose an expiry up to 365 days"))
	case errors.Is(err, domain.ErrTokenScope):
		writeRaw(w, http.StatusBadRequest, memberError("a token cannot hold that permission"))
	case errors.Is(err, domain.ErrToken):
		writeRaw(w, http.StatusNotFound, memberError("no such token"))
	default:
		http.Error(w, "tokens", http.StatusInternalServerError)
	}
}
