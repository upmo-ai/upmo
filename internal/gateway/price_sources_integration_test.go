//go:build integration

package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/upmo-ai/upmo/internal/gateway/adapters/anthropic"
	"github.com/upmo-ai/upmo/internal/gateway/adapters/openai"
	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/gateway/snapshot"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/httpserver"
	"github.com/upmo-ai/upmo/internal/platform/jobs"
	"github.com/upmo-ai/upmo/internal/platform/metrics"
	"github.com/upmo-ai/upmo/internal/platform/notices"
)

func insertCatalogPriceRow(t *testing.T, r *rig, vendor, model string, inRate, outRate, crRate, cwRate float64) {
	t.Helper()
	err := postgres.Store{Pool: r.owner}.ReplaceCatalog(context.Background(), "test-cat-v1", []domain.CatalogRow{{
		Vendor:  vendor,
		Model:   model,
		Version: "test-cat-v1",
		Price: domain.Price{
			Version:                   "test-cat-v1",
			InputMicroUSDPerMTok:      ptrInt64(int64(inRate * 1_000_000)),
			OutputMicroUSDPerMTok:     ptrInt64(int64(outRate * 1_000_000)),
			CacheReadMicroUSDPerMTok:  ptrInt64(int64(crRate * 1_000_000)),
			CacheWriteMicroUSDPerMTok: ptrInt64(int64(cwRate * 1_000_000)),
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
}

func ptrInt64(v int64) *int64 { return &v }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func rewriteHostClient(srv *httptest.Server) *http.Client {
	target, err := url.Parse(srv.URL)
	if err != nil {
		panic(err)
	}
	base := srv.Client()
	return &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			cloned := req.Clone(req.Context())
			cloned.URL.Scheme = target.Scheme
			cloned.URL.Host = target.Host
			cloned.Host = target.Host
			return base.Transport.RoundTrip(cloned)
		}),
	}
}

type collectSink struct {
	text strings.Builder
	done *app.StreamDone
	fail *app.StreamFail
}

func (s *collectSink) Delta(t string) error { s.text.WriteString(t); return nil }
func (s *collectSink) Notice(app.StreamNotice) error {
	return nil
}
func (s *collectSink) Done(d app.StreamDone) { s.done = &d }
func (s *collectSink) Fail(f app.StreamFail) { s.fail = &f }

func TestFeature_UPM354_CachedTokensNotDoubleBilled(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, masterKeys(t, 'a'))
	store := postgres.Store{Pool: r.owner}

	// Catalog: input=$10/MTok, output=$20/MTok, cache_read=$1/MTok, cache_write=$12/MTok.
	err := store.ReplaceCatalog(ctx, "cat-cache-v1", []domain.CatalogRow{
		{
			Vendor:  "openai",
			Model:   "gpt-4o",
			Version: "cat-cache-v1",
			Price: domain.Price{
				Version:                   "cat-cache-v1",
				InputMicroUSDPerMTok:      ptrInt64(10_000_000),
				OutputMicroUSDPerMTok:     ptrInt64(20_000_000),
				CacheReadMicroUSDPerMTok:  ptrInt64(1_000_000),
				CacheWriteMicroUSDPerMTok: ptrInt64(12_000_000),
			},
		},
		{
			Vendor:  "deepseek",
			Model:   "deepseek-chat",
			Version: "cat-cache-v1",
			Price: domain.Price{
				Version:                   "cat-cache-v1",
				InputMicroUSDPerMTok:      ptrInt64(10_000_000),
				OutputMicroUSDPerMTok:     ptrInt64(20_000_000),
				CacheReadMicroUSDPerMTok:  ptrInt64(1_000_000),
				CacheWriteMicroUSDPerMTok: ptrInt64(12_000_000),
			},
		},
		{
			Vendor:  "anthropic",
			Model:   "claude-sonnet-4",
			Version: "cat-cache-v1",
			Price: domain.Price{
				Version:                   "cat-cache-v1",
				InputMicroUSDPerMTok:      ptrInt64(10_000_000),
				OutputMicroUSDPerMTok:     ptrInt64(20_000_000),
				CacheReadMicroUSDPerMTok:  ptrInt64(1_000_000),
				CacheWriteMicroUSDPerMTok: ptrInt64(12_000_000),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// HTTP test server simulating OpenAI, DeepSeek, and Anthropic responses.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(req.Body).Decode(&body)
		model, _ := body["model"].(string)
		stream, _ := body["stream"].(bool)
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			switch model {
			case "gpt-4o":
				_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
				_, _ = fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1000,\"completion_tokens\":500,\"prompt_tokens_details\":{\"cached_tokens\":800}}}\n\n")
				_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			case "deepseek-chat":
				_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
				_, _ = fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1000,\"completion_tokens\":500,\"prompt_cache_hit_tokens\":800}}\n\n")
				_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			}
			return
		}
		switch model {
		case "gpt-4o":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
				"usage": map[string]any{
					"prompt_tokens":         1000,
					"completion_tokens":     500,
					"prompt_tokens_details": map[string]int{"cached_tokens": 800},
				},
			})
		case "deepseek-chat":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
				"usage": map[string]any{
					"prompt_tokens":           1000,
					"completion_tokens":       500,
					"prompt_cache_hit_tokens": 800,
				},
			})
		case "claude-sonnet-4":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"content": []map[string]string{{"type": "text", "text": "ok"}},
				"usage": map[string]int{
					"input_tokens":                200,
					"output_tokens":               500,
					"cache_read_input_tokens":     800,
					"cache_creation_input_tokens": 0,
				},
			})
		}
	}))
	t.Cleanup(srv.Close)

	addAcctAndRoute := func(routeName, vendor, adapterName, ref, model string) {
		v, ok := domain.VendorFor(vendor)
		if !ok {
			t.Fatalf("unknown vendor %q", vendor)
		}
		var acctID string
		if err := r.owner.QueryRow(ctx, `
			INSERT INTO gateway.provider_accounts
				(owner_user_id, provider, name, adapter, credential_ref, plan_kind, house, base_url, status, visibility)
			VALUES ($1::uuid, $2, $3, $4, $5, 'metered', false, $6, 'Connected', 'private')
			RETURNING id::text`,
			ada, vendor, routeName+"-acct", adapterName, ref, v.BaseURL,
		).Scan(&acctID); err != nil {
			t.Fatal(err)
		}
		if _, err := r.owner.Exec(ctx, `
			INSERT INTO gateway.provider_models (account_id, model, position, listed)
			VALUES ($1::uuid, $2, 1, true)`, acctID, model); err != nil {
			t.Fatal(err)
		}
		var routeID string
		if err := r.owner.QueryRow(ctx, `
			INSERT INTO gateway.routes (name, capability, installed) VALUES ($1, 'chat', true) RETURNING id::text`, routeName).Scan(&routeID); err != nil {
			t.Fatal(err)
		}
		if _, err := r.owner.Exec(ctx, `
			INSERT INTO gateway.route_links (route_id, position, provider_account_id, model, retry_budget)
			VALUES ($1::uuid, 1, $2::uuid, $3, 0)`, routeID, acctID, model); err != nil {
			t.Fatal(err)
		}
	}

	addAcctAndRoute("route-oai-cache", "openai", domain.AdapterOpenAI, "ref_oai", "gpt-4o")
	addAcctAndRoute("route-ds-cache", "deepseek", domain.AdapterOpenAI, "ref_ds", "deepseek-chat")
	addAcctAndRoute("route-ant-cache", "anthropic", domain.AdapterAnthropic, "ref_ant", "claude-sonnet-4")

	client := rewriteHostClient(srv)
	oaiAdapter := openai.Adapter{Client: client}
	antAdapter := anthropic.Adapter{Client: client}
	disp := app.Dispatch{
		Routes:  store,
		Teams:   store,
		Prices:  store,
		Ledger:  store,
		Secrets: staticSecrets{"ref_oai": "k", "ref_ds": "k", "ref_ant": "k"},
		Chat: map[string]app.Chatter{
			domain.AdapterOpenAI:    oaiAdapter,
			domain.AdapterAnthropic: antAdapter,
		},
		Stream: map[string]app.StreamChatter{
			domain.AdapterOpenAI:    oaiAdapter,
			domain.AdapterAnthropic: antAdapter,
		},
	}

	in := domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}
	who := domain.Principal{User: ada, Source: domain.SourceChat}

	// Non-streamed OpenAI, DeepSeek, Anthropic.
	for _, route := range []string{"route-oai-cache", "route-ds-cache", "route-ant-cache"} {
		res, err := disp.ChatCall(ctx, route, "", who, in)
		if err != nil {
			t.Fatalf("%s ChatCall: %v", route, err)
		}
		// Uncached 200*$10/MTok (2000) + output 500*$20/MTok (10000) + cache_read 800*$1/MTok (800) = 12800 micro-USD.
		if res.CostMicroUSD == nil || *res.CostMicroUSD != 12_800 {
			t.Fatalf("%s non-stream cost = %v, want 12800", route, res.CostMicroUSD)
		}
	}

	// Streamed OpenAI and DeepSeek.
	for _, route := range []string{"route-oai-cache", "route-ds-cache"} {
		sink := &collectSink{}
		if err := disp.ChatStreamCall(ctx, route, "", who, in, 0, sink); err != nil {
			t.Fatalf("%s ChatStreamCall: %v", route, err)
		}
		if sink.done == nil || sink.done.CostMicroUSD == nil || *sink.done.CostMicroUSD != 12_800 {
			t.Fatalf("%s stream done = %+v, want 12800", route, sink.done)
		}
	}

	// Verify all 5 ledger rows have input_tokens=200, cache_read=800, cost_micro_usd=12800.
	rows, err := r.owner.Query(ctx, `SELECT route, input_tokens, cache_read, output_tokens, cost_micro_usd, price_source FROM gateway.ledger`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		var route, src string
		var inp, cr, out int
		var cost int64
		if err := rows.Scan(&route, &inp, &cr, &out, &cost, &src); err != nil {
			t.Fatal(err)
		}
		if inp != 200 || cr != 800 || out != 500 || cost != 12_800 || src != "models_dev" {
			t.Fatalf("ledger row %s = in:%d cr:%d out:%d cost:%d src:%s", route, inp, cr, out, cost, src)
		}
	}
	if err := rows.Err(); err != nil || count != 5 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestFeature_UPM354_ProviderReportedCostAndFallbacks(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, masterKeys(t, 'a'))
	store := postgres.Store{Pool: r.owner}

	// Catalog prices for fallback check: $2/MTok input, $8/MTok output.
	err := store.ReplaceCatalog(ctx, "cat-rep-v1", []domain.CatalogRow{
		{
			Vendor:  "openrouter",
			Model:   "paid/model",
			Version: "cat-rep-v1",
			Price: domain.Price{
				Version:               "cat-rep-v1",
				InputMicroUSDPerMTok:  ptrInt64(2_000_000),
				OutputMicroUSDPerMTok: ptrInt64(8_000_000),
			},
		},
		{
			Vendor:  "openrouter",
			Model:   "meta-llama/llama:free",
			Version: "cat-rep-v1",
			Price: domain.Price{
				Version:               "cat-rep-v1",
				InputMicroUSDPerMTok:  ptrInt64(2_000_000),
				OutputMicroUSDPerMTok: ptrInt64(8_000_000),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var mode string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(req.Body).Decode(&body)
		stream, _ := body["stream"].(bool)
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			switch mode {
			case "cut-no-output":
				// Stream ends before any delta and before usage chunk (no [DONE]).
				_, _ = fmt.Fprint(w, ": ping\n\n")
			case "cut-after-output":
				// Stream emits text delta then cuts before usage chunk.
				_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
			}
			return
		}
		switch mode {
		case "openrouter-free-zero":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
				"usage":   map[string]any{"prompt_tokens": 1000, "completion_tokens": 500, "cost": 0},
			})
		case "openrouter-paid-zero":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
				"usage":   map[string]any{"prompt_tokens": 1_000_000, "completion_tokens": 500_000, "cost": 0},
			})
		case "openrouter-normal":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
				"usage":   map[string]any{"prompt_tokens": 1000, "completion_tokens": 500, "cost": 0.0042},
			})
		case "xai-ticks":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
				"usage":   map[string]any{"prompt_tokens": 1000, "completion_tokens": 500, "cost_in_usd_ticks": 15000},
			})
		case "perplexity-total-cost":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
				"usage": map[string]any{
					"prompt_tokens":     1000,
					"completion_tokens": 500,
					"cost":              map[string]any{"input_tokens_cost": 0.001, "output_tokens_cost": 0.002, "total_cost": 0.003},
				},
			})
		case "bad-cost-fallback":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"content": "ok"}}},
				"usage":   map[string]any{"prompt_tokens": 1_000_000, "completion_tokens": 500_000, "cost": "NaN"},
			})
		}
	}))
	t.Cleanup(srv.Close)

	addRoute := func(routeName, vendor, ref, model string) {
		v, ok := domain.VendorFor(vendor)
		if !ok {
			t.Fatalf("unknown vendor %q", vendor)
		}
		var acctID string
		if err := r.owner.QueryRow(ctx, `
			INSERT INTO gateway.provider_accounts
				(owner_user_id, provider, name, adapter, credential_ref, plan_kind, house, base_url, status, visibility)
			VALUES ($1::uuid, $2, $3, 'openai_compat', $4, 'metered', false, $5, 'Connected', 'private')
			RETURNING id::text`,
			ada, vendor, routeName+"-acct", ref, v.BaseURL,
		).Scan(&acctID); err != nil {
			t.Fatal(err)
		}
		if _, err := r.owner.Exec(ctx, `
			INSERT INTO gateway.provider_models (account_id, model, position, listed)
			VALUES ($1::uuid, $2, 1, true)`, acctID, model); err != nil {
			t.Fatal(err)
		}
		var routeID string
		if err := r.owner.QueryRow(ctx, `
			INSERT INTO gateway.routes (name, capability, installed) VALUES ($1, 'chat', true) RETURNING id::text`, routeName).Scan(&routeID); err != nil {
			t.Fatal(err)
		}
		if _, err := r.owner.Exec(ctx, `
			INSERT INTO gateway.route_links (route_id, position, provider_account_id, model, retry_budget)
			VALUES ($1::uuid, 1, $2::uuid, $3, 0)`, routeID, acctID, model); err != nil {
			t.Fatal(err)
		}
	}

	addRoute("r-free", "openrouter", "ref_free", "meta-llama/llama:free")
	addRoute("r-paid", "openrouter", "ref_paid", "paid/model")
	addRoute("r-xai", "xai", "ref_xai", "grok-3")
	addRoute("r-pplx", "perplexity", "ref_pplx", "sonar")

	// Set a workspace spend limit so holds are exercised during interrupted streams.
	if _, err := r.owner.Exec(ctx, `INSERT INTO gateway.usage_limits (kind, amount_micro_usd) VALUES ('workspace', 1000000000)`); err != nil {
		t.Fatal(err)
	}

	oai := openai.Adapter{Client: rewriteHostClient(srv)}
	disp := app.Dispatch{
		Routes:  store,
		Teams:   store,
		Prices:  store,
		Ledger:  store,
		Limits:  store,
		Secrets: staticSecrets{"ref_free": "k", "ref_paid": "k", "ref_xai": "k", "ref_pplx": "k"},
		Chat:    map[string]app.Chatter{domain.AdapterOpenAI: oai},
		Stream:  map[string]app.StreamChatter{domain.AdapterOpenAI: oai},
	}
	who := domain.Principal{User: ada, Source: domain.SourceChat}
	in := domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hello world!"}}}

	// 1. :free zero cost -> accepted as "provider".
	mode = "openrouter-free-zero"
	res, err := disp.ChatCall(ctx, "r-free", "", who, in)
	if err != nil || res.CostMicroUSD == nil || *res.CostMicroUSD != 0 {
		t.Fatalf("free zero: res=%+v err=%v", res, err)
	}

	// 2. paid model reporting 0 -> rejected, falls back to models_dev (1M*$2 + 0.5M*$8 = $6 = 6_000_000).
	mode = "openrouter-paid-zero"
	res, err = disp.ChatCall(ctx, "r-paid", "", who, in)
	if err != nil || res.CostMicroUSD == nil || *res.CostMicroUSD != 6_000_000 {
		t.Fatalf("paid zero fallback: res=%+v err=%v", res, err)
	}

	// 3. normal OpenRouter scalar cost -> 4200 ("provider").
	mode = "openrouter-normal"
	res, err = disp.ChatCall(ctx, "r-paid", "", who, in)
	if err != nil || res.CostMicroUSD == nil || *res.CostMicroUSD != 4200 {
		t.Fatalf("openrouter normal: res=%+v err=%v", res, err)
	}

	// 4. xAI ticks -> 2 ("provider").
	mode = "xai-ticks"
	res, err = disp.ChatCall(ctx, "r-xai", "", who, in)
	if err != nil || res.CostMicroUSD == nil || *res.CostMicroUSD != 2 {
		t.Fatalf("xai ticks: res=%+v err=%v", res, err)
	}

	// 5. Perplexity object total_cost -> 3000 ("provider").
	mode = "perplexity-total-cost"
	res, err = disp.ChatCall(ctx, "r-pplx", "", who, in)
	if err != nil || res.CostMicroUSD == nil || *res.CostMicroUSD != 3000 {
		t.Fatalf("perplexity: res=%+v err=%v", res, err)
	}

	// 6. Bad cost ("NaN") -> falls back to models_dev (6_000_000).
	mode = "bad-cost-fallback"
	res, err = disp.ChatCall(ctx, "r-paid", "", who, in)
	if err != nil || res.CostMicroUSD == nil || *res.CostMicroUSD != 6_000_000 {
		t.Fatalf("bad cost fallback: res=%+v err=%v", res, err)
	}

	// 7. Stream cut before output & usage -> knownZero ("not_billed"), hold released.
	mode = "cut-no-output"
	sink := &collectSink{}
	_ = disp.ChatStreamCall(ctx, "r-paid", "", who, in, 0, sink)
	if sink.fail == nil {
		t.Fatal("expected fail on cut stream")
	}

	// 8. Stream cut after partial output, before usage -> "estimate", positive cost.
	mode = "cut-after-output"
	sink2 := &collectSink{}
	_ = disp.ChatStreamCall(ctx, "r-paid", "", who, in, 0, sink2)
	if sink2.fail == nil || sink2.text.String() != "partial" {
		t.Fatalf("expected partial fail, got %+v", sink2)
	}

	// Check recorded price_source values in ledger and zero leftover holds.
	var held int64
	if err := r.owner.QueryRow(ctx, `SELECT COALESCE(sum(held_micro_usd), 0) FROM gateway.usage_spend`).Scan(&held); err != nil {
		t.Fatal(err)
	}
	if held != 0 {
		t.Fatalf("leftover held_micro_usd = %d, want 0", held)
	}

	type lrow struct {
		cost      int64
		estimated bool
		source    string
	}
	rows, err := r.owner.Query(ctx, `SELECT cost_micro_usd, estimated, price_source FROM gateway.ledger ORDER BY at, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []lrow
	for rows.Next() {
		var x lrow
		if err := rows.Scan(&x.cost, &x.estimated, &x.source); err != nil {
			t.Fatal(err)
		}
		got = append(got, x)
	}
	want := []lrow{
		{0, false, "provider"},
		{6_000_000, false, "models_dev"},
		{4200, false, "provider"},
		{2, false, "provider"},
		{3000, false, "provider"},
		{6_000_000, false, "models_dev"},
		{0, false, "not_billed"},
	}
	if len(got) != 8 {
		t.Fatalf("ledger count = %d, want 8: %+v", len(got), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("ledger[%d] = %+v, want %+v", i, got[i], w)
		}
	}
	if !got[7].estimated || got[7].source != "estimate" || got[7].cost <= 0 {
		t.Fatalf("ledger[7] = %+v, want positive estimate", got[7])
	}
}

type scriptChatter struct {
	usage domain.Usage
}

func (c *scriptChatter) Chat(_ context.Context, attempt app.Attempt, _ domain.Request) (domain.ChatResult, domain.FailureClass, error) {
	return domain.ChatResult{Text: "ok", Model: attempt.Model, Usage: c.usage}, domain.ClassOK, nil
}

func TestFeature_UPM354_PrecedenceOrderAndOverrideBeatsProvider(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, masterKeys(t, 'a'))
	store := postgres.Store{Pool: r.owner}

	modelID := "anthropic/claude-3.7-sonnet"
	acctID := insertProviderAndRoute(t, r, "r-prec", "openrouter", "OR Prec", "ref_prec", modelID, true)

	chatter := &scriptChatter{}
	disp := app.Dispatch{
		Routes:  store,
		Teams:   store,
		Prices:  store,
		Ledger:  store,
		Secrets: staticSecrets{"ref_prec": "k"},
		Chat:    map[string]app.Chatter{domain.AdapterOpenAI: chatter, domain.AdapterAnthropic: chatter, domain.AdapterFake: chatter},
	}
	who := domain.Principal{User: ada, Source: domain.SourceChat}
	in := domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "12345678"}}}

	// 1. No catalog, no list, no reported, no override -> unknown (NULL cost).
	chatter.usage = domain.Usage{Arrived: true, Input: 1_000_000, Output: 500_000}
	res1, err := disp.ChatCall(ctx, "r-prec", "", who, in)
	if err != nil || res1.CostMicroUSD != nil {
		t.Fatalf("step 1: %+v err=%v, want nil cost", res1, err)
	}

	// 2. Add models.dev catalog ($2 input, $10 output -> 2M + 5M = 7_000_000).
	insertCatalogPriceRow(t, r, "openrouter", modelID, 2.0, 10.0, 0.2, 2.5)
	res2, err := disp.ChatCall(ctx, "r-prec", "", who, in)
	if err != nil || *res2.CostMicroUSD != 7_000_000 {
		t.Fatalf("step 2: %v err=%v", res2.CostMicroUSD, err)
	}

	// 3. Add provider_list in account_models_seen ($4 input, $12 output -> 4M + 6M = 10_000_000).
	if _, err := r.owner.Exec(ctx, `
		INSERT INTO gateway.account_models_seen (account_id, model_id, position, pricing)
		VALUES ($1::uuid, $2, 1, '{"prompt":"0.000004","completion":"0.000012"}'::jsonb)`,
		acctID, modelID); err != nil {
		t.Fatal(err)
	}
	res3, err := disp.ChatCall(ctx, "r-prec", "", who, in)
	if err != nil || *res3.CostMicroUSD != 10_000_000 {
		t.Fatalf("step 3: %v err=%v", res3.CostMicroUSD, err)
	}

	// 4. Add provider-reported cost ($0.123 = 123_000) -> beats provider_list & models_dev.
	reported := int64(123_000)
	chatter.usage.ReportedCostMicroUSD = &reported
	res4, err := disp.ChatCall(ctx, "r-prec", "", who, in)
	if err != nil || *res4.CostMicroUSD != 123_000 {
		t.Fatalf("step 4: %v err=%v", res4.CostMicroUSD, err)
	}

	// 5. Add partial price_override on input ($1/MTok); output stays $12/MTok from provider_list -> 1M + 6M = 7_000_000, beats reported cost!
	if _, err := r.owner.Exec(ctx, `
		INSERT INTO gateway.price_overrides (account_id, model, input_micro_usd_per_mtok, updated_by)
		VALUES ($1::uuid, $2, 1000000, $3::uuid)`,
		acctID, modelID, ada); err != nil {
		t.Fatal(err)
	}
	res5, err := disp.ChatCall(ctx, "r-prec", "", who, in)
	if err != nil || *res5.CostMicroUSD != 7_000_000 {
		t.Fatalf("step 5: %v err=%v", res5.CostMicroUSD, err)
	}

	// Mutate override and replace catalog; all 5 historical ledger rows must keep their recorded cost and source!
	if _, err := r.owner.Exec(ctx, `DELETE FROM gateway.price_overrides WHERE account_id = $1::uuid`, acctID); err != nil {
		t.Fatal(err)
	}
	insertCatalogPriceRow(t, r, "openrouter", modelID, 99.0, 99.0, 9.0, 9.0)

	rows, err := r.owner.Query(ctx, `SELECT cost_micro_usd, price_source FROM gateway.ledger ORDER BY at, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type item struct {
		cost *int64
		src  string
	}
	var got []item
	for rows.Next() {
		var x item
		if err := rows.Scan(&x.cost, &x.src); err != nil {
			t.Fatal(err)
		}
		got = append(got, x)
	}
	want := []item{
		{nil, "unknown"},
		{ptrInt64(7_000_000), "models_dev"},
		{ptrInt64(10_000_000), "provider_list"},
		{ptrInt64(123_000), "provider"},
		{ptrInt64(7_000_000), "override"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows: %+v", len(got), got)
	}
	for i := range want {
		costMatch := (got[i].cost == nil) == (want[i].cost == nil) && (want[i].cost == nil || *got[i].cost == *want[i].cost)
		if !costMatch || got[i].src != want[i].src {
			t.Fatalf("row %d = {cost:%v src:%q}, want {cost:%v src:%q}", i, got[i].cost, got[i].src, want[i].cost, want[i].src)
		}
	}
}

func TestFeature_UPM354_MigrateSeedsSnapshotIdempotently(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, masterKeys(t, 'a'))

	snapVer, snapAsOf, snapRows, err := snapshot.Load()
	if err != nil {
		t.Fatal(err)
	}

	// 1. Fresh install: SeedCatalog seeds gateway.prices and catalog_meta (refreshed_at IS NULL).
	if err := SeedCatalog(ctx, r.owner); err != nil {
		t.Fatalf("fresh SeedCatalog: %v", err)
	}
	meta, ok, err := postgres.GetCatalogMeta(ctx, r.owner)
	if err != nil || !ok {
		t.Fatalf("GetCatalogMeta ok=%v err=%v", ok, err)
	}
	if meta.Version != snapVer || !meta.AsOf.UTC().Equal(snapAsOf) || meta.RefreshedAt != nil {
		t.Fatalf("fresh meta = %+v, want version=%s as_of=%v refreshed_at=nil", meta, snapVer, snapAsOf)
	}
	cnt, err := postgres.CatalogRowCount(ctx, r.owner)
	if err != nil || cnt != len(snapRows) {
		t.Fatalf("seeded row count = %d, want %d (err=%v)", cnt, len(snapRows), err)
	}

	// 2. Repeat run: does not replace existing catalog.
	if _, err := r.owner.Exec(ctx, `INSERT INTO gateway.prices (vendor, model, version) VALUES ('sentinel', 'sentinel-model', 'v')`); err != nil {
		t.Fatal(err)
	}
	seeded, err := postgres.SeedCatalog(ctx, r.owner, snapVer, snapAsOf, snapRows)
	if err != nil || seeded {
		t.Fatalf("repeat SeedCatalog seeded=%v err=%v, want false nil", seeded, err)
	}
	if r.count(`SELECT count(*) FROM gateway.prices WHERE vendor = 'sentinel'`) != 1 {
		t.Fatal("repeat SeedCatalog wiped sentinel row")
	}

	// 3. Upgrade with a newer snapshot over an older refreshed catalogue: replaces catalogue.
	olderRefreshed := snapAsOf.Add(-48 * time.Hour)
	if _, err := r.owner.Exec(ctx, `UPDATE gateway.catalog_meta SET version = 'old-refreshed', as_of = $1, refreshed_at = $1 WHERE id = 1`, olderRefreshed); err != nil {
		t.Fatal(err)
	}
	seeded, err = postgres.SeedCatalog(ctx, r.owner, snapVer, snapAsOf, snapRows)
	if err != nil || !seeded {
		t.Fatalf("newer snapshot over older refreshed seeded=%v err=%v, want true nil", seeded, err)
	}
	meta, _, _ = postgres.GetCatalogMeta(ctx, r.owner)
	if meta.Version != snapVer || !meta.AsOf.UTC().Equal(snapAsOf) || meta.RefreshedAt != nil {
		t.Fatalf("after upgrade meta = %+v", meta)
	}
	if r.count(`SELECT count(*) FROM gateway.prices WHERE vendor = 'sentinel'`) != 0 {
		t.Fatal("upgrade did not replace older prices")
	}

	// 4. Older snapshot under a newer refreshed catalogue: does NOT overwrite newer refreshed catalogue.
	newerRefreshed := snapAsOf.Add(48 * time.Hour)
	if _, err := r.owner.Exec(ctx, `UPDATE gateway.catalog_meta SET version = 'newer-refreshed', as_of = $1, refreshed_at = $1 WHERE id = 1`, newerRefreshed); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `INSERT INTO gateway.prices (vendor, model, version) VALUES ('sentinel', 'newer-model', 'v')`); err != nil {
		t.Fatal(err)
	}
	seeded, err = postgres.SeedCatalog(ctx, r.owner, snapVer, snapAsOf, snapRows)
	if err != nil || seeded {
		t.Fatalf("older snapshot under newer refreshed seeded=%v err=%v, want false nil", seeded, err)
	}
	meta, _, _ = postgres.GetCatalogMeta(ctx, r.owner)
	if meta.Version != "newer-refreshed" || !meta.AsOf.UTC().Equal(newerRefreshed) {
		t.Fatalf("newer refreshed meta overwritten: %+v", meta)
	}
	if r.count(`SELECT count(*) FROM gateway.prices WHERE vendor = 'sentinel'`) != 1 {
		t.Fatal("older snapshot wiped newer refreshed sentinel row")
	}

	// 5. Refresh and seeding share one advisory lock; seeding proceeds after unlock.
	conn, locked, unlock, err := postgres.TryLockCatalog(ctx, r.owner)
	if err != nil || !locked || conn == nil {
		t.Fatalf("first TryLockCatalog locked=%v err=%v", locked, err)
	}
	unlocked := false
	defer func() {
		if !unlocked {
			unlock()
		}
	}()
	_, locked2, _, err := postgres.TryLockCatalog(ctx, r.owner)
	if err != nil || locked2 {
		t.Fatalf("second TryLockCatalog locked=%v err=%v, want false", locked2, err)
	}
	lockCtx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	_, err = postgres.SeedCatalog(lockCtx, r.owner, snapVer, snapAsOf, snapRows)
	cancel()
	if err == nil {
		t.Fatal("SeedCatalog succeeded while refresh held the catalog lock")
	}
	var lockErr *pgconn.PgError
	if !errors.Is(err, context.DeadlineExceeded) && (!errors.As(err, &lockErr) || lockErr.Code != "55P03") {
		t.Fatalf("locked SeedCatalog error = %v, want deadline or lock-wait error", err)
	}
	unlock()
	unlocked = true
	if _, err := postgres.SeedCatalog(ctx, r.owner, snapVer, snapAsOf, snapRows); err != nil {
		t.Fatalf("SeedCatalog after unlock: %v", err)
	}
}

