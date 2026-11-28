// Command sandboxd serves the SandboxProvider port, the egress proxy, the
// editor access gate and the credential injector, so container privileges
// stay out of brain.
// Brain calls it with the v2 service bearer for the sandboxd audience.
package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/platform/brainops"
	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/httpserver"
	workerkeys "github.com/upmo-ai/upmo/internal/platform/keys"
	"github.com/upmo-ai/upmo/internal/platform/logging"
	"github.com/upmo-ai/upmo/internal/platform/metrics"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/sandboxd"
)

// sweepEvery is how often expired sandboxes are reclaimed.
const sweepEvery = time.Minute

func main() {
	if err := run(); err != nil {
		slog.Error("sandboxd stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadSandboxd()
	if err != nil {
		return err
	}
	log := logging.New(os.Stdout, cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	workerClient, err := openWorkerKeys(ctx, cfg)
	if err != nil {
		return err
	}

	db, err := openDatabase(ctx, cfg)
	if err != nil {
		return err
	}
	if db != nil {
		defer db.Close()
	}

	network := sandboxd.Network{Name: cfg.SandboxNetwork, ProxyURL: cfg.EgressProxyURL}
	driver, err := sandboxd.Open(ctx, cfg.Driver, network, cfg.EditorImage, cfg.EditorBaseURL, db, log)
	if err != nil {
		return err
	}
	if c, ok := driver.(io.Closer); ok {
		defer func() { _ = c.Close() }()
	}
	var reg *metrics.Registry
	var routeOpts []sandboxd.RouteOption
	if cfg.MetricsAddr != "" {
		reg = metrics.NewRegistry()
		routeOpts = append(routeOpts, sandboxd.WithMetrics(reg))
	}
	api, err := sandboxd.Routes(cfg.SandboxdKey, cfg.SandboxdKeyPrevious, driver, log, routeOpts...)
	if err != nil {
		return err
	}

	// Either server stopping stops the other.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	proxy := httpserver.New(cfg.ProxyAddr, sandboxd.Proxy(driver, db, workerClient, log))
	gateHandler, err := sandboxd.EditorGate(cfg.EditorKey, cfg.EditorKeyPrevious, driver, log)
	if err != nil {
		return err
	}
	gate := httpserver.New(cfg.EditorAddr, gateHandler)
	// Shutdown does not wait for hijacked connections, so every tunnel hangs
	// off ctx and closes when sandboxd stops.
	proxy.BaseContext = func(net.Listener) context.Context { return ctx }
	gate.BaseContext = func(net.Listener) context.Context { return ctx }
	go sandboxd.Sweep(ctx, driver, sweepEvery, log)
	servers := []*http.Server{httpserver.New(cfg.ListenAddr, api), proxy, gate}
	if reg != nil {
		servers = append(servers, metrics.Server(cfg.MetricsAddr, reg, cfg.MetricsToken))
	}
	errc := make(chan error, len(servers))
	for _, srv := range servers {
		go func() {
			errc <- httpserver.Run(ctx, srv, cfg.ShutdownTimeout, log)
			cancel()
		}()
	}
	errs := make([]error, len(servers))
	for i := range servers {
		errs[i] = <-errc
	}
	return errors.Join(errs...)
}

// openWorkerKeys builds the key service client when UPMO_WORKER_URL and
// UPMO_WORKER_SANDBOXD_KEY are set, and checks, signed, that the worker is
// ready. With neither set there is no client and every credential read
// fails closed. Sandboxd never holds the master key.
func openWorkerKeys(ctx context.Context, cfg config.Sandboxd) (*workerkeys.Client, error) {
	if cfg.WorkerURL == "" {
		return nil, nil
	}
	client, err := workerkeys.Open(workerkeys.Options{
		URL:         cfg.WorkerURL,
		Key:         cfg.WorkerSandboxdKey,
		Audience:    svcsig.AudienceWorkerSandboxd,
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

// openDatabase opens the upmo_sandbox pool, or none when
// UPMO_DATABASE_SANDBOX_URL is unset: every credential read then fails
// closed. The role must be upmo_sandbox, not a superuser, not BYPASSRLS, and
// owner of no table, so gateway.serving() is false for it.
func openDatabase(ctx context.Context, cfg config.Sandboxd) (*pgxpool.Pool, error) {
	if cfg.DatabaseSandboxURL == "" {
		return nil, nil
	}
	return brainops.OpenServingPool(ctx, "UPMO_DATABASE_SANDBOX_URL", cfg.DatabaseSandboxURL, "", "upmo_sandbox")
}
