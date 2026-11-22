package audit

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/upmo-ai/upmo/internal/platform/httpserver"
)

// auditView is the one permission that reads the sealed trail.
const auditView = "audit:view"

// Callers resolves the two credentials the events endpoint answers, so the
// guard lives here while the identity services stay in the brain. Token
// returns the reader of the personal token a bearer secret presented, with
// Allow narrowed to the token's scope; an error means an invalid token.
// Session returns the signed-in person's reader with their effective allow;
// false means no valid session.
type Callers interface {
	Token(ctx context.Context, secret string) (Reader, error)
	Session(ctx context.Context, r *http.Request) (Reader, bool, error)
}

// Guard is the middleware of the events endpoint. It resolves the caller the
// way Callers says, names them on the audit scope — so the middleware's
// refusals say who was refused — and answers 403 unless the caller's allow
// list holds audit:view as one exact comma-separated name, never a substring.
func Guard(callers Callers) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			var caller Reader
			if secret, ok := bearerSecret(r); ok {
				bearer, err := callers.Token(r.Context(), secret)
				if err != nil {
					httpserver.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid token"})
					return
				}
				caller = bearer
			} else {
				person, ok, err := callers.Session(r.Context(), r)
				if err != nil {
					http.Error(w, "audit", http.StatusInternalServerError)
					return
				}
				if !ok {
					httpserver.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
					return
				}
				caller = person
			}
			WithActor(r.Context(), Actor{
				Type:           "user",
				ID:             caller.UserID,
				RoleID:         caller.RoleID,
				CredentialType: caller.CredentialType,
				CredentialID:   caller.CredentialID,
			})
			if !slices.Contains(strings.Split(caller.Allow, ","), auditView) {
				httpserver.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "audit:view required"})
				return
			}
			next.ServeHTTP(w, r.WithContext(WithReader(r.Context(), caller)))
		})
	}
}

// bearerSecret reads the Authorization header's bearer secret, in the form
// the identity session handler accepts.
func bearerSecret(r *http.Request) (string, bool) {
	secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || secret == "" || strings.Contains(secret, " ") {
		return "", false
	}
	return secret, true
}
