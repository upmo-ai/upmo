// Package metrics provides a lightweight, concurrency-safe Prometheus text
// exposition registry (version 0.0.4) and HTTP/domain instrumentation helpers.
package metrics

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/platform/httpserver"
)

// ContentType is the Prometheus 0.0.4 text exposition media type.
const ContentType = "text/plain; version=0.0.4; charset=utf-8"

// DefaultHTTPBuckets are latency buckets in seconds for HTTP endpoints.
var DefaultHTTPBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// DefaultWorkBuckets are latency buckets in seconds for background jobs and
// model gateway calls.
var DefaultWorkBuckets = []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300}

type metricKind string

const (
	kindCounter   metricKind = "counter"
	kindGauge     metricKind = "gauge"
	kindHistogram metricKind = "histogram"
)

// Registry holds Prometheus metric families and optional scrape-time
// collectors. The zero value is not usable; call NewRegistry.
type Registry struct {
	mu         sync.RWMutex
	families   map[string]*family
	collectors []func(context.Context)

	httpRequests        *CounterVec
	httpDuration        *HistogramVec
	sseStreamsActive    *GaugeVec
	riverJobs           *CounterVec
	riverJobDuration    *HistogramVec
	riverQueueDepth     *GaugeVec
	gatewayCalls        *CounterVec
	gatewayDuration     *HistogramVec
	gatewayTokens       *CounterVec
	gatewayCostMicroUSD *CounterVec
	gatewayCacheHits    *CounterVec
	gatewayCacheMisses  *CounterVec
	sandboxdActiveSeats *GaugeVec
}

// NewRegistry creates a Registry pre-configured with standard Upmo metrics.
func NewRegistry() *Registry {
	r := &Registry{
		families: make(map[string]*family),
	}
	r.httpRequests = r.CounterVec(
		"upmo_http_requests_total",
		"Total HTTP requests served.",
		"service", "route", "status",
	)
	r.httpDuration = r.HistogramVec(
		"upmo_http_request_duration_seconds",
		"HTTP request latency in seconds.",
		DefaultHTTPBuckets,
		"service", "route", "status",
	)
	r.sseStreamsActive = r.GaugeVec(
		"upmo_sse_streams_active",
		"Active SSE streams currently open.",
		"kind",
	)
	r.riverJobs = r.CounterVec(
		"upmo_river_jobs_total",
		"Total River background jobs executed.",
		"kind", "outcome",
	)
	r.riverJobDuration = r.HistogramVec(
		"upmo_river_job_duration_seconds",
		"River job execution duration in seconds.",
		DefaultWorkBuckets,
		"kind",
	)
	r.riverQueueDepth = r.GaugeVec(
		"upmo_river_queue_depth",
		"Current number of non-finalized River jobs per queue.",
		"queue",
	)
	r.gatewayCalls = r.CounterVec(
		"upmo_gateway_calls_total",
		"Total model gateway provider attempts.",
		"provider", "model", "class",
	)
	r.gatewayDuration = r.HistogramVec(
		"upmo_gateway_call_duration_seconds",
		"Model gateway provider attempt duration in seconds.",
		DefaultWorkBuckets,
		"provider", "model",
	)
	r.gatewayTokens = r.CounterVec(
		"upmo_gateway_tokens_total",
		"Total tokens reported by model gateway attempts.",
		"provider", "model", "kind",
	)
	r.gatewayCostMicroUSD = r.CounterVec(
		"upmo_gateway_cost_micro_usd_total",
		"Total metered model gateway cost in micro-USD.",
		"provider", "model",
	)
	r.gatewayCacheHits = r.CounterVec(
		"upmo_gateway_cache_hits_total",
		"Total model gateway attempts with prompt cache read tokens.",
		"provider", "model",
	)
	r.gatewayCacheMisses = r.CounterVec(
		"upmo_gateway_cache_misses_total",
		"Total model gateway attempts with input tokens and no prompt cache read.",
		"provider", "model",
	)
	r.sandboxdActiveSeats = r.GaugeVec(
		"upmo_sandboxd_active_seats",
		"Active sandboxes held by sandboxd.",
	)
	return r
}

// AddCollector registers fn to run before every scrape so pull-based gauges can
// sample current state.
func (r *Registry) AddCollector(fn func(context.Context)) {
	if r == nil || fn == nil {
		return
	}
	r.mu.Lock()
	r.collectors = append(r.collectors, fn)
	r.mu.Unlock()
}

// CounterVec returns or registers a counter metric family with the given label names.
func (r *Registry) CounterVec(name, help string, labels ...string) *CounterVec {
	return &CounterVec{fam: r.family(name, help, kindCounter, nil, labels)}
}

// GaugeVec returns or registers a gauge metric family with the given label names.
func (r *Registry) GaugeVec(name, help string, labels ...string) *GaugeVec {
	return &GaugeVec{fam: r.family(name, help, kindGauge, nil, labels)}
}