func makeValidModelsDevJSON(modelCount, vendorCount int, baseInput float64) []byte {
	root := map[string]any{}
	// Map vendorCount from domain.Vendors() (excluding ollama/lmstudio if needed; use models.dev keys).
	mdKeys := []string{
		"anthropic", "openai", "google", "nebius", "deepseek",
		"groq", "xai", "openrouter", "mistral", "togetherai",
		"cerebras", "fireworks-ai", "perplexity", "cohere",
	}
	for i := range vendorCount {
		key := mdKeys[i%len(mdKeys)]
		root[key] = map[string]any{
			"models": map[string]any{
				fmt.Sprintf("vmodel-%d", i): map[string]any{
					"cost": map[string]any{"input": baseInput, "output": baseInput * 2},
				},
			},
		}
	}
	extra := map[string]any{}
	for i := range modelCount {
		extra[fmt.Sprintf("model-%03d", i)] = map[string]any{
			"cost": map[string]any{"input": baseInput, "output": baseInput * 2},
		}
	}
	root["extra-provider"] = map[string]any{"models": extra}
	raw, _ := json.Marshal(root)
	return raw
}

func catalogJSONWithTier(raw []byte, model string, threshold int, inputRate float64) []byte {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		panic(err)
	}
	extra := root["extra-provider"].(map[string]any)
	models := extra["models"].(map[string]any)
	entry := models[model].(map[string]any)
	cost := entry["cost"].(map[string]any)
	cost["tiers"] = []any{map[string]any{"tier": map[string]any{"type": "context", "size": threshold}, "input": inputRate}}
	result, err := json.Marshal(root)
	if err != nil {
		panic(err)
	}
	return result
}

// TestFeature_UPM394_SnapshotAloneSeedsContextTierPricing proves a fresh install
// with no network prices a call above 200,000 context tokens at the tier rate,
// from the embedded snapshot alone (no HTTP server, no refresh).
func TestFeature_UPM394_SnapshotAloneSeedsContextTierPricing(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, masterKeys(t, 'a'))
	store := postgres.Store{Pool: r.owner}

	if err := SeedCatalog(ctx, r.owner); err != nil {
		t.Fatalf("SeedCatalog: %v", err)
	}

	// models.dev google/gemini-2.5-pro in snapshot 2026-11-20T21:39:11Z (vendor
	// key "gemini"): base $1.25 in / $10 out, context tier above 200000 tokens
	// $2.50 in / $15 out. A tier change in a later snapshot must update these.
	accountID := insertProviderAndRoute(t, r, "r-upm394-tier", "gemini", "Gemini Tier", "ref_upm394_tier", "gemini-2.5-pro", true)
	if accountID == "" {
		t.Fatal("insertProviderAndRoute returned an empty account id")
	}
	chatter := &scriptChatter{}
	dispatch := app.Dispatch{Routes: store, Teams: store, Prices: store, Ledger: store, Secrets: staticSecrets{"ref_upm394_tier": "k"}, Chat: map[string]app.Chatter{domain.AdapterOpenAI: chatter}}
	who := domain.Principal{User: ada, Source: domain.SourceChat}
	in := domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}

	for _, tc := range []struct {
		name  string
		input int
		want  int64
	}{
		// 200,000 is not above the boundary: base rate. 200000*1.25 + 3*10 = 250,000 + 30.
		{"boundary 200000 uses base rate", 200_000, 250_030},
		// 200,001 is above it: tier rate. 200001*2.50 + 3*15 = 500,002.5 + 45 = 500,047.5, rounded half up.
		{"200001 uses tier rate", 200_001, 500_048},
	} {
		chatter.usage = domain.Usage{Arrived: true, Input: tc.input, Output: 3}
		res, err := dispatch.ChatCall(ctx, "r-upm394-tier", "", who, in)
		if err != nil || res.CostMicroUSD == nil {
			t.Fatalf("%s: cost=%v err=%v, want %d", tc.name, res.CostMicroUSD, err, tc.want)
		}
		if *res.CostMicroUSD != tc.want {
			t.Fatalf("%s: cost=%d, want %d", tc.name, *res.CostMicroUSD, tc.want)
		}
	}
	var source string
	if err := r.owner.QueryRow(ctx, `SELECT price_source FROM gateway.ledger WHERE route='r-upm394-tier' ORDER BY at DESC, id DESC LIMIT 1`).Scan(&source); err != nil || source != "models_dev" {
		t.Fatalf("price_source=%q err=%v, want models_dev", source, err)
	}
}

func TestFeature_UPM354_RefreshValidationKeepsOldCatalogAndNotifiesOnce(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, masterKeys(t, 'a'))
	if err := notices.Up(ctx, r.owner); err != nil {
		t.Fatal(err)
	}

	// Create one joined owner, one settings:manage admin, and one non-admin member.
	ownerID := uuid.NewString()
	adminID := uuid.NewString()
	memberID := uuid.NewString()
	for _, p := range []struct {
		id, email, roleKey string
	}{
		{ownerID, "owner-cat@example.com", "owner"},
		{adminID, "admin-cat@example.com", "admin"},
		{memberID, "member-cat@example.com", "member"},
	} {
		if _, err := r.owner.Exec(ctx, `
			INSERT INTO identity.users (id, email, name, role_id, joined_at)
			VALUES ($1::uuid, $2, $2, (SELECT id FROM identity.roles WHERE key = $3), now())`,
			p.id, p.email, p.roleKey,
		); err != nil {
			t.Fatal(err)
		}
	}

	// Seed an initial valid catalog (12 vendors, 200 models) with as_of = T0 and refreshed_at = NULL (snapshot-only!).
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	initJSON := makeValidModelsDevJSON(200, 12, 1.0)
	initRows, err := domain.ParseCatalog(bytes.NewReader(initJSON), "init-snap")
	if err != nil {
		t.Fatal(err)
	}
	for i := range initRows {
		if initRows[i].Model == "model-001" {
			initRows[i].Price.Tiers = []domain.PriceTier{{ContextMinTokens: 200_000, Price: domain.Price{InputMicroUSDPerMTok: ptrInt64(2_000_000)}}}
		}
	}
	if _, err := postgres.SeedCatalog(ctx, r.owner, "init-snap", t0, initRows); err != nil {
		t.Fatal(err)
	}
	if r.count(`SELECT count(*) FROM gateway.price_tiers WHERE model='model-001' AND context_min_tokens=200000`) != 1 {
		t.Fatal("SeedCatalog did not store model tiers")
	}

	// Add an override and a ledger row that must survive refreshes.
	acctID := insertProviderAndRoute(t, r, "r-ref", "openai", "OAI Ref", "ref_ref", "model-001", true)
	if _, err := r.owner.Exec(ctx, `
		INSERT INTO gateway.price_overrides (account_id, model, input_micro_usd_per_mtok, updated_by)
		VALUES ($1::uuid, 'model-001', 4200000, $2::uuid)`, acctID, adminID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `
		INSERT INTO gateway.ledger (route, link_position, provider_account_id, model, plan_kind, failure_class, user_id, cost_micro_usd, source, price_source)
		VALUES ('r-ref', 1, $1::uuid, 'model-001', 'metered', 'ok', $2::uuid, 777, 'chat', 'override')`, acctID, adminID); err != nil {
		t.Fatal(err)
	}

	var status int
	var payload []byte
	var redirectTargetHits atomic.Int32
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/redirect":
			http.Redirect(w, req, "/target", http.StatusFound)
		case "/target":
			redirectTargetHits.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(makeValidModelsDevJSON(210, 12, 9.0))
		default:
			w.WriteHeader(status)
			_, _ = w.Write(payload)
		}
	}))
	t.Cleanup(tlsSrv.Close)

	var plainHits atomic.Int32
	plainSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		plainHits.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(initJSON)
	}))
	t.Cleanup(plainSrv.Close)

	var logBuf syncBuf
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	now := t0.Add(24 * time.Hour)
	deps := CatalogRefreshDeps{
		Owner:  r.owner,
		URL:    tlsSrv.URL,
		Client: tlsSrv.Client(),
		SettingsManagers: func(ctx context.Context) ([]string, error) {
			return identity.SettingsManagers(ctx, r.owner)
		},
		Notify: func(ctx context.Context, tx pgx.Tx, userID, title string) error {
			uid, err := uuid.Parse(userID)
			if err != nil {
				return err
			}
			_, err = notices.InsertTx(ctx, tx, uid, notices.KindFailed, title, nil)
			return err
		},
		Now: func() time.Time { return now },
		Log: logger,
	}

	// Plain HTTP: refused by scheme check before any request hits the plain server.
	plainDeps := deps
	plainDeps.URL = plainSrv.URL
	plainDeps.Client = plainSrv.Client()
	if err := RefreshCatalogOnce(ctx, plainDeps); err == nil {
		t.Fatal("plain HTTP refresh succeeded, want scheme error")
	}
	if plainHits.Load() != 0 {
		t.Fatalf("plain HTTP server received %d requests, want 0", plainHits.Load())
	}
	meta, _, _ := postgres.GetCatalogMeta(ctx, r.owner)
	if !strings.Contains(meta.LastError, "https") {
		t.Fatalf("plain HTTP last_error = %q, want scheme error naming https", meta.LastError)
	}

	// Redirect refusal: even with a supplied client whose CheckRedirect allows
	// redirects, RefreshCatalogOnce overwrites CheckRedirect and refuses 302.
	redirClient := tlsSrv.Client()
	redirClient.CheckRedirect = func(*http.Request, []*http.Request) error { return nil }
	redirDeps := deps
	redirDeps.URL = tlsSrv.URL + "/redirect"
	redirDeps.Client = redirClient
	if err := RefreshCatalogOnce(ctx, redirDeps); err == nil {
		t.Fatal("redirecting refresh succeeded, want redirect refusal")
	}
	if redirectTargetHits.Load() != 0 {
		t.Fatalf("redirect target received %d hits, want 0", redirectTargetHits.Load())
	}

	// Size cap: valid catalog JSON padded with spaces to just over CatalogMaxBytes.
	oversizedValid := append(append([]byte(nil), initJSON...), bytes.Repeat([]byte(" "), int(CatalogMaxBytes)-len(initJSON)+5)...)
	status = 200
	payload = oversizedValid
	if err := RefreshCatalogOnce(ctx, deps); err == nil {
		t.Fatal("oversized valid catalog succeeded, want size error")
	}
	meta, _, _ = postgres.GetCatalogMeta(ctx, r.owner)
	if !strings.Contains(meta.LastError, "exceeds") {
		t.Fatalf("oversized last_error = %q, want size error", meta.LastError)
	}

	// 1. All validation failure modes keep old catalog and record sanitized last_error.
	badCases := []struct {
		name   string
		url    string
		status int
		body   []byte
	}{
		{"http-500-no-body-leak", tlsSrv.URL, 500, []byte("SECRET_BODY_DO_NOT_LOG")},
		{"empty-body", tlsSrv.URL, 200, []byte("")},
		{"empty-object", tlsSrv.URL, 200, []byte("{}")},
		{"null-document", tlsSrv.URL, 200, []byte("null")},
		{"bad-json", tlsSrv.URL, 200, []byte("{broken")},
		{"trailing-json", tlsSrv.URL, 200, append(append([]byte(nil), initJSON...), []byte(" {}")...)},
		{"trailing-garbage", tlsSrv.URL, 200, append(append([]byte(nil), initJSON...), []byte(" garbage")...)},
		{"negative-rate", tlsSrv.URL, 200, []byte(`{"openai":{"models":{"m":{"cost":{"input":-1,"output":1}}}}}`)},
		{"absurd-rate", tlsSrv.URL, 200, []byte(`{"openai":{"models":{"m":{"cost":{"input":10001,"output":1}}}}}`)},
		{"too-few-vendors", tlsSrv.URL, 200, makeValidModelsDevJSON(120, 5, 2.0)},
		{"too-few-models", tlsSrv.URL, 200, makeValidModelsDevJSON(20, 12, 2.0)},
		{"drop-over-25pct", tlsSrv.URL, 200, makeValidModelsDevJSON(120, 12, 2.0)},
	}
	for _, tc := range badCases {
		status = tc.status
		payload = tc.body
		d := deps
		d.URL = tc.url
		if err := RefreshCatalogOnce(ctx, d); err == nil {
			t.Fatalf("%s: expected error, got nil", tc.name)
		}
	}
	if strings.Contains(logBuf.String(), "SECRET_BODY_DO_NOT_LOG") {
		t.Fatalf("response body leaked into log: %s", logBuf.String())
	}
	meta, _, _ = postgres.GetCatalogMeta(ctx, r.owner)
	if meta.Version != "init-snap" || meta.RefreshedAt != nil || meta.LastError == "" || strings.Contains(meta.LastError, "SECRET_BODY_DO_NOT_LOG") {
		t.Fatalf("after failed checks meta = %+v", meta)
	}
	if cnt, _ := postgres.CatalogRowCount(ctx, r.owner); cnt != len(initRows) {
		t.Fatalf("old catalog altered: got %d rows, want %d", cnt, len(initRows))
	}

	// 2. Snapshot-only install >7 days stale: sends notice once per episode to settings:manage only.
	now = t0.Add(7*24*time.Hour + time.Minute)
	status = 503
	payload = []byte("down")
	beforeLines := strings.Count(strings.TrimSpace(logBuf.String()), "\n") + 1
	if err := RefreshCatalogOnce(ctx, deps); err == nil {
		t.Fatal("expected error on 503")
	}
	if r.count(`SELECT count(*) FROM gateway.price_tiers WHERE model='model-001' AND context_min_tokens=200000`) != 1 {
		t.Fatal("failed catalog refresh changed existing tiers")
	}
	afterLines := strings.Count(strings.TrimSpace(logBuf.String()), "\n") + 1
	if afterLines-beforeLines != 1 {
		t.Fatalf("expected exactly 1 log line per failed attempt, got %d", afterLines-beforeLines)
	}
	if r.count(`SELECT count(*) FROM notices.notifications WHERE user_id = $1::uuid`, adminID) != 1 {
		t.Fatal("expected 1 stale notice for settings:manage admin on snapshot-only install")
	}
	if r.count(`SELECT count(*) FROM notices.notifications WHERE user_id = $1::uuid`, ownerID) != 1 {
		t.Fatal("expected 1 stale notice for settings:manage owner on snapshot-only install")
	}
	if r.count(`SELECT count(*) FROM notices.notifications WHERE user_id = $1::uuid`, memberID) != 0 {
		t.Fatal("non-manager should not receive stale catalog notice")
	}

	// Second failure in the same stale episode must not send a second notice.
	now = now.Add(24 * time.Hour)
	if err := RefreshCatalogOnce(ctx, deps); err == nil {
		t.Fatal("expected error on 503")
	}
	if r.count(`SELECT count(*) FROM notices.notifications WHERE user_id = $1::uuid`, adminID) != 1 {
		t.Fatal("expected still 1 stale notice in same episode")
	}

	// 3. Valid refresh succeeds: swaps prices, updates refreshed_at & as_of, clears stale_notified_at & last_error, preserves overrides and ledger.
	status = 200
	payload = catalogJSONWithTier(makeValidModelsDevJSON(210, 12, 3.0), "model-001", 200_000, 6.0)
	now = now.Add(time.Hour)
	if err := RefreshCatalogOnce(ctx, deps); err != nil {
		t.Fatalf("valid refresh failed: %v", err)
	}
	meta, _, _ = postgres.GetCatalogMeta(ctx, r.owner)
	if meta.RefreshedAt == nil || !meta.AsOf.UTC().Equal(now) || meta.LastError != "" || meta.StaleNotifiedAt != nil {
		t.Fatalf("after valid refresh meta = %+v", meta)
	}
	p, ok, err := (postgres.Store{Pool: r.owner}).Price(ctx, "", "model-001")
	if err != nil || !ok || *p.InputMicroUSDPerMTok != 3_000_000 {
		t.Fatalf("refreshed price = %+v ok=%v err=%v", p, ok, err)
	}
	if len(p.Tiers) != 1 || p.Tiers[0].ContextMinTokens != 200_000 || p.Tiers[0].Price.InputMicroUSDPerMTok == nil || *p.Tiers[0].Price.InputMicroUSDPerMTok != 6_000_000 {
		t.Fatalf("refreshed tiers = %+v, want refreshed 200k tier at 6000000", p.Tiers)
	}
	if r.count(`SELECT count(*) FROM gateway.price_overrides WHERE account_id = $1::uuid AND input_micro_usd_per_mtok = 4200000`, acctID) != 1 {
		t.Fatal("price_overrides was altered by refresh")
	}
	if r.count(`SELECT count(*) FROM gateway.ledger WHERE cost_micro_usd = 777 AND price_source = 'override'`) != 1 {
		t.Fatal("ledger row was altered by refresh")
	}

	// 4. Next stale episode >7 days after successful refresh notifies once again.
	status = 502
	payload = []byte("bad gateway")
	now = now.Add(7*24*time.Hour + time.Minute)
	_ = RefreshCatalogOnce(ctx, deps)
	if r.count(`SELECT count(*) FROM notices.notifications WHERE user_id = $1::uuid`, adminID) != 2 {
		t.Fatal("expected second stale notice in new stale episode")
	}
}

