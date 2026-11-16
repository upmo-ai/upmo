// Command brain is the composition root: it hosts every bounded context in
// one process.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/agents"
	"github.com/upmo-ai/upmo/internal/artifacts"
	"github.com/upmo-ai/upmo/internal/chat"
	"github.com/upmo-ai/upmo/internal/decisions"
	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/knowledge"
	"github.com/upmo-ai/upmo/internal/memory"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/buildinfo"
	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/httpserver"
	"github.com/upmo-ai/upmo/internal/platform/jobs"
	"github.com/upmo-ai/upmo/internal/platform/logging"
	"github.com/upmo-ai/upmo/internal/platform/mail"
	"github.com/upmo-ai/upmo/internal/platform/metrics"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/platform/notices"
	"github.com/upmo-ai/upmo/internal/platform/secretchain"
	"github.com/upmo-ai/upmo/internal/runtime"
	"github.com/upmo-ai/upmo/internal/tasks"

	"github.com/riverqueue/river"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] != "" && os.Args[1][0] != '-' {
		if err := command(os.Args[1], os.Args[2:]); err != nil {
			slog.Error("brain command failed", "err", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("brain stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadBrain()
	if err != nil {
		return err
	}
	log := logging.New(os.Stdout, cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	owner, err := openOwnerPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	if err := migrateAll(ctx, owner); err != nil {
		owner.Close()
		return err
	}
	defer owner.Close()
	schemaVersions, err := migrate.SchemaVersions(ctx, owner)
	if err != nil {
		return err
	}
	if err := enableAppLogin(ctx, owner, cfg.DatabaseAppURL); err != nil {
		return err
	}
	pool, err := openAppPool(ctx, owner, cfg.DatabaseAppURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	workers := river.NewWorkers()
	// The sealer runs on the owner pool: pending and sealed events are the
	// app role's to insert and read, never to move. The owner pool stays
	// open until work.Stop has drained the workers at shutdown.
	audit.AddSealer(workers, owner, os.Stdout)
	audit.AddRetention(workers, owner, cfg.AuditRetentionDays)
	audit.AddVerifier(workers, owner)
	// The pusher runs only when a target is configured: with both the webhook
	// and the syslog collector unset no worker and no periodic job is
	// registered at all.
	auditPush := cfg.AuditWebhookURL != "" || cfg.AuditSyslogAddr != ""
	if auditPush {
		audit.AddPusher(workers, owner, audit.PushConfig{
			WebhookURL: cfg.AuditWebhookURL,
			SyslogAddr: cfg.AuditSyslogAddr,
			Secrets:    secretchain.FromEnv(os.LookupEnv),
		})
	}
	noticeStore := notices.Store{Pool: pool}
	memoryFacts := memory.NewFacts(pool)
	chat.AddSweepWorker(workers, chat.NewStore(pool, nil))
	if cfg.GatewayURL != "" && cfg.ServiceKey != "" {
		embedRoute := cfg.EmbedRoute
		if embedRoute == "" {
			embedRoute = "memory-embed"
		}
		gw, err := memory.OpenGateway(cfg.GatewayURL, cfg.ServiceKey, "memory-extract", embedRoute)
		if err != nil {
			return fmt.Errorf("brain: memory gateway: %w", err)
		}
		knowledgeBook := knowledge.NewStore(pool, nil)
		members := identity.Members(pool, cfg.PublicURL, linkMail(cfg.SMTP), log)
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
				return scoutProposer{inner: gw.For(userID.String())}
			},
			Notices: noticeStore,
		})
		memory.AddExtractWorker(workers, memoryFacts,
			func(userID string) memory.Proposer { return gw.For(userID) },
			func(userID string) memory.Embedder { return gw.For(userID) },
		)
		chatModel, err := chat.OpenModel(cfg.GatewayURL, cfg.ServiceKey)
		if err != nil {
			return fmt.Errorf("brain: chat gateway: %w", err)
		}
		// A reply runs as the person who wrote the line, with the access a
		// request of theirs would have, so row security applies to it.
		chat.AddReplyWorker(workers, chat.ReplyDeps{
			Rows:   chat.NewStore(pool, nil),
			Agents: chatAgentLookup{store: agents.NewStore(pool)},
			Model:  chatModel,
			Access: func(ctx context.Context, userID string) (chat.Access, bool, error) {
				person, err := identity.ParseUserID(userID)
				if err != nil {
					return chat.Access{}, false, err
				}
				return chatAccess(ctx, members, person)
			},
			Log: log,
		})
	}
	// Outbox drain runs with Open. Product topic handlers
	// register here when they exist; this slice leaves the registry empty.
	periodic := []*river.PeriodicJob{
		chat.SweepPeriodic(), audit.SealPeriodic(),
		audit.RetentionPeriodic(), audit.VerifyPeriodic(),
	}
	if auditPush {
		periodic = append(periodic, audit.PushPeriodic())
	}
	var reg *metrics.Registry
	if cfg.MetricsAddr != "" {
		reg = metrics.NewRegistry()
	}
	work, err := jobs.Open(pool,
		jobs.WithWorkers(workers),
		jobs.WithQueue(chat.ReplyQueue, cfg.ChatWorkers),
		jobs.WithPeriodic(periodic...),
		jobs.WithMetrics(reg),
	)
	if err != nil {
		return err
	}
	if err := work.Start(ctx); err != nil {
		return err
	}

	boxes, err := taskSandboxes(cfg, pool, log)
	if err != nil {
		return err
	}
	// The stream hub listens on one connection of its own, on the owner
	// credential only, and closes every open stream when the server
	// shuts down so a 30 minute stream does not hold the shutdown.
	listenConn, err := parseListenConfig(cfg.DatabaseURL)
	if err != nil {
		return err
	}
	streams := tasks.NewStreamHub(pool, listenConn, log, tasks.StreamConfig{})
	streams.Start(ctx)
	defer streams.Close()
	chatStreams := chat.NewStreamHub(pool, listenConn, log, chat.StreamConfig{})
	chatStreams.Start(ctx)
	defer chatStreams.Close()
	handler, err := routes(pool, cfg, log, boxes, work, noticeStore, streams, chatStreams, func(context.Context) (map[string]int64, error) {
		return maps.Clone(schemaVersions), nil
	}, reg)
	if err != nil {
		return err
	}
	info := buildinfo.Info()
	log.Info("brain started", "version", info.Version, "commit", info.Commit, "build_time", info.BuildTime)
	srv := httpserver.NewGuarded(cfg.ListenAddr, handler)
	srv.RegisterOnShutdown(streams.Close)
	srv.RegisterOnShutdown(chatStreams.Close)
	if reg != nil {
		srvCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		errc := make(chan error, 2)
		for _, s := range []*http.Server{srv, metrics.Server(cfg.MetricsAddr, reg)} {
			go func() {
				errc <- httpserver.Run(srvCtx, s, cfg.ShutdownTimeout, log)
				cancel()
			}()
		}
		err = errors.Join(<-errc, <-errc)
	} else {
		err = httpserver.Run(ctx, srv, cfg.ShutdownTimeout, log)
	}
	// Provisioning still running in the background gets the shutdown
	// timeout to settle its task before the pool closes. Any left after
	// that is abandoned: its task stays in Preparing and sandboxd reclaims
	// its sandbox at the sandbox TTL.
	wait, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if werr := work.Stop(wait); werr != nil {
		log.Warn("job workers still running at shutdown", "err", werr)
	}
	if werr := boxes.Wait(wait); werr != nil {
		log.Warn("sandbox provisioning still running at shutdown", "err", werr)
	}
	return err
}

// auditCallers resolves the events endpoint's two credentials through the
// identity services: a bearer secret runs through the token service and
// narrows the caller to the token's scope, anything else runs through the
// session lookup and the person's effective permissions.
type auditCallers struct {
	sessions identity.SessionService
	tokens   identity.TokenService
	members  identity.MemberService
}

// Token resolves one bearer secret to its token's reader.
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

// Session resolves the request's session to the signed-in person's reader.
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

// linkMail is the sender for invite and recover links. With SMTP unset it
// sends nothing and the links are only copied.
func linkMail(s config.SMTP) mail.Sender {
	if s.Host == "" {
		return mail.Noop{}
	}
	return mail.SMTP{Host: s.Host, Port: s.Port, User: s.User, Password: s.Password, From: s.From}
}

// taskSandboxes holds task sandboxes through sandboxd, signed with the
// service key, and records each task sandbox in the runtime
// schema through pool, so a restarted brain can still release it. Each new
// sandbox starts from UPMO_SANDBOX_IMAGE, or the runtime default, and is
// provisioned through sandboxd in the background once Open has returned.
// With UPMO_SANDBOXD_URL unset it has no provider, and a task
// that needs a sandbox is refused rather than opened.
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

// openOwnerPool opens the owner pool with MaxConns=2 and scrubs the URL
// from parse or connect errors.
func openOwnerPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	ownerConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, poolURLError("UPMO_DATABASE_URL", databaseURL)
	}
	ownerConfig.MaxConns = 2
	owner, err := pgxpool.NewWithConfig(ctx, ownerConfig)
	if err != nil {
		return nil, poolURLError("UPMO_DATABASE_URL", databaseURL)
	}
	return owner, nil
}

