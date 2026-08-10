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

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/httpserver"
	"github.com/upmo-ai/upmo/internal/platform/logging"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
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

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := migrate.Up(ctx, pool, identity.Schema, identity.Migrations()); err != nil {
		return err
	}

	handler, err := routes(pool, cfg, log)
	if err != nil {
		return err
	}
	return httpserver.Run(ctx, httpserver.New(cfg.ListenAddr, handler), cfg.ShutdownTimeout, log)
}

func routes(pool *pgxpool.Pool, cfg config.Config, log *slog.Logger) (http.Handler, error) {
	mux := http.NewServeMux()
	httpserver.Health(mux, pool.Ping)
	sessions := identity.Sessions(pool, cfg.SessionIdle, cfg.SessionMaxAge)
	passwords := identity.Passwords(pool, sessions)
	identity.MountSessions(mux, sessions, log, passwords.HasPassword)
	passkeys, err := identity.Passkeys(pool, sessions, cfg.PublicURL, cfg.WebAuthnRPID, log)
	if err != nil {
		return nil, err
	}
	identity.MountPasskeys(mux, passkeys, passwords, cfg.TrustedProxies)
	identity.MountMembers(mux, identity.Members(pool), sessions)
	identity.MountRoles(mux, identity.Roles(pool, log), sessions)
	identity.MountTeams(mux, identity.Teams(pool, log), sessions)
	return mux, nil
}
