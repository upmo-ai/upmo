package gateway

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway/adapters/fake"
	httpapi "github.com/upmo-ai/upmo/internal/gateway/adapters/http"
	"github.com/upmo-ai/upmo/internal/gateway/adapters/openai"
	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/gateway/app"
)

// Schema is the Postgres schema this process owns.
const Schema = "gateway"

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations returns this process's migration files.
func Migrations() fs.FS {
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic("gateway: embed migrations: " + err.Error())
	}
	return files
}

// Mount registers the JSON debug routes. cmd/gateway wraps them with HMAC.
func Mount(mux *http.ServeMux, pool *pgxpool.Pool, secrets app.Secrets) {
	store := postgres.Store{Pool: pool}
	f := fake.Adapter{YesNo: true, Reply: "ok"}
	o := openai.Adapter{}
	httpapi.Handler{
		Dispatch: app.Dispatch{
			Routes:  store,
			Secrets: secrets,
			Decide:  map[string]app.Decider{"fake": f, "openai_compat": o},
			Chat:    map[string]app.Chatter{"fake": f, "openai_compat": o},
		},
		Names: func(r *http.Request) ([]string, error) {
			return store.Names(r.Context())
		},
	}.Mount(mux)
}
