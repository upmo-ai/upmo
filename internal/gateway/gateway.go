package gateway

import (
	"context"
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway/adapters/anthropic"
	"github.com/upmo-ai/upmo/internal/gateway/adapters/fake"
	httpapi "github.com/upmo-ai/upmo/internal/gateway/adapters/http"
	"github.com/upmo-ai/upmo/internal/gateway/adapters/openai"
	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/gateway/adapters/vertex"
	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/platform/metrics"
	"github.com/upmo-ai/upmo/internal/platform/notices"
)

// Schema is the Postgres schema this process owns.
const Schema = "gateway"

// Ledger source constants are exposed through the gateway facade for callers
// in other contexts; validation and persistence use the domain values.
const (
	SourceChat           = domain.SourceChat
	SourceTask           = domain.SourceTask
	SourceAutomation     = domain.SourceAutomation
	SourceKnowledge      = domain.SourceKnowledge
	SourceMemory         = domain.SourceMemory
	SourceSystem         = domain.SourceSystem
	DefaultContextWindow = domain.DefaultContextWindow
)

// ProviderCaller is the provider settings caller passed to composition ports.
type ProviderCaller = app.Caller

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
		domain.AdapterVertex:    vertex.NewAdapter(),
	}
}

// MountOption configures Mount.
type MountOption func(*mountConfig)

type mountConfig struct {
	metrics          *metrics.Registry
	timeZone         func(context.Context) (string, error)
	settingsManagers func(context.Context) ([]string, error)
}

// WithMetrics attaches a Prometheus registry to the gateway dispatch handler.
func WithMetrics(reg *metrics.Registry) MountOption {
	return func(c *mountConfig) {
		c.metrics = reg
	}
}

// WithTimeZone attaches the workspace time zone lookup used for spend limit periods.
func WithTimeZone(fn func(context.Context) (string, error)) MountOption {
	return func(c *mountConfig) {
		c.timeZone = fn
	}
}

// WithSettingsManagers attaches the settings-manager user id lookup used for
// best-effort one-time unpriced-model estimate notices.
func WithSettingsManagers(fn func(context.Context) ([]string, error)) MountOption {
	return func(c *mountConfig) {
		c.settingsManagers = fn
	}
}

