// Command gateway holds model provider credentials and routes every model
// call.
package main

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/brainops"
	"github.com/upmo-ai/upmo/internal/platform/buildinfo"
	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/httpserver"
	"github.com/upmo-ai/upmo/internal/platform/logging"
	"github.com/upmo-ai/upmo/internal/platform/metrics"
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
	cfg, err := config.LoadGateway()
	if err != nil {
		return err
	}
	// Gateway seals the provider keys people add, so its startup guard requires
	// UPMO_MASTER_KEY in every environment before a pool opens.
	if err := config.CheckConfig(cfg, true, os.LookupEnv, os.Environ()); err != nil {
		return err
	}
	log := logging.New(os.Stdout, cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Gateway logs in as upmo_gateway: not a superuser, not BYPASSRLS, and
	// owner of no table. The worker command migrates; gateway only reads the
	// schema heads through a definer.
	pool, err := brainops.OpenServingPool(ctx, "UPMO_DATABASE_GATEWAY_URL", cfg.DatabaseGatewayURL, "", "upmo_gateway")
	if err != nil {
		return err
	}
	defer pool.Close()
	schemaVersions, err := gateway.SchemaVersions(ctx, pool)
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
	for _, s := range []*http.Server{srv, metrics.Server(cfg.MetricsAddr, reg, cfg.MetricsToken)} {
		go func() {
			errc <- httpserver.Run(srvCtx, s, cfg.ShutdownTimeout, log)
			cancel()
		}()
	}
	return errors.Join(<-errc, <-errc)
}

// maxBody caps every signed request body gateway reads and hashes.
const maxBody = 8 << 20

func routes(pool *pgxpool.Pool, cfg config.Config, log *slog.Logger, schemaVersions func(context.Context) (map[string]int64, error), reg *metrics.Registry) (http.Handler, error) {
	keys := make([][]byte, 0, 2)
	for _, secret := range []string{cfg.GatewayKey, cfg.GatewayKeyPrevious} {
		if secret == "" && len(keys) > 0 {
			continue
		}
		key, err := svcsig.ParseAudienceKey(secret, svcsig.AudienceGateway)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
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
	gateway.Mount(mux, pool, backends, log,
		gateway.WithMetrics(reg),
		gateway.WithTimeZone(func(ctx context.Context) (string, error) {
			return identity.WorkspaceTimeZone(ctx, pool)
		}),
		gateway.WithSettingsManagers(func(ctx context.Context) ([]string, error) {
			return identity.SettingsManagers(ctx, pool)
		}),
	)
	httpserver.Version(mux, schemaVersions)
	signed, err := svcsig.Handler(keys, svcsig.AudienceGateway, maxBody, mux)
	if err != nil {
		return nil, err
	}
	return metrics.MiddlewareWithMux("gateway", reg, mux)(signed), nil
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
