package metrics

import (
	"context"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRegistryExpositionAndEscaping(t *testing.T) {
	reg := NewRegistry()

	reg.ObserveHTTP("brain", "GET /api/tasks/{id}", 200, 15*time.Millisecond)
	reg.ObserveHTTP("brain", "GET /api/tasks/{id}", 200, 120*time.Millisecond)
	reg.IncSSEStream("chat")
	reg.IncSSEStream("tasks")
	reg.DecSSEStream("tasks")
	reg.ObserveRiverJob("chat_reply", "ok", 250*time.Millisecond)
	reg.SetRiverQueueDepth("chat_reply", 3)
	reg.ObserveGatewayCall("anthropic", `claude-"3"`, "ok", 80*time.Millisecond)
	reg.AddGatewayTokens("anthropic", `claude-"3"`, "input", 120)
	reg.AddGatewayCostMicroUSD("anthropic", `claude-"3"`, 450)
	reg.IncGatewayCacheHit("anthropic", `claude-"3"`)
	reg.IncGatewayCacheMiss("anthropic", `claude-"3"`)
	reg.IncGatewayHoldReleaseFailure()
	reg.SetSandboxdActiveSeats(2)

	// Add a custom family with newline/backslash in help and label values.
	c := reg.CounterVec("upmo_custom_total", "line1\nline2\\end", "tag")
	c.Add(2.5, "a\"b\\c\nd")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	reg.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != ContentType {
		t.Fatalf("Content-Type = %q, want %q", ct, ContentType)
	}
	body := rec.Body.String()

	wantSubstrings := []string{
		"# HELP upmo_http_requests_total Total HTTP requests served.",
		"# TYPE upmo_http_requests_total counter",
		`upmo_http_requests_total{service="brain",route="GET /api/tasks/{id}",status="200"} 2`,
		"# TYPE upmo_http_request_duration_seconds histogram",
		`upmo_http_request_duration_seconds_bucket{service="brain",route="GET /api/tasks/{id}",status="200",le="0.025"} 1`,
		`upmo_http_request_duration_seconds_bucket{service="brain",route="GET /api/tasks/{id}",status="200",le="0.25"} 2`,
		`upmo_http_request_duration_seconds_bucket{service="brain",route="GET /api/tasks/{id}",status="200",le="+Inf"} 2`,
		`upmo_http_request_duration_seconds_count{service="brain",route="GET /api/tasks/{id}",status="200"} 2`,
		`upmo_http_request_duration_seconds_sum{service="brain",route="GET /api/tasks/{id}",status="200"} 0.135`,
		`upmo_sse_streams_active{kind="chat"} 1`,
		`upmo_sse_streams_active{kind="tasks"} 0`,
		`upmo_river_jobs_total{kind="chat_reply",outcome="ok"} 1`,
		`upmo_river_job_duration_seconds_count{kind="chat_reply"} 1`,
		`upmo_river_queue_depth{queue="chat_reply"} 3`,
		`upmo_gateway_calls_total{provider="anthropic",model="claude-\"3\"",class="ok"} 1`,
		`upmo_gateway_tokens_total{provider="anthropic",model="claude-\"3\"",kind="input"} 120`,
		`upmo_gateway_cost_micro_usd_total{provider="anthropic",model="claude-\"3\""} 450`,
		`upmo_gateway_cache_hits_total{provider="anthropic",model="claude-\"3\""} 1`,
		`upmo_gateway_cache_misses_total{provider="anthropic",model="claude-\"3\""} 1`,
		`upmo_gateway_hold_release_failures_total 1`,
		`upmo_sandboxd_active_seats 2`,
		`# HELP upmo_custom_total line1\nline2\\end`,
		`upmo_custom_total{tag="a\"b\\c\nd"} 2.5`,
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in metrics output:\n%s", want, body)
		}
	}
}

func TestHandlerRejectsOtherPathsAndMethods(t *testing.T) {
	h := NewRegistry().Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/metrics", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /metrics status = %d, want 405", rec.Code)
	}

	for _, path := range []string{"/", "/healthz", "/metrics/extra"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want 404", path, rec.Code)
		}
	}
}