func TestFeature_UPM354_OverridesRequireSettingsManageAndAudit(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, masterKeys(t, 'a'))
	sealed := newAuditSealer(t, r.owner)

	// Member (ada) has no settings:manage; admins (bob and eve) do.
	r.svc.Allow = func(_ context.Context, user string) (string, error) {
		if user == bob || user == eve {
			return domain.SharePermission, nil
		}
		return "", nil
	}
	r.mount(nil)

	mux := http.NewServeMux()
	ProvidersHandler{
		Service: r.svc,
		Access: func(req *http.Request) (string, bool) {
			user := req.Header.Get("X-User")
			if user != "" {
				audit.WithActor(req.Context(), audit.Actor{Type: "user", ID: user, CredentialType: "session"})
			}
			return user, user != ""
		},
	}.MountProviders(mux, nil)
	r.handler = audit.Middleware(audit.Routes(), mux, r.app, "brain", nil, slog.Default())(mux)

	modelID := "anthropic/claude-3.7-sonnet"
	insertCatalogPriceRow(t, r, "openrouter", modelID, 3.0, 15.0, 0.3, 3.75)

	// 1. Member (ada) creates a private account.
	adaAcct := r.mustAdd(ada, map[string]any{
		"provider": "openrouter", "name": "Ada OR", "adapter": "openai_compat",
		"plan_kind": "metered", "base_url": "https://openrouter.ai/api", "api_key": adaKey,
		"models": []string{modelID}, "visibility": domain.VisibilityPrivate,
	})
	_ = sealed()

	// Member can GET merged pricing (read-only) using query param with slashed model id.
	code, raw := r.call(ada, http.MethodGet, "/api/providers/"+adaAcct.ID+"/pricing?model="+url.QueryEscape(modelID), nil)
	if code != http.StatusOK {
		t.Fatalf("GET pricing = %d %s", code, raw)
	}
	view := decode[app.ModelPricingView](t, raw)
	if view.Merged.Model != modelID || view.Merged.InputPerMTok == nil || *view.Merged.InputPerMTok != 3.0 {
		t.Fatalf("merged pricing = %+v", view)
	}

	// Member WITHOUT settings:manage CANNOT set or delete an override on their own private account (HTTP 403).
	code, raw = r.call(ada, http.MethodPut, "/api/providers/"+adaAcct.ID+"/overrides", map[string]any{
		"model":                    modelID,
		"input_micro_usd_per_mtok": 5_000_000,
	})
	if code != http.StatusForbidden {
		t.Fatalf("member PUT private override = %d %s, want 403", code, raw)
	}
	code, raw = r.call(ada, http.MethodDelete, "/api/providers/"+adaAcct.ID+"/overrides", map[string]any{
		"model": modelID,
	})
	if code != http.StatusForbidden {
		t.Fatalf("member DELETE private override = %d %s, want 403", code, raw)
	}
	_ = sealed()

	// Bypassing app layer: direct store / SQL as upmo_app WITHOUT settings:manage is denied by Postgres RLS!
	ovStore := r.svc.Accounts.(app.OverrideStore)
	_, err := ovStore.SetOverride(ctx, app.Caller{User: ada, Allow: ""}, adaAcct.ID, domain.PriceOverride{
		Model:                modelID,
		InputMicroUSDPerMTok: ptrInt64(5_000_000),
	})
	if !errors.Is(err, app.ErrOverridePermission) {
		t.Fatalf("direct SetOverride without settings:manage = %v, want ErrOverridePermission", err)
	}
	tx, err := r.app.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = tx.Exec(ctx, `SELECT set_config('upmo.user_id', $1, true), set_config('upmo.allow', '', true)`, ada)
	_, sqlErr := tx.Exec(ctx, `
		INSERT INTO gateway.price_overrides (account_id, model, input_micro_usd_per_mtok, updated_by)
		VALUES ($1::uuid, $2, 5000000, $3::uuid)`, adaAcct.ID, modelID, ada)
	_ = tx.Rollback(ctx)
	var pgErr *pgconn.PgError
	if !errors.As(sqlErr, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("raw SQL RLS error = %v, want SQLSTATE 42501", sqlErr)
	}

	// 2. Admin (bob, WITH settings:manage) creates a shared everyone account with OpenRouter pricing in account_models_seen.
	bobAcct := r.mustAdd(bob, map[string]any{
		"provider": "openrouter", "name": "Shared OR", "adapter": "openai_compat",
		"plan_kind": "metered", "base_url": "https://openrouter.ai/api", "api_key": newKey,
		"models": []string{modelID}, "visibility": domain.VisibilityEveryone,
	})
	if _, err := r.owner.Exec(ctx, `
		INSERT INTO gateway.account_models_seen (account_id, model_id, position, pricing)
		VALUES ($1::uuid, $2, 1, '{"prompt":"0.000004","completion":"0.000016"}'::jsonb)`,
		bobAcct.ID, modelID); err != nil {
		t.Fatal(err)
	}
	_ = sealed()

	// Non-manager member (ada) can read the shared account's pricing, cannot write.
	if code, _ := r.call(ada, http.MethodGet, "/api/providers/"+bobAcct.ID+"/pricing?model="+url.QueryEscape(modelID), nil); code != http.StatusOK {
		t.Fatalf("member GET shared pricing = %d, want 200", code)
	}
	if code, _ := r.call(ada, http.MethodPut, "/api/providers/"+bobAcct.ID+"/overrides", map[string]any{
		"model": modelID, "input_micro_usd_per_mtok": 5_000_000,
	}); code != http.StatusForbidden {
		t.Fatalf("member PUT shared override = %d, want 403", code)
	}
	_ = sealed()

	// Direct-SQL RLS check under upmo_app on a visible shared account (bobAcct):
	// with settings:manage, INSERT, UPDATE, and DELETE all work; without
	// settings:manage, INSERT fails with 42501 and UPDATE/DELETE affect 0 rows.
	runSQLAs := func(userID, allow, sqlQuery string, args ...any) (int64, error) {
		tx, err := r.app.Begin(ctx)
		if err != nil {
			return 0, err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SELECT set_config('upmo.user_id', $1, true), set_config('upmo.allow', $2, true)`, userID, allow); err != nil {
			return 0, err
		}
		tag, err := tx.Exec(ctx, sqlQuery, args...)
		if err != nil {
			return 0, err
		}
		if err := tx.Commit(ctx); err != nil {
			return 0, err
		}
		return tag.RowsAffected(), nil
	}
	rlsModel := "openai/gpt-4o"
	if n, err := runSQLAs(bob, domain.SharePermission, `
		INSERT INTO gateway.price_overrides (account_id, model, input_micro_usd_per_mtok, updated_by)
		VALUES ($1::uuid, $2, 1111111, $3::uuid)`, bobAcct.ID, rlsModel, bob); err != nil || n != 1 {
		t.Fatalf("direct SQL INSERT with settings:manage n=%d err=%v, want 1 nil", n, err)
	}
	if n, err := runSQLAs(bob, domain.SharePermission, `
		UPDATE gateway.price_overrides SET input_micro_usd_per_mtok = 2222222
		WHERE account_id = $1::uuid AND model = $2`, bobAcct.ID, rlsModel); err != nil || n != 1 {
		t.Fatalf("direct SQL UPDATE with settings:manage n=%d err=%v, want 1 nil", n, err)
	}
	if _, sqlErr := runSQLAs(ada, "", `
		INSERT INTO gateway.price_overrides (account_id, model, input_micro_usd_per_mtok, updated_by)
		VALUES ($1::uuid, 'other/model', 3333333, $2::uuid)`, bobAcct.ID, ada); !errors.As(sqlErr, &pgErr) || pgErr.Code != "42501" {
		t.Fatalf("direct SQL INSERT without settings:manage = %v, want SQLSTATE 42501", sqlErr)
	}
	if n, err := runSQLAs(ada, "", `
		UPDATE gateway.price_overrides SET input_micro_usd_per_mtok = 9999999
		WHERE account_id = $1::uuid AND model = $2`, bobAcct.ID, rlsModel); err != nil || n != 0 {
		t.Fatalf("direct SQL UPDATE without settings:manage n=%d err=%v, want 0 nil", n, err)
	}
	if n, err := runSQLAs(ada, "", `
		DELETE FROM gateway.price_overrides WHERE account_id = $1::uuid AND model = $2`, bobAcct.ID, rlsModel); err != nil || n != 0 {
		t.Fatalf("direct SQL DELETE without settings:manage n=%d err=%v, want 0 nil", n, err)
	}
	if r.count(`SELECT count(*) FROM gateway.price_overrides WHERE account_id = $1::uuid AND model = $2 AND input_micro_usd_per_mtok = 2222222`, bobAcct.ID, rlsModel) != 1 {
		t.Fatal("existing override row was modified or deleted without settings:manage")
	}
	if n, err := runSQLAs(bob, domain.SharePermission, `
		DELETE FROM gateway.price_overrides WHERE account_id = $1::uuid AND model = $2`, bobAcct.ID, rlsModel); err != nil || n != 1 {
		t.Fatalf("direct SQL DELETE with settings:manage n=%d err=%v, want 1 nil", n, err)
	}

	// Admin sets override (>20% above OpenRouter's $4 input -> warning against provider list!).
	code, raw = r.call(bob, http.MethodPut, "/api/providers/"+bobAcct.ID+"/overrides", map[string]any{
		"model":                    modelID,
		"input_micro_usd_per_mtok": 6_000_000,
	})
	if code != http.StatusOK {
		t.Fatalf("admin PUT override = %d %s", code, raw)
	}
	view = decode[app.ModelPricingView](t, raw)
	if view.Override == nil || *view.Override.InputMicroUSDPerMTok != 6_000_000 ||
		view.Merged.Sources.InputPerMTok != domain.SourceOverride || *view.Merged.InputPerMTok != 6.0 {
		t.Fatalf("admin PUT view = %+v", view)
	}
	if len(view.Warnings) != 1 || view.Warnings[0].BaseSource != domain.SourceProvider || view.Warnings[0].BaseMicro != 4_000_000 {
		t.Fatalf("warnings = %+v, want warning vs provider 4_000_000", view.Warnings)
	}
	events := sealed()
	if len(events) != 1 || events[0].Action != audit.PriceOverrideSet || events[0].TargetID != bobAcct.ID {
		t.Fatalf("set audit events = %+v", events)
	}
	if got := events[0].Changes.Values["input_micro_usd_per_mtok"]; got.From != nil || got.To != float64(6_000_000) {
		t.Fatalf("set audit change = %+v", got)
	}

	// An identical repeat by a different admin produces NO audit event and
	// leaves the original audit metadata untouched.
	var beforeBy string
	var beforeAt time.Time
	if err := r.owner.QueryRow(ctx, `
		SELECT updated_by::text, updated_at FROM gateway.price_overrides
		WHERE account_id = $1::uuid AND model = $2`, bobAcct.ID, modelID).Scan(&beforeBy, &beforeAt); err != nil {
		t.Fatal(err)
	}
	code, raw = r.call(eve, http.MethodPut, "/api/providers/"+bobAcct.ID+"/overrides", map[string]any{
		"model":                    modelID,
		"input_micro_usd_per_mtok": 6_000_000,
	})
	if code != http.StatusOK {
		t.Fatalf("repeat PUT = %d %s", code, raw)
	}
	if events := sealed(); len(events) != 0 {
		t.Fatalf("repeat identical PUT produced audit events: %+v", events)
	}
	var afterBy string
	var afterAt time.Time
	if err := r.owner.QueryRow(ctx, `
		SELECT updated_by::text, updated_at FROM gateway.price_overrides
		WHERE account_id = $1::uuid AND model = $2`, bobAcct.ID, modelID).Scan(&afterBy, &afterAt); err != nil {
		t.Fatal(err)
	}
	if afterBy != beforeBy || !afterAt.Equal(beforeAt) {
		t.Fatalf("repeat save changed metadata from (%s, %v) to (%s, %v)", beforeBy, beforeAt, afterBy, afterAt)
	}

	// Update override with >20% warning on cache_read (against models.dev $0.30 since OpenRouter list lacks cache_read).
	code, raw = r.call(bob, http.MethodPut, "/api/providers/"+bobAcct.ID+"/overrides", map[string]any{
		"model":                         modelID,
		"input_micro_usd_per_mtok":      6_000_000,
		"cache_read_micro_usd_per_mtok": 500_000,
	})
	if code != http.StatusOK {
		t.Fatalf("update PUT = %d %s", code, raw)
	}
	view = decode[app.ModelPricingView](t, raw)
	if len(view.Warnings) != 2 || view.Warnings[1].BaseSource != domain.SourceModelsDev || view.Warnings[1].BaseMicro != 300_000 {
		t.Fatalf("update warnings = %+v", view.Warnings)
	}
	events = sealed()
	if len(events) != 1 || events[0].Action != audit.PriceOverrideSet {
		t.Fatalf("update audit events = %+v", events)
	}
	if _, hasInput := events[0].Changes.Values["input_micro_usd_per_mtok"]; hasInput {
		t.Fatalf("unchanged input field included in update audit: %+v", events[0].Changes)
	}
	if got := events[0].Changes.Values["cache_read_micro_usd_per_mtok"]; got.From != nil || got.To != float64(500_000) {
		t.Fatalf("cache_read audit change = %+v", got)
	}

	// Delete override -> audited as PriceOverrideDelete; repeat DELETE -> NoChange.
	code, raw = r.call(bob, http.MethodDelete, "/api/providers/"+bobAcct.ID+"/overrides", map[string]any{
		"model": modelID,
	})
	if code != http.StatusOK {
		t.Fatalf("DELETE override = %d %s", code, raw)
	}
	view = decode[app.ModelPricingView](t, raw)
	if view.Override != nil || view.Merged.Sources.InputPerMTok != domain.SourceProvider {
		t.Fatalf("after DELETE view = %+v", view)
	}
	if events := sealed(); len(events) != 1 || events[0].Action != audit.PriceOverrideDelete {
		t.Fatalf("delete audit events = %+v", events)
	}
	code, _ = r.call(bob, http.MethodDelete, "/api/providers/"+bobAcct.ID+"/overrides", map[string]any{
		"model": modelID,
	})
	if code != http.StatusOK {
		t.Fatalf("repeat DELETE = %d", code)
	}
	if events := sealed(); len(events) != 0 {
		t.Fatalf("repeat DELETE produced audit events: %+v", events)
	}
}

func newAuditSealer(t *testing.T, owner *pgxpool.Pool) func() []audit.Row {
	t.Helper()
	var lastPos int64
	return func() []audit.Row {
		t.Helper()
		ctx := context.Background()
		tx, err := owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		// Seal pending rows via audit.SealBatch / read pending directly.
		rows, err := tx.Query(ctx, `
			SELECT id::text, action, outcome, COALESCE(target_id::text, ''), changes
			FROM audit.pending
			WHERE id IS NOT NULL
			ORDER BY occurred_at, id`)
		if err != nil {
			t.Fatal(err)
		}
		var out []audit.Row
		for rows.Next() {
			var row audit.Row
			var ch []byte
			if err := rows.Scan(&row.ID, &row.Action, &row.Outcome, &row.TargetID, &ch); err != nil {
				t.Fatal(err)
			}
			_ = json.Unmarshal(ch, &row.Changes)
			out = append(out, row)
		}
		rows.Close()
		if _, err := tx.Exec(ctx, `DELETE FROM audit.pending`); err != nil {
			t.Fatal(err)
		}
		_ = lastPos
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return out
	}
}

type failingCallPricer struct {
	inner postgres.Store
	err   error
}

func (f failingCallPricer) Price(ctx context.Context, vendor, model string) (domain.Price, bool, error) {
	return f.inner.Price(ctx, vendor, model)
}

func (f failingCallPricer) LookupCallPricing(context.Context, string, string, string) (app.CallPricing, error) {
	return app.CallPricing{}, f.err
}

type failingSimplePrices struct {
	err error
}

func (f failingSimplePrices) Price(context.Context, string, string) (domain.Price, bool, error) {
	return domain.Price{}, false, f.err
}

type failingCallPricerWithHighestRate struct {
	inner             postgres.Store
	rate              int64
	err               error
	block             bool
	highestAfterFirst error
	highestCalls      *atomic.Int32
}

func (f failingCallPricerWithHighestRate) Price(ctx context.Context, vendor, model string) (domain.Price, bool, error) {
	return f.inner.Price(ctx, vendor, model)
}

func (f failingCallPricerWithHighestRate) HighestRate(context.Context, string) (int64, bool, error) {
	if f.highestAfterFirst != nil && f.highestCalls != nil && f.highestCalls.Add(1) > 1 {
		return 0, false, f.highestAfterFirst
	}
	return f.rate, true, nil
}

func (f failingCallPricerWithHighestRate) LookupCallPricing(ctx context.Context, _, _, _ string) (app.CallPricing, error) {
	if f.block {
		<-ctx.Done()
		return app.CallPricing{}, ctx.Err()
	}
	return app.CallPricing{}, f.err
}

func TestFeature_UPM354_PriceLookupFailureFallsBackToEstimate(t *testing.T) {
	posReported := int64(42_000)
	zeroReported := int64(0)
	const knownVendorRate = int64(20_000_000)
	// 1_000_000 input + 500_000 output at $20/MTok = 30_000_000 micro-USD.
	const wantKnownEstimate = int64(30_000_000)
	for _, tc := range []struct {
		name          string
		model         string
		private       bool
		reportedCost  *int64
		wantSource    string
		wantEstimated bool
		wantCost      *int64
		wantErrSubstr string
		pricer        func(store postgres.Store, err error) app.Prices
	}{
		{
			name:          "CallPricer.LookupCallPricing error",
			model:         "gpt-4o",
			wantSource:    "unknown",
			wantEstimated: false,
			pricer: func(store postgres.Store, err error) app.Prices {
				return failingCallPricer{inner: store, err: err}
			},
		},
		{
			name:          "Prices.Price error",
			model:         "gpt-4o",
			wantSource:    "unknown",
			wantEstimated: false,
			pricer: func(_ postgres.Store, err error) app.Prices {
				return failingSimplePrices{err: err}
			},
		},
		{
			name:          "CallPricer.LookupCallPricing error keeps positive reported cost",
			model:         "gpt-4o",
			reportedCost:  &posReported,
			wantSource:    "provider",
			wantEstimated: false,
			wantCost:      &posReported,
			pricer: func(store postgres.Store, err error) app.Prices {
				return failingCallPricer{inner: store, err: err}
			},
		},
		{
			name:          "CallPricer.LookupCallPricing error accepts reported zero on :free",
			model:         "gpt-4o:free",
			reportedCost:  &zeroReported,
			wantSource:    "provider",
			wantEstimated: false,
			wantCost:      &zeroReported,
			pricer: func(store postgres.Store, err error) app.Prices {
				return failingCallPricer{inner: store, err: err}
			},
		},
		{
			name:          "CallPricer.LookupCallPricing error rejects reported zero on paid model",
			model:         "gpt-4o",
			reportedCost:  &zeroReported,
			wantSource:    "unknown",
			wantEstimated: false,
			pricer: func(store postgres.Store, err error) app.Prices {
				return failingCallPricer{inner: store, err: err}
			},
		},
		{
			name:          "CallPricer.LookupCallPricing error uses saved reservation vendor rate",
			model:         "gpt-4o",
			wantSource:    "estimate",
			wantEstimated: true,
			wantCost:      ptrInt64(wantKnownEstimate),
			pricer: func(store postgres.Store, err error) app.Prices {
				return failingCallPricerWithHighestRate{inner: store, rate: knownVendorRate, err: err}
			},
		},
		{
			name:          "CallPricer.LookupCallPricing and post-call HighestRate errors use saved reservation vendor rate",
			model:         "gpt-4o",
			wantSource:    "estimate",
			wantEstimated: true,
			wantCost:      ptrInt64(wantKnownEstimate),
			pricer: func(store postgres.Store, err error) app.Prices {
				return failingCallPricerWithHighestRate{
					inner:             store,
					rate:              knownVendorRate,
					err:               err,
					highestAfterFirst: err,
					highestCalls:      &atomic.Int32{},
				}
			},
		},
		{
			name:          "CallPricer.LookupCallPricing blocking past deadline still settles",
			model:         "gpt-4o",
			wantSource:    "estimate",
			wantEstimated: true,
			wantCost:      ptrInt64(wantKnownEstimate),
			wantErrSubstr: context.DeadlineExceeded.Error(),
			pricer: func(store postgres.Store, _ error) app.Prices {
				return failingCallPricerWithHighestRate{inner: store, rate: knownVendorRate, block: true}
			},
		},
		{
			name:          "private account retries vendor highest rate on lookup failure",
			model:         "gpt-4o",
			private:       true,
			wantSource:    "estimate",
			wantEstimated: true,
			wantCost:      ptrInt64(wantKnownEstimate),
			pricer: func(store postgres.Store, err error) app.Prices {
				return failingCallPricerWithHighestRate{inner: store, rate: knownVendorRate, err: err}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			r := newRig(t, masterKeys(t, 'a'))
			store := postgres.Store{Pool: r.owner}

			acctID := insertProviderAndRoute(t, r, "r-fail-price", "openai", "OAI FailPrice", "ref_fp", tc.model, true)
			if !tc.private {
				if _, err := r.owner.Exec(ctx, `UPDATE gateway.provider_accounts SET visibility = 'everyone' WHERE id = $1::uuid`, acctID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := r.owner.Exec(ctx, `INSERT INTO gateway.usage_limits (kind, amount_micro_usd) VALUES ('workspace', 1000000000)`); err != nil {
				t.Fatal(err)
			}

			var logBuf syncBuf
			lookupErr := errors.New("db pricing lookup boom")
			chatter := &scriptChatter{usage: domain.Usage{
				Arrived:              true,
				Input:                1_000_000,
				Output:               500_000,
				ReportedCostMicroUSD: tc.reportedCost,
			}}
			disp := app.Dispatch{
				Routes:  store,
				Teams:   store,
				Prices:  tc.pricer(store, lookupErr),
				Ledger:  store,
				Limits:  store,
				Secrets: staticSecrets{"ref_fp": "k"},
				Chat:    map[string]app.Chatter{domain.AdapterOpenAI: chatter, domain.AdapterFake: chatter},
				Log:     slog.New(slog.NewTextHandler(&logBuf, nil)),
			}

			who := domain.Principal{User: ada, Source: domain.SourceChat}
			in := domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "bill me"}}}
			res, err := disp.ChatCall(ctx, "r-fail-price", "", who, in)
			if err != nil {
				t.Fatalf("ChatCall failed on pricing lookup error: %v", err)
			}
			if tc.wantSource == "unknown" {
				if res.CostMicroUSD != nil {
					t.Fatalf("CostMicroUSD = %v, want nil for unknown", res.CostMicroUSD)
				}
			} else {
				if res.CostMicroUSD == nil || (tc.wantCost != nil && *res.CostMicroUSD != *tc.wantCost) || (tc.wantCost == nil && *res.CostMicroUSD <= 0) {
					t.Fatalf("CostMicroUSD = %v, want %v", res.CostMicroUSD, tc.wantCost)
				}
			}

			wantErrSub := lookupErr.Error()
			if tc.wantErrSubstr != "" {
				wantErrSub = tc.wantErrSubstr
			}
			lines := strings.Split(strings.TrimSpace(logBuf.String()), "\n")
			if len(lines) != 1 || !strings.Contains(lines[0], wantErrSub) {
				t.Fatalf("expected 1 log line containing %q, got %q", wantErrSub, logBuf.String())
			}

			var rowCount int
			var costMicro *int64
			var estimated, charged bool
			var priceSource string
			if err := r.owner.QueryRow(ctx, `
				SELECT count(*), max(cost_micro_usd), bool_and(estimated), bool_and(charged_to_limit), COALESCE(max(price_source), '')
				FROM gateway.ledger`,
			).Scan(&rowCount, &costMicro, &estimated, &charged, &priceSource); err != nil {
				t.Fatal(err)
			}
			wantCharged := !tc.private
			costMatches := (costMicro == nil) == (res.CostMicroUSD == nil) && (costMicro == nil || *costMicro == *res.CostMicroUSD)
			if rowCount != 1 || priceSource != tc.wantSource || estimated != tc.wantEstimated || charged != wantCharged || !costMatches {
				t.Fatalf("ledger = count:%d cost:%v est:%t charged:%t src:%q, want count:1 cost:%v est:%t charged:%t src:%s",
					rowCount, costMicro, estimated, charged, priceSource, res.CostMicroUSD, tc.wantEstimated, wantCharged, tc.wantSource)
			}

			var wantSpent int64
			if wantCharged && costMicro != nil {
				wantSpent = *costMicro
			}
			var held, spent int64
			if err := r.owner.QueryRow(ctx, `
				SELECT COALESCE(sum(held_micro_usd), 0), COALESCE(sum(spent_micro_usd), 0)
				FROM gateway.usage_spend
				WHERE kind = 'workspace'`).Scan(&held, &spent); err != nil {
				t.Fatal(err)
			}
			if held != 0 || spent != wantSpent {
				t.Fatalf("usage_spend held=%d spent=%d, want 0 and %d", held, spent, wantSpent)
			}

			var openHolds int
			if err := r.owner.QueryRow(ctx, `SELECT count(*) FROM gateway.usage_holds`).Scan(&openHolds); err != nil {
				t.Fatal(err)
			}
			if openHolds != 0 {
				t.Fatalf("open usage_holds = %d, want 0", openHolds)
			}
		})
	}
}

func TestFeature_UPM354_DailyRefreshScheduling(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, masterKeys(t, 'a'))
	if err := jobs.Up(ctx, r.owner); err != nil {
		t.Fatal(err)
	}

	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	initJSON := makeValidModelsDevJSON(200, 12, 1.0)
	initRows, err := domain.ParseCatalog(bytes.NewReader(initJSON), "init-snap")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := postgres.SeedCatalog(ctx, r.owner, "init-snap", t0, initRows); err != nil {
		t.Fatal(err)
	}

	var hits atomic.Int32
	var statusCode atomic.Int32
	statusCode.Store(http.StatusOK)
	validPayload := makeValidModelsDevJSON(205, 12, 2.0)
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		code := int(statusCode.Load())
		w.WriteHeader(code)
		if code == http.StatusOK {
			_, _ = w.Write(validPayload)
		} else {
			_, _ = w.Write([]byte("upstream down"))
		}
	}))
	t.Cleanup(tlsSrv.Close)

	var curNow atomic.Pointer[time.Time]
	startNow := t0.Add(24 * time.Hour)
	curNow.Store(&startNow)

	var logBuf syncBuf
	deps := CatalogRefreshDeps{
		Owner:  r.owner,
		URL:    tlsSrv.URL,
		Client: tlsSrv.Client(),
		Now:    func() time.Time { return *curNow.Load() },
		Log:    slog.New(slog.NewTextHandler(&logBuf, nil)),
	}

	// Verify catalogRefreshInsertOpts() deduplicates within the same UTC hour
	// and allows a job scheduled in the next hour, without relying on River
	// leader election.
	workers := river.NewWorkers()
	AddCatalogRefresher(workers, deps)
	c, err := jobs.Open(r.owner,
		jobs.WithWorkers(workers),
		jobs.WithPeriodic(CatalogRefreshPeriodic()),
	)
	if err != nil {
		t.Fatal(err)
	}

	insertRefresh := func(scheduledAt time.Time) bool {
		t.Helper()
		tx, err := r.owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		opts := catalogRefreshInsertOpts()
		opts.ScheduledAt = scheduledAt
		res, err := c.InsertTx(ctx, tx, CatalogRefreshArgs{}, opts)
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return res.UniqueSkippedAsDuplicate
	}

	hour1 := time.Now().UTC().Truncate(time.Hour).Add(2*time.Hour + 10*time.Minute)
	if skipped := insertRefresh(hour1); skipped {
		t.Fatal("first insert in hour1 was unexpectedly skipped as duplicate")
	}
	if skipped := insertRefresh(hour1.Add(30 * time.Minute)); !skipped {
		t.Fatal("second insert in same hour was not skipped as duplicate")
	}
	if n := r.count(`SELECT count(*) FROM platform.river_job WHERE kind = 'gateway_catalog_refresh'`); n != 1 {
		t.Fatalf("river_job count after same-hour duplicate insert = %d, want 1", n)
	}
	if skipped := insertRefresh(hour1.Add(time.Hour)); skipped {
		t.Fatal("insert in next hour was unexpectedly skipped as duplicate")
	}
	if n := r.count(`SELECT count(*) FROM platform.river_job WHERE kind = 'gateway_catalog_refresh'`); n != 2 {
		t.Fatalf("river_job count after next-hour insert = %d, want 2", n)
	}

	// Start the River client to verify RunOnStart triggers one immediate fetch.
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for hits.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	_ = c.Stop(ctx)
	if hits.Load() != 1 {
		t.Fatalf("RunOnStart hits = %d, want 1", hits.Load())
	}

	// Two additional runs within 24 hours do not fetch again.
	for _, offset := range []time.Duration{time.Hour, 23 * time.Hour} {
		next := startNow.Add(offset)
		curNow.Store(&next)
		if err := RefreshCatalogIfDue(ctx, deps); err != nil {
			t.Fatalf("RefreshCatalogIfDue(+%v) err = %v", offset, err)
		}
		if hits.Load() != 1 {
			t.Fatalf("within 24h (+%v) hits = %d, want 1", offset, hits.Load())
		}
	}

	// 25 hours later fetches again.
	t2 := startNow.Add(25 * time.Hour)
	curNow.Store(&t2)
	if err := RefreshCatalogIfDue(ctx, deps); err != nil {
		t.Fatalf("RefreshCatalogIfDue(+25h) err = %v", err)
	}
	if hits.Load() != 2 {
		t.Fatalf("at +25h hits = %d, want 2", hits.Load())
	}

	// A failed attempt sets last_attempt_at and also waits 24 hours before the next.
	statusCode.Store(http.StatusInternalServerError)
	t3 := t2.Add(25 * time.Hour)
	curNow.Store(&t3)
	if err := RefreshCatalogIfDue(ctx, deps); err == nil {
		t.Fatal("expected error on 500 refresh attempt")
	}
	if hits.Load() != 3 {
		t.Fatalf("failed attempt hits = %d, want 3", hits.Load())
	}
	meta, _, _ := postgres.GetCatalogMeta(ctx, r.owner)
	if meta.LastAttemptAt == nil || !meta.LastAttemptAt.UTC().Equal(t3) || meta.LastError == "" {
		t.Fatalf("after failed attempt meta = %+v, want last_attempt_at=%v and non-empty last_error", meta, t3)
	}

	for _, offset := range []time.Duration{time.Hour, 23 * time.Hour} {
		next := t3.Add(offset)
		curNow.Store(&next)
		if err := RefreshCatalogIfDue(ctx, deps); err != nil {
			t.Fatalf("within 24h after failed attempt (+%v) err = %v", offset, err)
		}
		if hits.Load() != 3 {
			t.Fatalf("within 24h after failed attempt (+%v) hits = %d, want 3", offset, hits.Load())
		}
	}

	statusCode.Store(http.StatusOK)
	t4 := t3.Add(25 * time.Hour)
	curNow.Store(&t4)
	if err := RefreshCatalogIfDue(ctx, deps); err != nil {
		t.Fatalf("25h after failed attempt err = %v", err)
	}
	if hits.Load() != 4 {
		t.Fatalf("25h after failed attempt hits = %d, want 4", hits.Load())
	}
}

func TestFeature_UPM354_FreshInstallPricesFromSnapshotNoNetwork(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, masterKeys(t, 'a'))

	if err := SeedCatalog(ctx, r.owner); err != nil {
		t.Fatalf("SeedCatalog: %v", err)
	}
	snapVer, _, snapRows, err := snapshot.Load()
	if err != nil {
		t.Fatal(err)
	}
	var target *domain.CatalogRow
	for i := range snapRows {
		row := &snapRows[i]
		v, ok := domain.VendorFor(row.Vendor)
		if ok && !v.Local && row.Kind == domain.KindChat &&
			row.Price.InputMicroUSDPerMTok != nil && *row.Price.InputMicroUSDPerMTok > 0 &&
			row.Price.OutputMicroUSDPerMTok != nil && *row.Price.OutputMicroUSDPerMTok > 0 {
			target = row
			break
		}
	}
	if target == nil {
		t.Fatal("no priced chat model found in embedded snapshot")
	}

	store := postgres.Store{Pool: r.owner}
	insertProviderAndRoute(t, r, "r-snap-only", target.Vendor, "Snap Acct", "ref_snap", target.Model, true)

	chatter := &scriptChatter{usage: domain.Usage{Arrived: true, Input: 1_000_000, Output: 500_000}}
	disp := app.Dispatch{
		Routes:  store,
		Teams:   store,
		Prices:  store,
		Ledger:  store,
		Limits:  store,
		Secrets: staticSecrets{"ref_snap": "k"},
		Chat:    map[string]app.Chatter{domain.AdapterOpenAI: chatter, domain.AdapterAnthropic: chatter, domain.AdapterFake: chatter},
	}

	who := domain.Principal{User: ada, Source: domain.SourceChat}
	in := domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "offline check"}}}
	res, err := disp.ChatCall(ctx, "r-snap-only", "", who, in)
	if err != nil {
		t.Fatalf("ChatCall: %v", err)
	}
	wantCost := (*target.Price.InputMicroUSDPerMTok*1_000_000 + *target.Price.OutputMicroUSDPerMTok*500_000 + 500_000) / 1_000_000
	if res.CostMicroUSD == nil || *res.CostMicroUSD != wantCost {
		t.Fatalf("CostMicroUSD = %v, want %d", res.CostMicroUSD, wantCost)
	}

	var count int
	var costMicro int64
	var estimated bool
	var priceSource, priceVersion string
	if err := r.owner.QueryRow(ctx, `
		SELECT count(*), COALESCE(max(cost_micro_usd), 0), bool_and(estimated), COALESCE(max(price_source), ''), COALESCE(max(price_version), '')
		FROM gateway.ledger`,
	).Scan(&count, &costMicro, &estimated, &priceSource, &priceVersion); err != nil {
		t.Fatal(err)
	}
	if count != 1 || costMicro != wantCost || estimated || priceSource != "models_dev" || priceVersion != snapVer {
		t.Fatalf("ledger = count:%d cost:%d est:%t src:%q ver:%q, want count:1 cost:%d est:false src:models_dev ver:%q",
			count, costMicro, estimated, priceSource, priceVersion, wantCost, snapVer)
	}
	meta, ok, err := postgres.GetCatalogMeta(ctx, r.owner)
	if err != nil || !ok || meta.RefreshedAt != nil {
		t.Fatalf("expected snapshot-only meta with refreshed_at=nil, got %+v ok=%v err=%v", meta, ok, err)
	}
}

func TestFeature_UPM354_MultiProviderPricingAndEstimates(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, masterKeys(t, 'a'))
	if err := notices.Up(ctx, r.owner); err != nil {
		t.Fatal(err)
	}
	store := postgres.Store{Pool: r.owner}

	// Create settings:manage owner (bob) and non-manager member (ada) in identity.
	for _, p := range []struct {
		id, email, name, roleKey string
	}{
		{bob, "bob@example.com", "Bob", "owner"},
		{ada, "ada@example.com", "Ada", "member"},
	} {
		if _, err := r.owner.Exec(ctx, `
			INSERT INTO identity.users (id, email, name, role_id, joined_at)
			VALUES ($1::uuid, $2, $3, (SELECT id FROM identity.roles WHERE key = $4), now())
			ON CONFLICT (id) DO UPDATE SET role_id = EXCLUDED.role_id, joined_at = now(), disabled_at = NULL`,
			p.id, p.email, p.name, p.roleKey); err != nil {
			t.Fatal(err)
		}
	}

	// Parse a multi-provider models.dev catalogue where:
	// - "shared-dual" is under openai ($2/$10) and openrouter ($4/$12).
	// - "cross-only" is under openrouter ($6 input, $10 output, $0.8 cache_read, $2 cache_write)
	//   and groq ($3 input, $20 output, $0.4 cache_read, $8 cache_write), so '' holds ($6, $20, $0.8, $8).
	// - "openai-highest" gives openai a highest rate of $30/MTok.
	const catalogJSON = `{
		"openai": {
			"models": {
				"shared-dual": {"cost": {"input": 2, "output": 10}},
				"openai-highest": {"cost": {"input": 5, "output": 30}}
			}
		},
		"openrouter": {
			"models": {
				"shared-dual": {"cost": {"input": 4, "output": 12}},
				"cross-only": {"cost": {"input": 6, "output": 10, "cache_read": 0.8, "cache_write": 2}}
			}
		},
		"groq": {
			"models": {
				"cross-only": {"cost": {"input": 3, "output": 20, "cache_read": 0.4, "cache_write": 8}}
			}
		}
	}`
	rows, err := domain.ParseCatalog(strings.NewReader(catalogJSON), "multi-v1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceCatalog(ctx, "multi-v1", rows); err != nil {
		t.Fatal(err)
	}

	// Workspace spend limit so settle/holds are also exercised.
	if _, err := r.owner.Exec(ctx, `INSERT INTO gateway.usage_limits (kind, amount_micro_usd) VALUES ('workspace', 10000000000)`); err != nil {
		t.Fatal(err)
	}

	insertProviderAndRoute(t, r, "r-oai-dual", "openai", "OpenAI Dual", "ref_oai_dual", "shared-dual", true)
	insertProviderAndRoute(t, r, "r-or-dual", "openrouter", "OpenRouter Dual", "ref_or_dual", "shared-dual", true)
	anthCrossAcct := insertProviderAndRoute(t, r, "r-anth-cross", "anthropic", "Anthropic Cross", "ref_anth_cross", "cross-only", true)
	insertProviderAndRoute(t, r, "r-oai-unknown", "openai", "OpenAI UnknownModel", "ref_oai_unk", "oai-unlisted-model", true)
	unknownAcct := insertProviderAndRoute(t, r, "r-host-unknown", "custom-proxy", "Custom Proxy", "ref_host_unk", "private-custom-model", true)
	failNoticeAcct := insertProviderAndRoute(t, r, "r-host-failnotice", "custom-proxy-2", "Custom Proxy FailNotice", "ref_host_fail", "private-fail-model", true)

	chatter := &scriptChatter{
		usage: domain.Usage{
			Arrived:    true,
			Input:      1_000_000,
			Output:     500_000,
			CacheRead:  1_000_000,
			CacheWrite: 1_000_000,
		},
	}
	var logBuf syncBuf
	notifier := postgres.EstimateNotifier{
		Pool: r.owner,
		SettingsManagers: func(ctx context.Context) ([]string, error) {
			return identity.SettingsManagers(ctx, r.owner)
		},
		Notify: func(ctx context.Context, tx pgx.Tx, userID, title string) error {
			uid, err := uuid.Parse(userID)
			if err != nil {
				return err
			}
			_, err = notices.InsertTx(ctx, tx, uid, notices.KindFailed, title, nil)
			return err
		},
	}
	disp := app.Dispatch{
		Routes: store,
		Teams:  store,
		Prices: store,
		Ledger: store,
		Limits: store,
		Secrets: staticSecrets{
			"ref_oai_dual":   "k",
			"ref_or_dual":    "k",
			"ref_anth_cross": "k",
			"ref_oai_unk":    "k",
			"ref_host_unk":   "k",
			"ref_host_fail":  "k",
		},
		Chat:           map[string]app.Chatter{domain.AdapterOpenAI: chatter, domain.AdapterAnthropic: chatter, domain.AdapterFake: chatter},
		EstimateNotice: notifier,
		Log:            slog.New(slog.NewTextHandler(&logBuf, nil)),
	}
	who := domain.Principal{User: ada, Source: domain.SourceChat}
	in := domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "12345678"}}}

	type ledgerCheck struct {
		cost      *int64
		estimated bool
		source    string
	}
	checkEq := func(got, want ledgerCheck) bool {
		costEq := (got.cost == nil) == (want.cost == nil) && (want.cost == nil || *got.cost == *want.cost)
		return costEq && got.estimated == want.estimated && got.source == want.source
	}
	latestLedger := func(route string) ledgerCheck {
		t.Helper()
		var lc ledgerCheck
		if err := r.owner.QueryRow(ctx, `
			SELECT cost_micro_usd, estimated, price_source
			FROM gateway.ledger
			WHERE route = $1
			ORDER BY at DESC, id DESC
			LIMIT 1`, route).Scan(&lc.cost, &lc.estimated, &lc.source); err != nil {
			t.Fatalf("query ledger for %s: %v", route, err)
		}
		return lc
	}

	// 1. Two providers listing shared-dual at different prices: each uses its own rate as models_dev, not estimated.
	chatter.usage = domain.Usage{Arrived: true, Input: 1_000_000, Output: 500_000}
	if _, err := disp.ChatCall(ctx, "r-oai-dual", "", who, in); err != nil {
		t.Fatalf("r-oai-dual: %v", err)
	}
	if got := latestLedger("r-oai-dual"); !checkEq(got, ledgerCheck{cost: ptrInt64(7_000_000), estimated: false, source: "models_dev"}) {
		t.Fatalf("r-oai-dual ledger = %+v, want 7_000_000 models_dev estimated=false", got)
	}
	if _, err := disp.ChatCall(ctx, "r-or-dual", "", who, in); err != nil {
		t.Fatalf("r-or-dual: %v", err)
	}
	if got := latestLedger("r-or-dual"); !checkEq(got, ledgerCheck{cost: ptrInt64(10_000_000), estimated: false, source: "models_dev"}) {
		t.Fatalf("r-or-dual ledger = %+v, want 10_000_000 models_dev estimated=false", got)
	}

	// 2. 5a: Model listed only under openrouter & groq, called through anthropic -> estimated at per-dimension max.
	// 1M * $6 + 0.5M * $20 + 1M * $0.8 + 1M * $8 = 6M + 10M + 0.8M + 8M = 24_800_000.
	chatter.usage = domain.Usage{Arrived: true, Input: 1_000_000, Output: 500_000, CacheRead: 1_000_000, CacheWrite: 1_000_000}
	if _, err := disp.ChatCall(ctx, "r-anth-cross", "", who, in); err != nil {
		t.Fatalf("r-anth-cross: %v", err)
	}
	if got := latestLedger("r-anth-cross"); !checkEq(got, ledgerCheck{cost: ptrInt64(24_800_000), estimated: true, source: "estimate"}) {
		t.Fatalf("r-anth-cross ledger = %+v, want 24_800_000 estimate estimated=true", got)
	}

	// 3. 5b: Known provider (openai) with unknown model -> estimated at openai's highest catalogue rate ($30/MTok).
	// 1M * $30 + 0.5M * $30 = 45_000_000. No 5c notice sent.
	chatter.usage = domain.Usage{Arrived: true, Input: 1_000_000, Output: 500_000}
	if _, err := disp.ChatCall(ctx, "r-oai-unknown", "", who, in); err != nil {
		t.Fatalf("r-oai-unknown: %v", err)
	}
	if got := latestLedger("r-oai-unknown"); !checkEq(got, ledgerCheck{cost: ptrInt64(45_000_000), estimated: true, source: "estimate"}) {
		t.Fatalf("r-oai-unknown ledger = %+v, want 45_000_000 estimate estimated=true", got)
	}
	if n := r.count(`SELECT count(*) FROM notices.notifications`); n != 0 {
		t.Fatalf("unexpected notices after 5a/5b calls: %d", n)
	}

	var spentBefore5c int64
	if err := r.owner.QueryRow(ctx, `SELECT COALESCE(sum(spent_micro_usd), 0) FROM gateway.usage_spend WHERE kind = 'workspace'`).Scan(&spentBefore5c); err != nil {
		t.Fatal(err)
	}

	// 4. 5c: Unknown host with unknown model -> NULL cost, estimated=false, price_source='unknown'.
	// Spend is unchanged and held is back to 0. Exactly ONE notice sent across two calls (to settings:manage bob only).
	for i := range 2 {
		if _, err := disp.ChatCall(ctx, "r-host-unknown", "", who, in); err != nil {
			t.Fatalf("r-host-unknown call %d: %v", i+1, err)
		}
		if got := latestLedger("r-host-unknown"); !checkEq(got, ledgerCheck{cost: nil, estimated: false, source: "unknown"}) {
			t.Fatalf("r-host-unknown call %d ledger = %+v, want nil cost unknown", i+1, got)
		}
	}
	var heldAfter5c, spentAfter5c int64
	if err := r.owner.QueryRow(ctx, `SELECT COALESCE(sum(held_micro_usd), 0), COALESCE(sum(spent_micro_usd), 0) FROM gateway.usage_spend WHERE kind = 'workspace'`).Scan(&heldAfter5c, &spentAfter5c); err != nil {
		t.Fatal(err)
	}
	if heldAfter5c != 0 || spentAfter5c != spentBefore5c {
		t.Fatalf("usage_spend after 5c: held=%d spent=%d, want 0 and %d", heldAfter5c, spentAfter5c, spentBefore5c)
	}
	if n := r.count(`SELECT count(*) FROM notices.notifications WHERE user_id = $1::uuid`, bob); n != 1 {
		t.Fatalf("bob notices after two 5c calls = %d, want 1", n)
	}
	if n := r.count(`SELECT count(*) FROM notices.notifications WHERE user_id = $1::uuid`, ada); n != 0 {
		t.Fatalf("member ada notices = %d, want 0", n)
	}
	var noticeTitle string
	if err := r.owner.QueryRow(ctx, `SELECT title FROM notices.notifications WHERE user_id = $1::uuid`, bob).Scan(&noticeTitle); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(noticeTitle, "private-custom-model") || !strings.Contains(strings.ToLower(noticeTitle), "unknown") || !strings.Contains(noticeTitle, "override") {
		t.Fatalf("5c notice title = %q, want model, unknown, and override suggestion", noticeTitle)
	}
	if n := r.count(`SELECT count(*) FROM gateway.ledger WHERE provider_account_id = $1::uuid AND model = 'private-custom-model' AND unknown_notified_at IS NOT NULL`, unknownAcct); n != 1 {
		t.Fatalf("unknown_notified_at ledger count = %d, want 1", n)
	}

	// 5. Notice failure is best-effort: call still succeeds and settles, and 1 warning line is logged.
	dispFailNotice := disp
	dispFailNotice.EstimateNotice = postgres.EstimateNotifier{
		Pool: r.owner,
		SettingsManagers: func(ctx context.Context) ([]string, error) {
			return []string{bob}, nil
		},
		Notify: func(context.Context, pgx.Tx, string, string) error {
			return errors.New("notice store boom")
		},
	}
	if _, err := dispFailNotice.ChatCall(ctx, "r-host-failnotice", "", who, in); err != nil {
		t.Fatalf("ChatCall failed when notice errored: %v", err)
	}
	if got := latestLedger("r-host-failnotice"); !checkEq(got, ledgerCheck{cost: nil, estimated: false, source: "unknown"}) {
		t.Fatalf("r-host-failnotice ledger = %+v, want nil cost unknown", got)
	}
	if openHolds := r.count(`SELECT count(*) FROM gateway.usage_holds`); openHolds != 0 {
		t.Fatalf("open holds = %d, want 0", openHolds)
	}
	if n := r.count(`SELECT count(*) FROM gateway.ledger WHERE provider_account_id = $1::uuid AND unknown_notified_at IS NOT NULL`, failNoticeAcct); n != 0 {
		t.Fatal("failed notice unexpectedly marked ledger.unknown_notified_at")
	}
	lines := strings.Split(strings.TrimSpace(logBuf.String()), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], "notice store boom") {
		t.Fatalf("expected 1 warning log line on notice failure, got %q", logBuf.String())
	}

	// 6. GetModelPricing & 20% override warning: never compare against '' row; label '' as other_provider.
	r.svc.Allow = func(_ context.Context, user string) (string, error) {
		if user == bob {
			return domain.SharePermission, nil
		}
		return "", nil
	}
	r.svc.LocalPlan = func(context.Context, string) bool { return true }
	r.mount(nil)
	if _, err := r.owner.Exec(ctx, `UPDATE gateway.provider_accounts SET visibility = 'everyone' WHERE id = $1::uuid`, anthCrossAcct); err != nil {
		t.Fatal(err)
	}
	pricingView, err := r.svc.GetModelPricing(ctx, bob, anthCrossAcct, "cross-only")
	if err != nil {
		t.Fatalf("GetModelPricing: %v", err)
	}
	if pricingView.Merged.Sources.InputPerMTok != domain.SourceOtherProvider || *pricingView.Merged.InputPerMTok != 6.0 {
		t.Fatalf("cross-only merged input = %v (%s), want 6.0 other_provider", pricingView.Merged.InputPerMTok, pricingView.Merged.Sources.InputPerMTok)
	}
	// Saving a wildly different override (e.g. $50 vs $6 in '' row) must NOT trigger a 20% warning against ''!
	ovView, err := r.svc.SetPriceOverride(ctx, bob, anthCrossAcct, domain.PriceOverride{
		Model:                "cross-only",
		InputMicroUSDPerMTok: ptrInt64(50_000_000),
	})
	if err != nil {
		t.Fatalf("SetPriceOverride: %v", err)
	}
	if len(ovView.Warnings) != 0 {
		t.Fatalf("expected 0 override warnings against '' row, got %+v", ovView.Warnings)
	}
}

func TestFeature_UPM354_UnknownPriceAndCheckConstraints(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, masterKeys(t, 'a'))
	if err := notices.Up(ctx, r.owner); err != nil {
		t.Fatal(err)
	}
	store := postgres.Store{Pool: r.owner}

	for _, p := range []struct {
		id, email, name, roleKey string
	}{
		{bob, "bob@example.com", "Bob", "owner"},
		{ada, "ada@example.com", "Ada", "member"},
	} {
		if _, err := r.owner.Exec(ctx, `
			INSERT INTO identity.users (id, email, name, role_id, joined_at)
			VALUES ($1::uuid, $2, $3, (SELECT id FROM identity.roles WHERE key = $4), now())
			ON CONFLICT (id) DO UPDATE SET role_id = EXCLUDED.role_id, joined_at = now(), disabled_at = NULL`,
			p.id, p.email, p.name, p.roleKey); err != nil {
			t.Fatal(err)
		}
	}

	acctID := insertProviderAndRoute(t, r, "r-unk-spend", "unknown-host-x", "Unknown Host X", "ref_unk_x", "unpriced-x", true)
	if _, err := r.owner.Exec(ctx, `UPDATE gateway.provider_accounts SET visibility = 'everyone' WHERE id = $1::uuid`, acctID); err != nil {
		t.Fatal(err)
	}
	// Workspace limit allows multiple 150 micro-USD holds (8 bytes -> 2 tokens * 75_000_000 / 1M).
	if _, err := r.owner.Exec(ctx, `INSERT INTO gateway.usage_limits (kind, amount_micro_usd) VALUES ('workspace', 1000)`); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	chatter := &countingChatter{
		count: &calls,
		usage: domain.Usage{Arrived: true, Input: 1_000_000, Output: 500_000},
	}
	notifier := postgres.EstimateNotifier{
		Pool: r.owner,
		SettingsManagers: func(ctx context.Context) ([]string, error) {
			return identity.SettingsManagers(ctx, r.owner)
		},
		Notify: func(ctx context.Context, tx pgx.Tx, userID, title string) error {
			uid, err := uuid.Parse(userID)
			if err != nil {
				return err
			}
			_, err = notices.InsertTx(ctx, tx, uid, notices.KindFailed, title, nil)
			return err
		},
	}
	fixedNow := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	disp := app.Dispatch{
		Routes:         store,
		Teams:          store,
		Prices:         store,
		Ledger:         store,
		Limits:         store,
		Secrets:        staticSecrets{"ref_unk_x": "k"},
		Chat:           map[string]app.Chatter{domain.AdapterOpenAI: chatter, domain.AdapterAnthropic: chatter, domain.AdapterFake: chatter},
		EstimateNotice: notifier,
		Now:            func() time.Time { return fixedNow },
	}
	who := domain.Principal{User: ada, Source: domain.SourceChat}
	in := domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "12345678"}}}

	for i := range 2 {
		res, err := disp.ChatCall(ctx, "r-unk-spend", "", who, in)
		if err != nil {
			t.Fatalf("call %d failed: %v", i+1, err)
		}
		if res.CostMicroUSD != nil || res.CostUSD != nil {
			t.Fatalf("call %d returned non-nil cost: micro=%v usd=%v", i+1, res.CostMicroUSD, res.CostUSD)
		}
	}

	rows, err := r.owner.Query(ctx, `
		SELECT cost_micro_usd, estimated, price_source, charged_to_limit
		FROM gateway.ledger
		ORDER BY at, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type lrow struct {
		cost    *int64
		est     bool
		src     string
		charged bool
	}
	var got []lrow
	for rows.Next() {
		var x lrow
		if err := rows.Scan(&x.cost, &x.est, &x.src, &x.charged); err != nil {
			t.Fatal(err)
		}
		got = append(got, x)
	}
	if len(got) != 2 {
		t.Fatalf("ledger count = %d, want 2", len(got))
	}
	for i, x := range got {
		if x.cost != nil || x.est || x.src != "unknown" || !x.charged {
			t.Fatalf("ledger[%d] = %+v, want cost=nil est=false src=unknown charged=true", i, x)
		}
	}

	var spent, held int64
	if err := r.owner.QueryRow(ctx, `
		SELECT spent_micro_usd, held_micro_usd
		FROM gateway.usage_spend
		WHERE kind = 'workspace'`).Scan(&spent, &held); err != nil {
		t.Fatal(err)
	}
	if spent != 0 || held != 0 {
		t.Fatalf("usage_spend spent=%d held=%d, want 0 and 0", spent, held)
	}
	if holds := r.count(`SELECT count(*) FROM gateway.usage_holds`); holds != 0 {
		t.Fatalf("usage_holds = %d, want 0", holds)
	}
	if n := r.count(`SELECT count(*) FROM notices.notifications WHERE user_id = $1::uuid`, bob); n != 1 {
		t.Fatalf("bob notices = %d, want 1", n)
	}

	// QueryMonthlyUsage counts both unknown-priced calls without treating them as $0 accurate spend.
	monthStart := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	usageSummary, err := QueryMonthlyUsage(ctx, r.owner, "2026-11", monthStart, monthStart.AddDate(0, 1, 0))
	if err != nil {
		t.Fatalf("QueryMonthlyUsage: %v", err)
	}
	if usageSummary.Totals.CostMicroUSD != 0 || usageSummary.Totals.UnknownCostCount != 0 || usageSummary.Totals.UnpricedCallCount != 2 {
		t.Fatalf("usage totals = %+v, want CostMicroUSD=0 UnknownCostCount=0 UnpricedCallCount=2", usageSummary.Totals)
	}

	// Exhausted limit (below hold estimate of 150) still refuses the call before reaching provider.
	if _, err := r.owner.Exec(ctx, `UPDATE gateway.usage_limits SET amount_micro_usd = 100 WHERE kind = 'workspace'`); err != nil {
		t.Fatal(err)
	}
	beforeCalls := calls.Load()
	_, err = disp.ChatCall(ctx, "r-unk-spend", "", who, in)
	var limitErr domain.LimitReached
	if !errors.As(err, &limitErr) {
		t.Fatalf("exhausted limit err = %v, want LimitReached", err)
	}
	if calls.Load() != beforeCalls {
		t.Fatalf("provider calls increased on refused call: %d -> %d", beforeCalls, calls.Load())
	}

	// Direct SQL CHECK constraints:
	// 1. Non-NULL cost with 'unknown' must fail.
	if _, err := r.owner.Exec(ctx, `
		INSERT INTO gateway.ledger (route, link_position, model, plan_kind, failure_class, user_id, cost_micro_usd, estimated, source, price_source)
		VALUES ('r-chk', 1, 'm', 'metered', '', $1::uuid, 10, false, 'chat', 'unknown')`, ada); err == nil {
		t.Fatal("expected CHECK failure for non-NULL cost with price_source='unknown'")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Fatalf("expected check_violation 23514, got %v", err)
		}
	}

	// 2. 'unknown' with estimated=true must fail.
	if _, err := r.owner.Exec(ctx, `
		INSERT INTO gateway.ledger (route, link_position, model, plan_kind, failure_class, user_id, cost_micro_usd, estimated, source, price_source)
		VALUES ('r-chk', 1, 'm', 'metered', '', $1::uuid, NULL, true, 'chat', 'unknown')`, ada); err == nil {
		t.Fatal("expected CHECK failure for price_source='unknown' with estimated=true")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Fatalf("expected check_violation 23514, got %v", err)
		}
	}

	// 3. Metered row with NULL cost and NULL price_source must fail.
	if _, err := r.owner.Exec(ctx, `
		INSERT INTO gateway.ledger (route, link_position, model, plan_kind, failure_class, user_id, cost_micro_usd, estimated, source, price_source)
		VALUES ('r-chk', 1, 'm', 'metered', '', $1::uuid, NULL, false, 'chat', NULL)`, ada); err == nil {
		t.Fatal("expected CHECK failure for metered row with NULL cost and NULL price_source")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Fatalf("expected check_violation 23514, got %v", err)
		}
	}

	// 4. Metered row with non-NULL cost and NULL price_source must fail.
	if _, err := r.owner.Exec(ctx, `
		INSERT INTO gateway.ledger (route, link_position, model, plan_kind, failure_class, user_id, cost_micro_usd, estimated, source, price_source)
		VALUES ('r-chk', 1, 'm', 'metered', '', $1::uuid, 10, false, 'chat', NULL)`, ada); err == nil {
		t.Fatal("expected CHECK failure for metered row with non-NULL cost and NULL price_source")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Fatalf("expected check_violation 23514, got %v", err)
		}
	}

	// 5. Setting unknown_notified_at on a non-unknown row must fail.
	if _, err := r.owner.Exec(ctx, `
		INSERT INTO gateway.ledger (route, link_position, model, plan_kind, failure_class, user_id, cost_micro_usd, estimated, source, price_source, unknown_notified_at)
		VALUES ('r-chk', 1, 'm', 'metered', '', $1::uuid, 10, true, 'chat', 'estimate', now())`, ada); err == nil {
		t.Fatal("expected CHECK failure for unknown_notified_at on non-unknown row")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Fatalf("expected check_violation 23514, got %v", err)
		}
	}
}