// HistogramVec returns or registers a histogram metric family with the given
// upper-bound buckets and label names.
func (r *Registry) HistogramVec(name, help string, buckets []float64, labels ...string) *HistogramVec {
	b := cleanBuckets(buckets)
	return &HistogramVec{fam: r.family(name, help, kindHistogram, b, labels)}
}

func cleanBuckets(in []float64) []float64 {
	if len(in) == 0 {
		in = DefaultHTTPBuckets
	}
	out := make([]float64, 0, len(in))
	for _, v := range in {
		if math.IsNaN(v) || math.IsInf(v, 1) {
			continue
		}
		out = append(out, v)
	}
	sort.Float64s(out)
	dedup := out[:0]
	for i, v := range out {
		if i > 0 && v == out[i-1] {
			continue
		}
		dedup = append(dedup, v)
	}
	return dedup
}

func (r *Registry) family(name, help string, kind metricKind, buckets []float64, labels []string) *family {
	r.mu.Lock()
	defer r.mu.Unlock()
	if f, ok := r.families[name]; ok {
		return f
	}
	f := &family{
		name:    name,
		help:    help,
		kind:    kind,
		buckets: buckets,
		labels:  append([]string(nil), labels...),
		series:  make(map[string]*series),
	}
	r.families[name] = f
	return f
}

// ObserveHTTP records one completed HTTP request.
func (r *Registry) ObserveHTTP(service, route string, status int, d time.Duration) {
	if r == nil {
		return
	}
	st := strconv.Itoa(status)
	r.httpRequests.Inc(service, route, st)
	r.httpDuration.Observe(d.Seconds(), service, route, st)
}

// IncSSEStream increments active SSE streams for kind (for example "chat" or "tasks").
func (r *Registry) IncSSEStream(kind string) {
	if r == nil {
		return
	}
	r.sseStreamsActive.Inc(kind)
}

// DecSSEStream decrements active SSE streams for kind.
func (r *Registry) DecSSEStream(kind string) {
	if r == nil {
		return
	}
	r.sseStreamsActive.Dec(kind)
}

// ObserveRiverJob records one completed River job attempt.
func (r *Registry) ObserveRiverJob(kind, outcome string, d time.Duration) {
	if r == nil {
		return
	}
	r.riverJobs.Inc(kind, outcome)
	r.riverJobDuration.Observe(d.Seconds(), kind)
}

// SetRiverQueueDepth sets the active job count for queue.
func (r *Registry) SetRiverQueueDepth(queue string, depth float64) {
	if r == nil {
		return
	}
	r.riverQueueDepth.Set(depth, queue)
}

// ObserveGatewayCall records one model gateway provider attempt.
func (r *Registry) ObserveGatewayCall(provider, model, class string, d time.Duration) {
	if r == nil {
		return
	}
	r.gatewayCalls.Inc(provider, model, class)
	r.gatewayDuration.Observe(d.Seconds(), provider, model)
}

// AddGatewayTokens adds n tokens of kind (input, output, cache_read, cache_write, reasoning).
func (r *Registry) AddGatewayTokens(provider, model, kind string, n int) {
	if r == nil || n <= 0 {
		return
	}
	r.gatewayTokens.Add(float64(n), provider, model, kind)
}

// AddGatewayCostMicroUSD adds micro-USD metered cost for provider and model.
func (r *Registry) AddGatewayCostMicroUSD(provider, model string, microUSD float64) {
	if r == nil || microUSD <= 0 {
		return
	}
	r.gatewayCostMicroUSD.Add(microUSD, provider, model)
}

// IncGatewayCacheHit records a model gateway call that hit the prompt cache.
func (r *Registry) IncGatewayCacheHit(provider, model string) {
	if r == nil {
		return
	}
	r.gatewayCacheHits.Inc(provider, model)
}

// IncGatewayCacheMiss records a model gateway call that missed the prompt cache.
func (r *Registry) IncGatewayCacheMiss(provider, model string) {
	if r == nil {
		return
	}
	r.gatewayCacheMisses.Inc(provider, model)
}

// SetSandboxdActiveSeats sets the current active sandbox seat count.
func (r *Registry) SetSandboxdActiveSeats(n float64) {
	if r == nil {
		return
	}
	r.sandboxdActiveSeats.Set(n)
}

// WriteTo runs registered collectors and writes all populated metric families in
// Prometheus 0.0.4 text format to w.
func (r *Registry) WriteTo(ctx context.Context, w io.Writer) error {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	collectors := append([]func(context.Context){}, r.collectors...)
	names := make([]string, 0, len(r.families))
	fams := make(map[string]*family, len(r.families))
	for k, v := range r.families {
		names = append(names, k)
		fams[k] = v
	}
	r.mu.RUnlock()

	for _, fn := range collectors {
		fn(ctx)
	}
	sort.Strings(names)

	var buf bytes.Buffer
	for _, name := range names {
		fams[name].writeTo(&buf)
	}
	_, err := w.Write(buf.Bytes())
	return err
}

