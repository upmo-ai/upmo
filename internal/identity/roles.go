package identity

import (
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	httpapi "github.com/upmo-ai/upmo/internal/identity/adapters/http"
	"github.com/upmo-ai/upmo/internal/identity/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/identity/app"
)

// RoleService is the roles use case.
type RoleService = app.Roles

// Roles builds the role use case.
func Roles(pool *pgxpool.Pool, log *slog.Logger) app.Roles {
	return app.Roles{Store: postgres.Passkeys{Pool: pool}, Log: log}
}

// MountRoles registers the role routes.
func MountRoles(mux *http.ServeMux, roles app.Roles, sessions app.Service) {
	(httpapi.Roles{Roles: roles, Sessions: sessions}).Mount(mux)
}