type countingChatter struct {
	count *atomic.Int32
	usage domain.Usage
}

func (c *countingChatter) Chat(_ context.Context, attempt app.Attempt, _ domain.Request) (domain.ChatResult, domain.FailureClass, error) {
	c.count.Add(1)
	return domain.ChatResult{Text: "ok", Model: attempt.Model, Usage: c.usage}, domain.ClassOK, nil
}

func TestFeature_UPM354_PricingVendorFollowsEndpointAndDiscovery(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, masterKeys(t, 'a'))
	store := postgres.Store{Pool: r.owner}
	r.svc.Allow = func(_ context.Context, user string) (string, error) {
		if user == bob {
			return domain.SharePermission, nil
		}
		return "", nil
	}
	r.svc.LocalPlan = func(context.Context, string) bool { return true }
	r.mount(nil)

	// Seed models.dev rows: shared-m has different rates under openai ($2/$10) and openrouter ($5/$20),
	// and parse via ParseCatalog so '' fallback row holds ($5/$20).
	// edited-kind-m and img-suggest-m differ in kind/modalities between openai and openrouter to prove
	// annotateKinds and input-support lookups follow PricingVendor (endpoint) instead of Provider label.
	const catalogJSON = `{
		"openai": {"models": {
			"shared-m": {"cost": {"input": 2, "output": 10}},
			"edited-kind-m": {"type": "embedding", "cost": {"input": 1, "output": 1}},
			"img-suggest-m": {"modalities": {"input": ["text"]}, "cost": {"input": 1, "output": 1}}
		}},
		"openrouter": {"models": {
			"shared-m": {"cost": {"input": 5, "output": 20}},
			"edited-kind-m": {"type": "chat", "modalities": {"input": ["text"]}, "cost": {"input": 1, "output": 1}},
			"img-suggest-m": {"modalities": {"input": ["text", "image"]}, "cost": {"input": 1, "output": 1}}
		}}
	}`
	catRows, err := domain.ParseCatalog(strings.NewReader(catalogJSON), "endpoint-v1")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceCatalog(ctx, "endpoint-v1", catRows); err != nil {
		t.Fatal(err)
	}

	// Two probe servers: savedSrv serves OpenAI-style pricing ($3/$12) for the saved key and a distinct
	// response when probed with an unsaved key; unsavedSrv serves different pricing ($99/$199).
	savedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") == "Bearer unsaved-secret-key" {
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"unsaved-key-m","pricing":{"prompt":"0.000088","completion":"0.000188"}}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"shared-m","pricing":{"prompt":"0.000003","completion":"0.000012"}}]}`)
	}))
	t.Cleanup(savedSrv.Close)
	unsavedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"unsaved-m","pricing":{"prompt":"0.000099","completion":"0.000199"}}]}`)
	}))
	t.Cleanup(unsavedSrv.Close)

	// 1. Account labelled "openai" whose base_url was edited to OpenRouter's host is priced with OpenRouter's vendor rows.
	editedAcct := r.mustAdd(bob, map[string]any{
		"provider": "openai", "name": "Edited To OR", "adapter": domain.AdapterOpenAI,
		"plan_kind": domain.PlanMetered, "base_url": "https://openrouter.ai/api", "api_key": newKey,
		"models": []string{"shared-m", "edited-kind-m", "img-suggest-m"}, "visibility": domain.VisibilityEveryone,
	})
	var routeID string
	if err := r.owner.QueryRow(ctx, `INSERT INTO gateway.routes (name, capability, installed) VALUES ('r-edited-or', 'chat', true) RETURNING id::text`).Scan(&routeID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `INSERT INTO gateway.route_links (route_id, position, provider_account_id, model, retry_budget) VALUES ($1::uuid, 1, $2::uuid, 'shared-m', 0)`, routeID, editedAcct.ID); err != nil {
		t.Fatal(err)
	}

	// 2. Metered account labelled "openai" on a custom host is never models_dev (uses 5a '' estimate or unknown).
	customAcct := r.mustAdd(bob, map[string]any{
		"provider": "openai", "name": "Custom Host", "adapter": domain.AdapterOpenAI,
		"plan_kind": domain.PlanMetered, "base_url": "https://custom-proxy.example.com", "api_key": newKey,
		"models": []string{"shared-m"}, "visibility": domain.VisibilityEveryone,
	})
	var customRouteID string
	if err := r.owner.QueryRow(ctx, `INSERT INTO gateway.routes (name, capability, installed) VALUES ('r-custom-host', 'chat', true) RETURNING id::text`).Scan(&customRouteID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `INSERT INTO gateway.route_links (route_id, position, provider_account_id, model, retry_budget) VALUES ($1::uuid, 1, $2::uuid, 'shared-m', 0)`, customRouteID, customAcct.ID); err != nil {
		t.Fatal(err)
	}

	// Local-plan account pointed at savedSrv for live probe/discovery tests.
	probeAcct := r.mustAdd(bob, map[string]any{
		"provider": "openai", "name": "Probe Custom Host", "adapter": domain.AdapterOpenAI,
		"plan_kind": domain.PlanLocal, "base_url": savedSrv.URL, "api_key": newKey,
		"models": []string{"shared-m"}, "visibility": domain.VisibilityEveryone,
	})

	var editedRef, customRef string
	if err := r.owner.QueryRow(ctx, `SELECT credential_ref FROM gateway.provider_accounts WHERE id = $1::uuid`, editedAcct.ID).Scan(&editedRef); err != nil {
		t.Fatal(err)
	}
	if err := r.owner.QueryRow(ctx, `SELECT credential_ref FROM gateway.provider_accounts WHERE id = $1::uuid`, customAcct.ID).Scan(&customRef); err != nil {
		t.Fatal(err)
	}

	chatter := &scriptChatter{usage: domain.Usage{Arrived: true, Input: 1_000_000, Output: 500_000}}
	disp := app.Dispatch{
		Routes:  store,
		Teams:   store,
		Prices:  store,
		Ledger:  store,
		Limits:  store,
		Secrets: staticSecrets{editedRef: "k", customRef: "k"},
		Chat:    map[string]app.Chatter{domain.AdapterOpenAI: chatter},
	}
	who := domain.Principal{User: bob, Source: domain.SourceChat}
	in := domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}

	// 1M*$5 + 0.5M*$20 = 15_000_000 from openrouter (NOT 7_000_000 from openai!).
	resOR, err := disp.ChatCall(ctx, "r-edited-or", "", who, in)
	if err != nil || resOR.CostMicroUSD == nil || *resOR.CostMicroUSD != 15_000_000 {
		t.Fatalf("edited-to-openrouter ChatCall = %+v err=%v, want 15_000_000", resOR, err)
	}
	var orSource string
	if err := r.owner.QueryRow(ctx, `SELECT price_source FROM gateway.ledger WHERE route = 'r-edited-or'`).Scan(&orSource); err != nil {
		t.Fatal(err)
	}
	if orSource != "models_dev" {
		t.Fatalf("edited-to-openrouter price_source = %q, want models_dev", orSource)
	}
	// GetModelPricing & 20% warning use OpenRouter ($5), not OpenAI ($2).
	// $4 vs $5 is 20% (not warned), whereas vs OpenAI $2 it would be +100% (warned)!
	ovView, err := r.svc.SetPriceOverride(ctx, bob, editedAcct.ID, domain.PriceOverride{
		Model:                "shared-m",
		InputMicroUSDPerMTok: ptrInt64(4_000_000),
	})
	if err != nil {
		t.Fatalf("SetPriceOverride: %v", err)
	}
	if len(ovView.Warnings) != 0 {
		t.Fatalf("expected 0 warnings vs OpenRouter $5, got %+v", ovView.Warnings)
	}

	// annotateKinds & input-support lookups follow PricingVendor (openrouter), not Provider label (openai).
	listedAccts, err := r.svc.List(ctx, bob)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var gotEditedKind domain.ModelKind
	for _, a := range listedAccts {
		if a.ID == editedAcct.ID {
			gotEditedKind = a.ModelKinds["edited-kind-m"]
		}
	}
	if gotEditedKind != domain.KindChat {
		t.Fatalf("editedAcct ModelKinds[edited-kind-m] = %q, want %q (from openrouter, not openai embedding)", gotEditedKind, domain.KindChat)
	}
	if _, err := r.owner.Exec(ctx, `UPDATE gateway.provider_accounts SET status = $2 WHERE id = $1::uuid`, editedAcct.ID, domain.StatusConnected); err != nil {
		t.Fatal(err)
	}
	_, imgErr := disp.ChatCall(ctx, "r-edited-or", "", who, domain.Input{
		Messages: []domain.Message{{
			Role:  domain.RoleUser,
			Parts: []domain.Part{{Kind: domain.InputImage, MIMEType: "image/png", Data: "abc"}},
		}},
	})
	var unsupported domain.UnsupportedInputError
	if !errors.As(imgErr, &unsupported) || unsupported.Suggestion != "img-suggest-m" {
		t.Fatalf("image ChatCall err = %v, want UnsupportedInputError suggesting img-suggest-m from openrouter", imgErr)
	}

	// Custom host before probing: never models_dev (uses 5a '' estimate).
	if _, err := disp.ChatCall(ctx, "r-custom-host", "", who, in); err != nil {
		t.Fatalf("custom host ChatCall: %v", err)
	}
	var customSource string
	var customEstimated bool
	if err := r.owner.QueryRow(ctx, `SELECT price_source, estimated FROM gateway.ledger WHERE route = 'r-custom-host'`).Scan(&customSource, &customEstimated); err != nil {
		t.Fatal(err)
	}
	if customSource != "estimate" || !customEstimated {
		t.Fatalf("custom host ledger = src:%q est:%t, want estimate true (never models_dev)", customSource, customEstimated)
	}
	customPricing, err := r.svc.GetModelPricing(ctx, bob, customAcct.ID, "shared-m")
	if err != nil {
		t.Fatalf("GetModelPricing custom host: %v", err)
	}
	if customPricing.Merged.Sources.InputPerMTok != domain.SourceOtherProvider {
		t.Fatalf("custom host GetModelPricing input source = %q, want other_provider", customPricing.Merged.Sources.InputPerMTok)
	}

	// 3. Probe saved account on its saved URL -> stores savedSrv's models and pricing.
	code, raw := r.call(bob, http.MethodPost, "/api/providers/test", map[string]any{"id": probeAcct.ID})
	if code != http.StatusOK || !decode[app.TestResult](t, raw).OK {
		t.Fatalf("saved probe = %d %s", code, raw)
	}
	seenBefore, err := r.svc.SeenModels(ctx, bob, probeAcct.ID)
	if err != nil || len(seenBefore) != 1 || seenBefore[0].ID != "shared-m" {
		t.Fatalf("seen after saved probe = %+v err=%v", seenBefore, err)
	}

	// Now test an unsaved URL against that saved account ID -> must NOT persist unsavedSrv's price list!
	code, raw = r.call(bob, http.MethodPost, "/api/providers/test", map[string]any{
		"id":       probeAcct.ID,
		"base_url": unsavedSrv.URL,
	})
	if code != http.StatusOK {
		t.Fatalf("unsaved url test = %d %s", code, raw)
	}
	testRes := decode[app.TestResult](t, raw)
	if !testRes.OK || len(testRes.Models) != 1 || testRes.Models[0].ID != "unsaved-m" {
		t.Fatalf("unsaved url test result = %+v", testRes)
	}
	seenAfterUnsavedTest, err := r.svc.SeenModels(ctx, bob, probeAcct.ID)
	if err != nil || len(seenAfterUnsavedTest) != 1 || seenAfterUnsavedTest[0].ID != "shared-m" || seenAfterUnsavedTest[0].Pricing["prompt"] != "0.000003" {
		t.Fatalf("seen models mutated by unsaved URL test: %+v err=%v", seenAfterUnsavedTest, err)
	}

	// Test with an unsaved API key on the saved URL -> must NOT persist its models or price list!
	code, raw = r.call(bob, http.MethodPost, "/api/providers/test", map[string]any{
		"id":      probeAcct.ID,
		"api_key": "unsaved-secret-key",
	})
	if code != http.StatusOK {
		t.Fatalf("unsaved api_key test = %d %s", code, raw)
	}
	keyTestRes := decode[app.TestResult](t, raw)
	if !keyTestRes.OK || len(keyTestRes.Models) != 1 || keyTestRes.Models[0].ID != "unsaved-key-m" {
		t.Fatalf("unsaved api_key test result = %+v", keyTestRes)
	}
	seenAfterUnsavedKeyTest, err := r.svc.SeenModels(ctx, bob, probeAcct.ID)
	if err != nil || len(seenAfterUnsavedKeyTest) != 1 || seenAfterUnsavedKeyTest[0].ID != "shared-m" || seenAfterUnsavedKeyTest[0].Pricing["prompt"] != "0.000003" {
		t.Fatalf("seen models mutated by unsaved api_key test: %+v err=%v", seenAfterUnsavedKeyTest, err)
	}

	// 4. Changing the saved account's base URL clears the old discovered models and price list.
	code, raw = r.call(bob, http.MethodPut, "/api/providers/"+probeAcct.ID, map[string]any{
		"base_url": unsavedSrv.URL,
	})
	if code != http.StatusOK {
		t.Fatalf("PUT base_url = %d %s", code, raw)
	}
	if n := r.count(`SELECT count(*) FROM gateway.account_models_seen WHERE account_id = $1::uuid`, probeAcct.ID); n != 0 {
		t.Fatalf("account_models_seen count after base_url change = %d, want 0", n)
	}
	var listed bool
	if err := r.owner.QueryRow(ctx, `SELECT listed FROM gateway.provider_models WHERE account_id = $1::uuid AND model = 'shared-m'`, probeAcct.ID).Scan(&listed); err != nil {
		t.Fatal(err)
	}
	if listed {
		t.Fatal("provider_models.listed remained true after base_url change cleared discovered models")
	}
}

