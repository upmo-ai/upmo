// Package brain is the public composition entrypoint for the Upmo brain server.
package brain

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/upmo-ai/upmo/internal/agents"
	"github.com/upmo-ai/upmo/internal/artifacts"
	"github.com/upmo-ai/upmo/internal/chat"
	"github.com/upmo-ai/upmo/internal/decisions"
	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/knowledge"
	"github.com/upmo-ai/upmo/internal/memory"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/brainops"
	"github.com/upmo-ai/upmo/internal/platform/buildinfo"
	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/httpserver"
	"github.com/upmo-ai/upmo/internal/platform/jobs"
	"github.com/upmo-ai/upmo/internal/platform/logging"
	"github.com/upmo-ai/upmo/internal/platform/metrics"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/platform/notices"
	"github.com/upmo-ai/upmo/internal/platform/secretchain"
	"github.com/upmo-ai/upmo/internal/provision"
	"github.com/upmo-ai/upmo/internal/runtime"
	"github.com/upmo-ai/upmo/internal/tasks"
)

// BuildInfo holds compile-time build metadata for the brain server.
type BuildInfo struct {
	Version string
	Commit  string
	BuiltAt time.Time
}

// Principal describes an authenticated user session extracted from a request context.
type Principal struct {
	ID        string
	UserID    string
	SessionID string
	Method    string
	SignedIn  time.Time
	RoleID    string
}

// SessionFrom retrieves the signed-in principal from ctx when wrapped by
// IdentityService.RequireSession.
func SessionFrom(ctx context.Context) (Principal, bool) {
	p, ok := identity.SessionFrom(ctx)
	if !ok {
		return Principal{}, false
	}
	return Principal{
		ID:        p.ID,
		UserID:    p.UserID.String(),
		SessionID: p.SessionID,
		Method:    p.Method,
		SignedIn:  p.SignedIn,
		RoleID:    p.RoleID,
	}, true
}

// IdentityService exposes session authentication and freshness checks to extension routes.
type IdentityService interface {
	RequireSession(next http.Handler) http.Handler
	SessionFrom(ctx context.Context) (Principal, bool)
	Fresh(p Principal) bool
}

// MemberService exposes member permissions and team memberships.
type MemberService interface {
	EffectiveAllow(ctx context.Context, userID string) (string, error)
	TeamsOf(ctx context.Context, userID string) ([]string, error)
}

func providerMembersAllowed(ctx context.Context, pool *pgxpool.Pool, user string) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("brain: member providers setting: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('upmo.user_id', $1, true)`, user); err != nil {
		return false, fmt.Errorf("brain: member providers setting: %w", err)
	}
	var allowed bool
	if err := tx.QueryRow(ctx, `SELECT identity.member_providers_allowed()`).Scan(&allowed); err != nil {
		return false, fmt.Errorf("brain: member providers setting: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("brain: member providers setting: %w", err)
	}
	return allowed, nil
}

// JobClient enqueues background River jobs inside a caller transaction.
type JobClient interface {
	InsertTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

// lazyJobs is handed to extension hooks before the job client exists, since
// opening it needs the workers those hooks register.
type lazyJobs struct {
	target atomic.Pointer[JobClient]
}

func (l *lazyJobs) set(c JobClient) { l.target.Store(&c) }

func (l *lazyJobs) InsertTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	c := l.target.Load()
	if c == nil {
		return nil, errors.New("brain: job client is not open yet")
	}
	return (*c).InsertTx(ctx, tx, args, opts)
}

// NoticeService inserts user notifications.
type NoticeService interface {
	Insert(ctx context.Context, userID uuid.UUID, kind, title string, taskID *uuid.UUID) (uuid.UUID, error)
}

// Sealed holds an AEAD-encrypted secret and its key identifier.
type Sealed struct {
	Ciphertext []byte
	Nonce      []byte
	KeyID      string
}

// Sealer seals and opens secrets under the process master keyring.
type Sealer interface {
	Ready() bool
	Seal(ref, plaintext string) (Sealed, error)
	Open(ref string, sealed Sealed) (string, error)
}

// Deps exposes composed services and configuration to extension options.
// Every field is set when the worker and periodic hooks run. Jobs is a
// client that enqueues once the job workers are open, so a hook may keep it
// but must not call InsertTx before brain.New returns.
type Deps struct {
	Owner          *pgxpool.Pool
	App            *pgxpool.Pool
	Identity       IdentityService
	Members        MemberService
	Notices        NoticeService
	Jobs           JobClient
	Sealer         Sealer
	Logger         *slog.Logger
	Config         Config
	BuildInfo      BuildInfo
	SchemaVersions func(context.Context) (map[string]int64, error)
}

type identityAdapter struct {
	id identity.Identity
}

func (a identityAdapter) RequireSession(next http.Handler) http.Handler {
	return a.id.RequireSession(next)
}

func (a identityAdapter) SessionFrom(ctx context.Context) (Principal, bool) {
	return SessionFrom(ctx)
}

func (a identityAdapter) Fresh(p Principal) bool {
	return a.id.Fresh(identity.Session{SignedIn: p.SignedIn})
}

type memberAdapter struct {
	members identity.MemberService
}

func (a memberAdapter) EffectiveAllow(ctx context.Context, userID string) (string, error) {
	id, err := identity.ParseUserID(userID)
	if err != nil {
		return "", err
	}
	return a.members.EffectiveAllow(ctx, id)
}

func (a memberAdapter) TeamsOf(ctx context.Context, userID string) ([]string, error) {
	id, err := identity.ParseUserID(userID)
	if err != nil {
		return nil, err
	}
	return a.members.TeamsOf(ctx, id)
}

type sealerAdapter struct {
	keys gateway.Keyring
}

func (a sealerAdapter) Ready() bool {
	return a.keys.Ready()
}

func (a sealerAdapter) Seal(ref, plaintext string) (Sealed, error) {
	s, err := a.keys.Seal(ref, plaintext)
	if err != nil {
		return Sealed{}, err
	}
	return Sealed{
		Ciphertext: s.Ciphertext,
		Nonce:      s.Nonce,
		KeyID:      s.KeyID,
	}, nil
}

