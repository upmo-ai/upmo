package httpserver

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHealthHandlers(t *testing.T) {
	mux := http.NewServeMux()
	Health(mux, nil)

	for _, path := range []string{"/healthz", "/readyz"} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if body := rec.Body.String(); body != "ok" {
				t.Fatalf("body = %q, want ok", body)
			}
		})
	}
}

func TestHealthRejectsPost(t *testing.T) {
	mux := http.NewServeMux()
	Health(mux, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/healthz", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestRunShutsDownOnCancel(t *testing.T) {
	mux := http.NewServeMux()
	Health(mux, nil)
	srv := New("127.0.0.1:0", mux)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, srv, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil))) }()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestReadyReportsTheCheck(t *testing.T) {
	mux := http.NewServeMux()
	Health(mux, func(context.Context) error { return context.DeadlineExceeded })
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestCrossOriginDeleteIsRejected(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /api/session", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	srv := New("127.0.0.1:0", mux)
	req := httptest.NewRequest(http.MethodDelete, "/api/session", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}