// Handler returns an http.Handler serving GET /metrics only. Other paths return
// 404 and non-GET methods return 405.
func (r *Registry) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, req *http.Request) {
		var buf bytes.Buffer
		if err := r.WriteTo(req.Context(), &buf); err != nil {
			httpserver.Fail(w, nil, "metrics unavailable", http.StatusInternalServerError, err)
			return
		}
		w.Header().Set("Content-Type", ContentType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(buf.Bytes())
	})
	return mux
}

// Server returns an internal *http.Server bound to addr serving GET /metrics.
func Server(addr string, reg *Registry) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           reg.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

// ListenAndServe serves GET /metrics on addr until ctx is cancelled, then
// shuts down gracefully within timeout.
func ListenAndServe(ctx context.Context, addr string, reg *Registry, timeout time.Duration, log *slog.Logger) error {
	return httpserver.Run(ctx, Server(addr, reg), timeout, log)
}

// CounterVec tracks monotonically increasing counters indexed by label values.
type CounterVec struct {
	fam *family
}

// Inc increments the counter for the given label values by 1.
func (c *CounterVec) Inc(values ...string) {
	c.Add(1, values...)
}

// Add adds delta (must be >= 0) to the counter for the given label values.
func (c *CounterVec) Add(delta float64, values ...string) {
	if c == nil || delta < 0 || math.IsNaN(delta) {
		return
	}
	s := c.fam.getOrCreate(values)
	if s != nil {
		addAtomicFloat(&s.val, delta)
	}
}

// GaugeVec tracks arbitrary numeric values indexed by label values.
type GaugeVec struct {
	fam *family
}

// Set sets the gauge for the given label values to v.
func (g *GaugeVec) Set(v float64, values ...string) {
	if g == nil || math.IsNaN(v) {
		return
	}
	s := g.fam.getOrCreate(values)
	if s != nil {
		s.val.Store(math.Float64bits(v))
	}
}

// Inc increments the gauge for the given label values by 1.
func (g *GaugeVec) Inc(values ...string) {
	g.Add(1, values...)
}

// Dec decrements the gauge for the given label values by 1.
func (g *GaugeVec) Dec(values ...string) {
	g.Add(-1, values...)
}

// Add adds delta to the gauge for the given label values.
func (g *GaugeVec) Add(delta float64, values ...string) {
	if g == nil || math.IsNaN(delta) {
		return
	}
	s := g.fam.getOrCreate(values)
	if s != nil {
		addAtomicFloat(&s.val, delta)
	}
}

// HistogramVec tracks distributions of observations indexed by label values.
type HistogramVec struct {
	fam *family
}

// Observe records one observation v for the given label values.
func (h *HistogramVec) Observe(v float64, values ...string) {
	if h == nil || math.IsNaN(v) {
		return
	}
	s := h.fam.getOrCreate(values)
	if s == nil {
		return
	}
	for i, bound := range h.fam.buckets {
		if v <= bound {
			s.buckets[i].Add(1)
		}
	}
	s.count.Add(1)
	addAtomicFloat(&s.sum, v)
}

type family struct {
	name    string
	help    string
	kind    metricKind
	buckets []float64
	labels  []string

	mu     sync.RWMutex
	series map[string]*series
}

type series struct {
	key     string
	values  []string
	val     atomic.Uint64
	buckets []atomic.Uint64
	count   atomic.Uint64
	sum     atomic.Uint64
}