func (a sealerAdapter) Open(ref string, sealed Sealed) (string, error) {
	return a.keys.Open(ref, gateway.Sealed{
		Ciphertext: sealed.Ciphertext,
		Nonce:      sealed.Nonce,
		KeyID:      sealed.KeyID,
	})
}

type internalDeps struct {
	identity    identity.Identity
	members     identity.MemberService
	roles       identity.RoleService
	teams       identity.TeamService
	tokens      identity.TokenService
	workspace   identity.WorkspaceService
	provision   identity.ProvisionStore
	providers   gateway.Providers
	routes      gateway.RouteService
	routeBook   gateway.RouteBook
	usageLimits gateway.UsageLimits
	keyring     gateway.Keyring
	agents      agents.Store
	notices     notices.Store
	metrics     *metrics.Registry
	buildRaw    buildinfo.Details
}

type options struct {
	owner         *pgxpool.Pool
	app           *pgxpool.Pool
	logger        *slog.Logger
	extraRoutes   []func(mux *http.ServeMux, deps Deps)
	provisioner   func(mux *http.ServeMux, deps Deps)
	extraWorkers  []func(workers *river.Workers, deps Deps)
	extraPeriodic []func(deps Deps) []*river.PeriodicJob
}

// Option customizes brain composition in New or Run.
type Option func(*options)

// WithExtraRoutes registers additional HTTP routes on the brain mux.
func WithExtraRoutes(fn func(mux *http.ServeMux, deps Deps)) Option {
	return func(o *options) {
		if fn != nil {
			o.extraRoutes = append(o.extraRoutes, fn)
		}
	}
}

// WithProvisioner replaces the default /api/v1/provision/* mount hook.
func WithProvisioner(fn func(mux *http.ServeMux, deps Deps)) Option {
	return func(o *options) {
		o.provisioner = fn
	}
}

// WithExtraWorkers registers additional River job workers at startup.
func WithExtraWorkers(fn func(workers *river.Workers, deps Deps)) Option {
	return func(o *options) {
		if fn != nil {
			o.extraWorkers = append(o.extraWorkers, fn)
		}
	}
}

// WithExtraPeriodic registers additional periodic River jobs at startup.
func WithExtraPeriodic(fn func(deps Deps) []*river.PeriodicJob) Option {
	return func(o *options) {
		if fn != nil {
			o.extraPeriodic = append(o.extraPeriodic, fn)
		}
	}
}

// WithPools injects pre-opened owner and app pools (primarily for tests).
func WithPools(owner, app *pgxpool.Pool) Option {
	return func(o *options) {
		o.owner = owner
		o.app = app
	}
}

// WithLogger overrides the default stdout logger.
func WithLogger(log *slog.Logger) Option {
	return func(o *options) {
		o.logger = log
	}
}

// App is a composed brain server ready to serve HTTP requests and background jobs.
type App struct {
	deps        Deps
	buildRaw    buildinfo.Details
	metricsReg  *metrics.Registry
	handler     http.Handler
	work        *jobs.Client
	boxes       *runtime.TaskSandboxes
	streams     *tasks.StreamHub
	chatStreams *chat.StreamHub
	ownsOwner   bool
	ownsApp     bool
	closeOnce   sync.Once
	stopOnce    sync.Once
}

// Handler returns the composed HTTP handler (audit, cross-origin guard, metrics, mux).
func (a *App) Handler() http.Handler {
	return a.handler
}

// Deps returns the composed dependencies.
func (a *App) Deps() Deps {
	return a.deps
}

// Serve runs the HTTP server (and optional metrics server) until ctx is
// cancelled, then gracefully drains background jobs and sandbox provisioning.
func (a *App) Serve(ctx context.Context) error {
	cfg := a.deps.Config
	log := a.deps.Logger
	log.Info("brain started", "version", a.buildRaw.Version, "commit", a.buildRaw.Commit, "build_time", a.buildRaw.BuildTime)

	srv := httpserver.NewGuarded(cfg.ListenAddr, a.handler)
	srv.RegisterOnShutdown(a.closeStreams)

	var err error
	if a.metricsReg != nil {
		srvCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		errc := make(chan error, 2)
		for _, s := range []*http.Server{srv, metrics.Server(cfg.MetricsAddr, a.metricsReg)} {
			go func() {
				errc <- httpserver.Run(srvCtx, s, cfg.ShutdownTimeout, log)
				cancel()
			}()
		}
		err = errors.Join(<-errc, <-errc)
	} else {
		err = httpserver.Run(ctx, srv, cfg.ShutdownTimeout, log)
	}

	a.stopWorkAndBoxes()
	return err
}

// Close releases background streams, workers, and any database pools opened by New.
func (a *App) Close() {
	a.closeOnce.Do(func() {
		a.closeStreams()
		a.stopWorkAndBoxes()
		if a.ownsApp && a.deps.App != nil {
			a.deps.App.Close()
		}
		if a.ownsOwner && a.deps.Owner != nil {
			a.deps.Owner.Close()
		}
	})
}

func (a *App) closeStreams() {
	if a.streams != nil {
		a.streams.Close()
	}
	if a.chatStreams != nil {
		a.chatStreams.Close()
	}
}

func (a *App) stopWorkAndBoxes() {
	a.stopOnce.Do(func() {
		// Provisioning still running in the background gets the shutdown
		// timeout to settle its task before the pool closes. Any left after
		// that is abandoned: its task stays in Preparing and sandboxd reclaims
		// its sandbox at the sandbox TTL.
		wait, cancel := context.WithTimeout(context.Background(), a.deps.Config.ShutdownTimeout)
		defer cancel()
		if a.work != nil {
			if werr := a.work.Stop(wait); werr != nil {
				a.deps.Logger.Warn("job workers still running at shutdown", "err", werr)
			}
		}
		if a.boxes != nil {
			if werr := a.boxes.Wait(wait); werr != nil {
				a.deps.Logger.Warn("sandbox provisioning still running at shutdown", "err", werr)
			}
		}
	})
}

// Run loads configuration from the environment, composes the brain server,
// serves until ctx is cancelled, and closes resources cleanly.
func Run(ctx context.Context, opts ...Option) error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	app, err := New(ctx, cfg, opts...)
	if err != nil {
		return err
	}
	defer app.Close()
	return app.Serve(ctx)
}

