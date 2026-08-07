package identity

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	httpapi "github.com/upmo-ai/upmo/internal/identity/adapters/http"
	"github.com/upmo-ai/upmo/internal/identity/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
)

// Sessions builds the session use case on the identity schema.
func Sessions(pool *pgxpool.Pool, idle, maxAge time.Duration) app.Service {
	return app.Service{Store: postgres.Sessions{Pool: pool}, Idle: idle, MaxAge: maxAge}
}

// MountSessions registers the session endpoints.
func MountSessions(mux *http.ServeMux, sessions app.Service, log *slog.Logger, hasPassword func(ctx context.Context, user domain.UserID) (bool, error)) {
	(httpapi.Handler{Sessions: sessions, Log: log, HasPassword: hasPassword}).Mount(mux)
}
