package gateway

import (
	"context"
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway/adapters/anthropic"
	"github.com/upmo-ai/upmo/internal/gateway/adapters/fake"
	httpapi "github.com/upmo-ai/upmo/internal/gateway/adapters/http"
	"github.com/upmo-ai/upmo/internal/gateway/adapters/openai"
	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
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

// adapter is everything every adapter key can do. An adapter may also embed
// (app.Embedder); one that cannot, such as anthropic, simply lacks the method.
type adapter interface {
	app.Decider
	app.Chatter
	app.StreamChatter
	app.Prober
}

// adapters is the one table of adapters the gateway knows. Dispatch and the
// provider settings both read it, so a key that can be saved is a key that
// can be called.
func adapters() map[string]adapter {
	return map[string]adapter{
		domain.AdapterFake:      fake.Adapter{YesNo: true, Reply: "ok"},
		domain.AdapterOpenAI:    openai.Adapter{},
		domain.AdapterAnthropic: anthropic.Adapter{},
	}
}

// Mount registers the JSON debug routes. cmd/gateway wraps them with HMAC.
func Mount(mux *http.ServeMux, pool *pgxpool.Pool, secrets app.Secrets, log *slog.Logger) {
	store := postgres.Store{Pool: pool}
	decide := map[string]app.Decider{}
	chat := map[string]app.Chatter{}
	stream := map[string]app.StreamChatter{}
	embed := map[string]app.Embedder{}
	for name, a := range adapters() {
		decide[name], chat[name], stream[name] = a, a, a
		// An adapter without embeddings stays out of the embed table, so an
		// embed route that reaches it finds no embed adapter and moves on.
		if e, ok := a.(app.Embedder); ok {
			embed[name] = e
		}
	}
	httpapi.Handler{
		Dispatch: app.Dispatch{
			Routes:  store,
			Pins:    store,
			Secrets: secrets,
			Prices:  store,
			Ledger:  store,
			Bundles: store,
			Teams:   store,
			Decide:  decide,
			Chat:    chat,
			Stream:  stream,
			Embed:   embed,
			Gates:   app.NewGates(),
			Status:  app.NewStatusRecorder(store.RecordStatus),
			Caps: func(model string) ([]string, bool) {
				have, ok, err := store.Capabilities(context.Background(), model)
				if err != nil || !ok {
					return nil, false
				}
				return have, true
			},
		},
		Names: func(r *http.Request) ([]string, error) {
			return store.Names(r.Context())
		},
		Log: log,
	}.Mount(mux)
}

// NewProviders is the use case behind the provider settings. It seals the
// keys a person adds with keys; a zero Keyring leaves it unable to save one.
// Nobody may share an account until the caller sets Allow.
func NewProviders(pool *pgxpool.Pool, keys Keyring, log *slog.Logger) app.Providers {
	table := adapters()
	probes := make(map[string]app.Prober, len(table))
	for name, a := range table {
		probes[name] = a
	}
	return app.Providers{
		Accounts: postgres.Providers{Pool: pool},
		Keys:     keys,
		Known: func(name string) bool {
			_, ok := table[name]
			return ok
		},
		Probes:  probes,
		Limiter: app.NewTestLimiter(10, time.Minute),
		Log:     log,
	}
}

// NewRouteBook reads routing rows for settings screens. It never resolves secrets.
func NewRouteBook(pool *pgxpool.Pool) app.RouteBook {
	return postgres.Store{Pool: pool}
}

// RoutesHandler serves the routing rows to a signed-in settings manager.
type RoutesHandler = httpapi.RoutesHandler

// ProvidersHandler serves a signed-in person's provider accounts.
type ProvidersHandler = httpapi.ProvidersHandler

// A model request and the input it is built from.
type (
	Input       = domain.Input
	Message     = domain.Message
	Request     = domain.Request
	Role        = domain.Role
	Breakpoint  = domain.Breakpoint
	ListedHop   = app.ListedHop
	ListedRoute = app.ListedRoute
	RouteBook   = app.RouteBook
	Provider    = domain.Provider
)

// UsableAgent reports whether an agent can be offered to a person now.
func UsableAgent(enabled bool, route, modelProvider, modelID string, routes []ListedRoute, accounts []Provider, defaultRoute string) bool {
	return app.UsableAgent(enabled, route, modelProvider, modelID, routes, accounts, defaultRoute)
}

// Message roles and cache breakpoints.
const (
	RoleUser           = domain.RoleUser
	BreakSystem        = domain.BreakSystem
	StatusConnected    = domain.StatusConnected
	StatusNotConnected = domain.StatusNotConnected
)

// Build returns the provider-neutral request for in.
func Build(in Input) (Request, error) { return domain.Build(in) }