func applyConfigDefaults(cfg Config) Config {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8080"
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = 10 * time.Second
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}
	if cfg.ChatRoute == "" {
		cfg.ChatRoute = "chat"
	}
	if cfg.ChatWorkers < 1 {
		cfg.ChatWorkers = 8
	}
	if cfg.SessionIdle <= 0 {
		cfg.SessionIdle = identity.IdleTimeout
	}
	if cfg.SessionMaxAge <= 0 {
		cfg.SessionMaxAge = identity.MaxAge
	}
	if cfg.AuditRetentionDays <= 0 {
		cfg.AuditRetentionDays = 365
	}
	return cfg
}

func resolveBuildInfo() (BuildInfo, buildinfo.Details) {
	raw := buildinfo.Info()
	var builtAt time.Time
	if parsed, err := time.Parse(time.RFC3339, raw.BuildTime); err == nil {
		builtAt = parsed
	}
	return BuildInfo{
		Version: raw.Version,
		Commit:  raw.Commit,
		BuiltAt: builtAt,
	}, raw
}

// New composes a brain server from cfg and opts, migrating the database and
// starting background workers and stream hubs.
func New(ctx context.Context, cfg Config, opts ...Option) (*App, error) {
	cfg = applyConfigDefaults(cfg)

	// Built on every start, before any pool opens: outside development the
	// known development keys and passwords refuse to start brain. This also
	// covers callers that build brain.Config directly instead of loading it
	// from the environment.
	env, err := config.ParseEnv(cfg.Env)
	if err != nil {
		return nil, err
	}
	internal := toInternalConfig(cfg)
	internal.Env = env
	if err := config.CheckConfig(internal, true, os.LookupEnv, os.Environ()); err != nil {
		return nil, err
	}

	var o options
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	log := o.logger
	if log == nil {
		log = logging.New(os.Stdout, cfg.LogLevel)
	}

	provisionKeys, err := provision.ParseKeys(cfg.ProvisionKeys)
	if err != nil {
		return nil, err
	}

	// Built on every start, before any pool opens: a plaintext Vault address
	// with a token fails brain start however the audit push is configured.
	secrets, err := secretchain.FromEnv(os.LookupEnv)
	if err != nil {
		return nil, err
	}

	owner := o.owner
	ownsOwner := false
	if owner == nil {
		owner, err = brainops.OpenOwnerPool(ctx, cfg.DatabaseURL)
		if err != nil {
			return nil, err
		}
		ownsOwner = true
		if err := brainops.MigrateAll(ctx, owner); err != nil {
			owner.Close()
			return nil, err
		}
	}
	cleanupOnErr := func() {
		if ownsOwner {
			owner.Close()
		}
	}

	schemaVersions, err := migrate.SchemaVersions(ctx, owner)
	if err != nil {
		cleanupOnErr()
		return nil, err
	}
	schemaLookup := func(context.Context) (map[string]int64, error) {
		return maps.Clone(schemaVersions), nil
	}

	pool := o.app
	ownsApp := false
	if pool == nil {
		if err := brainops.EnableAppLogin(ctx, owner, cfg.DatabaseAppURL); err != nil {
			cleanupOnErr()
			return nil, err
		}
		pool, err = brainops.OpenAppPool(ctx, owner, cfg.DatabaseAppURL)
		if err != nil {
			cleanupOnErr()
			return nil, err
		}
		ownsApp = true
	}
	cleanupPools := func() {
		if ownsApp {
			pool.Close()
		}
		cleanupOnErr()
	}

	masterKeys, err := gateway.KeyringFromEnv(os.LookupEnv, os.Environ())
	if err != nil {
		cleanupPools()
		return nil, err
	}

	// Rows sealed with a key the ring does not hold can never open; say so
	// once per store, without stopping start-up. So does a counting error.
	if unknown, err := brainops.UnknownKeyRows(ctx, owner, masterKeys); err != nil {
		log.Warn("counting sealed rows by key failed", "err", err)
	} else {
		for _, count := range unknown {
			if count.Unknown > 0 {
				log.Warn("sealed rows use a master key that is not loaded",
					"store", count.Store, "rows", count.Unknown, "key_ids", count.UnknownKeyIDs)
			}
		}
	}

	internalCfg := toInternalConfig(cfg)
	emails := brainops.NewEmailSealer(masterKeys)
	sessions := identity.Sessions(pool, cfg.SessionIdle, cfg.SessionMaxAge)
	passwords := identity.Passwords(pool, emails, sessions)
	passwords.Sealer = brainops.NewPasswordSealer(masterKeys)
	tokens := identity.APITokens(pool, log)
	passkeys, err := identity.Passkeys(pool, emails, sessions, cfg.PublicURL, cfg.WebAuthnRPID, log)
	if err != nil {
		cleanupPools()
		return nil, err
	}
	members := identity.Members(pool, emails, cfg.PublicURL, brainops.LinkMail(internalCfg.SMTP), log)
	roles := identity.Roles(pool, log)
	teams := identity.Teams(pool, log)
	workspace := identity.Workspace(pool)
	provIdentity := identity.Provision(owner, emails, cfg.PublicURL)
	idFacade := identity.New(sessions, passkeys, passwords)

	providers := gateway.NewProviders(pool, masterKeys, log)
	providers.LocalPlan = func(ctx context.Context, user string) bool {
		id, err := identity.ParseUserID(user)
		if err != nil {
			return false
		}
		allow, err := members.SettingsAllow(ctx, id)
		return err == nil && allow != ""
	}
	providers.Allow = func(ctx context.Context, user string) (string, error) {
		person, err := identity.ParseUserID(user)
		if err != nil {
			return "", err
		}
		return members.SettingsAllow(ctx, person)
	}
	providers.Teams = func(ctx context.Context, user string) ([]string, error) {
		person, err := identity.ParseUserID(user)
		if err != nil {
			return nil, err
		}
		return members.TeamsOf(ctx, person)
	}
	providers.Names = func(ctx context.Context, teamIDs []string) (map[string]string, error) {
		_, names, err := members.NamesOf(ctx, nil, teamIDs)
		return names, err
	}
	providers.MemberProvidersAllowed = func(ctx context.Context, user string) (bool, error) {
		return providerMembersAllowed(ctx, pool, user)
	}
	routesSvc := gateway.NewRoutes(pool, log)
	routesSvc.Allow = providers.Allow
	routesSvc.Teams = providers.Teams
	routesSvc.Names = providers.Names
	routeBook := gateway.NewRouteBook(pool)
	usageLimits := gateway.NewUsageLimits(pool, log)
	usageLimits.Allow = func(ctx context.Context, user string) (string, error) {
		person, err := identity.ParseUserID(user)
		if err != nil {
			return "", err
		}
		return members.EffectiveAllow(ctx, person)
	}
	usageLimits.TimeZone = func(ctx context.Context) (string, error) {
		return identity.WorkspaceTimeZone(ctx, owner)
	}
	usageLimits.PersonExists = func(ctx context.Context, id string) (bool, error) {
		names, _, err := members.NamesOf(ctx, []string{id}, nil)
		if err != nil {
			return false, err
		}
		return names[id] != "", nil
	}
	usageLimits.AutomationExists = func(ctx context.Context, id string) (bool, error) {
		return tasks.AutomationExists(ctx, pool, id)
	}
	agentStore := agents.NewStore(pool)
	providers.AgentUses = func(ctx context.Context, who gateway.ProviderCaller, provider string, models []string) ([]string, error) {
		person, err := identity.ParseUserID(who.User)
		if err != nil {
			return nil, err
		}
		access, ok, err := agentAccess(ctx, members, person)
		if err != nil || !ok {
			return nil, err
		}
		return agentStore.ProviderUses(ctx, access, provider, models)
	}
	// Automations currently refer only to a task template, not to a model; keep
	// the composition port explicit so a future model reference has one home.
	providers.AutomationUses = func(context.Context, gateway.ProviderCaller, string, []string) ([]string, error) {
		return []string{}, nil
	}
	noticeStore := notices.Store{Pool: pool}

	var reg *metrics.Registry
	if cfg.MetricsAddr != "" {
		reg = metrics.NewRegistry()
		brainops.RegisterUnknownKeyMetric(reg, owner, masterKeys)
	}

	pubBuild, rawBuild := resolveBuildInfo()
	lazy := &lazyJobs{}
	deps := Deps{
		Owner:          owner,
		App:            pool,
		Identity:       identityAdapter{id: idFacade},
		Members:        memberAdapter{members: members},
		Notices:        noticeStore,
		Jobs:           lazy,
		Sealer:         sealerAdapter{keys: masterKeys},
		Logger:         log,
		Config:         cfg,
		BuildInfo:      pubBuild,
		SchemaVersions: schemaLookup,
	}
	ideps := internalDeps{
		identity:    idFacade,
		members:     members,
		roles:       roles,
		teams:       teams,
		tokens:      tokens,
		workspace:   workspace,
		provision:   provIdentity,
		providers:   providers,
		routes:      routesSvc,
		routeBook:   routeBook,
		usageLimits: usageLimits,
		keyring:     masterKeys,
		agents:      agentStore,
		notices:     noticeStore,
		metrics:     reg,
		buildRaw:    rawBuild,
	}

	workers := river.NewWorkers()
	// The sealer runs on the owner pool: pending and sealed events are the
	// app role's to insert and read, never to move. The owner pool stays
	// open until work.Stop has drained the workers at shutdown.
	audit.AddSealer(workers, owner, os.Stdout)
	audit.AddRetention(workers, owner, cfg.AuditRetentionDays)
	audit.AddVerifier(workers, owner)
	gateway.AddSpendChecker(workers, owner, func(ctx context.Context) (string, error) {
		return identity.WorkspaceTimeZone(ctx, owner)
	}, reg, log)
	checkSecrets := append(slices.Clone(secrets), gateway.DBSecrets{Pool: owner, Keys: masterKeys})
	gateway.AddProviderChecker(workers, gateway.ProviderCheckDeps{
		Owner:    owner,
		App:      pool,
		Enqueuer: lazy,
		Secrets:  checkSecrets,
		AgentModelUses: func(ctx context.Context, provider string, models []string) ([]string, error) {
			return agents.SystemProviderModelUses(ctx, owner, provider, models)
		},
		AutomationModelUses: func(context.Context, string, []string) ([]string, error) {
			return nil, nil
		},
		ActiveUser: func(ctx context.Context, userID string) (bool, error) {
			return identity.ActiveUser(ctx, owner, userID)
		},
		SettingsManagers: func(ctx context.Context) ([]string, error) {
			return identity.SettingsManagers(ctx, owner)
		},
		Notify: func(ctx context.Context, tx pgx.Tx, userID, title string) error {
			uid, err := uuid.Parse(userID)
			if err != nil {
				return err
			}
			_, err = notices.InsertTx(ctx, tx, uid, notices.KindFailed, title, nil)
			return err
		},
		Log: log,
	})
	gateway.AddCatalogRefresher(workers, gateway.CatalogRefreshDeps{
		Owner: owner,
		SettingsManagers: func(ctx context.Context) ([]string, error) {
			return identity.SettingsManagers(ctx, owner)
		},
		Notify: func(ctx context.Context, tx pgx.Tx, userID, title string) error {
			uid, err := uuid.Parse(userID)
			if err != nil {
				return err
			}
			_, err = notices.InsertTx(ctx, tx, uid, notices.KindFailed, title, nil)
			return err
		},
		Log: log,
	})
	auditPush := cfg.AuditWebhookURL != "" || cfg.AuditSyslogAddr != ""
	if auditPush {
		audit.AddPusher(workers, owner, audit.PushConfig{
			WebhookURL: cfg.AuditWebhookURL,
			SyslogAddr: cfg.AuditSyslogAddr,
			Secrets:    secrets,
		})
	}
	memoryFacts := memory.NewFacts(pool)
	chat.AddSweepWorker(workers, chat.NewStore(pool, nil))
	provision.AddNonceSweep(workers, provIdentity)
	if cfg.GatewayURL != "" && cfg.ServiceKey != "" {
		embedRoute := cfg.EmbedRoute
		if embedRoute == "" {
			embedRoute = "memory-embed"
		}
		gw, err := memory.OpenGateway(cfg.GatewayURL, cfg.ServiceKey, "memory-extract", embedRoute)
		if err != nil {
			cleanupPools()
			return nil, fmt.Errorf("brain: memory gateway: %w", err)
		}
		knowledgeBook := knowledge.NewStore(pool, nil)
		tasks.AddScoutWorker(workers, tasks.ScoutDeps{
			Goals:  tasks.NewStore(pool, nil),
			Chat:   scoutChat{store: chat.NewStore(pool, nil), members: members},
			Memory: scoutMemory{editor: memoryFacts},
			Knowledge: scoutKnowledge{
				folders: knowledgeBook,
				finder:  knowledgeBook,
				members: members,
			},
			Propose: func(_ context.Context, userID uuid.UUID) tasks.Proposer {
				return scoutProposer{inner: gw.ForTask(userID.String())}
			},
			Notices: noticeStore,
		})
		memory.AddExtractWorker(workers, memoryFacts,
			func(userID string) memory.Proposer { return gw.For(userID) },
			func(userID string) memory.Embedder { return gw.For(userID) },
		)
		chatModel, err := chat.OpenModel(cfg.GatewayURL, cfg.ServiceKey)
		if err != nil {
			cleanupPools()
			return nil, fmt.Errorf("brain: chat gateway: %w", err)
		}
		if err := chat.AddReplyWorker(workers, chat.ReplyDeps{
			Rows: chat.NewStore(pool, nil),
			Agents: chatAgentLookup{store: agentStore, access: func(ctx context.Context, userID string) (agents.Access, error) {
				person, err := identity.ParseUserID(userID)
				if err != nil {
					return agents.Access{}, err
				}
				who, _, err := agentAccess(ctx, members, person)
				return who, err
			}},
			Model: chatModel,
			Access: func(ctx context.Context, userID string) (chat.Access, bool, error) {
				person, err := identity.ParseUserID(userID)
				if err != nil {
					return chat.Access{}, false, err
				}
				return chatAccess(ctx, members, person)
			},
			Log: log,
		}); err != nil {
			cleanupPools()
			return nil, fmt.Errorf("brain: chat reply worker: %w", err)
		}
	}
	for _, fn := range o.extraWorkers {
		fn(workers, deps)
	}

	periodic := []*river.PeriodicJob{
		chat.SweepPeriodic(), audit.SealPeriodic(),
		audit.RetentionPeriodic(), audit.VerifyPeriodic(),
		provision.NonceSweepPeriodic(), gateway.SpendCheckPeriodic(),
		gateway.ProviderCheckPeriodic(), gateway.CatalogRefreshPeriodic(),
	}
	if auditPush {
		periodic = append(periodic, audit.PushPeriodic())
	}
	for _, fn := range o.extraPeriodic {
		periodic = append(periodic, fn(deps)...)
	}

	work, err := jobs.Open(pool,
		jobs.WithWorkers(workers),
		jobs.WithQueue(chat.ReplyQueue, cfg.ChatWorkers),
		jobs.WithPeriodic(periodic...),
		jobs.WithMetrics(reg),
	)
	if err != nil {
		cleanupPools()
		return nil, err
	}
	if err := work.Start(ctx); err != nil {
		cleanupPools()
		return nil, err
	}
	lazy.set(work)
	deps.Jobs = work

	boxes, err := taskSandboxes(internalCfg, pool, log)
	if err != nil {
		_ = work.Stop(ctx)
		cleanupPools()
		return nil, err
	}

	var listenConn *pgx.ConnConfig
	if cfg.DatabaseURL != "" {
		listenConn, err = brainops.ParseListenConfig(cfg.DatabaseURL)
		if err != nil {
			_ = work.Stop(ctx)
			cleanupPools()
			return nil, err
		}
	} else {
		listenConn = owner.Config().ConnConfig.Copy()
	}

	streams := tasks.NewStreamHub(pool, listenConn, log, tasks.StreamConfig{})
	streams.Start(ctx)
	chatStreams := chat.NewStreamHub(pool, listenConn, log, chat.StreamConfig{})
	chatStreams.Start(ctx)

	handler, err := buildHandler(deps, ideps, boxes, work, streams, chatStreams, provisionKeys, o)
	if err != nil {
		streams.Close()
		chatStreams.Close()
		_ = work.Stop(ctx)
		cleanupPools()
		return nil, err
	}

	return &App{
		deps:        deps,
		buildRaw:    rawBuild,
		metricsReg:  reg,
		handler:     handler,
		work:        work,
		boxes:       boxes,
		streams:     streams,
		chatStreams: chatStreams,
		ownsOwner:   ownsOwner,
		ownsApp:     ownsApp,
	}, nil
}

