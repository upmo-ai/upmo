package identity

import (
	"context"
	"net/http"
	"time"

	httpapi "github.com/upmo-ai/upmo/internal/identity/adapters/http"
	"github.com/upmo-ai/upmo/internal/identity/app"
)

// Identity is the identity facade embedding this context's use cases.
type Identity struct {
	app.Service
	app.Passkeys
	app.Passwords
	Sessions app.Service
}

// New creates an Identity facade value embedding the use cases.
func New(sessions app.Service, passkeys app.Passkeys, passwords app.Passwords) Identity {
	return Identity{
		Service:   sessions,
		Sessions:  sessions,
		Passkeys:  passkeys,
		Passwords: passwords,
	}
}

// Fresh reports whether the session may change a credential.
func (id Identity) Fresh(session app.Session) bool {
	return id.Passkeys.Fresh(session)
}

// Principal is who acts in a request.
type Principal struct {
	ID        string
	UserID    UserID
	SessionID string
	Method    string
	SignedIn  time.Time
}

// User returns the principal's user ID.
func (p Principal) User() UserID {
	return p.UserID
}

type principalContextKey struct{}

// SessionFrom retrieves the signed-in principal from ctx.
func SessionFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalContextKey{}).(Principal)
	return p, ok
}

// WithPrincipal stores the principal in ctx.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, p)
}

// RequireSession protects an HTTP handler by requiring an active session cookie.
func (id Identity) RequireSession(next http.Handler) http.Handler {
	return RequireSession(id.Service, next)
}

// RequireSession wraps next with session authentication using the provided service.
func RequireSession(sessions app.Service, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		cookie, err := r.Cookie(httpapi.CookieName)
		if err != nil {
			refuseUnauthorised(w)
			return
		}
		session, err := sessions.Lookup(r.Context(), cookie.Value)
		if err != nil {
			httpapi.ClearCookie(w)
			refuseUnauthorised(w)
			return
		}
		p := Principal{
			ID:        session.ID,
			UserID:    session.UserID,
			SessionID: session.ID,
			Method:    session.Method,
			SignedIn:  session.SignedIn,
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}

func refuseUnauthorised(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte("{\"error\":\"unauthorised\"}\n"))
}