// Mount registers the JSON debug routes. cmd/gateway wraps them with HMAC.
func Mount(mux *http.ServeMux, pool *pgxpool.Pool, secrets app.Secrets, log *slog.Logger, opts ...MountOption) {
	var cfg mountConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	store := postgres.Store{Pool: pool}
	var estimateNotice app.EstimateNotifier
	if cfg.settingsManagers != nil {
		estimateNotice = postgres.EstimateNotifier{
			Pool:             pool,
			SettingsManagers: cfg.settingsManagers,
			Notify: func(ctx context.Context, tx pgx.Tx, userID, title string) error {
				uid, err := uuid.Parse(userID)
				if err != nil {
					return err
				}
				_, err = notices.InsertTx(ctx, tx, uid, notices.KindFailed, title, nil)
				return err
			},
		}
	}
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
			Routes:         store,
			Pins:           store,
			Secrets:        secrets,
			Prices:         store,
			Ledger:         store,
			Limits:         store,
			TimeZone:       cfg.timeZone,
			Bundles:        store,
			Teams:          store,
			Decide:         decide,
			Chat:           chat,
			Stream:         stream,
			Embed:          embed,
			Gates:          app.NewGates(),
			Status:         app.NewStatusRecorder(store.RecordStatus),
			EstimateNotice: estimateNotice,
			Metrics:        cfg.metrics,
			Log:            log,
			Caps: func(model string) ([]string, bool) {
				have, ok, err := store.Capabilities(context.Background(), model)
				if err != nil || !ok {
					return nil, false
				}
				return have, true
			},
			VendorCaps: func(vendor, model string) ([]string, bool) {
				have, ok, err := store.CapabilitiesForVendor(context.Background(), vendor, model)
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

func defaultProbes() map[string]app.Prober {
	table := adapters()
	probes := make(map[string]app.Prober, len(table))
	for name, a := range table {
		probes[name] = a
	}
	return probes
}

// NewProviders is the use case behind the provider settings. It seals the
// keys a person adds with keys; a zero Keyring leaves it unable to save one.
// Nobody may share an account until the caller sets Allow.
func NewProviders(pool *pgxpool.Pool, keys Keyring, log *slog.Logger) app.Providers {
	table := adapters()
	accounts := postgres.Providers{Pool: pool}
	catalog := postgres.Store{Pool: pool}
	return app.Providers{
		Accounts: accounts,
		Catalog:  catalog,
		SealTest: accounts.SealTest,
		Keys:     keys,
		Known: func(name string) bool {
			_, ok := table[name]
			return ok
		},
		Probes:  defaultProbes(),
		Limiter: app.NewTestLimiter(10, time.Minute),
		Log:     log,
	}
}

// NewRouteBook reads routing rows for settings screens. It never resolves secrets.
func NewRouteBook(pool *pgxpool.Pool) app.RouteBook {
	return postgres.Store{Pool: pool}
}

// NewRoutes is the use case behind creating, editing, sharing, and deleting
// gateway routes. Nobody may share a route until the caller sets Allow.
func NewRoutes(pool *pgxpool.Pool, log *slog.Logger) app.RouteService {
	return app.RouteService{
		Store: postgres.Routes{Pool: pool},
		Log:   log,
	}
}

// NewUsageLimits is the use case behind reading and managing monthly spend
// limits.
func NewUsageLimits(pool *pgxpool.Pool, log *slog.Logger) app.UsageLimits {
	return app.UsageLimits{
		Store: postgres.UsageLimits{Pool: pool},
		Log:   log,
	}
}

// NewUsage is the use case behind the usage reports. Nobody may read the
// workspace's usage until the caller sets Allow.
func NewUsage(pool *pgxpool.Pool) app.UsageService {
	return app.UsageService{Store: postgres.Usage{Pool: pool}}
}

// RoutesHandler serves the routing rows to a signed-in settings manager.
type RoutesHandler = httpapi.RoutesHandler

// ProvidersHandler serves a signed-in person's provider accounts.
type ProvidersHandler = httpapi.ProvidersHandler

// UsageLimitsHandler serves monthly spend limits to a signed-in person.
type UsageLimitsHandler = httpapi.UsageLimitsHandler

// UsageHandler serves the usage reports to a signed-in person.
type UsageHandler = httpapi.UsageHandler

// A model request and the input it is built from.
type (
	Input                 = domain.Input
	Message               = domain.Message
	Part                  = domain.Part
	UnsupportedInputError = domain.UnsupportedInputError
	Request               = domain.Request
	Role                  = domain.Role
	Breakpoint            = domain.Breakpoint
	Principal             = domain.Principal
	Tool                  = domain.Tool
	ToolCall              = domain.ToolCall
	ToolChoice            = domain.ToolChoice
	ChatResult            = domain.ChatResult
	Usage                 = domain.Usage
	ListedHop             = app.ListedHop
	ListedRoute           = app.ListedRoute
	RouteBook             = app.RouteBook
	RouteService          = app.RouteService
	UsageLimits           = app.UsageLimits
	UsageService          = app.UsageService
	UsageLimitsView       = app.UsageLimitsView
	UsageLimitRow         = app.UsageLimitRow
	OwnUsageLimits        = app.OwnUsageLimits
	SetUsageLimit         = app.SetUsageLimit
	UpdatedUsageLimit     = app.UpdatedUsageLimit
	Providers             = app.Providers
	Provider              = domain.Provider
	ModelKind             = domain.ModelKind
	ModelOverride         = domain.ModelOverride
	MergedModel           = domain.MergedModel
)

// UsableAgent reports whether an agent can be offered to a person now.
func UsableAgent(enabled bool, route, modelProvider, modelID string, routes []ListedRoute, accounts []Provider, defaultRoute string) bool {
	return app.UsableAgent(enabled, route, modelProvider, modelID, routes, accounts, defaultRoute)
}

// Message roles, input modalities, model kinds, and cache breakpoints.
const (
	RoleUser           = domain.RoleUser
	InputText          = domain.InputText
	InputImage         = domain.InputImage
	InputAudio         = domain.InputAudio
	InputFile          = domain.InputFile
	KindChat           = domain.KindChat
	KindEmbedding      = domain.KindEmbedding
	KindImage          = domain.KindImage
	KindRerank         = domain.KindRerank
	BreakSystem        = domain.BreakSystem
	StatusConnected    = domain.StatusConnected
	StatusNotConnected = domain.StatusNotConnected
)

// Build returns the provider-neutral request for in.
func Build(in Input) (Request, error) { return domain.Build(in) }