func buildHandler(
	deps Deps,
	ideps internalDeps,
	boxes *runtime.TaskSandboxes,
	work *jobs.Client,
	streams *tasks.StreamHub,
	chatStreams *chat.StreamHub,
	provisionKeys []provision.Key,
	o options,
) (http.Handler, error) {
	pool := deps.App
	cfg := deps.Config
	log := deps.Logger
	members := ideps.members
	sessions := ideps.identity.Sessions
	passwords := ideps.identity.Passwords
	passkeys := ideps.identity.Passkeys
	tokens := ideps.tokens
	reg := ideps.metrics

	mux := http.NewServeMux()
	httpserver.Health(mux, pool.Ping)
	httpserver.Version(mux, deps.SchemaVersions)
	identity.MountAPITokens(mux, sessions, tokens, log, passwords.HasPassword, pool, cfg.TrustedProxies)
	identity.MountPasskeys(mux, passkeys, passwords, cfg.TrustedProxies)
	identity.MountMembers(mux, members, sessions)
	identity.MountRoles(mux, ideps.roles, sessions)
	identity.MountTeams(mux, ideps.teams, sessions)
	identity.MountConnectorGrants(mux, identity.ConnectorGrants(pool), sessions)
	identity.MountWorkspace(mux, ideps.workspace, sessions)

	taskBook := tasks.NewStore(pool, work)
	taskBook.ReplaceTaskGrants = taskGrants{members: members}.ReplaceTaskGrantsTx
	var editorSigner tasks.EditorSigner
	if cfg.ServiceKey != "" {
		editorTokens, err := tasks.NewEditorTokens(cfg.ServiceKey, cfg.ServiceKeyPrevious)
		if err != nil {
			return nil, err
		}
		editorSigner = editorTokens
	}
	taskAccess := func(ctx context.Context, user identity.UserID) (tasks.Access, bool, error) {
		allow, err := members.TaskAllow(ctx, user)
		if err != nil || allow == "" {
			return tasks.Access{}, false, err
		}
		shares, err := members.ShareAccess(ctx, user, identity.TypeTask)
		if err != nil {
			return tasks.Access{}, false, err
		}
		return tasks.Access{
			UserID:     user.String(),
			Allow:      allow,
			Teams:      shares.Teams,
			SharedView: shares.SharedView,
			SharedEdit: shares.SharedEdit,
		}, true, nil
	}
	tasks.Handler{
		Book:         taskBook,
		List:         taskBook,
		View:         taskBook,
		Approvals:    taskBook,
		Shares:       taskBook,
		Grants:       taskGrants{members: members},
		Sandboxes:    boxes,
		Progress:     boxes,
		PublicURL:    cfg.PublicURL,
		Editor:       boxes,
		EditorSigner: editorSigner,
		Audit:        pool,
		Stream:       streams,
		Metrics:      reg,
		Access: func(r *http.Request) (tasks.Access, bool) {
			person, ok := identity.SessionFrom(r.Context())
			if !ok {
				return tasks.Access{}, false
			}
			access, ok, err := taskAccess(r.Context(), person.UserID)
			if err != nil || !ok {
				return tasks.Access{UserID: person.UserID.String()}, true
			}
			return access, true
		},
		Renew: func(ctx context.Context, r *http.Request) (tasks.Access, bool, error) {
			person, ok, err := identity.Recheck(ctx, sessions, r)
			if err != nil || !ok {
				return tasks.Access{}, false, err
			}
			return taskAccess(ctx, person.UserID)
		},
	}.Mount(mux, func(next http.Handler) http.Handler {
		return identity.RequireSession(sessions, next)
	})

	memory.Handler{
		Editor: memory.NewFacts(pool),
		User: func(r *http.Request) (string, bool) {
			person, ok := identity.SessionFrom(r.Context())
			if !ok {
				return "", false
			}
			return person.UserID.String(), true
		},
	}.Mount(mux, func(next http.Handler) http.Handler {
		return identity.RequireSession(sessions, next)
	})

	artifactStore := artifacts.Store{Pool: pool}
	artifacts.Handler{
		Catalog: artifactStore,
		Blobs:   artifacts.Volume{Root: cfg.ArtifactsDir},
		Access: func(r *http.Request) (artifacts.Access, bool) {
			person, ok := identity.SessionFrom(r.Context())
			if !ok {
				return artifacts.Access{}, false
			}
			allow, err := members.ArtifactAllow(r.Context(), person.UserID)
			if err != nil || allow == "" {
				return artifacts.Access{UserID: person.UserID.String()}, true
			}
			shares, err := members.ShareAccess(r.Context(), person.UserID, identity.TypeArtifact)
			if err != nil {
				return artifacts.Access{UserID: person.UserID.String()}, true
			}
			return artifacts.Access{
				UserID:     person.UserID.String(),
				Allow:      allow,
				Teams:      shares.Teams,
				SharedView: shares.SharedView,
				SharedEdit: shares.SharedEdit,
			}, true
		},
	}.Mount(mux, func(next http.Handler) http.Handler {
		return identity.RequireSession(sessions, next)
	})

	chatBook := chat.NewStore(pool, work)
	if cfg.GatewayURL != "" && cfg.ServiceKey != "" {
		chatBook.ReplyRoute = cfg.ChatRoute
	}
	chat.Handler{
		Book:    chatBook,
		Stream:  chatStreams,
		Metrics: reg,
		Access: func(r *http.Request) (chat.Access, bool) {
			person, ok := identity.SessionFrom(r.Context())
			if !ok {
				return chat.Access{}, false
			}
			access, ok, err := chatAccess(r.Context(), members, person.UserID)
			if err != nil || !ok {
				return chat.Access{UserID: person.UserID.String()}, true
			}
			return access, true
		},
		Renew: func(ctx context.Context, r *http.Request) (chat.Access, bool, error) {
			person, ok, err := identity.Recheck(ctx, sessions, r)
			if err != nil || !ok {
				return chat.Access{}, false, err
			}
			return chatAccess(ctx, members, person.UserID)
		},
	}.Mount(mux, func(next http.Handler) http.Handler {
		return identity.RequireSession(sessions, next)
	})

	knowledgeBook := knowledge.NewStore(pool, work)
	knowledgeConverter := knowledge.Converter{Text: knowledge.PassThroughText{}}
	if cfg.MarkitdownURL != "" {
		knowledgeConverter.Sidecar = &knowledge.Sidecar{BaseURL: cfg.MarkitdownURL}
	}
	var knowledgeEmbedFor func(userID string) knowledge.Embedder
	if cfg.GatewayURL != "" && cfg.ServiceKey != "" && cfg.EmbedRoute != "" {
		embedClient, err := knowledge.OpenEmbedder(cfg.GatewayURL, cfg.ServiceKey, cfg.EmbedRoute)
		if err != nil {
			return nil, fmt.Errorf("brain: knowledge embedder: %w", err)
		}
		knowledgeEmbedFor = embedClient.For
	}
	knowledgeHandler := knowledge.Handler{
		Store:     knowledgeBook,
		Pending:   knowledgeBook,
		Folders:   knowledgeBook,
		Mutate:    knowledgeBook,
		Sources:   knowledgeBook,
		Delete:    knowledgeBook,
		Finder:    knowledgeBook,
		Fetcher:   knowledge.NewFetcher(),
		Converter: knowledgeConverter,
		EmbedFor:  knowledgeEmbedFor,
		Access: func(r *http.Request) (knowledge.Access, bool) {
			person, ok := identity.SessionFrom(r.Context())
			if !ok {
				return knowledge.Access{}, false
			}
			allow, err := members.FolderAllow(r.Context(), person.UserID)
			if err != nil || allow == "" {
				return knowledge.Access{UserID: person.UserID.String()}, true
			}
			shares, err := members.ShareAccess(r.Context(), person.UserID, identity.TypeFolder)
			if err != nil {
				return knowledge.Access{UserID: person.UserID.String()}, true
			}
			return knowledge.Access{
				UserID:     person.UserID.String(),
				Allow:      allow,
				Teams:      shares.Teams,
				SharedView: shares.SharedView,
				SharedEdit: shares.SharedEdit,
			}, true
		},
	}
	if cfg.KnowledgeDir != "" {
		knowledgeHandler.Blobs = knowledge.Volume{Root: cfg.KnowledgeDir}
	}
	knowledgeHandler.Mount(mux, func(next http.Handler) http.Handler {
		return identity.RequireSession(sessions, next)
	})

	decisions.Handler{
		List: decisions.Store{Pool: pool},
		Access: func(r *http.Request) (decisions.Access, bool) {
			person, ok := identity.SessionFrom(r.Context())
			if !ok {
				return decisions.Access{}, false
			}
			allow, err := members.SettingsAllow(r.Context(), person.UserID)
			if err != nil {
				return decisions.Access{UserID: person.UserID.String()}, true
			}
			return decisions.Access{UserID: person.UserID.String(), Allow: allow}, true
		},
	}.Mount(mux, func(next http.Handler) http.Handler {
		return identity.RequireSession(sessions, next)
	})

	gateway.ProvidersHandler{
		Service: ideps.providers,
		Log:     log,
		Access: func(r *http.Request) (string, bool) {
			person, ok := identity.SessionFrom(r.Context())
			if !ok {
				return "", false
			}
			return person.UserID.String(), true
		},
	}.MountProviders(mux, func(next http.Handler) http.Handler {
		return identity.RequireSession(sessions, next)
	})

	gateway.RoutesHandler{
		Book:     ideps.routeBook,
		Service:  ideps.routes,
		Accounts: ideps.providers,
		Log:      log,
		Access: func(r *http.Request) (string, bool) {
			person, ok := identity.SessionFrom(r.Context())
			if !ok {
				return "", false
			}
			return person.UserID.String(), true
		},
	}.MountRoutes(mux, func(next http.Handler) http.Handler {
		return identity.RequireSession(sessions, next)
	})

	gateway.UsageLimitsHandler{
		Service: ideps.usageLimits,
		Log:     log,
		Access: func(r *http.Request) (string, bool) {
			person, ok := identity.SessionFrom(r.Context())
			if !ok {
				return "", false
			}
			return person.UserID.String(), true
		},
	}.MountLimits(mux, func(next http.Handler) http.Handler {
		return identity.RequireSession(sessions, next)
	})

	agents.Handler{
		Agents:       ideps.agents,
		Teams:        agentTeamLookup{members: members},
		Log:          log,
		Routes:       ideps.routes,
		Accounts:     ideps.providers,
		DefaultRoute: cfg.ChatRoute,
		Access: func(r *http.Request) (agents.Access, bool) {
			person, ok := identity.SessionFrom(r.Context())
			if !ok {
				return agents.Access{}, false
			}
			access, ok, err := agentAccess(r.Context(), members, person.UserID)
			if err != nil || !ok {
				return agents.Access{}, false
			}
			return access, true
		},
	}.Mount(mux, func(next http.Handler) http.Handler {
		return identity.RequireSession(sessions, next)
	})

	notices.Handler{
		List:    ideps.notices,
		Dismiss: ideps.notices,
		User: func(r *http.Request) (uuid.UUID, bool) {
			person, ok := identity.SessionFrom(r.Context())
			if !ok {
				return uuid.Nil, false
			}
			return person.UserID.UUID, true
		},
	}.Mount(mux, func(next http.Handler) http.Handler {
		return identity.RequireSession(sessions, next)
	})

	(&audit.ReadHandler{Pool: pool}).Mount(mux, audit.Guard(auditCallers{sessions: sessions, tokens: tokens, members: members}))

	if o.provisioner != nil {
		o.provisioner(mux, deps)
	} else {
		provision.Handler{
			Owner:      deps.Owner,
			Identity:   ideps.provision,
			MasterKeys: ideps.keyring,
			Keys:       provisionKeys,
			Log:        log,
		}.Mount(mux)
	}
	for _, fn := range o.extraRoutes {
		fn(mux, deps)
	}

	// The audit middleware sits outside the cross-origin guard, so a request
	// Protect refuses is still recorded; the mux resolves the pattern then.
	h := audit.Middleware(audit.Routes(), mux, pool, "brain", cfg.TrustedProxies, log)(httpserver.Protect(mux))
	return httpserver.SecurityHeaders(metrics.MiddlewareWithMux("brain", reg, mux)(h)), nil
}

