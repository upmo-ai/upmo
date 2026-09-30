// Package sandboxd is the process that holds the privileges brain never has:
// it serves the SandboxProvider port, hosts the drivers, the
// egress proxy and the credential injector. Only cmd/sandboxd imports it, and
// it is the only tree that may import a container engine client.
package sandboxd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/upmo-ai/upmo/internal/platform/httpserver"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/runtime"
	"github.com/upmo-ai/upmo/internal/sandboxd/adapters/docker"
	httpapi "github.com/upmo-ai/upmo/internal/sandboxd/adapters/http"
	"github.com/upmo-ai/upmo/internal/sandboxd/adapters/kubernetes"
	"github.com/upmo-ai/upmo/internal/sandboxd/adapters/stub"
	"github.com/upmo-ai/upmo/internal/sandboxd/egress"
	"github.com/upmo-ai/upmo/internal/sandboxd/inject"
)

// Driver choices for Open (UPMO_SANDBOX_DRIVER).
const (
	// DriverAuto uses Docker when the engine answers and the stub otherwise.
	DriverAuto = "auto"
	// DriverDocker requires the Docker engine.
	DriverDocker = "docker"
	// DriverKubernetes requires a reachable Kubernetes API.
	DriverKubernetes = "kubernetes"
	// DriverStub uses the in-memory stub.
	DriverStub = "stub"
)

// Driver is a SandboxProvider that also tells the egress proxy what each
// sandbox may reach.
type Driver interface {
	runtime.SandboxProvider
	egress.Book
}

// sweeper is a driver that holds sandboxes outside this process and must
// reclaim them when their TTL passes.
type sweeper interface {
	Sweep(ctx context.Context) (int, error)
}

// Stub returns the in-memory driver. It accepts only the container tier.
func Stub() Driver {
	return stub.New(nil)
}

// Network is how Docker sandboxes reach the egress proxy.
type Network struct {
	// Name is the internal Docker network sandboxes join; empty is the
	// driver default.
	Name string
	// ProxyURL is the egress proxy as a sandbox reaches it; empty is the
	// driver default.
	ProxyURL string
}

// Open returns the driver named by choice. auto, the default, uses the
// Docker engine from DOCKER_HOST or the default socket when it answers, and
// falls back to the stub with a warning when it does not. docker fails
// instead of falling back. network applies to the Docker driver only.
func Open(ctx context.Context, choice string, network Network, log *slog.Logger) (Driver, error) {
	return open(ctx, choice, log, func(ctx context.Context) (Driver, error) {
		d, err := docker.Open(ctx, docker.Options{Network: network.Name, ProxyURL: network.ProxyURL})
		if err != nil {
			return nil, err
		}
		e := d.Engine()
		log.Info("sandbox driver", "driver", DriverDocker, "host", e.Host, "version", e.Version,
			"rootless", e.Rootless, "userns", e.UserNS, "seccomp", e.Seccomp,
			"network", d.Network(), "proxy", network.ProxyURL)
		if !e.Rootless && !e.UserNS {
			log.Warn("sandbox driver: the engine runs without user namespaces; root in a sandbox is root on the engine host")
		}
		return d, nil
	})
}

func open(ctx context.Context, choice string, log *slog.Logger, dial func(context.Context) (Driver, error)) (Driver, error) {
	switch choice {
	case DriverStub:
		log.Info("sandbox driver", "driver", DriverStub)
		return Stub(), nil
	case DriverKubernetes:
		d, err := kubernetes.Open(ctx, kubernetes.Options{})
		if err != nil {
			return nil, fmt.Errorf("sandboxd: kubernetes driver: %w", err)
		}
		log.Info("sandbox driver", "driver", DriverKubernetes)
		return d, nil
	case DriverDocker:
		d, err := dial(ctx)
		if err != nil {
			return nil, fmt.Errorf("sandboxd: docker driver: %w", err)
		}
		return d, nil
	case "", DriverAuto:
		d, err := dial(ctx)
		if err != nil {
			log.Warn("sandbox driver: docker engine unreachable, using the in-memory stub; sandboxes start no container",
				"driver", DriverStub, "err", err)
			return Stub(), nil
		}
		return d, nil
	default:
		return nil, fmt.Errorf("sandboxd: unknown driver %q", choice)
	}
}

// Sweep reclaims expired sandboxes now and then every interval until ctx
// ends. A driver that holds nothing outside this process is left alone.
func Sweep(ctx context.Context, driver Driver, every time.Duration, log *slog.Logger) {
	s, ok := driver.(sweeper)
	if !ok || every <= 0 {
		return
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		n, err := s.Sweep(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Warn("sandbox sweep", "err", err)
		}
		if n > 0 {
			log.Info("sandbox sweep", "removed", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Routes serves the SandboxProvider port behind the service HMAC for the
// sandboxd audience. previousKey is optional, for rotation.
// /healthz and /readyz stay open for the container healthcheck.
func Routes(serviceKey, previousKey string, driver Driver, log *slog.Logger) (http.Handler, error) {
	keys := make([][]byte, 0, 2)
	for _, secret := range []string{serviceKey, previousKey} {
		if secret == "" && len(keys) > 0 {
			continue
		}
		master, err := svcsig.ParseKey(secret)
		if err != nil {
			return nil, err
		}
		derived, err := svcsig.Derive(master, svcsig.AudienceSandboxd)
		if err != nil {
			return nil, err
		}
		keys = append(keys, derived)
	}
	mux := http.NewServeMux()
	httpserver.Health(mux, nil)
	httpapi.Handler{Provider: driver, Log: log}.Mount(mux)
	return svcsig.Handler(keys, svcsig.AudienceSandboxd, mux)
}

// Proxy returns the egress proxy for the sandboxes driver holds. env:
// references resolve from sandboxd's environment. A host that
// names a credential the process cannot resolve is refused; every other
// allowed host tunnels. CONNECT still does not rewrite TLS bytes.
func Proxy(driver Driver, log *slog.Logger) http.Handler {
	return egress.Proxy{Book: driver, Inject: inject.Injector{Resolver: inject.Env{}}, Log: log}
}
