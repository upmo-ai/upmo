// Package httpapi serves the identity session cookie over plain HTTP.
package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/platform/httpserver"
)

// CookieName is the session cookie. The __Host- prefix requires Secure,
// Path=/ and no Domain.
const CookieName = "__Host-upmo_session"

const unauthorised = `{"error":"unauthorised"}` + "\n"

// Handler is the session endpoints.
type Handler struct {
	Sessions    app.Service
	Log         *slog.Logger
	HasPassword func(ctx context.Context, user domain.UserID) (bool, error)
	Tokens      interface {
		Authenticate(ctx context.Context, secret string) (app.Bearer, error)
	}
	Limit interface {
		Take(ctx context.Context, key string, now time.Time, burst, perSecond float64) (bool, time.Duration, error)
	}
	Trusted []*net.IPNet
	Now     func() time.Time
}

// Mount registers the session routes.
func (h Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/session", h.current)
	mux.HandleFunc("DELETE /api/session", h.signOut)
	mux.HandleFunc("DELETE /api/sessions", h.signOutEverywhere)
}

func (h Handler) current(w http.ResponseWriter, r *http.Request) {
	if secret, ok := bearerSecret(r); ok {
		h.presentToken(w, r, secret)
		return
	}
	session, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	has := false
	if h.HasPassword != nil {
		var err error
		has, err = h.HasPassword(r.Context(), session.UserID)
		if err != nil {
			http.Error(w, "session", http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id":        session.UserID.String(),
		"sign_in_method": session.Method,
		"signed_in_at":   session.SignedIn,
		"has_password":   has,
	})
}

func (h Handler) signOut(w http.ResponseWriter, r *http.Request) {
	if !h.person(w, r) {
		return
	}
	session, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if err := h.Sessions.End(r.Context(), session.ID); err != nil {
		http.Error(w, "session", http.StatusInternalServerError)
		return
	}
	ClearCookie(w)
	noContent(w)
}

func (h Handler) signOutEverywhere(w http.ResponseWriter, r *http.Request) {
	if !h.person(w, r) {
		return
	}
	session, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if err := h.Sessions.EndSessions(r.Context(), session.UserID); err != nil {
		http.Error(w, "session", http.StatusInternalServerError)
		return
	}
	ClearCookie(w)
	noContent(w)
}

func (h Handler) presentToken(w http.ResponseWriter, r *http.Request, secret string) {
	w.Header().Set("Cache-Control", "no-store")
	if h.Tokens == nil {
		if !h.allow(w, r) {
			return
		}
		writeRaw(w, http.StatusUnauthorized, `{"error":"invalid token"}`+"\n")
		return
	}
	bearer, err := h.Tokens.Authenticate(r.Context(), secret)
	if err != nil {
		if !h.allow(w, r) {
			return
		}
		writeRaw(w, http.StatusUnauthorized, `{"error":"invalid token"}`+"\n")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"principal": "token",
		"user_id":   bearer.UserID.String(),
		"name":      bearer.Name,
		"scope":     bearer.Scope,
	})
}

func (h Handler) person(w http.ResponseWriter, r *http.Request) bool {
	return allowPerson(w, r)
}

func allowPerson(w http.ResponseWriter, r *http.Request) bool {
	if _, ok := bearerSecret(r); !ok {
		return true
	}
	writeRaw(w, http.StatusForbidden, `{"error":"needs a signed-in person"}`+"\n")
	return false
}

func (h Handler) allow(w http.ResponseWriter, r *http.Request) bool {
	if h.Limit == nil {
		return true
	}
	now := time.Now()
	if h.Now != nil {
		now = h.Now()
	}
	allowed, retry, err := h.Limit.Take(r.Context(), domain.ThrottleAddress(domain.ClientIP(r.RemoteAddr, r.Header.Get("X-Forwarded-For"), h.Trusted)), now, domain.ThrottleBurst, domain.ThrottlePerSecond)
	if err != nil {
		http.Error(w, "throttle", http.StatusInternalServerError)
		return false
	}
	if !allowed {
		secs := int(retry.Round(time.Second) / time.Second)
		if secs < 1 {
			secs = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		writeRaw(w, http.StatusTooManyRequests, slowDown)
		return false
	}
	return true
}

func bearerSecret(r *http.Request) (string, bool) {
	secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || secret == "" || strings.Contains(secret, " ") {
		return "", false
	}
	return secret, true
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
	httpserver.WriteJSON(w, status, body)
}

func noContent(w http.ResponseWriter) {
	httpserver.NoContent(w)
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
