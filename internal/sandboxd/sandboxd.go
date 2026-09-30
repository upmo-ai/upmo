// Package sandboxd is the process that holds the privileges brain never has:
// it serves the SandboxProvider port, hosts the drivers, the
// egress proxy and the credential injector. Only cmd/sandboxd imports it, and
// it is the only tree that may import a container engine client.
package sandboxd

import (
	"log/slog"
	"net/http"

	"github.com/upmo-ai/upmo/internal/platform/httpserver"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/runtime"
	httpapi "github.com/upmo-ai/upmo/internal/sandboxd/adapters/http"
	"github.com/upmo-ai/upmo/internal/sandboxd/adapters/stub"
	"github.com/upmo-ai/upmo/internal/sandboxd/egress"
	"github.com/upmo-ai/upmo/internal/sandboxd/inject"
)

// Driver is a SandboxProvider that also tells the egress proxy what each
// sandbox may reach.
type Driver interface {
	runtime.SandboxProvider
	egress.Book
}

// Stub returns the in-memory driver. It accepts only the container tier.
func Stub() Driver {
	return stub.New(nil)
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

// Proxy returns the egress proxy for the sandboxes driver holds. With no
// credential backend wired, every reference is refused.
func Proxy(driver Driver, log *slog.Logger) http.Handler {
	return egress.Proxy{Book: driver, Inject: inject.Injector{Resolver: inject.Refuse{}}, Log: log}
}
