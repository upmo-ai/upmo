// Command brain is the composition root: it hosts every bounded context in
// one process.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

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
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/tasks"
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
	owner.Close()
	if err != nil {
		return err
	}
	pool, err := requestPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	handler, err := routes(pool, cfg, log)
	if err != nil {
		return err
	}
	return httpserver.Run(ctx, httpserver.New(cfg.ListenAddr, handler), cfg.ShutdownTimeout, log)
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

func routes(pool *pgxpool.Pool, cfg config.Config, log *slog.Logger) (http.Handler, error) {
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
	identity.MountMembers(mux, identity.Members(pool), sessions)
	identity.MountRoles(mux, identity.Roles(pool, log), sessions)
	identity.MountTeams(mux, identity.Teams(pool, log), sessions)
	identity.MountConnectorGrants(mux, identity.ConnectorGrants(pool), sessions)
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
	return mux, nil
}
