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
	if err := migrate.Up(ctx, pool, gateway.Schema, gateway.Migrations()); err != nil {
		return err
	}

	handler, err := routes(pool, cfg)
	if err != nil {
		return err
	}
	return httpserver.Run(ctx, httpserver.New(cfg.ListenAddr, handler), cfg.ShutdownTimeout, log)
}

func routes(pool *pgxpool.Pool, cfg config.Config) (http.Handler, error) {
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
	mux := http.NewServeMux()
	httpserver.Health(mux, pool.Ping)
	gateway.Mount(mux, pool, secretBackends())
	return svcsig.Handler(keys, svcsig.AudienceGateway, mux)
}

func secretBackends() gateway.Chain {
	chain := gateway.Chain{gateway.EnvSecrets{}}
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