func (f *family) getOrCreate(values []string) *series {
	if len(values) != len(f.labels) {
		return nil
	}
	key := canonicalLabels(f.labels, values)
	f.mu.RLock()
	s, ok := f.series[key]
	f.mu.RUnlock()
	if ok {
		return s
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.series[key]; ok {
		return s
	}
	s = &series{
		key:    key,
		values: append([]string(nil), values...),
	}
	if f.kind == kindHistogram {
		s.buckets = make([]atomic.Uint64, len(f.buckets))
	}
	f.series[key] = s
	return s
}

func (f *family) writeTo(buf *bytes.Buffer) {
	f.mu.RLock()
	if len(f.series) == 0 {
		f.mu.RUnlock()
		return
	}
	keys := make([]string, 0, len(f.series))
	items := make(map[string]*series, len(f.series))
	for k, v := range f.series {
		keys = append(keys, k)
		items[k] = v
	}
	f.mu.RUnlock()
	sort.Strings(keys)

	fmt.Fprintf(buf, "# HELP %s %s\n", f.name, escapeHelp(f.help))
	fmt.Fprintf(buf, "# TYPE %s %s\n", f.name, f.kind)

	for _, key := range keys {
		s := items[key]
		switch f.kind {
		case kindCounter, kindGauge:
			val := math.Float64frombits(s.val.Load())
			fmt.Fprintf(buf, "%s%s %s\n", f.name, formatLabels(f.labels, s.values), formatFloat(val))
		case kindHistogram:
			for i, bound := range f.buckets {
				c := s.buckets[i].Load()
				fmt.Fprintf(buf, "%s_bucket%s %d\n", f.name, formatBucketLabels(f.labels, s.values, formatFloat(bound)), c)
			}
			total := s.count.Load()
			sum := math.Float64frombits(s.sum.Load())
			fmt.Fprintf(buf, "%s_bucket%s %d\n", f.name, formatBucketLabels(f.labels, s.values, "+Inf"), total)
			fmt.Fprintf(buf, "%s_sum%s %s\n", f.name, formatLabels(f.labels, s.values), formatFloat(sum))
			fmt.Fprintf(buf, "%s_count%s %d\n", f.name, formatLabels(f.labels, s.values), total)
		}
	}
}

func addAtomicFloat(target *atomic.Uint64, delta float64) {
	for {
		oldBits := target.Load()
		nextBits := math.Float64bits(math.Float64frombits(oldBits) + delta)
		if target.CompareAndSwap(oldBits, nextBits) {
			return
		}
	}
}

func canonicalLabels(names, values []string) string {
	if len(names) == 0 {
		return ""
	}
	var b strings.Builder
	for i := range names {
		if i > 0 {
			b.WriteByte('\x00')
		}
		b.WriteString(names[i])
		b.WriteByte('=')
		b.WriteString(values[i])
	}
	return b.String()
}

func formatLabels(names, values []string) string {
	if len(names) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('{')
	for i := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(names[i])
		b.WriteString(`="`)
		b.WriteString(escapeLabel(values[i]))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

func formatBucketLabels(names, values []string, le string) string {
	var b strings.Builder
	b.WriteByte('{')
	for i := range names {
		b.WriteString(names[i])
		b.WriteString(`="`)
		b.WriteString(escapeLabel(values[i]))
		b.WriteString(`",`)
	}
	b.WriteString(`le="`)
	b.WriteString(le)
	b.WriteString(`"}`)
	return b.String()
}

func escapeLabel(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

func escapeHelp(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'g', 15, 64)
}

// Middleware instruments HTTP requests for service using Go 1.22+ ServeMux
// patterns and bounded route normalization fallback.
func Middleware(service string, reg *Registry) func(http.Handler) http.Handler {
	return MiddlewareWithMux(service, reg, nil)
}

// MiddlewareWithMux is Middleware with an optional ServeMux used to resolve
// route patterns when an outer guard rejects a request before the mux runs.
func MiddlewareWithMux(service string, reg *Registry, mux *http.ServeMux) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if reg == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			started := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)

			status := rec.status
			if status == 0 {
				status = http.StatusOK
			}
			route := resolveRoute(r, mux, status)
			reg.ObserveHTTP(service, route, status, time.Since(started))
		})
	}
}

func resolveRoute(r *http.Request, mux *http.ServeMux, status int) string {
	if r.Pattern != "" {
		return r.Pattern
	}
	if mux != nil {
		if _, pattern := mux.Handler(r); pattern != "" {
			return pattern
		}
		return "unmatched"
	}
	if status == http.StatusNotFound {
		return "unmatched"
	}
	if status == http.StatusMethodNotAllowed {
		return r.Method + " unmatched"
	}
	return r.Method + " " + normalizePath(r.URL.Path)
}

func normalizePath(p string) string {
	if p == "" || p == "/" {
		return "/"
	}
	parts := strings.Split(p, "/")
	for i, seg := range parts {
		if seg == "" {
			continue
		}
		if isVariableSegment(seg) {
			parts[i] = "{id}"
		}
	}
	return strings.Join(parts, "/")
}

func isVariableSegment(seg string) bool {
	if _, err := uuid.Parse(seg); err == nil {
		return true
	}
	digits := true
	for _, r := range seg {
		if r < '0' || r > '9' {
			digits = false
			break
		}
	}
	if digits {
		return true
	}
	// Collapse long opaque hex/token segments that are not Connect service paths.
	if len(seg) >= 24 && !strings.Contains(seg, ".") {
		return true
	}
	return false
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if !r.wroteHeader {
		r.status = status
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	if !r.wroteHeader {
		r.status = http.StatusOK
		r.wroteHeader = true
	}
	return r.ResponseWriter.Write(p)
}

// Unwrap exposes the underlying ResponseWriter so http.ResponseController
// retains Flush, SetWriteDeadline, and Hijack for streaming routes.
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}
