// Package httpserver builds the HTTP server with fixed timeouts, health
// handlers and graceful shutdown.
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/upmo-ai/upmo/internal/platform/buildinfo"
)

// ClientIP returns the client address, trusting forwarded hops only from a
// configured proxy.
func ClientIP(remote, forwarded string, trusted []*net.IPNet) string {
	host := hostOnly(remote)
	ip := net.ParseIP(host)
	if ip == nil || !trustedIP(ip, trusted) {
		return host
	}
	for hops := strings.Split(forwarded, ","); len(hops) > 0; hops = hops[:len(hops)-1] {
		hop := hostOnly(strings.TrimSpace(hops[len(hops)-1]))
		parsed := net.ParseIP(hop)
		if parsed != nil && !trustedIP(parsed, trusted) {
			return hop
		}
	}
	return host
}

func hostOnly(value string) string {
	if host, _, err := net.SplitHostPort(value); err == nil {
		return host
	}
	return value
}

func trustedIP(ip net.IP, trusted []*net.IPNet) bool {
	for _, network := range trusted {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// New returns an http.Server on addr with conservative timeouts. Every route
// sits behind the cross-origin guard.
func New(addr string, h http.Handler) *http.Server {
	return newServer(addr, Protect(h))
}

// NewGuarded is New for a handler that already carries its cross-origin guard
// inside, the way brain wraps Protect with the audit middleware so a refused
// request is still recorded.
func NewGuarded(addr string, h http.Handler) *http.Server {
	return newServer(addr, h)
}

func newServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

// Protect rejects cross-origin POST, PUT, PATCH and DELETE.
func Protect(h http.Handler) http.Handler {
	return http.NewCrossOriginProtection().Handler(h)
}

type readyResponse struct {
	Status    string `json:"status"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
}

// Health registers /healthz and /readyz on mux. ready is the readiness check;
// a nil check reports ready.
func Health(mux *http.ServeMux, ready func(context.Context) error) {
	mux.HandleFunc("GET /healthz", healthz)
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		info := buildinfo.Info()
		w.Header().Set("X-Upmo-Version", info.Version)
		w.Header().Set("X-Upmo-Commit", info.Commit)
		w.Header().Add("Vary", "Accept")
		if ready != nil {
			if err := ready(r.Context()); err != nil {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		if strings.Contains(strings.ToLower(r.Header.Get("Accept")), "application/json") {
			WriteJSON(w, http.StatusOK, readyResponse{
				Status:    "ok",
				Version:   info.Version,
				Commit:    info.Commit,
				BuildTime: info.BuildTime,
			})
			return
		}
		writeOK(w)
	})
}

// Version registers GET /version and GET /api/version on mux. schemas may be
// nil when the service has no schema version source.
func Version(mux *http.ServeMux, schemas func(context.Context) (map[string]int64, error)) {
	handler := func(w http.ResponseWriter, r *http.Request) {
		versions := map[string]int64{}
		if schemas != nil {
			loaded, err := schemas(r.Context())
			if err != nil {
				Fail(w, nil, "unavailable", http.StatusServiceUnavailable)
				return
			}
			if loaded != nil {
				versions = loaded
			}
		}
		info := buildinfo.Info()
		WriteJSON(w, http.StatusOK, buildinfo.Response{
			Version:        info.Version,
			Commit:         info.Commit,
			BuildTime:      info.BuildTime,
			SchemaVersions: versions,
		})
	}
	mux.HandleFunc("GET /version", handler)
	mux.HandleFunc("GET /api/version", handler)
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	writeOK(w)
}

func writeOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// WriteJSON writes body encoded as JSON with the given status code.
func WriteJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// NoContent writes status 204 No Content.
func NoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

// Fail writes a plain text error and logs err when log is non-nil.
func Fail(w http.ResponseWriter, log *slog.Logger, msg string, status int, err ...error) {
	if log != nil && len(err) > 0 && err[0] != nil {
		log.Error(msg, "err", err[0])
	}
	http.Error(w, msg, status)
}

// Run serves srv until ctx is cancelled, then shuts down within timeout.
func Run(ctx context.Context, srv *http.Server, timeout time.Duration, log *slog.Logger) error {
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return err
	}
	errc := make(chan error, 1)
	go func() {
		info := buildinfo.Info()
		log.Info("http server listening", "addr", ln.Addr().String(), "version", info.Version, "commit", info.Commit)
		errc <- srv.Serve(ln)
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	log.Info("http server shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
