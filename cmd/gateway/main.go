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
	"github.com/upmo-ai/upmo/internal/platform/keys"
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
	// LoadGateway refuses a master key: gateway opens a saved provider key
	// with the data key the worker unwraps for it.
	cfg, err := config.LoadGateway()
	if err != nil {
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
	workerKeys, err := openWorkerKeys(ctx, cfg)
	if err != nil {
		return err
	}
	handler, err := routes(pool, cfg, workerKeys, log, func(context.Context) (map[string]int64, error) {
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

// openWorkerKeys builds the key service client and checks, signed, that
// the worker is ready. Gateway opens every saved provider key through it,
// so UPMO_WORKER_URL is required.
func openWorkerKeys(ctx context.Context, cfg config.Config) (*keys.Client, error) {
	if cfg.WorkerURL == "" {
		return nil, errors.New("config: UPMO_WORKER_URL is required")
	}
	client, err := keys.Open(keys.Options{
		URL:         cfg.WorkerURL,
		Key:         cfg.WorkerGatewayKey,
		Audience:    svcsig.AudienceWorkerGateway,
		Development: cfg.Env == config.EnvDevelopment,
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

func routes(pool *pgxpool.Pool, cfg config.Config, workerKeys *keys.Client, log *slog.Logger, schemaVersions func(context.Context) (map[string]int64, error), reg *metrics.Registry) (http.Handler, error) {
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
	mux := http.NewServeMux()
	httpserver.Health(mux, pool.Ping)
	backends, err := secretBackends(pool, workerKeys)
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
// keys people sealed into the database (when the worker's key service is
// set up), then Vault.
func secretBackends(pool *pgxpool.Pool, workerKeys *keys.Client) (gateway.Chain, error) {
	fromEnv, err := secretchain.FromEnv(os.LookupEnv) // env, then Vault when set
	if err != nil {
		return nil, err
	}
	chain := gateway.Chain{fromEnv[0]}
	if workerKeys != nil {
		chain = append(chain, gateway.DBSecrets{Pool: pool, Unwrap: workerKeys})
	}
	return append(chain, fromEnv[1:]...), nil
}