func TestFeature_UPM354_InterruptedStreamWithOverridesBillsEstimateNoNotice(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, masterKeys(t, 'a'))
	if err := notices.Up(ctx, r.owner); err != nil {
		t.Fatal(err)
	}
	store := postgres.Store{Pool: r.owner}

	if _, err := r.owner.Exec(ctx, `
		INSERT INTO identity.users (id, email, name, role_id, joined_at)
		VALUES ($1::uuid, 'bob@example.com', 'Bob', (SELECT id FROM identity.roles WHERE key = 'owner'), now())
		ON CONFLICT (id) DO UPDATE SET role_id = EXCLUDED.role_id, joined_at = now(), disabled_at = NULL`, bob); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial!\"}}]}\n\n")
	}))
	t.Cleanup(srv.Close)

	// Custom host (no vendor-wide catalog rate), complete input & output overrides.
	var acctID string
	if err := r.owner.QueryRow(ctx, `
		INSERT INTO gateway.provider_accounts
			(owner_user_id, provider, name, adapter, credential_ref, plan_kind, house, base_url, status, visibility)
		VALUES ($1::uuid, 'custom-proxy', 'Custom Stream', 'openai_compat', 'ref_cut_ov', 'metered', false, $2, 'Connected', 'everyone')
		RETURNING id::text`, ada, srv.URL).Scan(&acctID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `INSERT INTO gateway.provider_models (account_id, model, position, listed) VALUES ($1::uuid, 'custom-m', 1, true)`, acctID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `
		INSERT INTO gateway.price_overrides (account_id, model, input_micro_usd_per_mtok, output_micro_usd_per_mtok, updated_by)
		VALUES ($1::uuid, 'custom-m', 4000000, 16000000, $2::uuid)`, acctID, bob); err != nil {
		t.Fatal(err)
	}
	var routeID string
	if err := r.owner.QueryRow(ctx, `INSERT INTO gateway.routes (name, capability, installed) VALUES ('r-cut-ov', 'chat', true) RETURNING id::text`).Scan(&routeID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `INSERT INTO gateway.route_links (route_id, position, provider_account_id, model, retry_budget) VALUES ($1::uuid, 1, $2::uuid, 'custom-m', 0)`, routeID, acctID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `INSERT INTO gateway.usage_limits (kind, amount_micro_usd) VALUES ('workspace', 1000000000)`); err != nil {
		t.Fatal(err)
	}

	oai := openai.Adapter{Client: srv.Client()}
	notifier := postgres.EstimateNotifier{
		Pool: r.owner,
		SettingsManagers: func(ctx context.Context) ([]string, error) {
			return identity.SettingsManagers(ctx, r.owner)
		},
		Notify: func(ctx context.Context, tx pgx.Tx, userID, title string) error {
			uid, err := uuid.Parse(userID)
			if err != nil {
				return err
			}
			_, err = notices.InsertTx(ctx, tx, uid, notices.KindFailed, title, nil)
			return err
		},
	}
	disp := app.Dispatch{
		Routes:         store,
		Teams:          store,
		Prices:         store,
		Ledger:         store,
		Limits:         store,
		Secrets:        staticSecrets{"ref_cut_ov": "k"},
		Stream:         map[string]app.StreamChatter{domain.AdapterOpenAI: oai},
		EstimateNotice: notifier,
	}
	sink := &collectSink{}
	// 8 input bytes -> 2 input tokens ($4/MTok = 8) + 8 output bytes "partial!" -> 2 output tokens ($16/MTok = 32) => 40 micro-USD.
	err := disp.ChatStreamCall(ctx, "r-cut-ov", "", domain.Principal{User: ada, Source: domain.SourceChat}, domain.Input{
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "12345678"}},
	}, 0, sink)
	if err != nil || sink.fail == nil || sink.text.String() != "partial!" {
		t.Fatalf("stream err=%v sink=%+v", err, sink)
	}

	var cost int64
	var estimated bool
	var priceSource string
	if err := r.owner.QueryRow(ctx, `SELECT cost_micro_usd, estimated, price_source FROM gateway.ledger WHERE route = 'r-cut-ov'`).Scan(&cost, &estimated, &priceSource); err != nil {
		t.Fatal(err)
	}
	if cost != 40 || !estimated || priceSource != "estimate" {
		t.Fatalf("ledger = cost:%d est:%t src:%q, want 40 true estimate", cost, estimated, priceSource)
	}
	if n := r.count(`SELECT count(*) FROM notices.notifications`); n != 0 {
		t.Fatalf("unexpected notices on overridden interrupted stream: %d", n)
	}
	var spent, held int64
	if err := r.owner.QueryRow(ctx, `SELECT spent_micro_usd, held_micro_usd FROM gateway.usage_spend WHERE kind = 'workspace'`).Scan(&spent, &held); err != nil {
		t.Fatal(err)
	}
	if spent != 40 || held != 0 {
		t.Fatalf("usage_spend spent=%d held=%d, want 40 and 0", spent, held)
	}
}

