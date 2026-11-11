// Command gateway holds model provider credentials and routes every model
// call.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/httpserver"
	"github.com/upmo-ai/upmo/internal/platform/logging"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
)

func main() {
	if err := run(); err != nil {
		slog.Error("gateway stopped", "err", err)
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
	// Accounts shared with a team point at identity's teams, so that schema
	// is brought up first. brain does the same before it migrates gateway.
	if err := migrate.Up(ctx, pool, identity.Schema, identity.Migrations()); err != nil {
		return err
	}
	if err := migrate.Up(ctx, pool, gateway.Schema, gateway.Migrations()); err != nil {
		return err
	}

	handler, err := routes(pool, cfg, log)
	if err != nil {
		return err
	}
	return httpserver.Run(ctx, httpserver.New(cfg.ListenAddr, handler), cfg.ShutdownTimeout, log)
}

func routes(pool *pgxpool.Pool, cfg config.Config, log *slog.Logger) (http.Handler, error) {
	master, err := svcsig.ParseKey(cfg.ServiceKey)
	if err != nil {
		return nil, err
	}
	derived, err := svcsig.Derive(master, svcsig.AudienceGateway)
	if err != nil {
		return nil, err
	}
	keys := [][]byte{derived}
	if cfg.ServiceKeyPrevious != "" {
		prev, err := svcsig.ParseKey(cfg.ServiceKeyPrevious)
		if err != nil {
			return nil, err
		}
		d, err := svcsig.Derive(prev, svcsig.AudienceGateway)
		if err != nil {
			return nil, err
		}
		keys = append(keys, d)
	}
	masterKeys, err := gateway.KeyringFromEnv(os.LookupEnv, os.Environ())
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	httpserver.Health(mux, pool.Ping)
	gateway.Mount(mux, pool, secretBackends(pool, masterKeys), log)
	return svcsig.Handler(keys, svcsig.AudienceGateway, mux)
}

// secretBackends is the order a key is looked for: the environment, then the
// keys people sealed into the database (when a master key is set), then Vault.
func secretBackends(pool *pgxpool.Pool, keys gateway.Keyring) gateway.Chain {
	chain := gateway.Chain{gateway.EnvSecrets{}}
	if keys.Ready() {
		chain = append(chain, gateway.DBSecrets{Pool: pool, Keys: keys})
	}
	if addr, token := os.Getenv("UPMO_VAULT_ADDR"), os.Getenv("UPMO_VAULT_TOKEN"); addr != "" && token != "" {
		chain = append(chain, gateway.VaultSecrets{
			Addr:  addr,
			Token: token,
			Path:  os.Getenv("UPMO_VAULT_PATH"),
			Fetch: gateway.VaultHTTP,
		})
	}
	return chain
}
