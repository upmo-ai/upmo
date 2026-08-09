package identity

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	httpapi "github.com/upmo-ai/upmo/internal/identity/adapters/http"
	"github.com/upmo-ai/upmo/internal/identity/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/identity/app"
)

// MemberService is the members use case. Commands use this name so they do not import the app package.
type MemberService = app.Members

// Members builds the members use case.
func Members(pool *pgxpool.Pool) app.Members {
	return app.Members{Store: postgres.Passkeys{Pool: pool}}
}

// MountMembers registers the member routes.
func MountMembers(mux *http.ServeMux, members app.Members, sessions app.Service) {
	(httpapi.Members{Members: members, Sessions: sessions}).Mount(mux)
}