// parseListenConfig parses the owner database URL for stream LISTEN
// connections and scrubs the URL from parse errors.
func parseListenConfig(databaseURL string) (*pgx.ConnConfig, error) {
	listenConn, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return nil, poolURLError("UPMO_DATABASE_URL", databaseURL)
	}
	return listenConn, nil
}

// requestPool opens connections that log in as upmo_app, which owns nothing,
// and sets that role again on every checkout.
func requestPool(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, appPoolError(url)
	}
	setRole := func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET ROLE upmo_app")
		return err
	}
	cfg.AfterConnect = setRole
	cfg.PrepareConn = func(ctx context.Context, conn *pgx.Conn) (bool, error) {
		err := setRole(ctx, conn)
		return err == nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, appPoolError(url)
	}
	return pool, nil
}

// appPoolError reports a request pool that did not open.
func appPoolError(appURL string) error {
	return poolURLError("UPMO_DATABASE_APP_URL", appURL)
}

// poolURLError reports a Postgres URL that could not be opened or parsed. The
// parse and connect errors name the URL, which holds the password and would
// reach the stopped log, so only the host is kept from it.
func poolURLError(envName, rawURL string) error {
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		return fmt.Errorf("brain: %s could not be opened (host %s)", envName, u.Host)
	}
	return fmt.Errorf("brain: %s could not be opened", envName)
}