func TestFeature_UPM354_ConcurrentFirstOverrideSaves(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, masterKeys(t, 'a'))
	sealed := newAuditSealer(t, r.owner)

	r.svc.Allow = func(_ context.Context, user string) (string, error) {
		if user == bob || user == eve {
			return domain.SharePermission, nil
		}
		return "", nil
	}
	r.mount(nil)
	acct := r.mustAdd(bob, map[string]any{
		"provider": "openrouter", "name": "Concurrent Ov", "adapter": domain.AdapterOpenAI,
		"plan_kind": domain.PlanMetered, "base_url": "https://openrouter.ai/api", "api_key": newKey,
		"visibility": domain.VisibilityEveryone,
	})
	_ = sealed()

	ovStore := r.svc.Accounts.(app.OverrideStore)

	// 1. Concurrent first saves with different rates: 1 creation audit + 1 change audit, final row matches last writer.
	for i := range 5 {
		model := fmt.Sprintf("concurrent-diff-%d", i)
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		errs := make(chan error, 2)
		for _, w := range []struct {
			user string
			rate int64
		}{
			{bob, 1_000_000},
			{eve, 2_000_000},
		} {
			go func() {
				defer wg.Done()
				<-start
				_, err := ovStore.SetOverride(ctx, app.Caller{User: w.user, Allow: domain.SharePermission}, acct.ID, domain.PriceOverride{
					Model:                model,
					InputMicroUSDPerMTok: ptrInt64(w.rate),
				})
				errs <- err
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("trial %d SetOverride error: %v", i, err)
			}
		}

		events := sealed()
		if len(events) != 2 {
			t.Fatalf("trial %d diff audit events = %d, want 2 (1 create + 1 update): %+v", i, len(events), events)
		}
		ch0 := events[0].Changes.Values["input_micro_usd_per_mtok"]
		ch1 := events[1].Changes.Values["input_micro_usd_per_mtok"]
		if ch0.From != nil || ch1.From != ch0.To {
			t.Fatalf("trial %d audit chain broken: first=%+v second=%+v", i, ch0, ch1)
		}
		var finalRate int64
		var finalBy string
		if err := r.owner.QueryRow(ctx, `
			SELECT input_micro_usd_per_mtok, updated_by::text
			FROM gateway.price_overrides
			WHERE account_id = $1::uuid AND model = $2`, acct.ID, model).Scan(&finalRate, &finalBy); err != nil {
			t.Fatal(err)
		}
		wantRate := int64(ch1.To.(float64))
		wantBy := bob
		if wantRate == 2_000_000 {
			wantBy = eve
		}
		if finalRate != wantRate || finalBy != wantBy {
			t.Fatalf("trial %d final row = (%d, %s), want (%d, %s)", i, finalRate, finalBy, wantRate, wantBy)
		}
	}

	// 2. Concurrent first saves with identical rates: 1 creation audit + 1 NoChange, final row matches creator.
	for i := range 5 {
		model := fmt.Sprintf("concurrent-same-%d", i)
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		errs := make(chan error, 2)
		for _, user := range []string{bob, eve} {
			go func() {
				defer wg.Done()
				<-start
				_, err := ovStore.SetOverride(ctx, app.Caller{User: user, Allow: domain.SharePermission}, acct.ID, domain.PriceOverride{
					Model:                model,
					InputMicroUSDPerMTok: ptrInt64(3_000_000),
				})
				errs <- err
			}()
		}
		close(start)
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("trial %d identical SetOverride error: %v", i, err)
			}
		}
		events := sealed()
		if len(events) != 1 {
			t.Fatalf("trial %d identical audit events = %d, want 1 (1 create + 1 NoChange): %+v", i, len(events), events)
		}
		ch := events[0].Changes.Values["input_micro_usd_per_mtok"]
		if ch.From != nil || ch.To != float64(3_000_000) {
			t.Fatalf("trial %d identical audit change = %+v", i, ch)
		}
	}
}

type failingSettleWrapper struct {
	inner             app.Limits
	failCount         int
	commitBeforeError bool
	calls             *atomic.Int32
	lastResID         *string
	err               error
}

func (f failingSettleWrapper) Reserve(ctx context.Context, req app.ReserveRequest) (app.Reservation, error) {
	res, err := f.inner.Reserve(ctx, req)
	if err == nil && f.lastResID != nil {
		*f.lastResID = res.ID
	}
	return res, err
}

func (f failingSettleWrapper) Settle(ctx context.Context, res app.Reservation, row domain.Entry) error {
	n := int(f.calls.Add(1))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if n <= f.failCount {
		if f.commitBeforeError {
			if err := f.inner.Settle(ctx, res, row); err != nil {
				return err
			}
		}
		return f.err
	}
	return f.inner.Settle(ctx, res, row)
}

func (f failingSettleWrapper) Release(ctx context.Context, res app.Reservation) error {
	return f.inner.Release(ctx, res)
}

func TestFeature_UPM354_SettleRetryAndFailureLogsMetric(t *testing.T) {
	const wantCost = int64(25_000_000) // 1M*$10 + 0.5M*$30

	t.Run("settle fails once then succeeds", func(t *testing.T) {
		ctx := context.Background()
		r := newRig(t, masterKeys(t, 'a'))
		store := postgres.Store{Pool: r.owner}
		insertCatalogPriceRow(t, r, "openai", "gpt-4o", 10, 30, 0, 0)
		acctID := insertProviderAndRoute(t, r, "r-settle-retry", "openai", "OAI Settle Retry", "ref_sr", "gpt-4o", true)
		if _, err := r.owner.Exec(ctx, `UPDATE gateway.provider_accounts SET visibility = 'everyone' WHERE id = $1::uuid`, acctID); err != nil {
			t.Fatal(err)
		}
		if _, err := r.owner.Exec(ctx, `INSERT INTO gateway.usage_limits (kind, amount_micro_usd) VALUES ('workspace', 1000000000)`); err != nil {
			t.Fatal(err)
		}

		var calls atomic.Int32
		reg := metrics.NewRegistry()
		var logBuf syncBuf
		chatter := &scriptChatter{usage: domain.Usage{Arrived: true, Input: 1_000_000, Output: 500_000}}
		disp := app.Dispatch{
			Routes:  store,
			Teams:   store,
			Prices:  store,
			Ledger:  store,
			Limits:  failingSettleWrapper{inner: store, failCount: 1, calls: &calls, err: errors.New("transient settle error")},
			Secrets: staticSecrets{"ref_sr": "k"},
			Chat:    map[string]app.Chatter{domain.AdapterOpenAI: chatter},
			Metrics: reg,
			Log:     slog.New(slog.NewTextHandler(&logBuf, nil)),
		}

		who := domain.Principal{User: ada, Source: domain.SourceChat}
		res, err := disp.ChatCall(ctx, "r-settle-retry", "", who, domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}})
		if err != nil {
			t.Fatalf("ChatCall failed on single transient Settle error: %v", err)
		}
		if res.CostMicroUSD == nil || *res.CostMicroUSD != wantCost {
			t.Fatalf("CostMicroUSD = %v, want %d", res.CostMicroUSD, wantCost)
		}
		if got := calls.Load(); got != 2 {
			t.Fatalf("Settle calls = %d, want 2", got)
		}
		if count := r.count(`SELECT count(*) FROM gateway.ledger`); count != 1 {
			t.Fatalf("ledger count = %d, want 1", count)
		}
		if holds := r.count(`SELECT count(*) FROM gateway.usage_holds`); holds != 0 {
			t.Fatalf("usage_holds = %d, want 0", holds)
		}
		var spent, held int64
		if err := r.owner.QueryRow(ctx, `SELECT spent_micro_usd, held_micro_usd FROM gateway.usage_spend WHERE kind = 'workspace'`).Scan(&spent, &held); err != nil {
			t.Fatal(err)
		}
		if spent != wantCost || held != 0 {
			t.Fatalf("usage_spend spent=%d held=%d, want %d and 0", spent, held, wantCost)
		}
		rec := httptest.NewRecorder()
		reg.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if strings.Contains(rec.Body.String(), "upmo_gateway_settle_failures_total") {
			t.Fatalf("unexpected settle failure metric on recovered retry:\n%s", rec.Body.String())
		}
		if logBuf.String() != "" {
			t.Fatalf("unexpected error log on recovered retry: %q", logBuf.String())
		}
	})

	t.Run("settle always fails releases hold, logs row details, and increments metric", func(t *testing.T) {
		ctx := context.Background()
		r := newRig(t, masterKeys(t, 'a'))
		store := postgres.Store{Pool: r.owner}
		insertCatalogPriceRow(t, r, "openai", "gpt-4o", 10, 30, 5, 10)
		acctID := insertProviderAndRoute(t, r, "r-settle-fail", "openai", "OAI Settle Fail", "ref_sf", "gpt-4o", true)
		if _, err := r.owner.Exec(ctx, `UPDATE gateway.provider_accounts SET visibility = 'everyone' WHERE id = $1::uuid`, acctID); err != nil {
			t.Fatal(err)
		}
		if _, err := r.owner.Exec(ctx, `INSERT INTO gateway.usage_limits (kind, amount_micro_usd) VALUES ('workspace', 1000000000)`); err != nil {
			t.Fatal(err)
		}

		var calls atomic.Int32
		var resID string
		settleErr := errors.New("permanent settle failure")
		reg := metrics.NewRegistry()
		var logBuf syncBuf
		chatter := &scriptChatter{usage: domain.Usage{
			Arrived:    true,
			Input:      1_000_000,
			Output:     500_000,
			CacheRead:  200,
			CacheWrite: 100,
			Reasoning:  50,
		}}
		disp := app.Dispatch{
			Routes:  store,
			Teams:   store,
			Prices:  store,
			Ledger:  store,
			Limits:  failingSettleWrapper{inner: store, failCount: 10, calls: &calls, lastResID: &resID, err: settleErr},
			Secrets: staticSecrets{"ref_sf": "k"},
			Chat:    map[string]app.Chatter{domain.AdapterOpenAI: chatter},
			Metrics: reg,
			Log:     slog.New(slog.NewTextHandler(&logBuf, nil)),
		}

		autoID := uuid.NewString()
		who := domain.Principal{User: ada, Source: domain.SourceAutomation, AutomationID: autoID}
		_, err := disp.ChatCall(ctx, "r-settle-fail", "", who, domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}})
		if !errors.Is(err, settleErr) {
			t.Fatalf("ChatCall err = %v, want %v", err, settleErr)
		}
		if got := calls.Load(); got != 2 {
			t.Fatalf("Settle calls = %d, want 2 (initial + 1 retry)", got)
		}
		if resID == "" {
			t.Fatal("expected non-empty reservation id")
		}
		if count := r.count(`SELECT count(*) FROM gateway.ledger`); count != 0 {
			t.Fatalf("ledger count = %d, want 0", count)
		}
		if holds := r.count(`SELECT count(*) FROM gateway.usage_holds`); holds != 0 {
			t.Fatalf("usage_holds = %d, want 0 (hold must be released)", holds)
		}
		var spent, held int64
		if err := r.owner.QueryRow(ctx, `SELECT COALESCE(sum(spent_micro_usd), 0), COALESCE(sum(held_micro_usd), 0) FROM gateway.usage_spend`).Scan(&spent, &held); err != nil {
			t.Fatal(err)
		}
		if spent != 0 || held != 0 {
			t.Fatalf("usage_spend spent=%d held=%d, want 0 and 0", spent, held)
		}

		logLine := strings.TrimSpace(logBuf.String())
		// 1M*$10 + 0.5M*$30 + 200*$5 + 100*$10 = 25_000_000 + 1_000 + 1_000 = 25_002_000
		for _, want := range []string{
			"gateway settle failed",
			"reservation_id=" + resID,
			"account_id=" + acctID,
			"model=gpt-4o",
			"user_id=" + ada,
			"source=automation",
			"automation_id=" + autoID,
			"cost_micro_usd=25002000",
			"estimated=false",
			"price_source=models_dev",
			"input_tokens=1000000",
			"output_tokens=500000",
			"cache_read=200",
			"cache_write=100",
			"reasoning=50",
			settleErr.Error(),
		} {
			if !strings.Contains(logLine, want) {
				t.Fatalf("settle failure log missing %q:\n%s", want, logLine)
			}
		}

		rec := httptest.NewRecorder()
		reg.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if !strings.Contains(rec.Body.String(), "upmo_gateway_settle_failures_total 1") {
			t.Fatalf("metrics missing upmo_gateway_settle_failures_total 1:\n%s", rec.Body.String())
		}
	})

	t.Run("settle commits then loses response and retry succeeds idempotently", func(t *testing.T) {
		ctx := context.Background()
		r := newRig(t, masterKeys(t, 'a'))
		store := postgres.Store{Pool: r.owner}
		insertCatalogPriceRow(t, r, "openai", "gpt-4o", 10, 30, 0, 0)
		acctID := insertProviderAndRoute(t, r, "r-settle-idem", "openai", "OAI Settle Idem", "ref_si", "gpt-4o", true)
		if _, err := r.owner.Exec(ctx, `UPDATE gateway.provider_accounts SET visibility = 'everyone' WHERE id = $1::uuid`, acctID); err != nil {
			t.Fatal(err)
		}
		if _, err := r.owner.Exec(ctx, `INSERT INTO gateway.usage_limits (kind, amount_micro_usd) VALUES ('workspace', 1000000000)`); err != nil {
			t.Fatal(err)
		}

		var calls atomic.Int32
		var resID string
		reg := metrics.NewRegistry()
		var logBuf syncBuf
		chatter := &scriptChatter{usage: domain.Usage{Arrived: true, Input: 1_000_000, Output: 500_000}}
		disp := app.Dispatch{
			Routes:  store,
			Teams:   store,
			Prices:  store,
			Ledger:  store,
			Limits:  failingSettleWrapper{inner: store, failCount: 1, commitBeforeError: true, calls: &calls, lastResID: &resID, err: errors.New("lost settle response")},
			Secrets: staticSecrets{"ref_si": "k"},
			Chat:    map[string]app.Chatter{domain.AdapterOpenAI: chatter},
			Metrics: reg,
			Log:     slog.New(slog.NewTextHandler(&logBuf, nil)),
		}

		who := domain.Principal{User: ada, Source: domain.SourceChat}
		res, err := disp.ChatCall(ctx, "r-settle-idem", "", who, domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}})
		if err != nil {
			t.Fatalf("ChatCall failed on lost settle response: %v", err)
		}
		if res.CostMicroUSD == nil || *res.CostMicroUSD != wantCost {
			t.Fatalf("CostMicroUSD = %v, want %d", res.CostMicroUSD, wantCost)
		}
		if got := calls.Load(); got != 2 {
			t.Fatalf("Settle calls = %d, want 2", got)
		}
		if resID == "" {
			t.Fatal("expected non-empty reservation id")
		}
		if count := r.count(`SELECT count(*) FROM gateway.ledger`); count != 1 {
			t.Fatalf("ledger count = %d, want 1", count)
		}
		var holdRows int
		if err := r.owner.QueryRow(ctx, `SELECT count(*) FROM gateway.ledger WHERE hold_id = $1::uuid`, resID).Scan(&holdRows); err != nil {
			t.Fatal(err)
		}
		if holdRows != 1 {
			t.Fatalf("ledger rows with hold_id=%s = %d, want 1", resID, holdRows)
		}
		if holds := r.count(`SELECT count(*) FROM gateway.usage_holds`); holds != 0 {
			t.Fatalf("usage_holds = %d, want 0", holds)
		}
		var spent, held int64
		if err := r.owner.QueryRow(ctx, `SELECT spent_micro_usd, held_micro_usd FROM gateway.usage_spend WHERE kind = 'workspace'`).Scan(&spent, &held); err != nil {
			t.Fatal(err)
		}
		if spent != wantCost || held != 0 {
			t.Fatalf("usage_spend spent=%d held=%d, want %d and 0", spent, held, wantCost)
		}
		rec := httptest.NewRecorder()
		reg.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if strings.Contains(rec.Body.String(), "upmo_gateway_settle_failures_total") {
			t.Fatalf("unexpected settle failure metric on idempotent retry:\n%s", rec.Body.String())
		}
		if logBuf.String() != "" {
			t.Fatalf("unexpected error log on idempotent retry: %q", logBuf.String())
		}
	})

	t.Run("duplicate hold_id in ledger is rejected", func(t *testing.T) {
		ctx := context.Background()
		r := newRig(t, masterKeys(t, 'a'))
		holdID := uuid.NewString()
		insertRow := func() error {
			_, err := r.owner.Exec(ctx, `
				INSERT INTO gateway.ledger (route, link_position, model, plan_kind, failure_class, user_id, cost_micro_usd, source, price_source, hold_id)
				VALUES ('r-dup-hold', 1, 'gpt-4o', 'metered', '', $1::uuid, 100, 'chat', 'models_dev', $2::uuid)`,
				ada, holdID,
			)
			return err
		}
		if err := insertRow(); err != nil {
			t.Fatalf("first insert with hold_id failed: %v", err)
		}
		err := insertRow()
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
			t.Fatalf("second insert with same hold_id = %v, want SQLSTATE 23505", err)
		}
	})
}

func TestFeature_UPM387_CustomHostAndAliasUsePricingMap(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, masterKeys(t, 'a'))
	r.svc.Allow = func(_ context.Context, user string) (string, error) {
		if user == bob {
			return domain.SharePermission, nil
		}
		return "", nil
	}
	r.mount(nil)
	store := postgres.Store{Pool: r.owner}
	if err := store.ReplaceCatalog(ctx, "upm387-map-v1", []domain.CatalogRow{
		{Vendor: "openrouter", Model: "custom-model", Version: "upm387-map-v1", Price: domain.Price{Version: "upm387-map-v1", InputMicroUSDPerMTok: ptrInt64(7_000_000), OutputMicroUSDPerMTok: ptrInt64(11_000_000)}},
		{Vendor: "openrouter", Model: "upstream-model", Version: "upm387-map-v1", Price: domain.Price{Version: "upm387-map-v1", InputMicroUSDPerMTok: ptrInt64(3_000_000), OutputMicroUSDPerMTok: ptrInt64(5_000_000)}},
	}); err != nil {
		t.Fatal(err)
	}
	acct := r.mustAdd(bob, map[string]any{"provider": "openai", "name": "Mapped custom host", "adapter": domain.AdapterOpenAI, "plan_kind": domain.PlanMetered, "base_url": "https://proxy.example.test/v1", "api_key": newKey, "models": []string{"custom-model", "deployment-alias"}, "visibility": domain.VisibilityEveryone})
	code, raw := r.call(bob, http.MethodPut, "/api/providers/"+acct.ID+"/pricing-map", map[string]any{"pricing_provider": "openrouter", "models": map[string]any{"deployment-alias": "upstream-model"}})
	if code != http.StatusNoContent {
		t.Fatalf("set pricing map = %d %s", code, raw)
	}
	var ref string
	if err := r.owner.QueryRow(ctx, `SELECT credential_ref FROM gateway.provider_accounts WHERE id=$1::uuid`, acct.ID).Scan(&ref); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		sent, catalog string
		want          int64
	}{{"custom-model", "custom-model", 12_500_000}, {"deployment-alias", "upstream-model", 5_500_000}} {
		var route string
		if err := r.owner.QueryRow(ctx, `INSERT INTO gateway.routes (name, capability, installed) VALUES ($1, 'chat', true) RETURNING id::text`, "r-upm387-"+tc.sent).Scan(&route); err != nil {
			t.Fatal(err)
		}
		if _, err := r.owner.Exec(ctx, `INSERT INTO gateway.route_links (route_id, position, provider_account_id, model, retry_budget) VALUES ($1::uuid, 1, $2::uuid, $3, 0)`, route, acct.ID, tc.sent); err != nil {
			t.Fatal(err)
		}
		dispatch := app.Dispatch{Routes: store, Teams: store, Prices: store, Ledger: store, Limits: store, Secrets: staticSecrets{ref: "k"}, Chat: map[string]app.Chatter{domain.AdapterOpenAI: &scriptChatter{usage: domain.Usage{Arrived: true, Input: 1_000_000, Output: 500_000}}}}
		result, err := dispatch.ChatCall(ctx, "r-upm387-"+tc.sent, "", domain.Principal{User: bob, Source: domain.SourceChat}, domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}})
		if err != nil || result.CostMicroUSD == nil || *result.CostMicroUSD != tc.want {
			t.Fatalf("%s pricing = %+v err=%v", tc.sent, result, err)
		}
		var source, version string
		if err := r.owner.QueryRow(ctx, `SELECT price_source, price_version FROM gateway.ledger WHERE route=$1`, "r-upm387-"+tc.sent).Scan(&source, &version); err != nil {
			t.Fatal(err)
		}
		if source != "models_dev" || version != "upm387-map-v1" {
			t.Fatalf("%s source/version = %q/%q (catalog row %s)", tc.sent, source, version, tc.catalog)
		}
	}
}