func taskSandboxes(cfg config.Config, pool *pgxpool.Pool, log *slog.Logger) (*runtime.TaskSandboxes, error) {
	boxes := &runtime.TaskSandboxes{Store: runtime.NewTaskSandboxStore(pool), Image: cfg.SandboxImage, Log: log}
	if cfg.SandboxdURL == "" {
		return boxes, nil
	}
	provider, err := runtime.Open(cfg.SandboxdURL, cfg.ServiceKey)
	if err != nil {
		return nil, fmt.Errorf("brain: sandboxd: %w", err)
	}
	boxes.Provider = provider
	return boxes, nil
}

type auditCallers struct {
	sessions identity.SessionService
	tokens   identity.TokenService
	members  identity.MemberService
}

func (c auditCallers) Token(ctx context.Context, secret string) (audit.Reader, error) {
	bearer, err := c.tokens.Authenticate(ctx, secret)
	if err != nil {
		return audit.Reader{}, err
	}
	return audit.Reader{
		UserID:         bearer.UserID.String(),
		RoleID:         bearer.RoleID,
		CredentialType: "token",
		CredentialID:   bearer.ID,
		Allow:          strings.Join(bearer.Scope, ","),
	}, nil
}

func (c auditCallers) Session(ctx context.Context, r *http.Request) (audit.Reader, bool, error) {
	person, ok, err := identity.Recheck(ctx, c.sessions, r)
	if err != nil || !ok {
		return audit.Reader{}, false, err
	}
	allow, err := c.members.EffectiveAllow(ctx, person.UserID)
	if err != nil {
		return audit.Reader{}, false, err
	}
	return audit.Reader{
		UserID:         person.UserID.String(),
		RoleID:         person.RoleID,
		CredentialType: "session",
		CredentialID:   person.SessionID,
		Allow:          allow,
	}, true, nil
}