func TestMiddlewarePatternAndFallbackNormalization(t *testing.T) {
	reg := NewRegistry()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		// Verify Unwrap lets ResponseController reach the underlying ResponseWriter.
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("Flush via ResponseController failed: %v", err)
		}
	})
	guarded := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Block") == "1" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})

	handler := Middleware("brain", reg)(guarded)

	// Matched mux route uses r.Pattern.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tasks/0193376c-0046-8120-bcb2-dcac9ac3fd56", nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}

	// Unmatched 404 collapses to "unmatched".
	rec404 := httptest.NewRecorder()
	handler.ServeHTTP(rec404, httptest.NewRequest(http.MethodGet, "/api/unknown/0193376c-0046-8120-bcb2-dcac9ac3fd56", nil))
	if rec404.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec404.Code)
	}

	// Outer guard refusal normalizes UUID and numeric IDs when r.Pattern is empty.
	rec403 := httptest.NewRecorder()
	req403 := httptest.NewRequest(http.MethodPost, "/api/tasks/0193376c-0046-8120-bcb2-dcac9ac3fd56/items/42", nil)
	req403.Header.Set("X-Block", "1")
	handler.ServeHTTP(rec403, req403)
	if rec403.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec403.Code)
	}

	// MiddlewareWithMux resolves registered pattern even on outer refusal.
	muxHandler := MiddlewareWithMux("gateway", reg, mux)(guarded)
	recMux403 := httptest.NewRecorder()
	reqMux403 := httptest.NewRequest(http.MethodGet, "/api/tasks/0193376c-0046-8120-bcb2-dcac9ac3fd56", nil)
	reqMux403.Header.Set("X-Block", "1")
	muxHandler.ServeHTTP(recMux403, reqMux403)
	if recMux403.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recMux403.Code)
	}

	scrape := httptest.NewRecorder()
	reg.Handler().ServeHTTP(scrape, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := scrape.Body.String()

	for _, want := range []string{
		`upmo_http_requests_total{service="brain",route="GET /api/tasks/{id}",status="201"} 1`,
		`upmo_http_requests_total{service="brain",route="unmatched",status="404"} 1`,
		`upmo_http_requests_total{service="brain",route="POST /api/tasks/{id}/items/{id}",status="403"} 1`,
		`upmo_http_requests_total{service="gateway",route="GET /api/tasks/{id}",status="403"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}

func TestRegistryConcurrencyAndCollector(t *testing.T) {
	reg := NewRegistry()
	var scrapes atomic.Int64
	reg.AddCollector(func(context.Context) {
		n := scrapes.Add(1)
		reg.SetSandboxdActiveSeats(float64(n))
	})

	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for range 50 {
				reg.ObserveHTTP("brain", "GET /readyz", 200, time.Millisecond)
				reg.IncSSEStream("chat")
				reg.DecSSEStream("chat")
				if id%2 == 0 {
					rec := httptest.NewRecorder()
					reg.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
					if rec.Code != http.StatusOK {
						t.Errorf("scrape status = %d", rec.Code)
					}
				}
			}
		}(g)
	}
	wg.Wait()

	rec := httptest.NewRecorder()
	reg.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `upmo_http_requests_total{service="brain",route="GET /readyz",status="200"} 400`) {
		t.Fatalf("unexpected total in:\n%s", body)
	}
	if !strings.Contains(body, `upmo_sse_streams_active{kind="chat"} 0`) {
		t.Fatalf("expected 0 active sse streams in:\n%s", body)
	}
}

func TestListenAndServeShutdown(t *testing.T) {
	reg := NewRegistry()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- ListenAndServe(ctx, "127.0.0.1:0", reg, time.Second, slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ListenAndServe returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListenAndServe did not stop on cancel")
	}
}

func TestSpecialFloatAndNegativeHandling(t *testing.T) {
	if got := formatFloat(math.Inf(1)); got != "+Inf" {
		t.Fatalf("formatFloat(+Inf) = %q, want %q", got, "+Inf")
	}
	if got := formatFloat(math.Inf(-1)); got != "-Inf" {
		t.Fatalf("formatFloat(-Inf) = %q, want %q", got, "-Inf")
	}
	if got := formatFloat(math.NaN()); got != "NaN" {
		t.Fatalf("formatFloat(NaN) = %q, want %q", got, "NaN")
	}

	reg := NewRegistry()

	c := reg.CounterVec("upmo_test_counter_total", "test counter", "kind")
	c.Add(2.5, "ok")
	c.Add(-1, "ok")
	c.Add(math.Inf(-1), "ok")
	c.Add(math.NaN(), "ok")
	c.Add(-5, "counter_ignored_neg")
	c.Add(math.NaN(), "counter_ignored_nan")

	g := reg.GaugeVec("upmo_test_gauge", "test gauge", "kind")
	g.Set(math.Inf(1), "pos_inf")
	g.Set(math.Inf(-1), "neg_inf")
	g.Set(7, "keep")
	g.Set(math.NaN(), "keep")
	g.Set(math.NaN(), "gauge_ignored_nan")

	h := reg.HistogramVec("upmo_test_hist", "test hist", []float64{1, 5}, "kind")
	h.Observe(math.NaN(), "hist_ignored_nan")
	h.Observe(math.Inf(1), "pos_inf")
	h.Observe(math.NaN(), "pos_inf")
	h.Observe(math.Inf(-1), "neg_inf")

	rec := httptest.NewRecorder()
	reg.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	wantSubstrings := []string{
		`upmo_test_counter_total{kind="ok"} 2.5`,
		`upmo_test_gauge{kind="pos_inf"} +Inf`,
		`upmo_test_gauge{kind="neg_inf"} -Inf`,
		`upmo_test_gauge{kind="keep"} 7`,
		`upmo_test_hist_bucket{kind="pos_inf",le="1"} 0`,
		`upmo_test_hist_bucket{kind="pos_inf",le="5"} 0`,
		`upmo_test_hist_bucket{kind="pos_inf",le="+Inf"} 1`,
		`upmo_test_hist_sum{kind="pos_inf"} +Inf`,
		`upmo_test_hist_count{kind="pos_inf"} 1`,
		`upmo_test_hist_bucket{kind="neg_inf",le="1"} 1`,
		`upmo_test_hist_bucket{kind="neg_inf",le="5"} 1`,
		`upmo_test_hist_bucket{kind="neg_inf",le="+Inf"} 1`,
		`upmo_test_hist_sum{kind="neg_inf"} -Inf`,
		`upmo_test_hist_count{kind="neg_inf"} 1`,
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in metrics output:\n%s", want, body)
		}
	}

	for _, unwanted := range []string{"counter_ignored_neg", "counter_ignored_nan", "gauge_ignored_nan", "hist_ignored_nan"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("unexpected %q in metrics output:\n%s", unwanted, body)
		}
	}
}
