package identity

import (
	"context"
	"log/slog"
	"net"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	httpapi "github.com/upmo-ai/upmo/internal/identity/adapters/http"
	"github.com/upmo-ai/upmo/internal/identity/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
)

// TokenService is the personal token use case. Commands use this name so they
// do not import the app package.
type TokenService = app.APITokens

// APITokens builds personal token use cases.
func APITokens(pool *pgxpool.Pool, log *slog.Logger) app.APITokens {
	return app.APITokens{Store: postgres.Passkeys{Pool: pool}, Log: log}
}

// MountAPITokens registers token management and accepts a bearer on GET /api/session.
func MountAPITokens(mux *http.ServeMux, sessions app.Service, tokens app.APITokens, log *slog.Logger, hasPassword func(ctx context.Context, user domain.UserID) (bool, error), pool *pgxpool.Pool, trusted []*net.IPNet) {
	(httpapi.Handler{
		Sessions: sessions, Log: log, HasPassword: hasPassword,
		Tokens: tokens, Limit: postgres.Passkeys{Pool: pool}, Trusted: trusted,
	}).Mount(mux)
	(httpapi.Tokens{Tokens: tokens, Sessions: sessions}).Mount(mux)
}
