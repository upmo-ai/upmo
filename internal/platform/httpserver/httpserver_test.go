package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/platform/buildinfo"
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

func TestReadyIncludesBuildHeadersAndJSON(t *testing.T) {
	mux := http.NewServeMux()
	Health(mux, nil)
	info := buildinfo.Info()

	plain := httptest.NewRecorder()
	mux.ServeHTTP(plain, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if plain.Code != http.StatusOK || plain.Body.String() != "ok" {
		t.Fatalf("plain readyz = %d %q", plain.Code, plain.Body.String())
	}
	if got := plain.Header().Get("X-Upmo-Version"); got != info.Version {
		t.Fatalf("X-Upmo-Version = %q, want %q", got, info.Version)
	}
	if got := plain.Header().Get("X-Upmo-Commit"); got != info.Commit {
		t.Fatalf("X-Upmo-Commit = %q, want %q", got, info.Commit)
	}
	if got := plain.Header().Get("Vary"); got != "Accept" {
		t.Fatalf("Vary = %q, want %q", got, "Accept")
	}

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	req.Header.Set("Accept", "application/json")
	jsonRec := httptest.NewRecorder()
	mux.ServeHTTP(jsonRec, req)
	if jsonRec.Code != http.StatusOK {
		t.Fatalf("json readyz = %d", jsonRec.Code)
	}
	if got := jsonRec.Header().Get("Vary"); got != "Accept" {
		t.Fatalf("json Vary = %q, want %q", got, "Accept")
	}
	if ct := jsonRec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
	var got readyResponse
	if err := json.Unmarshal(jsonRec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "ok" || got.Version != info.Version || got.Commit != info.Commit || got.BuildTime != info.BuildTime {
		t.Fatalf("json readyz = %+v, want info %+v", got, info)
	}
}

func TestVersionHandlers(t *testing.T) {
	info := buildinfo.Info()

	t.Run("with schemas", func(t *testing.T) {
		mux := http.NewServeMux()
		Version(mux, func(context.Context) (map[string]int64, error) {
			return map[string]int64{"audit": 2, "identity": 3}, nil
		})
		for _, path := range []string{"/version", "/api/version"} {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("%s status = %d, want 200", path, rec.Code)
			}
			var got buildinfo.Response
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("%s decode: %v", path, err)
			}
			if got.Version != info.Version || got.Commit != info.Commit || got.BuildTime != info.BuildTime {
				t.Fatalf("%s info = %+v, want %+v", path, got, info)
			}
			if got.SchemaVersions["audit"] != 2 || got.SchemaVersions["identity"] != 3 {
				t.Fatalf("%s schema_versions = %v", path, got.SchemaVersions)
			}
		}
	})

	t.Run("nil schemas returns empty object", func(t *testing.T) {
		mux := http.NewServeMux()
		Version(mux, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/version", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"schema_versions":{}`) {
			t.Fatalf("body = %s, want empty schema_versions object", rec.Body.String())
		}
	})

	t.Run("error returns 503", func(t *testing.T) {
		mux := http.NewServeMux()
		Version(mux, func(context.Context) (map[string]int64, error) {
			return nil, errors.New("db down")
		})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/version", nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
	})

	t.Run("rejects post", func(t *testing.T) {
		mux := http.NewServeMux()
		Version(mux, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/version", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want 405", rec.Code)
		}
	})
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestRunLogsBuildInfo(t *testing.T) {
	mux := http.NewServeMux()
	Health(mux, nil)
	srv := New("127.0.0.1:0", mux)
	buf := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(buf, nil))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, srv, time.Second, log) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), "http server listening") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v", err)
	}
	logged := buf.String()
	if !strings.Contains(logged, "version=") || !strings.Contains(logged, "commit=") {
		t.Fatalf("listening log missing version/commit: %s", logged)
	}
}
