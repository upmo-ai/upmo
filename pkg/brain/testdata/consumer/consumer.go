package consumer

import (
	"context"
	"net/http"
	"time"

	"github.com/upmo-ai/upmo/pkg/brain"
)

// Compose builds a brain app from outside the github.com/upmo-ai/upmo module,
// importing only pkg/brain and the standard library.
func Compose(ctx context.Context) (*brain.App, error) {
	cfg := brain.Config{
		DatabaseAppURL:    "postgres://upmo_app:upmo_app@localhost:5432/upmo?sslmode=disable",
		DatabaseRunnerURL: "postgres://upmo_runner:upmo_runner@localhost:5432/upmo?sslmode=disable",
		PublicURL:         "http://localhost:5173",
		WebAuthnRPID:      "localhost",
		ListenAddr:        ":8080",
		ShutdownTimeout:   5 * time.Second,
	}
	return brain.New(ctx, cfg,
		brain.WithExtraRoutes(func(mux *http.ServeMux, deps brain.Deps) {
			mux.Handle("GET /api/ee/ping", deps.Identity.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				p, ok := deps.Identity.SessionFrom(r.Context())
				if !ok {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				if deps.Identity.Fresh(r.Context(), p) {
					w.Header().Set("X-Fresh", "true")
				}
				w.Header().Set("X-Version", deps.BuildInfo.Version)
				w.WriteHeader(http.StatusOK)
			})))
		}),
	)
}
