package httpserver

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientIP(t *testing.T) {
	proxy := mustCIDR(t, "203.0.113.8/32")
	got := ClientIP("203.0.113.8:443", "198.51.100.4, 203.0.113.8", []*net.IPNet{proxy})
	if got != "198.51.100.4" {
		t.Fatalf("forwarded = %s", got)
	}
	got = ClientIP("198.51.100.9:443", "203.0.113.8", []*net.IPNet{proxy})
	if got != "198.51.100.9" {
		t.Fatalf("untrusted connection used the header: %s", got)
	}
}

func mustCIDR(t *testing.T, cidr string) *net.IPNet {
	t.Helper()
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatal(err)
	}
	return network
}

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

func TestWriteJSONAndNoContentAndFail(t *testing.T) {
	t.Run("writeJSON", func(t *testing.T) {
		rec := httptest.NewRecorder()
		WriteJSON(rec, http.StatusOK, map[string]string{"status": "ok"})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if rec.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("content-type = %q", rec.Header().Get("Content-Type"))
		}
		if rec.Body.String() != `{"status":"ok"}`+"\n" {
			t.Fatalf("body = %s", rec.Body.String())
		}
	})

	t.Run("noContent", func(t *testing.T) {
		rec := httptest.NewRecorder()
		NoContent(rec)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", rec.Code)
		}
	})

	t.Run("fail", func(t *testing.T) {
		rec := httptest.NewRecorder()
		Fail(rec, nil, "bad request", http.StatusBadRequest)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
		if rec.Body.String() != "bad request\n" {
			t.Fatalf("body = %q", rec.Body.String())
		}
	})
}