func routes(pool *pgxpool.Pool, cfg config.Config, log *slog.Logger, boxes *runtime.TaskSandboxes, work *jobs.Client, noticeStore notices.Store, streams *tasks.StreamHub, chatStreams *chat.StreamHub, schemaVersions func(context.Context) (map[string]int64, error), reg *metrics.Registry) (http.Handler, error) {
	mux := http.NewServeMux()
	httpserver.Health(mux, pool.Ping)
	httpserver.Version(mux, schemaVersions)
	sessions := identity.Sessions(pool, cfg.SessionIdle, cfg.SessionMaxAge)
	masterKeys, err := gateway.KeyringFromEnv(os.LookupEnv, os.Environ())
	if err != nil {
		return nil, err
	}
	passwords := identity.Passwords(pool, sessions)
	passwords.Sealer = passwordSealer{keys: masterKeys}
	tokens := identity.APITokens(pool, log)
	identity.MountAPITokens(mux, sessions, tokens, log, passwords.HasPassword, pool, cfg.TrustedProxies)
	passkeys, err := identity.Passkeys(pool, sessions, cfg.PublicURL, cfg.WebAuthnRPID, log)
	if err != nil {
		return nil, err
	}
	identity.MountPasskeys(mux, passkeys, passwords, cfg.TrustedProxies)
	members := identity.Members(pool, cfg.PublicURL, linkMail(cfg.SMTP), log)
	identity.MountMembers(mux, members, sessions)
	identity.MountRoles(mux, identity.Roles(pool, log), sessions)
	identity.MountTeams(mux, identity.Teams(pool, log), sessions)
	identity.MountConnectorGrants(mux, identity.ConnectorGrants(pool), sessions)
	identity.MountWorkspace(mux, identity.Workspace(pool), sessions)
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
	// taskAccess is what a person may do with tasks. It reports false for a
	// person with no task permission and an error when it could not be read.
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
				// A failed lookup refuses the request but keeps the session.
				return tasks.Access{UserID: person.UserID.String()}, true
			}
			return access, true
		},
		// An open stream looks the session up again, then the person's allow
		// list and shares, so a sign-out or a revoked share ends it.
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
			if err != nil {
				// A failed lookup refuses the request but keeps the session.
				return artifacts.Access{UserID: person.UserID.String()}, true
			}
			if allow == "" {
				return artifacts.Access{UserID: person.UserID.String()}, true
			}
			shares, err := members.ShareAccess(r.Context(), person.UserID, identity.TypeArtifact)
			if err != nil {
				// A failed lookup refuses the request but keeps the session.
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
				// A failed lookup refuses the request but keeps the session.
				return chat.Access{UserID: person.UserID.String()}, true
			}
			return access, true
		},
		// An open stream looks the session up again, then the person's allow
		// list and shares, so a sign-out or a revoked share ends it.
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
	// The agents handler is mounted beside the gateway handlers below: its
	// usable answer reads the same routes and provider accounts.
	knowledgeBook := knowledge.NewStore(pool, work)
	knowledgeConverter := knowledge.Converter{Text: knowledge.PassThroughText{}}
	if markitdownURL := os.Getenv("UPMO_MARKITDOWN_URL"); markitdownURL != "" {
		knowledgeConverter.Sidecar = &knowledge.Sidecar{BaseURL: markitdownURL}
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
			if err != nil {
				// A failed lookup refuses the request but keeps the session.
				return knowledge.Access{UserID: person.UserID.String()}, true
			}
			if allow == "" {
				return knowledge.Access{UserID: person.UserID.String()}, true
			}
			shares, err := members.ShareAccess(r.Context(), person.UserID, identity.TypeFolder)
			if err != nil {
				// A failed lookup refuses the request but keeps the session.
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
				// A failed lookup refuses the request but keeps the session.
				return decisions.Access{UserID: person.UserID.String()}, true
			}
			return decisions.Access{UserID: person.UserID.String(), Allow: allow}, true
		},
	}.Mount(mux, func(next http.Handler) http.Handler {
		return identity.RequireSession(sessions, next)
	})
	// One keyring for the whole process: it seals the stored password hashes
	// and the provider keys people add.
	// A local-plan account reaches private addresses on the workspace's own
	// network, so it stays with the people who manage the settings; every
	// other plan stays open to each person's own accounts.
	providers := gateway.NewProviders(pool, masterKeys, log)
	providers.LocalPlan = func(ctx context.Context, user string) bool {
		id, err := identity.ParseUserID(user)
		if err != nil {
			return false
		}
		allow, err := members.SettingsAllow(ctx, id)
		return err == nil && allow != ""
	}
	// Sharing an account is settings:manage. Teams are read for this request;
	// the next request reads them again.
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
	providers.Names = func(ctx context.Context, teams []string) (map[string]string, error) {
		_, names, err := members.NamesOf(ctx, nil, teams)
		return names, err
	}
	gateway.ProvidersHandler{
		Service: providers,
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
	routeBook := gateway.NewRouteBook(pool)
	gateway.RoutesHandler{
		Book:     routeBook,
		Accounts: providers,
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
	// The pickers only offer what a person can use, so each agent carries its
	// usable answer: the install's chat route decides for one that names no
	// route of its own.
	agents.Handler{
		Agents:       agents.NewStore(pool),
		Teams:        agentTeamLookup{members: members},
		Log:          log,
		Routes:       routeBook,
		Accounts:     providers,
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
		List:    noticeStore,
		Dismiss: noticeStore,
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
	// The events endpoint answers a session cookie or a bearer token: the
	// token narrows the person to the token's scope, the session to their
	// effective permissions, and only audit:view reads. The guard resolves
	// the caller for the handler and names them on the audit scope, so the
	// middleware's refusals say who was refused.
	(&audit.ReadHandler{Pool: pool}).Mount(mux, audit.Guard(auditCallers{sessions: sessions, tokens: tokens, members: members}))
	// The audit middleware sits outside the cross-origin guard, so a request
	// Protect refuses is still recorded; the mux resolves the pattern then.
	h := audit.Middleware(audit.Routes(), mux, pool, "brain", cfg.TrustedProxies, log)(httpserver.Protect(mux))
	return metrics.MiddlewareWithMux("brain", reg, mux)(h), nil
}

// agentAccess is what a person may do with agents. It carries their effective
// permissions and teams, like chatAccess, but needs no chat permission.
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

type chatAgentLookup struct {
	store agents.Store
}

func (l chatAgentLookup) Agent(ctx context.Context, access chat.Access, id string) (chat.Agent, error) {
	a, inline, err := l.store.AgentForUse(ctx, agents.Access{
		UserID: access.UserID,
		Allow:  access.Allow,
		Teams:  access.Teams,
	}, id)
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

// chatAccess is what a person may do with chats. It reports false for a
// person with no chat permission and an error when it could not be read.
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

// scoutProposer maps memory gateway retries onto the tasks scout retry sentinel.
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

// scoutChat reads recent lines with the person's own chat access, so a chat
// shared with them is visible to the job the way it is to their request.
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
