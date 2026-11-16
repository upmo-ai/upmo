package identity

import (
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	httpapi "github.com/upmo-ai/upmo/internal/identity/adapters/http"
	"github.com/upmo-ai/upmo/internal/identity/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/identity/app"
)

// TeamService is the teams use case.
type TeamService = app.Teams

// Teams builds the team use case.
func Teams(pool *pgxpool.Pool, log *slog.Logger) app.Teams {
	return app.Teams{Store: postgres.Passkeys{Pool: pool}, Log: log}
}

// MountTeams registers the team routes.
func MountTeams(mux *http.ServeMux, teams app.Teams, sessions app.Service) {
	(httpapi.Teams{Teams: teams, Sessions: sessions}).Mount(mux)
}
