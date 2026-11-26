package identity

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	httpapi "github.com/upmo-ai/upmo/internal/identity/adapters/http"
	"github.com/upmo-ai/upmo/internal/identity/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
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

// StepUp reports whether the session may make a sensitive change. It is
// false when the passkeys could not be read.
func (id Identity) StepUp(ctx context.Context, session app.Session) bool {
	ok, err := id.Passkeys.StepUp(ctx, session)
	return err == nil && ok
}

// RedeemSSOLink redeems an 'sso' or 'sso_stepup' one-time link.
func (id Identity) RedeemSSOLink(ctx context.Context, token, activeCookie, agent string) (newCookie string, refreshed bool, err error) {
	return id.Passkeys.RedeemSSOLink(ctx, token, activeCookie, agent)
}

// Principal is who acts in a request.
type Principal struct {
	ID        string
	UserID    UserID
	SessionID string
	Method    string
	SignedIn  time.Time
	RoleID    string
	// TokenHash is the hash of the session token, which the worker's
	// provider-test resolves to this person again. It is never logged.
	TokenHash []byte
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
			refuseUnauthorized(w)
			return
		}
		session, err := sessions.Lookup(r.Context(), cookie.Value)
		if err != nil {
			httpapi.ClearCookie(w)
			refuseUnauthorized(w)
			return
		}
		p := Principal{
			ID:        session.ID,
			UserID:    session.UserID,
			SessionID: session.ID,
			Method:    session.Method,
			SignedIn:  session.SignedIn,
			RoleID:    session.RoleID,
			TokenHash: session.TokenHash,
		}
		ctx := WithPrincipal(r.Context(), p)
		audit.WithActor(ctx, audit.Actor{Type: "user", ID: session.UserID.String(), RoleID: session.RoleID, CredentialType: "session", CredentialID: session.ID})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Recheck looks up the session cookie of r again, as RequireSession did when
// the request began, for a request that stays open. It reports false when r
// carries no cookie or the session is unknown or has ended; any other error
// means the lookup could not be made. Like any lookup it counts as use of the
// session.
func Recheck(ctx context.Context, sessions app.Service, r *http.Request) (Principal, bool, error) {
	cookie, err := r.Cookie(httpapi.CookieName)
	if err != nil {
		return Principal{}, false, nil
	}
	session, err := sessions.Lookup(ctx, cookie.Value)
	if errors.Is(err, app.ErrNotFound) || errors.Is(err, app.ErrEnded) || errors.Is(err, app.ErrMalformed) {
		return Principal{}, false, nil
	}
	if err != nil {
		return Principal{}, false, err
	}
	return Principal{
		ID:        session.ID,
		UserID:    session.UserID,
		SessionID: session.ID,
		Method:    session.Method,
		SignedIn:  session.SignedIn,
		RoleID:    session.RoleID,
		TokenHash: session.TokenHash,
	}, true, nil
}

func refuseUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte("{\"error\":\"unauthorized\"}\n"))
}

// WorkspaceService is the workspace settings use case.
type WorkspaceService = app.Workspace

// WorkspaceSettings is the single-row workspace configuration.
type WorkspaceSettings = domain.Workspace

var (
	// ErrSSORequired refuses non-SSO sign-in for non-owners when the workspace requires SSO.
	ErrSSORequired = domain.ErrSSORequired
	// ErrSettingsOnly refuses a person who cannot manage workspace settings.
	ErrSettingsOnly = domain.ErrSettingsOnly
	// ErrInvalidWorkspace refuses workspace settings that fail domain validation.
	ErrInvalidWorkspace = domain.ErrInvalidWorkspace
)

// Workspace builds the workspace settings use case.
func Workspace(pool *pgxpool.Pool) app.Workspace {
	return app.Workspace{Store: postgres.Passkeys{Pool: pool}}
}

// MountWorkspace registers the workspace settings routes. passkeys looks up
// the session and decides step-up for a change.
func MountWorkspace(mux *http.ServeMux, workspace app.Workspace, passkeys app.Passkeys) {
	(httpapi.Workspace{Workspace: workspace, Sessions: passkeys.Sessions, Pass: passkeys}).Mount(mux)
}

// WorkspaceTimeZone reads the workspace's configured time zone through a
// definer any serving pool may call.
func WorkspaceTimeZone(ctx context.Context, pool *pgxpool.Pool) (string, error) {
	return postgres.WorkspaceTimeZone(ctx, pool)
}

// SettingsManagers returns the IDs of all active users holding settings:manage,
// through a definer any serving pool may call.
func SettingsManagers(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	return postgres.SettingsManagers(ctx, pool)
}

// ActorOfTokenHash names the joined, enabled person whose unexpired session
// or API token hashes to hash. It reads the hash columns, so only the owner
// pool may call it.
func ActorOfTokenHash(ctx context.Context, owner *pgxpool.Pool, hash []byte) (string, bool, error) {
	return postgres.ActorOfTokenHash(ctx, owner, hash)
}

// StoredPassword is one person's stored password as the worker reads it.
type StoredPassword = postgres.StoredPassword

// StoredPasswordByAddress finds the person, disabled or not, behind any of an
// address's index hashes. Only the worker's owner pool may call it.
func StoredPasswordByAddress(ctx context.Context, owner *pgxpool.Pool, hashes [][]byte) (StoredPassword, bool, error) {
	return postgres.StoredPasswordByAddress(ctx, owner, hashes)
}

// StoredPasswordOfSession finds the enabled person behind an unexpired
// session hash, with their stored address and its index hash. Only the
// worker's owner pool may call it.
func StoredPasswordOfSession(ctx context.Context, owner *pgxpool.Pool, tokenHash []byte) (StoredPassword, string, []byte, bool, error) {
	return postgres.StoredPasswordOfSession(ctx, owner, tokenHash)
}

// TakePasswordVerify counts one password check for an address's window and
// returns the count, at most 6. Only the worker's owner pool may call it.
func TakePasswordVerify(ctx context.Context, owner *pgxpool.Pool, subject []byte) (int, error) {
	return postgres.TakePasswordVerify(ctx, owner, subject)
}

// SweepPasswordVerify deletes password check windows older than 10 minutes.
func SweepPasswordVerify(ctx context.Context, owner *pgxpool.Pool) (int64, error) {
	return postgres.SweepPasswordVerify(ctx, owner)
}

// ActiveUser reports whether userID is a joined, non-disabled user.
func ActiveUser(ctx context.Context, owner *pgxpool.Pool, userID string) (bool, error) {
	return postgres.ActiveUser(ctx, owner, userID)
}
