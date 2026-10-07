// Command sandboxd serves the SandboxProvider port, the egress proxy and the
// credential injector, so container privileges stay out of brain.
// Brain calls it with the service HMAC for the sandboxd audience.
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

	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/httpserver"
	"github.com/upmo-ai/upmo/internal/platform/logging"
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

	network := sandboxd.Network{Name: cfg.SandboxNetwork, ProxyURL: cfg.EgressProxyURL}
	driver, err := sandboxd.Open(ctx, cfg.Driver, network, cfg.EditorImage, log)
	if err != nil {
		return err
	}
	if c, ok := driver.(io.Closer); ok {
		defer func() { _ = c.Close() }()
	}
	api, err := sandboxd.Routes(cfg.ServiceKey, cfg.ServiceKeyPrevious, driver, log)
	if err != nil {
		return err
	}

	// Either server stopping stops the other.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	proxy := httpserver.New(cfg.ProxyAddr, sandboxd.Proxy(driver, log))
	// Shutdown does not wait for hijacked connections, so every tunnel hangs
	// off ctx and closes when sandboxd stops.
	proxy.BaseContext = func(net.Listener) context.Context { return ctx }
	go sandboxd.Sweep(ctx, driver, sweepEvery, log)
	errc := make(chan error, 2)
	for _, srv := range []*http.Server{httpserver.New(cfg.ListenAddr, api), proxy} {
		go func() {
			errc <- httpserver.Run(ctx, srv, cfg.ShutdownTimeout, log)
			cancel()
		}()
	}
	return errors.Join(<-errc, <-errc)
}
