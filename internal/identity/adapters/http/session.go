// Package httpapi serves the identity session cookie over plain HTTP.
package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/upmo-ai/upmo/internal/identity/app"
)

// CookieName is the session cookie. The __Host- prefix requires Secure,
// Path=/ and no Domain.
const CookieName = "__Host-upmo_session"

const unauthorised = `{"error":"unauthorised"}` + "\n"

// Handler is the session endpoints.
type Handler struct {
	Sessions app.Service
	Log      *slog.Logger
}

// Mount registers the session routes.
func (h Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/session", h.current)
	mux.HandleFunc("DELETE /api/session", h.signOut)
	mux.HandleFunc("DELETE /api/sessions", h.signOutEverywhere)
}

func (h Handler) current(w http.ResponseWriter, r *http.Request) {
	session, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id":        session.UserID.String(),
		"sign_in_method": session.Method,
		"signed_in_at":   session.SignedIn,
	})
}

func (h Handler) signOut(w http.ResponseWriter, r *http.Request) {
	session, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if err := h.Sessions.End(r.Context(), session.ID); err != nil {
		http.Error(w, "session", http.StatusInternalServerError)
		return
	}
	ClearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) signOutEverywhere(w http.ResponseWriter, r *http.Request) {
	session, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if err := h.Sessions.EndSessions(r.Context(), session.UserID); err != nil {
		http.Error(w, "session", http.StatusInternalServerError)
		return
	}
	ClearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) authenticate(w http.ResponseWriter, r *http.Request) (app.Session, bool) {
	w.Header().Set("Cache-Control", "no-store")
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		refuse(w, http.StatusUnauthorized)
		return app.Session{}, false
	}
	session, err := h.Sessions.Lookup(r.Context(), cookie.Value)
	if err != nil {
		h.log(err)
		ClearCookie(w)
		refuse(w, http.StatusUnauthorized)
		return app.Session{}, false
	}
	return session, true
}

func (h Handler) log(err error) {
	if h.Log == nil {
		return
	}
	reason := "unknown"
	switch {
	case errors.Is(err, app.ErrMalformed):
		reason = "malformed"
	case errors.Is(err, app.ErrEnded):
		reason = "ended"
	}
	h.Log.Debug("session refused", "reason", reason)
}

func refuse(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(unauthorised))
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// SetCookie writes the session cookie with the attributes the __Host- prefix requires.
func SetCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearCookie expires the session cookie.
func ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}