func TestFeature_UPM387_OverrideUsesOriginalModelWithPricingAlias(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, masterKeys(t, 'a'))
	store := postgres.Store{Pool: r.owner}
	if err := store.ReplaceCatalog(ctx, "upm387-override-v1", []domain.CatalogRow{
		{Vendor: "openrouter", Model: "upstream-model", Version: "upm387-override-v1", Price: domain.Price{Version: "upm387-override-v1", InputMicroUSDPerMTok: ptrInt64(3_000_000), OutputMicroUSDPerMTok: ptrInt64(5_000_000)}},
	}); err != nil {
		t.Fatal(err)
	}
	acct := r.mustAdd(ada, map[string]any{"provider": "openai", "name": "Aliased override", "adapter": domain.AdapterOpenAI, "plan_kind": domain.PlanMetered, "base_url": "https://api.openai.com", "api_key": adaKey, "models": []string{"deployment-alias"}, "visibility": domain.VisibilityPrivate})
	if _, err := r.owner.Exec(ctx, `UPDATE gateway.provider_accounts SET pricing_provider='openrouter', pricing_models='{"deployment-alias":"upstream-model"}'::jsonb WHERE id=$1::uuid`, acct.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `INSERT INTO gateway.price_overrides (account_id, model, input_micro_usd_per_mtok, output_micro_usd_per_mtok, updated_by) VALUES ($1::uuid, 'deployment-alias', 1000000, 2000000, $2::uuid)`, acct.ID, ada); err != nil {
		t.Fatal(err)
	}
	var route string
	if err := r.owner.QueryRow(ctx, `INSERT INTO gateway.routes (name, capability, installed) VALUES ('r-upm387-override-alias', 'chat', true) RETURNING id::text`).Scan(&route); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `INSERT INTO gateway.route_links (route_id, position, provider_account_id, model, retry_budget) VALUES ($1::uuid, 1, $2::uuid, 'deployment-alias', 0)`, route, acct.ID); err != nil {
		t.Fatal(err)
	}
	var ref string
	if err := r.owner.QueryRow(ctx, `SELECT credential_ref FROM gateway.provider_accounts WHERE id=$1::uuid`, acct.ID).Scan(&ref); err != nil {
		t.Fatal(err)
	}
	dispatch := app.Dispatch{Routes: store, Teams: store, Prices: store, Ledger: store, Limits: store, Secrets: staticSecrets{ref: "k"}, Chat: map[string]app.Chatter{domain.AdapterOpenAI: &scriptChatter{usage: domain.Usage{Arrived: true, Input: 1_000_000, Output: 500_000}}}}
	result, err := dispatch.ChatCall(ctx, "r-upm387-override-alias", "", domain.Principal{User: ada, Source: domain.SourceChat}, domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}})
	if err != nil || result.CostMicroUSD == nil || *result.CostMicroUSD != 2_000_000 {
		t.Fatalf("aliased override pricing = %+v err=%v, want override cost 2_000_000", result, err)
	}
	var source string
	if err := r.owner.QueryRow(ctx, `SELECT price_source FROM gateway.ledger WHERE route='r-upm387-override-alias'`).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if source != "override" {
		t.Fatalf("aliased override price_source = %q, want override", source)
	}
}

func TestFeature_UPM387_CustomHostWithoutPricingMapKeepsUPM354Price(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, masterKeys(t, 'a'))
	store := postgres.Store{Pool: r.owner}
	if err := store.ReplaceCatalog(ctx, "upm387-control-v1", []domain.CatalogRow{
		{Vendor: "openrouter", Model: "custom-model", Version: "upm387-control-v1", Price: domain.Price{Version: "upm387-control-v1", InputMicroUSDPerMTok: ptrInt64(7_000_000), OutputMicroUSDPerMTok: ptrInt64(11_000_000)}},
	}); err != nil {
		t.Fatal(err)
	}
	acct := r.mustAdd(ada, map[string]any{"provider": "openai", "name": "Unmapped custom host", "adapter": domain.AdapterOpenAI, "plan_kind": domain.PlanMetered, "base_url": "https://proxy.example.test/v1", "api_key": adaKey, "models": []string{"custom-model"}, "visibility": domain.VisibilityPrivate})
	var route string
	if err := r.owner.QueryRow(ctx, `INSERT INTO gateway.routes (name, capability, installed) VALUES ('r-upm387-custom-control', 'chat', true) RETURNING id::text`).Scan(&route); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `INSERT INTO gateway.route_links (route_id, position, provider_account_id, model, retry_budget) VALUES ($1::uuid, 1, $2::uuid, 'custom-model', 0)`, route, acct.ID); err != nil {
		t.Fatal(err)
	}
	var ref string
	if err := r.owner.QueryRow(ctx, `SELECT credential_ref FROM gateway.provider_accounts WHERE id=$1::uuid`, acct.ID).Scan(&ref); err != nil {
		t.Fatal(err)
	}
	dispatch := app.Dispatch{Routes: store, Teams: store, Prices: store, Ledger: store, Limits: store, Secrets: staticSecrets{ref: "k"}, Chat: map[string]app.Chatter{domain.AdapterOpenAI: &scriptChatter{usage: domain.Usage{Arrived: true, Input: 1_000_000, Output: 500_000}}}}
	if _, err := dispatch.ChatCall(ctx, "r-upm387-custom-control", "", domain.Principal{User: ada, Source: domain.SourceChat}, domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}); err != nil {
		t.Fatalf("unmapped custom host ChatCall: %v", err)
	}
	var source string
	var estimated bool
	if err := r.owner.QueryRow(ctx, `SELECT price_source, estimated FROM gateway.ledger WHERE route='r-upm387-custom-control'`).Scan(&source, &estimated); err != nil {
		t.Fatal(err)
	}
	if !((source == "unknown" && !estimated) || (source == "estimate" && estimated)) {
		t.Fatalf("unmapped custom host ledger = source:%q estimated:%t, want unknown or estimate", source, estimated)
	}
}

func TestFeature_UPM387_PricingMapRequiresSettingsManageAndAudit(t *testing.T) {
	r := newRig(t, masterKeys(t, 'a'))
	sealed := newAuditSealer(t, r.owner)
	r.svc.Allow = func(_ context.Context, user string) (string, error) {
		if user == bob || user == eve {
			return domain.SharePermission, nil
		}
		return "", nil
	}
	mux := http.NewServeMux()
	ProvidersHandler{Service: r.svc, Access: func(req *http.Request) (string, bool) {
		return req.Header.Get("X-User"), req.Header.Get("X-User") != ""
	}}.MountProviders(mux, nil)
	r.handler = audit.Middleware(audit.Routes(), mux, r.app, "brain", nil, slog.Default())(httpserver.Protect(mux))
	private := r.mustAdd(ada, map[string]any{"provider": "openai", "name": "Ada private mapping", "adapter": domain.AdapterOpenAI, "plan_kind": "metered", "base_url": "https://api.openai.com", "api_key": adaKey, "models": []string{"alias-model"}, "visibility": domain.VisibilityPrivate})
	shared := r.mustAdd(bob, map[string]any{"provider": "openrouter", "name": "Shared mapping", "adapter": domain.AdapterOpenAI, "plan_kind": "metered", "base_url": "https://openrouter.ai/api", "api_key": newKey, "models": []string{"alias-model"}, "visibility": domain.VisibilityEveryone})
	_ = sealed()
	if code, body := r.call(ada, http.MethodPut, "/api/providers/"+private.ID+"/pricing-map", map[string]any{"pricing_provider": "openrouter", "models": map[string]any{"alias-model": "catalog-model"}}); code != http.StatusForbidden {
		t.Fatalf("member private pricing map = %d %s, want 403", code, body)
	}
	if events := sealed(); len(events) != 1 || events[0].Action != audit.PricingMapSet || events[0].Outcome != audit.Denied {
		t.Fatalf("denied pricing map audit events = %+v", events)
	}
	if code, body := r.call(bob, http.MethodPut, "/api/providers/"+private.ID+"/pricing-map", map[string]any{"pricing_provider": "openrouter", "models": map[string]any{"alias-model": "catalog-model"}}); code != http.StatusNotFound {
		t.Fatalf("manager other member private pricing map = %d %s, want 404", code, body)
	}
	for _, event := range sealed() {
		if event.Outcome == audit.Success {
			t.Fatalf("hidden private pricing map was audited as success: %+v", event)
		}
	}
	var privateProvider string
	var privateModels []byte
	if err := r.owner.QueryRow(context.Background(), `SELECT pricing_provider, pricing_models FROM gateway.provider_accounts WHERE id=$1::uuid`, private.ID).Scan(&privateProvider, &privateModels); err != nil || privateProvider != "" || string(privateModels) != "{}" {
		t.Fatalf("private map after denied update = %q %s err=%v", privateProvider, privateModels, err)
	}
	if code, body := r.call(bob, http.MethodPut, "/api/providers/"+shared.ID+"/pricing-map", map[string]any{"pricing_provider": "openrouter", "models": map[string]any{"alias-model": "catalog-model"}}); code != http.StatusNoContent {
		t.Fatalf("admin pricing map = %d %s", code, body)
	}
	events := sealed()
	if len(events) != 1 || events[0].Action != audit.PricingMapSet || events[0].TargetID != shared.ID {
		t.Fatalf("pricing map audit events = %+v", events)
	}
	if !slices.Contains(events[0].Changes.Fields, "pricing_provider") || !slices.Contains(events[0].Changes.Fields, "pricing_model") {
		t.Fatalf("pricing map audit changes = %+v", events[0].Changes)
	}
	if code, body := r.call(bob, http.MethodPut, "/api/providers/"+shared.ID+"/pricing-map", map[string]any{"models": map[string]any{"alias-model": "   "}}); code != http.StatusNoContent {
		t.Fatalf("clear pricing model without provider = %d %s", code, body)
	}
	var pricingProvider string
	var pricingModels []byte
	if err := r.owner.QueryRow(context.Background(), `SELECT pricing_provider, pricing_models FROM gateway.provider_accounts WHERE id=$1::uuid`, shared.ID).Scan(&pricingProvider, &pricingModels); err != nil {
		t.Fatal(err)
	}
	if pricingProvider != "openrouter" || string(pricingModels) != "{}" {
		t.Fatalf("omitted vendor/cleared map = %q %s", pricingProvider, pricingModels)
	}
	events = sealed()
	if len(events) != 1 || events[0].Outcome != audit.Success || !slices.Contains(events[0].Changes.Fields, "pricing_model") {
		t.Fatalf("clear pricing map audit events = %+v", events)
	}
	if code, body := r.call(bob, http.MethodPut, "/api/providers/"+shared.ID+"/pricing-map", map[string]any{"pricing_provider": "unknown-vendor"}); code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown pricing vendor = %d %s", code, body)
	}
	var houseID string
	if err := r.owner.QueryRow(context.Background(), `INSERT INTO gateway.provider_accounts (name, adapter, credential_ref, plan_kind, house, base_url, provider, visibility) VALUES ('House map', 'openai_compat', 'house-map-ref', 'metered', true, 'https://api.example.test', 'openai', 'everyone') RETURNING id::text`).Scan(&houseID); err != nil {
		t.Fatal(err)
	}
	if code, body := r.call(bob, http.MethodPut, "/api/providers/"+houseID+"/pricing-map", map[string]any{"pricing_provider": "openai", "models": map[string]any{"alias-model": "house-model"}}); code != http.StatusNoContent {
		t.Fatalf("manager house pricing map = %d %s", code, body)
	}
	var houseProvider string
	var houseModels []byte
	if err := r.owner.QueryRow(context.Background(), `SELECT pricing_provider, pricing_models FROM gateway.provider_accounts WHERE id=$1::uuid`, houseID).Scan(&houseProvider, &houseModels); err != nil || houseProvider != "openai" || string(houseModels) != `{"alias-model": "house-model"}` {
		t.Fatalf("house pricing map = %q %s err=%v", houseProvider, houseModels, err)
	}
	managerPrivate := r.mustAdd(bob, map[string]any{"provider": "openai", "name": "Manager private mapping", "adapter": domain.AdapterOpenAI, "plan_kind": "metered", "base_url": "https://api.openai.com", "api_key": newKey, "models": []string{"alias-model"}, "visibility": domain.VisibilityPrivate})
	if code, body := r.call(bob, http.MethodPut, "/api/providers/"+managerPrivate.ID+"/pricing-map", map[string]any{"pricing_provider": "openai"}); code != http.StatusNoContent {
		t.Fatalf("manager own private pricing map = %d %s", code, body)
	}
	var storedVendor string
	var storedModels []byte
	if err := r.owner.QueryRow(context.Background(), `SELECT pricing_provider, pricing_models FROM gateway.provider_accounts WHERE id=$1::uuid`, managerPrivate.ID).Scan(&storedVendor, &storedModels); err != nil || storedVendor != "openai" || string(storedModels) != "{}" {
		t.Fatalf("manager own private stored mapping = %q %s err=%v", storedVendor, storedModels, err)
	}
	tx, err := r.owner.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(context.Background(), `SET LOCAL ROLE upmo_app`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `SELECT set_config('upmo.allow', 'providers:read', true)`); err != nil {
		t.Fatal(err)
	}
	var updated bool
	err = tx.QueryRow(context.Background(), `SELECT gateway.set_provider_pricing_map($1::uuid, 'openai', '{}'::jsonb)`, houseID).Scan(&updated)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" || pgErr.Message != "pricing map requires settings:manage" {
		t.Fatalf("direct pricing map function without settings:manage = %v, want its settings:manage guard", err)
	}
	_ = tx.Rollback(context.Background())

	managerTx, err := r.owner.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = managerTx.Rollback(context.Background()) }()
	if _, err := managerTx.Exec(context.Background(), `SET LOCAL ROLE upmo_app`); err != nil {
		t.Fatal(err)
	}
	if _, err := managerTx.Exec(context.Background(), `SELECT set_config('upmo.user_id', $1, true), set_config('upmo.allow', 'settings:manage', true)`, bob); err != nil {
		t.Fatal(err)
	}
	err = managerTx.QueryRow(context.Background(), `SELECT gateway.set_provider_pricing_map($1::uuid, 'openai', '{}'::jsonb)`, private.ID).Scan(&updated)
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" || pgErr.Message != "pricing map requires account access" {
		t.Fatalf("manager direct call for another member private account = %v, want account access guard", err)
	}
}

func TestFeature_UPM387_PricingMapLimitsReturnBadRequest(t *testing.T) {
	r := newRig(t, masterKeys(t, 'a'))
	r.svc.Allow = func(_ context.Context, user string) (string, error) {
		if user == bob {
			return domain.SharePermission, nil
		}
		return "", nil
	}
	r.mount(nil)
	acct := r.mustAdd(bob, map[string]any{"provider": "openrouter", "name": "Bounded map", "adapter": domain.AdapterOpenAI, "plan_kind": "metered", "base_url": "https://openrouter.ai/api", "api_key": newKey, "models": []string{"model-a"}, "visibility": domain.VisibilityEveryone})
	tooMany := make(map[string]any, 2001)
	for i := 0; i < 2001; i++ {
		tooMany[fmt.Sprintf("model-%04d", i)] = "catalog-model"
	}
	for name, models := range map[string]map[string]any{"count": tooMany, "size": func() map[string]any {
		m := make(map[string]any, 700)
		for i := 0; i < 700; i++ {
			m[fmt.Sprintf("model-%04d", i)] = strings.Repeat("x", 110)
		}
		return m
	}()} {
		code, body := r.call(bob, http.MethodPut, "/api/providers/"+acct.ID+"/pricing-map", map[string]any{"models": models})
		if code != http.StatusBadRequest || !strings.Contains(string(body), "65,536 bytes") {
			t.Fatalf("%s pricing map = %d %s, want 400 size-limit message", name, code, body)
		}
	}
	clears := make(map[string]any, len(tooMany))
	for model := range tooMany {
		clears[model] = nil
	}
	if code, body := r.call(bob, http.MethodPut, "/api/providers/"+acct.ID+"/pricing-map", map[string]any{"models": clears}); code != http.StatusNoContent {
		t.Fatalf("large clearing delta = %d %s, want 204", code, body)
	}
	mergedAtLimit := make(map[string]string, 2000)
	for i := 0; i < 2000; i++ {
		mergedAtLimit[fmt.Sprintf("m%04d", i)] = "x"
	}
	if _, err := r.owner.Exec(context.Background(), `UPDATE gateway.provider_accounts SET pricing_models=$2::jsonb WHERE id=$1::uuid`, acct.ID, jsonBytes(t, mergedAtLimit)); err != nil {
		t.Fatal(err)
	}
	if code, body := r.call(bob, http.MethodPut, "/api/providers/"+acct.ID+"/pricing-map", map[string]any{"models": map[string]any{"another": "x"}}); code != http.StatusBadRequest {
		t.Fatalf("merged 2,001-entry pricing map = %d %s, want 400", code, body)
	}
	boundaryMap := make(map[string]string, 2000)
	for i := 0; i < 2000; i++ {
		boundaryMap[fmt.Sprintf("m%04d", i)] = ""
	}
	baseSize := func(models map[string]string) int {
		return len(jsonBytes(t, models)) + 2*len(models) - 1
	}
	delta := 65536 - baseSize(boundaryMap)
	for i := 0; i < 2000 && delta > 0; i++ {
		add := delta / (2000 - i)
		if delta%(2000-i) != 0 {
			add++
		}
		boundaryMap[fmt.Sprintf("m%04d", i)] = strings.Repeat("x", add)
		delta -= add
	}
	if delta != 0 || baseSize(boundaryMap) != 65536 {
		t.Fatalf("failed to construct exact pricing map boundary: remaining delta %d, size %d", delta, baseSize(boundaryMap))
	}
	if _, err := r.owner.Exec(context.Background(), `UPDATE gateway.provider_accounts SET pricing_models=$2::jsonb WHERE id=$1::uuid`, acct.ID, jsonBytes(t, boundaryMap)); err != nil {
		t.Fatalf("seed exact-boundary pricing map: %v", err)
	}
	boundaryDelta := map[string]string{"m0000": boundaryMap["m0000"]}
	if code, body := r.call(bob, http.MethodPut, "/api/providers/"+acct.ID+"/pricing-map", map[string]any{"models": boundaryDelta}); code != http.StatusNoContent {
		t.Fatalf("65,536-byte pricing map = %d %s, want 204", code, body)
	}
	boundaryDelta["m0000"] += "x"
	if baseSize(boundaryMap)+1 != 65537 {
		t.Fatalf("constructed pricing map size = %d, want 65,537", baseSize(boundaryMap)+1)
	}
	if code, body := r.call(bob, http.MethodPut, "/api/providers/"+acct.ID+"/pricing-map", map[string]any{"models": boundaryDelta}); code != http.StatusBadRequest {
		t.Fatalf("65,537-byte pricing map = %d %s, want 400", code, body)
	}

	// A compact map can fit while the database's jsonb text representation crosses the limit.
	underBoth := make(map[string]string, 1300)
	for i := 0; i < 1300; i++ {
		underBoth[fmt.Sprintf("model-%04d", i)] = strings.Repeat("x", 32)
	}
	for len(jsonBytes(t, underBoth))+2*len(underBoth)-1 < 65450 {
		underBoth["model-0000"] += "x"
	}
	if _, err := r.owner.Exec(context.Background(), `UPDATE gateway.provider_accounts SET pricing_models=$2::jsonb WHERE id=$1::uuid`, acct.ID, jsonBytes(t, underBoth)); err != nil {
		t.Fatal(err)
	}
	if code, body := r.call(bob, http.MethodPut, "/api/providers/"+acct.ID+"/pricing-map", map[string]any{"models": map[string]any{"edge": strings.Repeat("y", 30)}}); code != http.StatusNoContent {
		t.Fatalf("map under both byte limits = %d %s, want 204", code, body)
	}
	if _, err := r.owner.Exec(context.Background(), `UPDATE gateway.provider_accounts SET pricing_models=$2::jsonb WHERE id=$1::uuid`, acct.ID, jsonBytes(t, underBoth)); err != nil {
		t.Fatal(err)
	}
	if code, body := r.call(bob, http.MethodPut, "/api/providers/"+acct.ID+"/pricing-map", map[string]any{"models": map[string]any{"edge": strings.Repeat("y", 120)}}); code != http.StatusBadRequest {
		t.Fatalf("compact-under/jsonb-over map = %d %s, want 400", code, body)
	}
	if _, err := r.owner.Exec(context.Background(), `UPDATE gateway.provider_accounts SET pricing_models=$2::jsonb WHERE id=$1::uuid`, acct.ID, jsonBytes(t, tooMany)); err == nil {
		t.Fatal("database accepted a pricing map above 2,000 entries")
	}
	if err := (postgres.Providers{Pool: r.owner}).SetPricingMap(context.Background(), app.Caller{User: bob, Allow: domain.SharePermission}, acct.ID, nil, map[string]string{"model-a": strings.Repeat("x", 65530)}); !errors.Is(err, app.ErrPricingMapLimit) {
		t.Fatalf("direct store oversized pricing map = %v, want ErrPricingMapLimit", err)
	}

	if _, err := r.owner.Exec(context.Background(), `UPDATE gateway.provider_accounts SET pricing_models='{}'::jsonb WHERE id=$1::uuid`, acct.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(context.Background(), `UPDATE gateway.provider_accounts SET pricing_models='{"a":["x"]}'::jsonb WHERE id=$1::uuid`, acct.ID); err == nil {
		t.Fatal("pricing map CHECK accepted an array value")
	} else {
		var checkErr *pgconn.PgError
		if !errors.As(err, &checkErr) || checkErr.Code != "23514" {
			t.Fatalf("array value CHECK error = %v, want SQLSTATE 23514", err)
		}
	}
	tx, err := r.owner.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(context.Background(), `SET LOCAL ROLE upmo_app`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `SELECT set_config('upmo.user_id', $1, true), set_config('upmo.allow', 'settings:manage', true)`, bob); err != nil {
		t.Fatal(err)
	}
	var updated bool
	err = tx.QueryRow(context.Background(), `SELECT gateway.set_provider_pricing_map($1::uuid, 'openai', '{"bad": 1}'::jsonb)`, acct.ID).Scan(&updated)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "22023" {
		t.Fatalf("non-string direct pricing map value = %v, want database refusal", err)
	}
	_ = tx.Rollback(context.Background())
	tx, err = r.owner.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `SET LOCAL ROLE upmo_app`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `SELECT set_config('upmo.user_id', $1, true), set_config('upmo.allow', 'settings:manage', true)`, bob); err != nil {
		t.Fatal(err)
	}
	err = tx.QueryRow(context.Background(), `SELECT gateway.set_provider_pricing_map($1::uuid, 'openai', '{"a":["x"]}'::jsonb)`, acct.ID).Scan(&updated)
	if !errors.As(err, &pgErr) || pgErr.Code != "22023" {
		t.Fatalf("array-valued direct pricing map = %v, want database refusal", err)
	}
	_ = tx.Rollback(context.Background())
	tx, err = r.owner.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(context.Background(), `SET LOCAL ROLE upmo_app`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `SELECT set_config('upmo.user_id', $1, true), set_config('upmo.allow', 'settings:manage', true)`, bob); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(context.Background(), `SELECT gateway.set_provider_pricing_map($1::uuid, 'openai', '{"a":"x"}'::jsonb)`, acct.ID).Scan(&updated); err != nil || !updated {
		t.Fatalf("string-valued direct pricing map updated=%t err=%v, want accepted", updated, err)
	}
}

