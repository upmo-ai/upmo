// Package httpserver builds the HTTP server with fixed timeouts, health
// handlers and graceful shutdown.
package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// New returns an http.Server on addr with conservative timeouts. Every route
// sits behind the cross-origin guard.
func New(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           Protect(h),
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

// Health registers /healthz and /readyz on mux. ready is the readiness check;
// a nil check reports ready.
func Health(mux *http.ServeMux, ready func(context.Context) error) {
	mux.HandleFunc("GET /healthz", healthz)
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if ready != nil {
			if err := ready(r.Context()); err != nil {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		writeOK(w)
	})
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	writeOK(w)
}

func writeOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// Run serves srv until ctx is cancelled, then shuts down within timeout.
func Run(ctx context.Context, srv *http.Server, timeout time.Duration, log *slog.Logger) error {
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return err
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("http server listening", "addr", ln.Addr().String())
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