func agentAccess(ctx context.Context, members identity.MemberService, user identity.UserID) (agents.Access, bool, error) {
	allow, err := members.EffectiveAllow(ctx, user)
	if err != nil {
		return agents.Access{}, false, err
	}
	shares, err := members.ShareAccess(ctx, user, identity.TypeAgent)
	if err != nil {
		return agents.Access{}, false, err
	}
	return agents.Access{
		UserID: user.String(),
		Allow:  allow,
		Teams:  shares.Teams,
	}, true, nil
}

// chatAgentLookup reads an agent for a chat reply. The agent row policy reads
// agent permissions and agent shares, which chat access does not carry, so the
// access is looked up again for agents.
type chatAgentLookup struct {
	store  agents.Store
	access func(ctx context.Context, userID string) (agents.Access, error)
}

func (l chatAgentLookup) Agent(ctx context.Context, access chat.Access, id string) (chat.Agent, error) {
	who, err := l.access(ctx, access.UserID)
	if err != nil {
		return chat.Agent{}, err
	}
	a, inline, err := l.store.AgentForUse(ctx, who, id)
	if errors.Is(err, agents.ErrAgentNotFound) {
		return chat.Agent{}, chat.ErrAgentNotFound
	}
	if err != nil {
		return chat.Agent{}, err
	}
	return chat.Agent{
		ID:            a.ID,
		Name:          a.Name,
		Description:   a.Description,
		PromptOverlay: agents.AppendInlineLessons(a.Instructions, inline),
		Route:         a.DefaultRoute,
		ModelProvider: a.DefaultModelProvider,
		ModelID:       a.DefaultModelID,
		Enabled:       a.Enabled,
	}, nil
}