func jsonBytes(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestFeature_UPM388_RefreshReplacesPriceTiersAndHighestRate(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, masterKeys(t, 'a'))
	store := postgres.Store{Pool: r.owner}
	row := domain.CatalogRow{Vendor: "gemini", Model: "tier-model", Version: "tier-v1", Price: domain.Price{
		InputMicroUSDPerMTok: ptrInt64(1_250_000),
		Tiers:                []domain.PriceTier{{ContextMinTokens: 200_000, Price: domain.Price{InputMicroUSDPerMTok: ptrInt64(2_500_000)}}},
	}}
	if err := store.ReplaceCatalog(ctx, "tier-v1", []domain.CatalogRow{row}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceCatalog(ctx, "tier-v1", []domain.CatalogRow{row}); err != nil {
		t.Fatal(err)
	}
	if r.count(`SELECT count(*) FROM gateway.price_tiers WHERE vendor='gemini' AND model='tier-model'`) != 1 {
		t.Fatal("repeated refresh duplicated a tier")
	}
	price, found, err := store.Price(ctx, "gemini", "tier-model")
	if err != nil || !found || len(price.Tiers) != 1 || price.Tiers[0].ContextMinTokens != 200_000 {
		t.Fatalf("price=%+v found=%v err=%v", price, found, err)
	}
	if rate, ok, err := store.HighestRate(ctx, "gemini"); err != nil || !ok || rate != 5_000_000 {
		t.Fatalf("HighestRate=%d ok=%v err=%v, want 5000000", rate, ok, err)
	}
	row.Version = "tier-v2"
	row.Price.Tiers = []domain.PriceTier{
		{ContextMinTokens: 100_000, Price: domain.Price{InputMicroUSDPerMTok: ptrInt64(2_000_000)}},
		{ContextMinTokens: 256_000, Price: domain.Price{InputMicroUSDPerMTok: ptrInt64(3_000_000)}},
	}
	if err := store.ReplaceCatalog(ctx, "tier-v2", []domain.CatalogRow{row}); err != nil {
		t.Fatal(err)
	}
	if r.count(`SELECT count(*) FROM gateway.price_tiers WHERE vendor='gemini' AND model='tier-model' AND context_min_tokens=200000`) != 0 || r.count(`SELECT count(*) FROM gateway.price_tiers WHERE vendor='gemini' AND model='tier-model' AND context_min_tokens IN (100000,256000)`) != 2 {
		t.Fatal("refresh did not replace tier rows")
	}
	badRefresh := row
	badRefresh.Price.Tiers = []domain.PriceTier{{ContextMinTokens: 100_000}, {ContextMinTokens: 100_000}}
	if err := store.ReplaceCatalog(ctx, "invalid-tier-refresh", []domain.CatalogRow{badRefresh}); err == nil {
		t.Fatal("invalid catalog refresh succeeded")
	}
	if r.count(`SELECT count(*) FROM gateway.price_tiers WHERE vendor='gemini' AND model='tier-model'`) != 2 {
		t.Fatal("failed catalog refresh changed existing tiers")
	}
	if rate, ok, err := store.HighestRate(ctx, "gemini"); err != nil || !ok || rate != 6_000_000 {
		t.Fatalf("HighestRate=%d ok=%v err=%v, want 6000000 after tier refresh", rate, ok, err)
	}
	var accountID string
	if err := r.owner.QueryRow(ctx, `INSERT INTO gateway.provider_accounts (name, adapter, credential_ref, plan_kind, house, provider, visibility) VALUES ('tier-list', 'fake', 'tier-list', 'subscription', true, 'gemini', 'everyone') RETURNING id`).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	userID := uuid.NewString()
	if err := store.Record(ctx, domain.Entry{Route: "tier-route", LinkPosition: 1, AccountID: accountID, Model: "tier-model", PlanKind: domain.PlanSubscription, Class: domain.ClassOK, Principal: domain.Principal{User: userID, Source: domain.SourceChat}, Usage: domain.Usage{Arrived: true, Input: 300_000}, ListCostMicroUSD: ptrInt64(900_000)}); err != nil {
		t.Fatal(err)
	}
	row.Price.Tiers[1].Price.InputMicroUSDPerMTok = ptrInt64(4_000_000)
	if err := store.ReplaceCatalog(ctx, "tier-v3", []domain.CatalogRow{row}); err != nil {
		t.Fatal(err)
	}
	window := domain.UsageWindowFor(domain.UsageToday, time.Now(), time.UTC)
	scan, err := (postgres.Usage{Pool: r.app}).WorkspaceUsage(ctx, app.Caller{User: userID, Allow: domain.UsageViewPermission}, window, domain.GroupModel)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Buckets) != 1 || scan.Buckets[0].Requests != 1 || scan.Buckets[0].ListMicroUSD == nil || *scan.Buckets[0].ListMicroUSD != 900_000 || len(scan.Keyed) != 1 || scan.Keyed[0].Requests != 1 {
		t.Fatalf("tiered usage rows = %+v", scan)
	}
	personal, err := (postgres.Usage{Pool: r.app}).PersonalUsage(ctx, app.Caller{User: userID}, window)
	if err != nil || len(personal.Buckets) != 1 || personal.Buckets[0].ListMicroUSD == nil || *personal.Buckets[0].ListMicroUSD != 900_000 {
		t.Fatalf("member tiered personal usage = %+v err=%v", personal, err)
	}
	if err := store.Record(ctx, domain.Entry{Route: "unknown-list-route", LinkPosition: 1, AccountID: accountID, Model: "unknown-list-model", PlanKind: domain.PlanSubscription, Class: domain.ClassOK, Principal: domain.Principal{User: userID, Source: domain.SourceChat}, Usage: domain.Usage{Arrived: true, Input: 1}}); err != nil {
		t.Fatal(err)
	}
	unknown, err := (postgres.Usage{Pool: r.app}).WorkspaceUsage(ctx, app.Caller{User: userID, Allow: domain.UsageViewPermission}, window, domain.GroupModel)
	if err != nil {
		t.Fatal(err)
	}
	foundUnknown := false
	for _, cell := range unknown.Keyed {
		if cell.Model == "unknown-list-model" {
			foundUnknown = true
			if cell.ListMicroUSD != nil {
				t.Fatalf("unknown list price cell = %v, want nil", *cell.ListMicroUSD)
			}
		}
	}
	if !foundUnknown {
		t.Fatalf("unknown list model missing from usage cells: %+v", unknown.Keyed)
	}
	var visibleTiers int
	if err := r.app.QueryRow(ctx, `SELECT count(*) FROM gateway.price_tiers WHERE vendor='gemini' AND model='tier-model'`).Scan(&visibleTiers); err != nil || visibleTiers != 2 {
		t.Fatalf("upmo_app SELECT price_tiers = %d err=%v, want 2", visibleTiers, err)
	}
	for _, tc := range []struct{ action, query string }{
		{"INSERT", `INSERT INTO gateway.price_tiers (vendor, model, context_min_tokens) VALUES ('gemini','tier-model',999)`},
		{"UPDATE", `UPDATE gateway.price_tiers SET context_min_tokens=999 WHERE vendor='gemini' AND model='tier-model'`},
		{"DELETE", `DELETE FROM gateway.price_tiers WHERE vendor='gemini' AND model='tier-model'`},
	} {
		if _, err := r.app.Exec(ctx, tc.query); err == nil {
			t.Errorf("upmo_app unexpectedly allowed %s", tc.action)
		} else {
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
				t.Errorf("upmo_app %s error = %v, want SQLSTATE 42501", tc.action, err)
			}
		}
	}
	cost := int64(1)
	if err := store.Record(ctx, domain.Entry{Route: "invalid-metered-list", LinkPosition: 1, AccountID: accountID, Model: "tier-model", PlanKind: domain.PlanMetered, Class: domain.ClassOK, Principal: domain.Principal{User: userID, Source: domain.SourceChat}, CostMicroUSD: &cost, PriceSource: "provider", ListCostMicroUSD: ptrInt64(1)}); err == nil {
		t.Fatal("metered ledger row accepted a list cost")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "ledger_list_cost_plan_check" {
			t.Fatalf("metered list-cost error = %v, want SQLSTATE 23514 for ledger_list_cost_plan_check", err)
		}
	}
	if err := store.Record(ctx, domain.Entry{Route: "tier-route", LinkPosition: 1, AccountID: accountID, Model: "tier-model", PlanKind: domain.PlanMetered, Class: domain.ClassOK, Principal: domain.Principal{User: userID, Source: domain.SourceChat}, Usage: domain.Usage{Arrived: true, CacheWrite: 25, CacheWrite1h: 10}, CostMicroUSD: &cost, Estimated: true, PriceSource: "estimate"}); err != nil {
		t.Fatal(err)
	}
	var cacheWrite, cacheWrite1h int
	if err := r.owner.QueryRow(ctx, `SELECT cache_write, cache_write_1h FROM gateway.ledger WHERE plan_kind='metered' AND model='tier-model'`).Scan(&cacheWrite, &cacheWrite1h); err != nil || cacheWrite != 25 || cacheWrite1h != 10 {
		t.Fatalf("ledger cache writes = %d/%d err=%v", cacheWrite, cacheWrite1h, err)
	}
}

func TestFeature_UPM388_HighestRateReservesTwiceInputForBaseAndNullTierInput(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, masterKeys(t, 'c'))
	store := postgres.Store{Pool: r.owner}
	for _, tc := range []struct {
		name string
		row  domain.CatalogRow
		want int64
	}{
		{name: "base", row: domain.CatalogRow{Vendor: "openai", Model: "reserve-base", Version: "v1", Price: domain.Price{InputMicroUSDPerMTok: ptrInt64(3_000_000), OutputMicroUSDPerMTok: ptrInt64(1_000_000), CacheWriteMicroUSDPerMTok: ptrInt64(2_000_000)}}, want: 6_000_000},
		{name: "tier output rate exceeds twice base input", row: domain.CatalogRow{Vendor: "openai", Model: "reserve-tier", Version: "v1", Price: domain.Price{InputMicroUSDPerMTok: ptrInt64(3_000_000), OutputMicroUSDPerMTok: ptrInt64(1_000_000), Tiers: []domain.PriceTier{{ContextMinTokens: 200_000, Price: domain.Price{OutputMicroUSDPerMTok: ptrInt64(8_000_000)}}}}}, want: 8_000_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := store.ReplaceCatalog(ctx, "v1", []domain.CatalogRow{tc.row}); err != nil {
				t.Fatal(err)
			}
			rate, ok, err := store.HighestRate(ctx, "openai")
			if err != nil || !ok || rate != tc.want {
				t.Fatalf("HighestRate=%d ok=%v err=%v, want %d", rate, ok, err, tc.want)
			}
		})
	}
}

func TestFeature_UPM388_EstimateRateKeepsCatalogCeiling(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, masterKeys(t, 'c'))
	store := postgres.Store{Pool: r.owner}
	rows := []domain.CatalogRow{
		{Vendor: "estimate-tier", Model: "tiered", Version: "v1", Price: domain.Price{InputMicroUSDPerMTok: ptrInt64(2_000_000), Tiers: []domain.PriceTier{{ContextMinTokens: 100, Price: domain.Price{InputMicroUSDPerMTok: ptrInt64(3_000_000)}}}}},
		{Vendor: "estimate-flat", Model: "flat", Version: "v1", Price: domain.Price{InputMicroUSDPerMTok: ptrInt64(4_000_000)}},
	}
	if err := store.ReplaceCatalog(ctx, "v1", rows); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		vendor string
		want   int64
	}{{"estimate-tier", 3_000_000}, {"estimate-flat", 4_000_000}} {
		got, ok, err := store.EstimateRate(ctx, tc.vendor)
		if err != nil || !ok || got != tc.want {
			t.Errorf("EstimateRate(%s)=%d ok=%v err=%v, want %d", tc.vendor, got, ok, err, tc.want)
		}
	}
	if got, ok, err := store.HighestRate(ctx, "estimate-tier"); err != nil || !ok || got != 6_000_000 {
		t.Fatalf("HighestRate tier=%d ok=%v err=%v, want 6000000", got, ok, err)
	}
}

func TestFeature_UPM388_DispatchPricesTieredMixedCacheWriteAndRecords(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, masterKeys(t, 'd'))
	store := postgres.Store{Pool: r.owner}
	const version = "upm388-dispatch-v1"
	if err := store.ReplaceCatalog(ctx, version, []domain.CatalogRow{
		{Vendor: "openai", Model: "tier-dispatch", Version: version, Price: domain.Price{
			InputMicroUSDPerMTok: ptrInt64(1_000_000), OutputMicroUSDPerMTok: ptrInt64(1_000_000), CacheWriteMicroUSDPerMTok: ptrInt64(3_000_000),
			Tiers: []domain.PriceTier{{ContextMinTokens: 200_000, Price: domain.Price{InputMicroUSDPerMTok: ptrInt64(4_000_000), OutputMicroUSDPerMTok: ptrInt64(2_000_000), CacheWriteMicroUSDPerMTok: ptrInt64(5_000_000)}}},
		}},
		{Vendor: "openai", Model: "tier-local", Version: version, Price: domain.Price{InputMicroUSDPerMTok: ptrInt64(2_000_000)}},
		{Vendor: "", Model: "cross-list", Version: version, Price: domain.Price{InputMicroUSDPerMTok: ptrInt64(3_000_000)}},
	}); err != nil {
		t.Fatal(err)
	}
	accountID := insertProviderAndRoute(t, r, "r-upm388-tier-dispatch", "openai", "Tier Dispatch", "ref_upm388_tier", "tier-dispatch", true)
	if _, err := r.owner.Exec(ctx, `UPDATE gateway.provider_accounts SET plan_kind='subscription' WHERE id=$1::uuid`, accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `INSERT INTO gateway.price_overrides(account_id,model,input_micro_usd_per_mtok,updated_by) VALUES($1::uuid,'tier-dispatch',9000000,$2::uuid)`, accountID, ada); err != nil {
		t.Fatal(err)
	}
	usage := domain.Usage{Arrived: true, Input: 210_000, CacheWrite: 20, CacheWrite1h: 10}
	dispatch := app.Dispatch{Routes: store, Teams: store, Prices: store, Ledger: store, Secrets: staticSecrets{"ref_upm388_tier": "secret"}, Chat: map[string]app.Chatter{domain.AdapterOpenAI: &scriptChatter{usage: usage}}}
	result, err := dispatch.ChatCall(ctx, "r-upm388-tier-dispatch", "", domain.Principal{User: ada, Source: domain.SourceChat}, domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}})
	if err != nil || result.CostMicroUSD != nil {
		t.Fatalf("subscription tiered mixed-write dispatch = %+v err=%v, want nil billed cost", result, err)
	}
	var gotCost *int64
	var cacheWrite, cacheWrite1h int
	var source *string
	var listCost *int64
	if err := r.owner.QueryRow(ctx, `SELECT cost_micro_usd,cache_write,cache_write_1h,price_source,list_cost_micro_usd FROM gateway.ledger WHERE route='r-upm388-tier-dispatch'`).Scan(&gotCost, &cacheWrite, &cacheWrite1h, &source, &listCost); err != nil {
		t.Fatal(err)
	}
	if gotCost != nil || cacheWrite != 20 || cacheWrite1h != 10 || source != nil || listCost == nil || *listCost != 840_130 {
		t.Fatalf("tier dispatch ledger cost/cache/source/list=%d/%d/%d/%v/%v, want NULL cost/source and list 840130", gotCost, cacheWrite, cacheWrite1h, source, listCost)
	}
	if err := store.ReplaceCatalog(ctx, "upm388-dispatch-v2", []domain.CatalogRow{
		{Vendor: "openai", Model: "tier-dispatch", Version: "upm388-dispatch-v2", Price: domain.Price{InputMicroUSDPerMTok: ptrInt64(7_000_000)}},
		{Vendor: "openai", Model: "tier-local", Version: "upm388-dispatch-v2", Price: domain.Price{InputMicroUSDPerMTok: ptrInt64(2_000_000)}},
		{Vendor: "", Model: "cross-list", Version: "upm388-dispatch-v2", Price: domain.Price{InputMicroUSDPerMTok: ptrInt64(3_000_000)}},
	}); err != nil {
		t.Fatal(err)
	}
	var frozen *int64
	if err := r.owner.QueryRow(ctx, `SELECT list_cost_micro_usd FROM gateway.ledger WHERE route='r-upm388-tier-dispatch'`).Scan(&frozen); err != nil || frozen == nil || *frozen != 840_130 {
		t.Fatalf("recorded list price after catalog replacement = %v err=%v, want frozen 840130", frozen, err)
	}
	localAccount := insertProviderAndRoute(t, r, "r-upm388-local", "openai", "Local list", "ref_upm388_local", "tier-local", true)
	if _, err := r.owner.Exec(ctx, `UPDATE gateway.provider_accounts SET plan_kind='local' WHERE id=$1::uuid`, localAccount); err != nil {
		t.Fatal(err)
	}
	localDispatch := app.Dispatch{Routes: store, Teams: store, Prices: store, Ledger: store, Secrets: staticSecrets{"ref_upm388_local": "secret"}, Chat: map[string]app.Chatter{domain.AdapterOpenAI: &scriptChatter{usage: domain.Usage{Arrived: true, Input: 10}}}}
	if _, err := localDispatch.ChatCall(ctx, "r-upm388-local", "", domain.Principal{User: ada, Source: domain.SourceChat}, domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	var localList *int64
	if err := r.owner.QueryRow(ctx, `SELECT list_cost_micro_usd FROM gateway.ledger WHERE route='r-upm388-local'`).Scan(&localList); err != nil || localList == nil || *localList != 20 {
		t.Fatalf("PlanLocal list cost = %v err=%v, want 20", localList, err)
	}
	crossAccount := insertProviderAndRoute(t, r, "r-upm388-cross", "openai", "Cross list", "ref_upm388_cross", "cross-list", true)
	if _, err := r.owner.Exec(ctx, `UPDATE gateway.provider_accounts SET plan_kind='subscription' WHERE id=$1::uuid`, crossAccount); err != nil {
		t.Fatal(err)
	}
	crossDispatch := app.Dispatch{Routes: store, Teams: store, Prices: store, Ledger: store, Secrets: staticSecrets{"ref_upm388_cross": "secret"}, Chat: map[string]app.Chatter{domain.AdapterOpenAI: &scriptChatter{usage: domain.Usage{Arrived: true, Input: 10}}}}
	if _, err := crossDispatch.ChatCall(ctx, "r-upm388-cross", "", domain.Principal{User: ada, Source: domain.SourceChat}, domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	var crossList *int64
	if err := r.owner.QueryRow(ctx, `SELECT list_cost_micro_usd FROM gateway.ledger WHERE route='r-upm388-cross'`).Scan(&crossList); err != nil || crossList == nil || *crossList != 30 {
		t.Fatalf("cross-provider list cost = %v err=%v, want 30", crossList, err)
	}
	unknownAccount := insertProviderAndRoute(t, r, "r-upm388-unknown", "openai", "Unknown list", "ref_upm388_unknown", "no-list-rate", true)
	if _, err := r.owner.Exec(ctx, `UPDATE gateway.provider_accounts SET plan_kind='subscription' WHERE id=$1::uuid`, unknownAccount); err != nil {
		t.Fatal(err)
	}
	unknownDispatch := app.Dispatch{Routes: store, Teams: store, Prices: store, Ledger: store, Secrets: staticSecrets{"ref_upm388_unknown": "secret"}, Chat: map[string]app.Chatter{domain.AdapterOpenAI: &scriptChatter{usage: domain.Usage{Arrived: true, Input: 10}}}}
	if _, err := unknownDispatch.ChatCall(ctx, "r-upm388-unknown", "", domain.Principal{User: ada, Source: domain.SourceChat}, domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	var unknownList *int64
	if err := r.owner.QueryRow(ctx, `SELECT list_cost_micro_usd FROM gateway.ledger WHERE route='r-upm388-unknown'`).Scan(&unknownList); err != nil || unknownList != nil {
		t.Fatalf("unknown-rate list cost = %v err=%v, want NULL", unknownList, err)
	}
	unarrivedAccount := insertProviderAndRoute(t, r, "r-upm388-unarrived", "openai", "Unarrived list", "ref_upm388_unarrived", "tier-dispatch", true)
	if _, err := r.owner.Exec(ctx, `UPDATE gateway.provider_accounts SET plan_kind='subscription' WHERE id=$1::uuid`, unarrivedAccount); err != nil {
		t.Fatal(err)
	}
	unarrivedDispatch := app.Dispatch{Routes: store, Teams: store, Prices: store, Ledger: store, Secrets: staticSecrets{"ref_upm388_unarrived": "secret"}, Chat: map[string]app.Chatter{domain.AdapterOpenAI: &scriptChatter{}}}
	if _, err := unarrivedDispatch.ChatCall(ctx, "r-upm388-unarrived", "", domain.Principal{User: ada, Source: domain.SourceChat}, domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	var unarrivedList *int64
	if err := r.owner.QueryRow(ctx, `SELECT list_cost_micro_usd FROM gateway.ledger WHERE route='r-upm388-unarrived'`).Scan(&unarrivedList); err != nil || unarrivedList != nil {
		t.Fatalf("unarrived list cost = %v err=%v, want NULL", unarrivedList, err)
	}
	if _, err := r.owner.Exec(ctx, `UPDATE gateway.ledger SET at=now()-interval '2 days' WHERE route='r-upm388-tier-dispatch'`); err != nil {
		t.Fatal(err)
	}
	if err := RollupUsageOnce(ctx, r.owner, zone("UTC"), time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	var dailyCost *int64
	if err := r.owner.QueryRow(ctx, `SELECT cost_micro_usd FROM gateway.usage_daily WHERE model='tier-dispatch' AND user_id=$1::uuid`, ada).Scan(&dailyCost); err != nil || dailyCost == nil || *dailyCost != 0 {
		t.Fatalf("usage_daily cost for non-metered list row = %v err=%v, want 0 (list value must not leak in)", dailyCost, err)
	}
	limits, err := (postgres.UsageLimits{Pool: r.app}).GetUsageLimits(ctx, app.Caller{User: ada}, time.Now(), false)
	if err != nil || limits.Own.SpentMicroUSD != 0 {
		t.Fatalf("limit spend after list-priced calls = %d err=%v, want zero", limits.Own.SpentMicroUSD, err)
	}
	if accountID == "" {
		t.Fatal("insertProviderAndRoute returned an empty account id")
	}
}

func TestFeature_UPM388_ConcurrentCatalogRefreshesSerialize(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, masterKeys(t, 'b'))
	store := postgres.Store{Pool: r.owner}
	rows := [][]domain.CatalogRow{
		{{Vendor: "gemini", Model: "refresh-a", Version: "refresh-a", Price: domain.Price{InputMicroUSDPerMTok: ptrInt64(1_000_000), Tiers: []domain.PriceTier{{ContextMinTokens: 100_000, Price: domain.Price{InputMicroUSDPerMTok: ptrInt64(2_000_000)}}}}}},
		{{Vendor: "gemini", Model: "refresh-b", Version: "refresh-b", Price: domain.Price{InputMicroUSDPerMTok: ptrInt64(3_000_000), Tiers: []domain.PriceTier{{ContextMinTokens: 200_000, Price: domain.Price{InputMicroUSDPerMTok: ptrInt64(4_000_000)}}}}}},
	}
	start := make(chan struct{})
	errs := make(chan error, len(rows))
	var wg sync.WaitGroup
	for i := range rows {
		wg.Add(1)
		go func(i int) { defer wg.Done(); <-start; errs <- store.ReplaceCatalog(ctx, rows[i][0].Version, rows[i]) }(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if r.count(`SELECT count(*) FROM gateway.prices WHERE vendor='gemini'`) != 1 || r.count(`SELECT count(*) FROM gateway.price_tiers`) != 1 {
		t.Fatal("concurrent refresh left a partial or combined catalog")
	}
	var model string
	var threshold int
	if err := r.owner.QueryRow(ctx, `SELECT p.model, t.context_min_tokens FROM gateway.prices p JOIN gateway.price_tiers t USING (vendor,model)`).Scan(&model, &threshold); err != nil {
		t.Fatal(err)
	}
	if !(model == "refresh-a" && threshold == 100_000) && !(model == "refresh-b" && threshold == 200_000) {
		t.Fatalf("refresh rows = %s/%d", model, threshold)
	}
}
