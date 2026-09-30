// Command brain is the composition root: it hosts every bounded context in
// one process.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/artifacts"
	artifacthttp "github.com/upmo-ai/upmo/internal/artifacts/adapters/http"
	"github.com/upmo-ai/upmo/internal/chat"
	"github.com/upmo-ai/upmo/internal/decisions"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/knowledge"
	"github.com/upmo-ai/upmo/internal/memory"
	memoryhttp "github.com/upmo-ai/upmo/internal/memory/adapters/http"
	memorystore "github.com/upmo-ai/upmo/internal/memory/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/httpserver"
	"github.com/upmo-ai/upmo/internal/platform/logging"
	"github.com/upmo-ai/upmo/internal/platform/mail"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/runtime"
	"github.com/upmo-ai/upmo/internal/tasks"
	taskhttp "github.com/upmo-ai/upmo/internal/tasks/adapters/http"
	taskstore "github.com/upmo-ai/upmo/internal/tasks/adapters/postgres"
	taskapp "github.com/upmo-ai/upmo/internal/tasks/app"
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
	owner.Close()
	if err != nil {
		return err
	}
	pool, err := requestPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	seats, err := sandboxSeats(cfg, pool, log)
	if err != nil {
		return err
	}
	handler, err := routes(pool, cfg, log, seats)
	if err != nil {
		return err
	}
	err = httpserver.Run(ctx, httpserver.New(cfg.ListenAddr, handler), cfg.ShutdownTimeout, log)
	// Provisioning still running in the background gets the shutdown
	// timeout to settle its task before the pool closes. Any left after
	// that is abandoned: its task stays in Preparing and sandboxd reclaims
	// its sandbox at the seat TTL.
	wait, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if werr := seats.Wait(wait); werr != nil {
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

// sandboxSeats holds task sandboxes through sandboxd, signed with the
// service key, and records each seat in the runtime
// schema through pool, so a restarted brain can still release it. Each new
// sandbox starts from UPMO_SEAT_IMAGE, or the runtime default, and is
// provisioned through sandboxd in the background once Open has returned.
// With UPMO_SANDBOXD_URL unset it has no provider, and a task
// that needs a sandbox is refused rather than opened.
func sandboxSeats(cfg config.Config, pool *pgxpool.Pool, log *slog.Logger) (*runtime.Seats, error) {
	seats := &runtime.Seats{Book: runtime.SeatStore(pool), Image: cfg.SeatImage, Log: log}
	if cfg.SandboxdURL == "" {
		return seats, nil
	}
	provider, err := runtime.Open(cfg.SandboxdURL, cfg.ServiceKey)
	if err != nil {
		return nil, fmt.Errorf("brain: sandboxd: %w", err)
	}
	seats.Provider = provider
	return seats, nil
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

func routes(pool *pgxpool.Pool, cfg config.Config, log *slog.Logger, seats *runtime.Seats) (http.Handler, error) {
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
	taskBook := taskstore.Store{Pool: pool}
	taskhttp.Handler{
		Book:      taskBook,
		List:      taskBook,
		View:      taskBook,
		Sandboxes: seats,
		Progress:  seats,
		Access: func(r *http.Request) (taskapp.Access, bool) {
			person, ok := identity.SessionFrom(r.Context())
			if !ok {
				return taskapp.Access{}, false
			}
			allow, err := members.TaskAllow(r.Context(), person.UserID)
			if err != nil || allow == "" {
				return taskapp.Access{}, false
			}
			shares, err := members.ShareAccess(r.Context(), person.UserID, identity.TypeTask)
			if err != nil {
				return taskapp.Access{}, false
			}
			return taskapp.Access{
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
	memoryhttp.Handler{
		Editor: memorystore.Facts{Pool: pool},
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
	artifacthttp.Handler{
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
	return mux, nil
}