type agentTeamLookup struct {
	members identity.MemberService
}

func (a agentTeamLookup) TeamNames(ctx context.Context, teamIDs []string) (map[string]string, error) {
	_, names, err := a.members.NamesOf(ctx, nil, teamIDs)
	return names, err
}

func chatAccess(ctx context.Context, members identity.MemberService, user identity.UserID) (chat.Access, bool, error) {
	allow, err := members.ChatAllow(ctx, user)
	if err != nil || allow == "" {
		return chat.Access{}, false, err
	}
	shares, err := members.ShareAccess(ctx, user, identity.TypeChat)
	if err != nil {
		return chat.Access{}, false, err
	}
	return chat.Access{
		UserID:     user.String(),
		Allow:      allow,
		Teams:      shares.Teams,
		SharedView: shares.SharedView,
		SharedEdit: shares.SharedEdit,
	}, true, nil
}

type scoutProposer struct {
	inner interface {
		Propose(ctx context.Context, prompt string) (string, error)
	}
}

func (p scoutProposer) Propose(ctx context.Context, prompt string) (string, error) {
	text, err := p.inner.Propose(ctx, prompt)
	if err != nil && errors.Is(err, memory.ErrRetry) {
		return "", fmt.Errorf("%w: %v", tasks.ErrRetry, err)
	}
	return text, err
}

