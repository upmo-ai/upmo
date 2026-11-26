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
	"github.com/upmo-ai/upmo/internal/kernel"
	"github.com/upmo-ai/upmo/internal/knowledge"
	"github.com/upmo-ai/upmo/internal/memory"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/brainops"
	"github.com/upmo-ai/upmo/internal/platform/buildinfo"
	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/httpserver"
	"github.com/upmo-ai/upmo/internal/platform/jobs"
	"github.com/upmo-ai/upmo/internal/platform/keys"
	"github.com/upmo-ai/upmo/internal/platform/logging"
	"github.com/upmo-ai/upmo/internal/platform/metrics"
	"github.com/upmo-ai/upmo/internal/platform/notices"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/platform/trustfence"
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
	// Fresh reports whether p may make a sensitive change: signed in within
	// the step-up window and, when the account has a passkey, with it.
	Fresh(ctx context.Context, p Principal) bool
}

// MemberService exposes member permissions and team memberships.
type MemberService interface {
	EffectiveAllow(ctx context.Context, userID string) (string, error)
	TeamsOf(ctx context.Context, userID string) ([]string, error)
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

// Deps exposes composed services and configuration to extension options.
// Every field is set when the worker and periodic hooks run. Jobs is a
// client that enqueues once the job workers are open, so a hook may keep it
// but must not call InsertTx before brain.New returns. Brain holds no
// privileged pool: App logs in as upmo_app.
type Deps struct {
	App            *pgxpool.Pool
	Identity       IdentityService
	Members        MemberService
	Notices        NoticeService
	Jobs           JobClient
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

func (a identityAdapter) Fresh(ctx context.Context, p Principal) bool {
	user, err := identity.ParseUserID(p.UserID)
	if err != nil {
		return false
	}
	return a.id.StepUp(ctx, identity.Session{UserID: user, Method: p.Method, SignedIn: p.SignedIn})
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

type internalDeps struct {
	identity    identity.Identity
	members     identity.MemberService
	roles       identity.RoleService
	teams       identity.TeamService
	tokens      identity.TokenService
	workspace   identity.WorkspaceService
	runner      *pgxpool.Pool
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
	app           *pgxpool.Pool
	runner        *pgxpool.Pool
	logger        *slog.Logger
	extraRoutes   []func(mux *http.ServeMux, deps Deps)
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

// WithPools injects pre-opened app and runner pools (primarily for tests).
// They pass the same role check as the pools brain opens itself.
func WithPools(app, runner *pgxpool.Pool) Option {
	return func(o *options) {
		o.app = app
		o.runner = runner
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
	runner      *pgxpool.Pool
	ownsRunner  bool
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
// canceled, then gracefully drains background jobs and sandbox provisioning.
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
		for _, s := range []*http.Server{srv, metrics.Server(cfg.MetricsAddr, a.metricsReg, cfg.MetricsToken)} {
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
		if a.ownsRunner && a.runner != nil {
			a.runner.Close()
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
// serves until ctx is canceled, and closes resources cleanly.
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
	if cfg.ArtifactsDir != "" {
		if err := os.MkdirAll(cfg.ArtifactsDir, 0o700); err != nil {
			return nil, fmt.Errorf("brain: create artifacts directory: %w", err)
		}
	}
	if cfg.KnowledgeDir != "" {
		if err := os.MkdirAll(cfg.KnowledgeDir, 0o700); err != nil {
			return nil, fmt.Errorf("brain: create knowledge directory: %w", err)
		}
	}
	if cfg.AvatarsDir != "" {
		if err := os.MkdirAll(cfg.AvatarsDir, 0o700); err != nil {
			return nil, fmt.Errorf("brain: create avatars directory: %w", err)
		}
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

	// Brain opens only the app and runner roles. Neither is a superuser,
	// BYPASSRLS, or the owner of a table: row security binds both.
	pool := o.app
	ownsApp := false
	if pool == nil {
		pool, err = brainops.OpenServingPool(ctx, "UPMO_DATABASE_APP_URL", cfg.DatabaseAppURL, "upmo_app", "upmo_app")
		if err != nil {
			return nil, err
		}
		ownsApp = true
	} else if err := brainops.CheckServingRole(ctx, pool, "UPMO_DATABASE_APP_URL", "upmo_app"); err != nil {
		return nil, err
	}
	runner := o.runner
	ownsRunner := false
	if runner == nil {
		runner, err = brainops.OpenServingPool(ctx, "UPMO_DATABASE_RUNNER_URL", cfg.DatabaseRunnerURL, "", "upmo_runner")
		if err != nil {
			if ownsApp {
				pool.Close()
			}
			return nil, err
		}
		ownsRunner = true
	}
	cleanupPools := func() {
		if ownsApp {
			pool.Close()
		}
		if ownsRunner {
			runner.Close()
		}
	}
	if !ownsRunner {
		if err := brainops.CheckServingRole(ctx, runner, "UPMO_DATABASE_RUNNER_URL", "upmo_runner"); err != nil {
			cleanupPools()
			return nil, err
		}
	}

	schemaVersions, err := gateway.SchemaVersions(ctx, pool)
	if err != nil {
		cleanupPools()
		return nil, err
	}
	schemaLookup := func(context.Context) (map[string]int64, error) {
		return maps.Clone(schemaVersions), nil
	}

	masterKeys, err := gateway.KeyringFromEnv(os.LookupEnv, os.Environ())
	if err != nil {
		cleanupPools()
		return nil, err
	}

	internalCfg := toInternalConfig(cfg)
	sessions := identity.Sessions(pool, cfg.SessionIdle, cfg.SessionMaxAge)
	// Brain holds no master key for passwords or credentials: the worker
	// wraps every data key, checks every password and computes every email
	// index MAC. The master key it still holds only opens stored addresses.
	workerKeys, err := workerClient(ctx, cfg)
	if err != nil {
		cleanupPools()
		return nil, err
	}
	emails := brainops.WorkerEmails{Keys: workerKeys, Ring: masterKeys}
	passwords := identity.Passwords(pool, emails, sessions)
	passwords.Keys = brainops.WorkerPasswords{Keys: workerKeys}
	tokens := identity.APITokens(pool, log)
	passkeys, err := identity.Passkeys(pool, emails, sessions, cfg.PublicURL, cfg.WebAuthnRPID, log)
	if err != nil {
		cleanupPools()
		return nil, err
	}
	members := identity.WithAvatars(identity.Members(pool, emails, cfg.PublicURL, brainops.LinkMail(env, internalCfg.SMTP), log), cfg.AvatarsDir)
	roles := identity.Roles(pool, log)
	teams := identity.Teams(pool, log)
	workspace := identity.Workspace(pool)
	idFacade := identity.New(sessions, passkeys, passwords)

	// Brain seals a provider key under a data key the worker wraps; the
	// master key it still holds serves email only.
	providers := gateway.NewProviders(pool, gateway.WorkerSealer{Keys: workerKeys}, log)
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
		return brainops.MemberProvidersAllowed(ctx, pool, user)
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
		return identity.WorkspaceTimeZone(ctx, pool)
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
	providers.AgentUses = func(ctx context.Context, who gateway.ProviderCaller, provider string, models, routes []string) ([]string, error) {
		person, err := identity.ParseUserID(who.User)
		if err != nil {
			return nil, err
		}
		access, ok, err := agentAccess(ctx, members, person)
		if err != nil || !ok {
			return nil, err
		}
		return agentStore.ProviderUses(ctx, access, provider, models, routes)
	}
	// An automation runs the agent its template names. It depends on the
	// account when that agent does. The agents are found without the caller's
	// agent visibility, and only the automations the caller may see are named.
	providers.AutomationUses = func(ctx context.Context, who gateway.ProviderCaller, provider string, models, routes []string) ([]string, error) {
		person, err := identity.ParseUserID(who.User)
		if err != nil {
			return nil, err
		}
		access, ok, err := taskAccessFor(ctx, members, person)
		if err != nil {
			return nil, err
		}
		// The task allow list carries only task permissions. The automations
		// policy also lets a person who manages members read every automation,
		// so that permission is added here for this lookup.
		allow, err := members.EffectiveAllow(ctx, person)
		if err != nil {
			return nil, err
		}
		if slices.Contains(strings.Split(allow, ","), "members:manage") {
			access.UserID = person.String()
			access.Allow = strings.Trim(access.Allow+",members:manage", ",")
		} else if !ok {
			return []string{}, nil
		}
		agentIDs, err := agents.SystemProviderAgentIDs(ctx, pool, provider, models, routes)
		if err != nil {
			return nil, err
		}
		return tasks.NewStore(pool, runner, nil).AutomationUses(ctx, access, agentIDs)
	}
	noticeStore := notices.Store{Pool: pool}

	var reg *metrics.Registry
	if cfg.MetricsAddr != "" {
		reg = metrics.NewRegistry()
	}

	pubBuild, rawBuild := resolveBuildInfo()
	lazy := &lazyJobs{}
	deps := Deps{
		App:            pool,
		Identity:       identityAdapter{id: idFacade},
		Members:        memberAdapter{members: members},
		Notices:        noticeStore,
		Jobs:           lazy,
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
		runner:      runner,
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

	// The audit sealer, retention, verifier and pusher, the spend, usage,
	// provider and catalog jobs, and the provision nonce sweep run in the
	// worker command on the privileged pool, never here.
	workers := river.NewWorkers()
	memoryFacts := memory.NewFacts(pool)
	chat.AddSweepWorker(workers, chat.NewStore(pool, nil))
	if cfg.GatewayURL != "" && cfg.GatewayKey != "" {
		embedRoute := cfg.EmbedRoute
		if embedRoute == "" {
			embedRoute = "memory-embed"
		}
		gw, err := memory.OpenGateway(cfg.GatewayURL, cfg.GatewayKey, "memory-extract", embedRoute)
		if err != nil {
			cleanupPools()
			return nil, fmt.Errorf("brain: memory gateway: %w", err)
		}
		knowledgeBook := knowledge.NewStore(pool, nil)
		tasks.AddScoutWorker(workers, tasks.ScoutDeps{
			Goals:  tasks.NewStore(pool, runner, nil),
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
		chatTurns, err := chat.OpenTurns(cfg.GatewayURL, cfg.GatewayKey)
		if err != nil {
			cleanupPools()
			return nil, fmt.Errorf("brain: chat gateway: %w", err)
		}
		agentWho := func(ctx context.Context, userID string) (agents.Access, error) {
			person, err := identity.ParseUserID(userID)
			if err != nil {
				return agents.Access{}, err
			}
			who, _, err := agentAccess(ctx, members, person)
			return who, err
		}
		if err := chat.AddReplyWorker(workers, chat.ReplyDeps{
			Rows:      chat.NewStore(pool, lazy),
			Agents:    chatAgentLookup{store: agentStore, access: agentWho},
			Lessons:   chatLessons{store: agentStore, access: agentWho},
			Turns:     chatTurns,
			Memory:    chatMemoryLookup{facts: memoryFacts, embed: func(userID string) memory.Embedder { return gw.For(userID) }},
			Knowledge: chatKnowledgeLookup{folders: knowledgeBook, finder: knowledgeBook, embedFor: func(userID string) knowledge.Embedder { return gw.For(userID) }, members: members},
			Tools:     chatActionTools{tasks: tasks.NewStore(pool, runner, lazy), agents: agentStore, memory: memoryFacts, members: members},
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

	periodic := []*river.PeriodicJob{chat.SweepPeriodic()}
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
	lazy.set(work)
	if err := work.Start(ctx); err != nil {
		cleanupPools()
		return nil, err
	}
	deps.Jobs = work

	boxes, err := taskSandboxes(internalCfg, pool, log)
	if err != nil {
		_ = work.Stop(ctx)
		cleanupPools()
		return nil, err
	}

	// The stream LISTEN connections log in as upmo_app too.
	var listenConn *pgx.ConnConfig
	if cfg.DatabaseAppURL != "" {
		listenConn, err = brainops.ParseListenConfig("UPMO_DATABASE_APP_URL", cfg.DatabaseAppURL)
		if err != nil {
			_ = work.Stop(ctx)
			cleanupPools()
			return nil, err
		}
	} else {
		listenConn = pool.Config().ConnConfig.Copy()
	}

	streams := tasks.NewStreamHub(pool, listenConn, log, tasks.StreamConfig{})
	streams.Start(ctx)
	chatStreams := chat.NewStreamHub(pool, listenConn, log, chat.StreamConfig{})
	chatStreams.Start(ctx)

	handler, err := buildHandler(deps, ideps, boxes, work, streams, chatStreams, o)
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
		runner:      runner,
		ownsRunner:  ownsRunner,
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
	identity.MountAvatars(mux, members, sessions, pool)
	identity.MountRoles(mux, ideps.roles, sessions)
	identity.MountTeams(mux, ideps.teams, sessions)
	identity.MountConnectorGrants(mux, identity.ConnectorGrants(pool), passkeys)
	identity.MountWorkspace(mux, ideps.workspace, passkeys)

	taskBook := tasks.NewStore(pool, ideps.runner, work)
	taskBook.ReplaceTaskGrants = taskGrants{members: members}.ReplaceTaskGrantsTx
	var editorSigner tasks.EditorSigner
	if cfg.EditorKey != "" {
		editorTokens, err := tasks.NewEditorTokens(cfg.EditorKey, cfg.EditorKeyPrevious)
		if err != nil {
			return nil, err
		}
		editorSigner = editorTokens
	}
	stepUp := func(r *http.Request) bool {
		person, ok := identity.SessionFrom(r.Context())
		if !ok {
			return false
		}
		return ideps.identity.StepUp(r.Context(), identity.Session{UserID: person.UserID, Method: person.Method, SignedIn: person.SignedIn})
	}
	taskAccess := func(ctx context.Context, user identity.UserID) (tasks.Access, bool, error) {
		return taskAccessFor(ctx, members, user)
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
		StepUp:       stepUp,
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

	knowledgeConverter := knowledge.Converter{Text: knowledge.PassThroughText{}}
	if cfg.MarkitdownURL != "" && cfg.MarkitdownKey != "" {
		sidecar, err := knowledge.OpenSidecar(cfg.MarkitdownURL, cfg.MarkitdownKey)
		if err != nil {
			return nil, fmt.Errorf("brain: markitdown: %w", err)
		}
		knowledgeConverter.Sidecar = sidecar
	}

	chatBook := chat.NewStore(pool, work)
	chatBook.ReplaceChatGrants = chatGrants{members: members}.ReplaceChatGrantsTx
	if cfg.GatewayURL != "" && cfg.GatewayKey != "" {
		chatBook.ReplyRoute = cfg.ChatRoute
	}
	chat.Handler{
		Book:      chatBook,
		Shares:    chatBook,
		Grants:    chatGrants{members: members},
		Converter: knowledgeConverter,
		Stream:    chatStreams,
		Metrics:   reg,
		StepUp:    stepUp,
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
	var knowledgeEmbedFor func(userID string) knowledge.Embedder
	if cfg.GatewayURL != "" && cfg.GatewayKey != "" && cfg.EmbedRoute != "" {
		embedClient, err := knowledge.OpenEmbedder(cfg.GatewayURL, cfg.GatewayKey, cfg.EmbedRoute)
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
		StepUp:  stepUp,
		Access: func(r *http.Request) (string, bool) {
			person, ok := identity.SessionFrom(r.Context())
			if !ok {
				return "", false
			}
			return person.UserID.String(), true
		},
		ActorTokenHash: func(r *http.Request) []byte {
			person, _ := identity.SessionFrom(r.Context())
			return person.TokenHash
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

	usage := gateway.NewUsage(pool)
	usage.Allow = func(ctx context.Context, user string) (string, error) {
		person, err := identity.ParseUserID(user)
		if err != nil {
			return "", err
		}
		return members.EffectiveAllow(ctx, person)
	}
	usage.Teams = ideps.providers.Teams
	// The workspace zone for everyone until a person can choose their own.
	usage.TimeZone = func(ctx context.Context, _ string) (string, error) {
		return identity.WorkspaceTimeZone(ctx, pool)
	}
	usage.People = func(ctx context.Context, ids []string) (map[string]string, error) {
		names, _, err := members.NamesOf(ctx, ids, nil)
		return names, err
	}
	usage.TeamNames = func(ctx context.Context, ids []string) (map[string]string, error) {
		_, names, err := members.NamesOf(ctx, nil, ids)
		return names, err
	}
	usage.Agents = func(ctx context.Context, user string, _ []string) (map[string]string, error) {
		person, err := identity.ParseUserID(user)
		if err != nil {
			return nil, err
		}
		access, ok, err := agentAccess(ctx, members, person)
		if err != nil || !ok {
			return nil, err
		}
		list, err := agents.ListAgents(ctx, ideps.agents, access)
		if err != nil {
			return nil, err
		}
		names := make(map[string]string, len(list))
		for _, a := range list {
			names[a.ID] = a.Name
		}
		return names, nil
	}
	usage.Goals = func(ctx context.Context, user string, _ []string) (map[string]string, error) {
		person, err := identity.ParseUserID(user)
		if err != nil {
			return nil, err
		}
		access, ok, err := taskAccess(ctx, person)
		if err != nil || !ok {
			return nil, err
		}
		list, err := taskBook.List(ctx, access)
		if err != nil {
			return nil, err
		}
		goals := make(map[string]string, len(list))
		for _, t := range list {
			goals[t.ID] = t.Title
		}
		return goals, nil
	}
	gateway.UsageHandler{
		Service: usage,
		Log:     log,
		Access: func(r *http.Request) (string, bool) {
			person, ok := identity.SessionFrom(r.Context())
			if !ok {
				return "", false
			}
			return person.UserID.String(), true
		},
	}.MountUsage(mux, func(next http.Handler) http.Handler {
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

	// /api/v1/provision/* is served by the worker command, which holds the
	// privileged pool provisioning writes with.
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
	provider, err := runtime.Open(cfg.SandboxdURL, cfg.SandboxdKey)
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

func taskAccessFor(ctx context.Context, members identity.MemberService, user identity.UserID) (tasks.Access, bool, error) {
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
	cards, err := agents.Index(ctx, l.store, who, id)
	if errors.Is(err, agents.ErrAgentNotFound) {
		return chat.Agent{}, chat.ErrAgentNotFound
	}
	if err != nil {
		return chat.Agent{}, err
	}
	lessonCards := make([]chat.LessonCard, 0, len(cards))
	for _, c := range cards {
		lessonCards = append(lessonCards, chat.LessonCard{ID: c.ID, Title: c.Title, Cue: c.Cue})
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
		Cards:         lessonCards,
	}, nil
}

// chatLessons opens a house rule for a chat reply, through the agents facade.
type chatLessons struct {
	store  agents.Store
	access func(ctx context.Context, userID string) (agents.Access, error)
}

func (l chatLessons) OpenLesson(ctx context.Context, access chat.Access, agentID, lessonID string) (chat.Lesson, error) {
	who, err := l.access(ctx, access.UserID)
	if err != nil {
		return chat.Lesson{}, err
	}
	got, err := agents.Open(ctx, l.store, who, agentID, lessonID)
	if err != nil {
		return chat.Lesson{}, err
	}
	return chat.Lesson{ID: got.ID, Title: got.Title, Cue: got.Cue, Body: got.Body, AlwaysInline: got.AlwaysInline}, nil
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

type chatMemoryLookup struct {
	facts memory.RetrievalStore
	embed func(string) memory.Embedder
}

func (s chatMemoryLookup) SearchMemory(ctx context.Context, access chat.Access, query string) (string, bool, bool, error) {
	recall, err := memory.SearchMemory(ctx, s.facts, s.embed(access.UserID), nil, access.UserID, query, time.Now())
	return recall.Text, recall.Exposure.Untrusted, recall.Exposure.Private, err
}

type chatKnowledgeLookup struct {
	folders  knowledge.FolderLister
	finder   knowledge.Finder
	embedFor func(string) knowledge.Embedder
	members  identity.MemberService
}

func (s chatKnowledgeLookup) SearchKnowledge(ctx context.Context, chatAccess chat.Access, scope chat.AccessScope, folderID, query string) ([]chat.KnowledgeHit, error) {
	person, err := identity.ParseUserID(chatAccess.UserID)
	if err != nil {
		return nil, err
	}
	allow, err := s.members.FolderAllow(ctx, person)
	if err != nil {
		return nil, err
	}
	shares, err := s.members.ShareAccess(ctx, person, identity.TypeFolder)
	if err != nil {
		return nil, err
	}
	access := knowledge.Access{
		UserID:     chatAccess.UserID,
		Allow:      allow,
		Teams:      chat.NarrowTeamLiteral(scope, shares.Teams),
		SharedView: "{}",
		SharedEdit: "{}",
	}
	folders := []knowledge.Folder{}
	if folderID != "" {
		folders = append(folders, knowledge.Folder{ID: folderID})
	} else {
		folders, err = knowledge.ListFolders(ctx, s.folders, access)
		if err != nil {
			return nil, err
		}
	}
	out := make([]chat.KnowledgeHit, 0, 8)
	for _, folder := range folders {
		if len(out) >= 8 {
			break
		}
		hits, err := knowledge.Search(ctx, s.finder, s.embedFor(chatAccess.UserID), nil, access, folder.ID, query)
		if err != nil {
			return nil, err
		}
		for _, hit := range hits {
			if len(out) >= 8 {
				break
			}
			body := hit.Body
			if hit.Provenance.Untrusted() {
				body = trustfence.Wrap(trustfence.TagUntrust, "knowledge", body)
			}
			out = append(out, chat.KnowledgeHit{SourceID: hit.SourceID, Seq: hit.Seq, Body: body, Untrusted: hit.Provenance.Untrusted()})
		}
	}
	return out, nil
}

type chatActionTools struct {
	tasks   tasks.Store
	agents  agents.Store
	memory  memory.Editor
	members identity.MemberService
}

func (t chatActionTools) taskAccess(ctx context.Context, userID string, scope chat.AccessScope, needCreate bool) (tasks.Access, error) {
	person, err := identity.ParseUserID(userID)
	if err != nil {
		return tasks.Access{}, err
	}
	allow, err := t.members.TaskAllow(ctx, person)
	if err != nil {
		return tasks.Access{}, err
	}
	if allow == "" || (needCreate && !slices.Contains(strings.Split(allow, ","), "task:create")) {
		return tasks.Access{}, tasks.ErrAccess
	}
	shares, err := t.members.ShareAccess(ctx, person, identity.TypeTask)
	if err != nil {
		return tasks.Access{}, err
	}
	return tasks.Access{
		UserID:     userID,
		Allow:      allow,
		Teams:      chat.NarrowTeamLiteral(scope, shares.Teams),
		SharedView: "{}",
		SharedEdit: "{}",
	}, nil
}

func (t chatActionTools) ListTasks(ctx context.Context, current chat.Access, scope chat.AccessScope, limit int) ([]chat.TaskItem, error) {
	access, err := t.taskAccess(ctx, current.UserID, scope, false)
	if err != nil {
		return nil, err
	}
	rows, err := tasks.ListTasks(ctx, t.tasks, access)
	if err != nil {
		return nil, err
	}
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	out := make([]chat.TaskItem, 0, len(rows))
	for _, row := range rows {
		out = append(out, chat.TaskItem{
			ID:        row.ID,
			Title:     row.Title,
			Status:    string(row.Status),
			Phase:     string(row.Phase),
			UpdatedAt: row.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	return out, nil
}

func (t chatActionTools) OpenTask(ctx context.Context, current chat.Access, scope chat.AccessScope, in chat.NewTaskInput) (chat.TaskItem, error) {
	access, err := t.taskAccess(ctx, current.UserID, scope, true)
	if err != nil {
		return chat.TaskItem{}, err
	}
	created, err := tasks.OpenTask(ctx, t.tasks, nil, access, tasks.Opening{Goal: in.Goal})
	if err != nil {
		return chat.TaskItem{}, err
	}
	return chat.TaskItem{
		ID:     created.ID,
		Title:  created.Goal,
		Status: string(created.Status),
		Phase:  string(created.Status),
	}, nil
}

func (t chatActionTools) SaveMemory(ctx context.Context, userID, claim string) (string, error) {
	return memory.AddFact(ctx, t.memory, userID, claim, time.Now().UTC())
}

func (t chatActionTools) ListAgents(ctx context.Context, current chat.Access, scope chat.AccessScope) ([]chat.AgentItem, error) {
	person, err := identity.ParseUserID(current.UserID)
	if err != nil {
		return nil, err
	}
	allow, err := t.members.EffectiveAllow(ctx, person)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(strings.Split(allow, ","), "agent:view") {
		return nil, agents.ErrAccess
	}
	shares, err := t.members.ShareAccess(ctx, person, identity.TypeAgent)
	if err != nil {
		return nil, err
	}
	// Strip settings:manage so team-scoped chats never bypass narrow team RLS via may_manage.
	access := agents.Access{
		UserID: current.UserID,
		Allow:  "agent:view",
		Teams:  chat.NarrowTeamLiteral(scope, shares.Teams),
	}
	rows, err := agents.ListAgents(ctx, t.agents, access)
	if err != nil {
		return nil, err
	}
	out := make([]chat.AgentItem, 0, len(rows))
	for _, a := range rows {
		if !a.Enabled || !a.Share.CanUse {
			continue
		}
		out = append(out, chat.AgentItem{
			ID:          a.ID,
			Name:        a.Name,
			Description: a.Description,
		})
	}
	return out, nil
}

func (t chatActionTools) CreateAutomation(ctx context.Context, current chat.Access, scope chat.AccessScope, in chat.AutomationInput) (string, string, error) {
	access, err := t.taskAccess(ctx, current.UserID, scope, true)
	if err != nil {
		return "", "", err
	}
	templateID, err := tasks.SaveTemplate(ctx, t.tasks, access, "", tasks.Template{
		Goal:    in.Goal,
		AgentID: in.AgentID,
		Budget:  int64(in.BudgetCents) * kernel.MicroUSDPerCent,
	})
	if err != nil {
		return "", "", err
	}
	automationID, err := tasks.SaveAutomation(ctx, t.tasks, access, templateID, in.Kind, in.Spec)
	if err != nil {
		return "", "", err
	}
	return templateID, automationID, nil
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

// workerClient builds the key service client and checks, signed, that the
// worker is ready. Brain seals every password and saved provider key, and
// checks every password, through it, so it is required.
func workerClient(ctx context.Context, cfg Config) (*keys.Client, error) {
	if cfg.WorkerURL == "" {
		return nil, errors.New("config: UPMO_WORKER_URL is required")
	}
	client, err := keys.Open(keys.Options{
		URL:         cfg.WorkerURL,
		Key:         cfg.WorkerBrainKey,
		Audience:    svcsig.AudienceWorkerBrain,
		Development: cfg.Env == string(config.EnvDevelopment),
		CAFile:      os.Getenv("UPMO_TLS_CA_FILE"),
	})
	if err != nil {
		return nil, err
	}
	if err := client.Ready(ctx); err != nil {
		return nil, errors.New("config: worker is not ready")
	}
	return &client, nil
}
