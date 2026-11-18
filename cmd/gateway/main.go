// Command gateway holds model provider credentials and routes every model
// call.
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
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/buildinfo"
	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/httpserver"
	"github.com/upmo-ai/upmo/internal/platform/logging"
	"github.com/upmo-ai/upmo/internal/platform/metrics"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/platform/secretchain"
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
	// Gateway seals the provider keys people add, so outside development it
	// also needs UPMO_MASTER_KEY, checked before any pool opens.
	if err := config.CheckConfig(cfg, true, os.LookupEnv, os.Environ()); err != nil {
		return err
	}
	log := logging.New(os.Stdout, cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := openPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	// The audit schema migrates first: identity's grants and the request
	// pools assume the audit role and tables already exist. Accounts shared
	// with a team then point at identity's teams. brain does the same before
	// it migrates gateway.
	if err := migrate.Up(ctx, pool, audit.Schema, audit.Migrations()); err != nil {
		return err
	}
	if err := migrate.Up(ctx, pool, identity.Schema, identity.Migrations()); err != nil {
		return err
	}
	if err := migrate.Up(ctx, pool, gateway.Schema, gateway.Migrations()); err != nil {
		return err
	}
	schemaVersions, err := migrate.SchemaVersions(ctx, pool)
	if err != nil {
		return err
	}

	info := buildinfo.Info()
	log.Info("gateway started", "version", info.Version, "commit", info.Commit, "build_time", info.BuildTime)

	var reg *metrics.Registry
	if cfg.MetricsAddr != "" {
		reg = metrics.NewRegistry()
	}
	handler, err := routes(pool, cfg, log, func(context.Context) (map[string]int64, error) {
		return maps.Clone(schemaVersions), nil
	}, reg)
	if err != nil {
		return err
	}
	srv := httpserver.New(cfg.ListenAddr, handler)
	if reg == nil {
		return httpserver.Run(ctx, srv, cfg.ShutdownTimeout, log)
	}
	srvCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errc := make(chan error, 2)
	for _, s := range []*http.Server{srv, metrics.Server(cfg.MetricsAddr, reg)} {
		go func() {
			errc <- httpserver.Run(srvCtx, s, cfg.ShutdownTimeout, log)
			cancel()
		}()
	}
	return errors.Join(<-errc, <-errc)
}

func routes(pool *pgxpool.Pool, cfg config.Config, log *slog.Logger, schemaVersions func(context.Context) (map[string]int64, error), reg *metrics.Registry) (http.Handler, error) {
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
	backends, err := secretBackends(pool, masterKeys)
	if err != nil {
		return nil, err
	}
	gateway.Mount(mux, pool, backends, log, gateway.WithMetrics(reg))
	signed, err := svcsig.Handler(keys, svcsig.AudienceGateway, mux)
	if err != nil {
		return nil, err
	}
	versionMux := http.NewServeMux()
	httpserver.Version(versionMux, schemaVersions)
	versionHandler := metrics.MiddlewareWithMux("gateway", reg, versionMux)(versionMux)
	signedHandler := metrics.MiddlewareWithMux("gateway", reg, mux)(signed)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := versionMux.Handler(r); pattern != "" {
			versionHandler.ServeHTTP(w, r)
			return
		}
		signedHandler.ServeHTTP(w, r)
	}), nil
}

// secretBackends is the order a key is looked for: the environment, then the
// keys people sealed into the database (when a master key is set), then Vault.
func secretBackends(pool *pgxpool.Pool, keys gateway.Keyring) (gateway.Chain, error) {
	fromEnv, err := secretchain.FromEnv(os.LookupEnv) // env, then Vault when set
	if err != nil {
		return nil, err
	}
	chain := gateway.Chain{fromEnv[0]}
	if keys.Ready() {
		chain = append(chain, gateway.DBSecrets{Pool: pool, Keys: keys})
	}
	return append(chain, fromEnv[1:]...), nil
}

// openPool opens the gateway pool without exposing UPMO_DATABASE_URL in parse
// or connect errors.
func openPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, poolURLError("UPMO_DATABASE_URL", databaseURL)
	}
	return pool, nil
}

// poolURLError reports a Postgres URL that could not be opened. The parse and
// connect errors name the URL, which holds the password and would reach the
// gateway stopped log, so only the host is kept from it.
func poolURLError(envName, rawURL string) error {
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		return fmt.Errorf("gateway: %s could not be opened (host %s)", envName, u.Host)
	}
	return fmt.Errorf("gateway: %s could not be opened", envName)
}