type scoutChat struct {
	store   chat.Store
	members identity.MemberService
}

func (s scoutChat) RecentLines(ctx context.Context, userID uuid.UUID, chatID string, limit int) ([]string, bool, error) {
	person, err := identity.ParseUserID(userID.String())
	if err != nil {
		return nil, false, err
	}
	access, ok, err := chatAccess(ctx, s.members, person)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	lines, err := s.store.RecentLines(ctx, access, chatID, limit)
	return lines, err == nil, err
}

type scoutMemory struct {
	editor memory.Editor
}

func (s scoutMemory) ProfileAndFacts(ctx context.Context, userID uuid.UUID) (string, []tasks.MemoryFact, error) {
	page, err := memory.LoadPage(ctx, s.editor, userID.String())
	if err != nil {
		return "", nil, err
	}
	facts := make([]tasks.MemoryFact, 0, len(page.Facts))
	for _, row := range page.Facts {
		facts = append(facts, tasks.MemoryFact{ID: row.ID, Claim: row.Claim})
	}
	return page.Profile, facts, nil
}

type scoutKnowledge struct {
	folders knowledge.FolderLister
	finder  knowledge.Finder
	members identity.MemberService
}

func (s scoutKnowledge) Search(ctx context.Context, userID uuid.UUID, query string, limit int) ([]tasks.KnowledgeHit, error) {
	query = strings.TrimSpace(query)
	if query == "" || limit <= 0 {
		return nil, nil
	}
	person, err := identity.ParseUserID(userID.String())
	if err != nil {
		return nil, err
	}
	allow, err := s.members.FolderAllow(ctx, person)
	if err != nil || allow == "" {
		return nil, nil
	}
	shares, err := s.members.ShareAccess(ctx, person, identity.TypeFolder)
	if err != nil {
		return nil, err
	}
	access := knowledge.Access{
		UserID:     userID.String(),
		Allow:      allow,
		Teams:      shares.Teams,
		SharedView: shares.SharedView,
		SharedEdit: shares.SharedEdit,
	}
	folders, err := knowledge.ListFolders(ctx, s.folders, access)
	if err != nil {
		return nil, err
	}
	var out []tasks.KnowledgeHit
	seen := map[string]bool{}
	for _, folder := range folders {
		if len(out) >= limit {
			break
		}
		hits, err := knowledge.Search(ctx, s.finder, nil, nil, access, folder.ID, query)
		if err != nil {
			return nil, err
		}
		for _, hit := range hits {
			if len(out) >= limit {
				break
			}
			key := hit.SourceID + ":" + hit.Body
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, tasks.KnowledgeHit{SourceID: hit.SourceID, Body: hit.Body})
		}
	}
	return out, nil
}
