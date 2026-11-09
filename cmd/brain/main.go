// Command brain is the composition root: it hosts every bounded context in
// one process.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/artifacts"
	"github.com/upmo-ai/upmo/internal/chat"
	"github.com/upmo-ai/upmo/internal/decisions"
	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/knowledge"
	"github.com/upmo-ai/upmo/internal/memory"
	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/httpserver"
	"github.com/upmo-ai/upmo/internal/platform/jobs"
	"github.com/upmo-ai/upmo/internal/platform/logging"
	"github.com/upmo-ai/upmo/internal/platform/mail"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/platform/notices"
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
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := logging.New(os.Stdout, cfg.LogLevel)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	owner, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	err = migrate.Up(ctx, owner, identity.Schema, identity.Migrations())
	if err == nil {
		err = migrate.Up(ctx, owner, memory.Schema, memory.Migrations())
	}
	if err == nil {
		err = migrate.Up(ctx, owner, knowledge.Schema, knowledge.Migrations())
	}
	if err == nil {
		err = migrate.Up(ctx, owner, tasks.Schema, tasks.Migrations())
	}
	if err == nil {
		err = migrate.Up(ctx, owner, chat.Schema, chat.Migrations())
	}
	if err == nil {
		err = migrate.Up(ctx, owner, decisions.Schema, decisions.Migrations())
	}
	if err == nil {
		err = migrate.Up(ctx, owner, artifacts.Schema, artifacts.Migrations())
	}
	if err == nil {
		err = migrate.Up(ctx, owner, runtime.Schema, runtime.Migrations())
	}
	if err == nil {
		// brain serves the provider settings from the gateway tables, so it
		// brings that schema up to date too. gateway does the same at its start.
		err = migrate.Up(ctx, owner, gateway.Schema, gateway.Migrations())
	}
	if err == nil {
		err = jobs.Up(ctx, owner)
	}
	if err == nil {
		err = notices.Up(ctx, owner)
	}
	owner.Close()
	if err != nil {
		return err
	}
	pool, err := requestPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	workers := river.NewWorkers()
	noticeStore := notices.Store{Pool: pool}
	memoryFacts := memory.NewFacts(pool)
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
			Chat:   chat.NewStore(pool, nil),
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
	}
	// Outbox drain runs with Open. Product topic handlers
	// register here when they exist; this slice leaves the registry empty.
	work, err := jobs.Open(pool, jobs.WithWorkers(workers))
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
	// The stream hub listens on one connection of its own, made from the
	// same URL as the pool, and closes every open stream when the server
	// shuts down so a 30 minute stream does not hold the shutdown.
	listenConn, err := pgx.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return err
	}
	streams := tasks.NewStreamHub(pool, listenConn, log, tasks.StreamConfig{})
	streams.Start(ctx)
	defer streams.Close()
	handler, err := routes(pool, cfg, log, boxes, work, noticeStore, streams)
	if err != nil {
		return err
	}
	srv := httpserver.New(cfg.ListenAddr, handler)
	srv.RegisterOnShutdown(streams.Close)
	err = httpserver.Run(ctx, srv, cfg.ShutdownTimeout, log)
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

// requestPool opens connections that run as upmo_app, which owns nothing, and
// sets that role again on every checkout.
func requestPool(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
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
	return pgxpool.NewWithConfig(ctx, cfg)
}

func routes(pool *pgxpool.Pool, cfg config.Config, log *slog.Logger, boxes *runtime.TaskSandboxes, work *jobs.Client, noticeStore notices.Store, streams *tasks.StreamHub) (http.Handler, error) {
	mux := http.NewServeMux()
	httpserver.Health(mux, pool.Ping)
	sessions := identity.Sessions(pool, cfg.SessionIdle, cfg.SessionMaxAge)
	passwords := identity.Passwords(pool, sessions)
	identity.MountAPITokens(mux, sessions, identity.APITokens(pool, log), log, passwords.HasPassword, pool, cfg.TrustedProxies)
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
	taskBook := tasks.NewStore(pool, work)
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
		Stream:       streams,
		Access: func(r *http.Request) (tasks.Access, bool) {
			person, ok := identity.SessionFrom(r.Context())
			if !ok {
				return tasks.Access{}, false
			}
			access, ok, err := taskAccess(r.Context(), person.UserID)
			return access, ok && err == nil
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
			if err != nil || allow == "" {
				return artifacts.Access{}, false
			}
			shares, err := members.ShareAccess(r.Context(), person.UserID, identity.TypeArtifact)
			if err != nil {
				return artifacts.Access{}, false
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
	chat.Handler{
		Book: chat.NewStore(pool, work),
		Access: func(r *http.Request) (chat.Access, bool) {
			person, ok := identity.SessionFrom(r.Context())
			if !ok {
				return chat.Access{}, false
			}
			allow, err := members.ChatAllow(r.Context(), person.UserID)
			if err != nil || allow == "" {
				return chat.Access{}, false
			}
			shares, err := members.ShareAccess(r.Context(), person.UserID, identity.TypeChat)
			if err != nil {
				return chat.Access{}, false
			}
			return chat.Access{
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
			if err != nil || allow == "" {
				return knowledge.Access{}, false
			}
			shares, err := members.ShareAccess(r.Context(), person.UserID, identity.TypeFolder)
			if err != nil {
				return knowledge.Access{}, false
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
			if err != nil || allow == "" {
				return decisions.Access{}, false
			}
			return decisions.Access{UserID: person.UserID.String(), Allow: allow}, true
		},
	}.Mount(mux, func(next http.Handler) http.Handler {
		return identity.RequireSession(sessions, next)
	})
	gateway.RoutesHandler{
		Book: gateway.NewRouteBook(pool),
		Access: func(r *http.Request) (string, string, bool) {
			person, ok := identity.SessionFrom(r.Context())
			if !ok {
				return "", "", false
			}
			allow, err := members.SettingsAllow(r.Context(), person.UserID)
			if err != nil || allow == "" {
				return "", "", false
			}
			return person.UserID.String(), allow, true
		},
	}.MountRoutes(mux, func(next http.Handler) http.Handler {
		return identity.RequireSession(sessions, next)
	})
	masterKeys, err := gateway.KeyringFromEnv(os.LookupEnv, os.Environ())
	if err != nil {
		return nil, err
	}
	gateway.ProvidersHandler{
		Service: gateway.NewProviders(pool, masterKeys, log),
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
	return mux, nil
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
