//go:build integration

package gateway

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/gateway/adapters/vertex"
	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/gateway/snapshot"
)

func vertexTestKey(t *testing.T, email, keyID string) string {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(map[string]string{ //nolint:gosec // ephemeral RSA test fixture generated in memory
		"type":           "service_account",
		"client_email":   email,
		"private_key_id": keyID,
		"private_key":    string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"token_uri":      "https://evil.example/should-be-stripped",
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

type vertexRoundTrip func(*http.Request) (*http.Response, error)

func (f vertexRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type vertexStreamSink struct {
	text  strings.Builder
	tools []app.ToolCallDelta
	done  *app.StreamDone
	fail  *app.StreamFail
}

func (s *vertexStreamSink) Delta(text string) error             { s.text.WriteString(text); return nil }
func (s *vertexStreamSink) Notice(app.StreamNotice) error       { return nil }
func (s *vertexStreamSink) Done(d app.StreamDone)               { s.done = &d }
func (s *vertexStreamSink) Fail(f app.StreamFail)               { s.fail = &f }
func (s *vertexStreamSink) ToolCallDelta(d app.ToolCallDelta) error {
	s.tools = append(s.tools, d)
	return nil
}

func TestFeature_UPM401_VertexProviderAddTestChangeAndSecretHygiene(t *testing.T) {
	r := newRig(t, masterKeys(t, 7))
	ctx := context.Background()
	saJSON := vertexTestKey(t, "ada-vertex@demo-proj.iam.gserviceaccount.com", "vertexkeyid0042")

	var tokenHits atomic.Int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		tokenHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"access_token":"test-probe-token","expires_in":3600}`)
	}))
	t.Cleanup(tokenSrv.Close)

	vAdapter := vertex.NewAdapter()
	vAdapter.Client = tokenSrv.Client()
	vAdapter.TokenURL = tokenSrv.URL
	r.svc.Probes[domain.AdapterVertex] = vAdapter

	regionalURL := "https://us-central1-aiplatform.googleapis.com/v1/projects/demo-proj/locations/us-central1/endpoints/openapi"

	// Reject malformed JSON and mismatched host/location.
	if _, err := r.svc.Add(ctx, ada, app.NewProvider{
		Provider: "vertex", Name: "Bad JSON", Adapter: domain.AdapterVertex, PlanKind: domain.PlanMetered,
		BaseURL: regionalURL, APIKey: "sk-not-json",
	}); err == nil {
		t.Fatal("expected invalid JSON error")
	}
	if _, err := r.svc.Add(ctx, ada, app.NewProvider{
		Provider: "vertex", Name: "Bad Region", Adapter: domain.AdapterVertex, PlanKind: domain.PlanMetered,
		BaseURL: "https://us-central1-aiplatform.googleapis.com/v1/projects/demo-proj/locations/global/endpoints/openapi",
		APIKey:  saJSON,
	}); err == nil {
		t.Fatal("expected host/location mismatch error")
	}

	// Add valid Vertex account.
	created, err := r.svc.Add(ctx, ada, app.NewProvider{
		Provider: "vertex", Name: "Ada Vertex", Adapter: domain.AdapterVertex, PlanKind: domain.PlanMetered,
		BaseURL: regionalURL, APIKey: saJSON, Models: []string{"google/gemini-2.5-flash"},
	})
	if err != nil {
		t.Fatalf("Add vertex: %v", err)
	}
	if created.Adapter != domain.AdapterVertex || created.KeyHint != "0042" || !created.HasKey {
		t.Fatalf("created = %+v", created)
	}

	// Sealed credential opens cleanly and stripped token_uri.
	secrets := DBSecrets{Pool: r.owner, Keys: r.keys}
	opened, err := secrets.Get(created.CredentialRef)
	if err != nil {
		t.Fatalf("DBSecrets.Get: %v", err)
	}
	if strings.Contains(opened, "evil.example") || strings.Contains(opened, "token_uri") {
		t.Fatalf("stored credential retained token_uri")
	}

	// Test saved account: returns OK=true, Models=nil, updates status to Connected.
	testRes, err := r.svc.Test(ctx, ada, app.TestRequest{ID: created.ID})
	if err != nil || !testRes.OK || testRes.Models != nil {
		t.Fatalf("Test = %+v err=%v", testRes, err)
	}
	if tokenHits.Load() != 1 {
		t.Fatalf("tokenHits = %d, want 1", tokenHits.Load())
	}

	// Change without APIKey keeps sealed key and hint.
	newName := "Ada Vertex Renamed"
	updated, err := r.svc.Change(ctx, ada, created.ID, app.ProviderChange{Name: &newName})
	if err != nil {
		t.Fatalf("Change: %v", err)
	}
	if updated.Name != newName || updated.KeyHint != "0042" || !updated.HasKey {
		t.Fatalf("updated = %+v", updated)
	}
	if strings.Contains(r.logs.String(), "PRIVATE KEY") || strings.Contains(r.logs.String(), "test-probe-token") {
		t.Fatalf("secret leaked into logs: %s", r.logs.String())
	}
}

func TestFeature_UPM401_VertexDispatchPricingCapabilitiesAndNoEmbed(t *testing.T) {
	r := newRig(t, masterKeys(t, 9))
	ctx := context.Background()

	// Seed the real embedded models.dev snapshot so google-vertex -> vertex is verified end-to-end.
	ver, asOf, snapRows, err := snapshot.Load()
	if err != nil {
		t.Fatalf("snapshot.Load: %v", err)
	}
	if _, err := postgres.SeedCatalog(ctx, r.owner, ver, asOf, snapRows); err != nil {
		t.Fatalf("SeedCatalog: %v", err)
	}

	// Confirm the real snapshot populated vertex rows for Gemini, exact Gemma, and MaaS.
	store := postgres.Store{Pool: r.owner}
	for _, wantModel := range []string{"gemini-2.5-flash", "google/gemma-4-26b-a4b-it-maas", "deepseek-ai/deepseek-v3.2-maas"} {
		row, ok, err := store.CatalogModel(ctx, "vertex", wantModel)
		if err != nil || !ok || row.Vendor != "vertex" {
			t.Fatalf("snapshot missing vertex/%s: ok=%v row=%+v err=%v", wantModel, ok, row, err)
		}
	}

	// Seed deterministic vertex tier & capability rows for exact pinned micro-USD assertions.
	if _, err := r.owner.Exec(ctx, `
		INSERT INTO gateway.prices (vendor, model, version, kind, inputs, outputs, input_usd_per_mtok, output_usd_per_mtok, cache_read_usd_per_mtok, cache_write_usd_per_mtok, capabilities, context_window)
		VALUES
			('vertex', 'gemini-tier-test', 'test-v1', 'chat', '{text}', '{text}', 1.25, 10.0, 0.30, 0.0, '{tools}', 1000000),
			('vertex', 'google/gemma-exact-test', 'test-v1', 'chat', '{text}', '{text}', 0.20, 0.80, NULL, NULL, '{tools}', 128000),
			('vertex', 'gemma-exact-test', 'test-v1', 'chat', '{text}', '{text}', 99.0, 99.0, NULL, NULL, '{tools}', 128000),
			('', 'google/cap-vertex-model', 'test-v1', 'chat', '{text}', '{text}', 1.0, 1.0, NULL, NULL, '{}', 128000),
			('vertex', 'cap-vertex-model', 'test-v1', 'chat', '{text}', '{text}', 2.0, 4.0, NULL, NULL, '{tools}', 128000)
		ON CONFLICT (vendor, model) DO UPDATE SET
			input_usd_per_mtok = EXCLUDED.input_usd_per_mtok,
			output_usd_per_mtok = EXCLUDED.output_usd_per_mtok,
			capabilities = EXCLUDED.capabilities`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `
		INSERT INTO gateway.price_tiers (vendor, model, context_min_tokens, input_usd_per_mtok, output_usd_per_mtok, cache_read_usd_per_mtok, cache_write_usd_per_mtok)
		VALUES ('vertex', 'gemini-tier-test', 200000, 2.50, 15.0, 0.60, 0.0)
		ON CONFLICT (vendor, model, context_min_tokens) DO UPDATE SET
			input_usd_per_mtok = EXCLUDED.input_usd_per_mtok,
			output_usd_per_mtok = EXCLUDED.output_usd_per_mtok`); err != nil {
		t.Fatal(err)
	}

	saJSON := vertexTestKey(t, "disp@demo-proj.iam.gserviceaccount.com", "dispkeyid123456")
	globalURL := "https://aiplatform.googleapis.com/v1/projects/demo-proj/locations/global/endpoints/openapi"
	acct, err := r.svc.Add(ctx, ada, app.NewProvider{
		Provider: "vertex", Name: "Dispatch Vertex", Adapter: domain.AdapterVertex, PlanKind: domain.PlanMetered,
		BaseURL: globalURL, APIKey: saJSON,
		Models: []string{
			"google/gemini-tier-test",
			"google/gemma-exact-test",
			"deepseek-ai/deepseek-v3.2-maas",
			"google/cap-vertex-model",
			"google/unknown-model-xyz",
		},
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	var mints atomic.Int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := mints.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":"tok-%d","expires_in":3600}`, n)
	}))
	t.Cleanup(tokenSrv.Close)

	var apiCalls atomic.Int32
	var force401Once atomic.Bool
	var forceStream401Once atomic.Bool
	var nextPromptTokens atomic.Int64
	nextPromptTokens.Store(1_000_000)
	var nextCompletionTokens atomic.Int64
	nextCompletionTokens.Store(1_000_000)

	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		apiCalls.Add(1)
		if !strings.HasSuffix(req.URL.Path, "/v1/projects/demo-proj/locations/global/endpoints/openapi/chat/completions") {
			http.Error(w, "wrong path "+req.URL.Path, http.StatusNotFound)
			return
		}
		auth := req.Header.Get("Authorization")
		if force401Once.CompareAndSwap(true, false) && auth == "Bearer tok-1" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if forceStream401Once.CompareAndSwap(true, false) && auth == "Bearer tok-2" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if auth != "Bearer tok-1" && auth != "Bearer tok-2" && auth != "Bearer tok-3" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(req.Body)
		if strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w,
				"data: {\"choices\":[{\"delta\":{\"content\":\"streamed\",\"tool_calls\":[{\"index\":0,\"id\":\"vc1\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{\\\"q\\\":\\\"v\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n"+
					"data: {\"usage\":{\"prompt_tokens\":%d,\"completion_tokens\":%d}}\n\n"+
					"data: [DONE]\n\n",
				nextPromptTokens.Load(), nextCompletionTokens.Load(),
			)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w,
			`{"choices":[{"finish_reason":"stop","message":{"content":"ok"}}],"usage":{"prompt_tokens":%d,"completion_tokens":%d}}`,
			nextPromptTokens.Load(), nextCompletionTokens.Load(),
		)
	}))
	t.Cleanup(apiSrv.Close)

	transport := vertexRoundTrip(func(req *http.Request) (*http.Response, error) {
		moved := req.Clone(req.Context())
		if req.URL.Hostname() == "oauth2.googleapis.com" {
			moved.URL.Scheme = "http"
			moved.URL.Host = strings.TrimPrefix(tokenSrv.URL, "http://")
		} else {
			moved.URL.Scheme = "http"
			moved.URL.Host = strings.TrimPrefix(apiSrv.URL, "http://")
		}
		moved.Host = ""
		return http.DefaultTransport.RoundTrip(moved)
	})
	vAdapter := vertex.NewAdapter()
	vAdapter.Client = &http.Client{Transport: transport}

	disp := app.Dispatch{
		Routes:  store,
		Pins:    store,
		Secrets: DBSecrets{Pool: r.owner, Keys: r.keys},
		Prices:  store,
		Ledger:  store,
		Limits:  store,
		Teams:   store,
		Chat:    map[string]app.Chatter{domain.AdapterVertex: vAdapter},
		Stream:  map[string]app.StreamChatter{domain.AdapterVertex: vAdapter},
		Embed:   map[string]app.Embedder{}, // vertex intentionally absent
		Gates:   app.NewGates(),
		VendorCaps: func(vendor, model string) ([]string, bool) {
			have, ok, cerr := store.CapabilitiesForVendor(ctx, vendor, model)
			if cerr != nil || !ok {
				return nil, false
			}
			return have, true
		},
	}

	makeRoute := func(routeName, capability, model string, need []string) {
		t.Helper()
		if need == nil {
			need = []string{}
		}
		var routeID string
		if err := r.owner.QueryRow(ctx, `
			INSERT INTO gateway.routes (name, capability, installed) VALUES ($1, $2, true) RETURNING id::text`,
			routeName, capability).Scan(&routeID); err != nil {
			t.Fatal(err)
		}
		if _, err := r.owner.Exec(ctx, `
			INSERT INTO gateway.route_links (route_id, position, provider_account_id, model, retry_budget, required_capabilities)
			VALUES ($1::uuid, 1, $2::uuid, $3, 0, $4)`,
			routeID, acct.ID, model, need); err != nil {
			t.Fatal(err)
		}
	}

	latestLedger := func(model string) (cost *int64, source string, estimated bool) {
		t.Helper()
		if err := r.owner.QueryRow(ctx, `
			SELECT cost_micro_usd, price_source, estimated
			FROM gateway.ledger
			WHERE provider_account_id = $1::uuid AND model = $2
			ORDER BY at DESC, id DESC LIMIT 1`, acct.ID, model).Scan(&cost, &source, &estimated); err != nil {
			t.Fatalf("ledger query %s: %v", model, err)
		}
		return cost, source, estimated
	}

	who := domain.Principal{User: ada, Source: domain.SourceChat}
	input := domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}

	// 1. Base tier (<=200k input) on google/gemini-tier-test -> resolves vertex/gemini-tier-test:
	//    100k * $1.25/MTok + 100k * $10/MTok = 125_000 + 1_000_000 = 1_125_000 micro-USD.
	makeRoute("v-tier", "chat", "google/gemini-tier-test", nil)
	nextPromptTokens.Store(100_000)
	nextCompletionTokens.Store(100_000)
	if _, err := disp.ChatCall(ctx, "v-tier", "", who, input); err != nil {
		t.Fatalf("base tier chat: %v", err)
	}
	cost, src, est := latestLedger("google/gemini-tier-test")
	if cost == nil || *cost != 1_125_000 || src != "models_dev" || est {
		t.Fatalf("base tier ledger = cost:%v src:%q est:%t, want 1125000 models_dev false", cost, src, est)
	}
	if mints.Load() != 1 {
		t.Fatalf("mints after call 1 = %d, want 1", mints.Load())
	}

	// 2. High tier (>200k input) uses cached token (mints still 1) and resolves vertex/gemini-tier-test >200k tier:
	//    300k * $2.50/MTok + 100k * $15/MTok = 750_000 + 1_500_000 = 2_250_000 micro-USD.
	nextPromptTokens.Store(300_000)
	nextCompletionTokens.Store(100_000)
	if _, err := disp.ChatCall(ctx, "v-tier", "", who, input); err != nil {
		t.Fatalf("high tier chat: %v", err)
	}
	cost, src, est = latestLedger("google/gemini-tier-test")
	if cost == nil || *cost != 2_250_000 || src != "models_dev" || est {
		t.Fatalf("high tier ledger = cost:%v src:%q est:%t, want 2250000 models_dev false", cost, src, est)
	}
	if mints.Load() != 1 {
		t.Fatalf("mints after cached call 2 = %d, want 1", mints.Load())
	}

	// 3. Exact match preserved for google/gemma-exact-test ($0.20 in, $0.80 out), NOT stripped to gemma-exact-test ($99/$99),
	//    AND exercises 401 cache eviction + single retry (mints becomes 2).
	makeRoute("v-gemma", "chat", "google/gemma-exact-test", nil)
	force401Once.Store(true)
	nextPromptTokens.Store(1_000_000)
	nextCompletionTokens.Store(1_000_000)
	if _, err := disp.ChatCall(ctx, "v-gemma", "", who, input); err != nil {
		t.Fatalf("gemma chat: %v", err)
	}
	cost, src, est = latestLedger("google/gemma-exact-test")
	if cost == nil || *cost != 1_000_000 || src != "models_dev" || est {
		t.Fatalf("gemma exact ledger = cost:%v src:%q est:%t, want 1000000 models_dev false", cost, src, est)
	}
	if mints.Load() != 2 {
		t.Fatalf("mints after 401 retry = %d, want 2", mints.Load())
	}

	// 4. Capability-gated dispatch resolves vertex/cap-vertex-model (with tools) instead of ''/google/cap-vertex-model (no tools).
	makeRoute("v-cap", "chat", "google/cap-vertex-model", []string{"tools"})
	nextPromptTokens.Store(1_000_000)
	nextCompletionTokens.Store(1_000_000)
	if _, err := disp.ChatCall(ctx, "v-cap", "", who, input); err != nil {
		t.Fatalf("capability-gated chat failed: %v", err)
	}
	cost, src, est = latestLedger("google/cap-vertex-model")
	if cost == nil || *cost != 6_000_000 || src != "models_dev" || est {
		t.Fatalf("cap model ledger = cost:%v src:%q est:%t, want 6000000 models_dev false", cost, src, est)
	}

	// 5. Partial per-account price override on google/gemini-tier-test (input override $5.00/MTok, output falls back to catalog $10.00/MTok):
	//    100k * $5 + 100k * $10 = 1_500_000 micro-USD.
	inOverride := int64(5_000_000)
	if _, err := r.owner.Exec(ctx, `
		INSERT INTO gateway.price_overrides (account_id, model, input_micro_usd_per_mtok, updated_by)
		VALUES ($1::uuid, 'google/gemini-tier-test', $2, $3::uuid)`, acct.ID, inOverride, ada); err != nil {
		t.Fatal(err)
	}
	nextPromptTokens.Store(100_000)
	nextCompletionTokens.Store(100_000)
	if _, err := disp.ChatCall(ctx, "v-tier", "", who, input); err != nil {
		t.Fatalf("partial override chat: %v", err)
	}
	cost, src, est = latestLedger("google/gemini-tier-test")
	if cost == nil || *cost != 1_500_000 || src != "override" || est {
		t.Fatalf("partial override ledger = cost:%v src:%q est:%t, want 1500000 override false", cost, src, est)
	}

	// 6. pricing_models alias + pricing_provider override + unknown model (NULL cost when vendor has no catalog rows).
	if _, err := r.owner.Exec(ctx, `
		UPDATE gateway.provider_accounts
		SET pricing_models = '{"google/unknown-model-xyz":"gemini-tier-test"}'::jsonb
		WHERE id = $1::uuid`, acct.ID); err != nil {
		t.Fatal(err)
	}
	makeRoute("v-alias", "chat", "google/unknown-model-xyz", nil)
	nextPromptTokens.Store(100_000)
	nextCompletionTokens.Store(100_000)
	if _, err := disp.ChatCall(ctx, "v-alias", "", who, input); err != nil {
		t.Fatalf("pricing_models alias chat: %v", err)
	}
	cost, src, est = latestLedger("google/unknown-model-xyz")
	if cost == nil || *cost != 1_125_000 || src != "models_dev" || est {
		t.Fatalf("pricing_models alias ledger = cost:%v src:%q est:%t, want 1125000 models_dev false", cost, src, est)
	}

	// Clear pricing_models on the Vertex host -> unknown model falls back to Vertex's vendor estimate rate.
	if _, err := r.owner.Exec(ctx, `
		UPDATE gateway.provider_accounts
		SET pricing_models = '{}'::jsonb
		WHERE id = $1::uuid`, acct.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := disp.ChatCall(ctx, "v-alias", "", who, input); err != nil {
		t.Fatalf("vertex estimate unknown model chat: %v", err)
	}
	cost, src, est = latestLedger("google/unknown-model-xyz")
	if cost == nil || *cost <= 0 || src != "estimate" || !est {
		t.Fatalf("vertex unknown model ledger = cost:%v src:%q est:%t, want positive estimate true", cost, src, est)
	}

	// Pointing at a custom proxy host (no PricingVendor, no pricing_provider) -> unknown model records NULL cost (never 0).
	if _, err := r.owner.Exec(ctx, `
		UPDATE gateway.provider_accounts
		SET base_url = 'https://vertex-proxy.example.com/v1/projects/demo-proj/locations/global/endpoints/openapi',
		    pricing_provider = ''
		WHERE id = $1::uuid`, acct.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := disp.ChatCall(ctx, "v-alias", "", who, input); err != nil {
		t.Fatalf("proxy unknown model chat: %v", err)
	}
	cost, src, est = latestLedger("google/unknown-model-xyz")
	if cost != nil || src != "unknown" || est {
		t.Fatalf("unknown model ledger = cost:%v src:%q est:%t, want nil unknown false", cost, src, est)
	}

	// Setting pricing_provider = 'vertex' on a custom proxy host restores exact Vertex alias pricing.
	if _, err := r.owner.Exec(ctx, `
		UPDATE gateway.provider_accounts
		SET pricing_provider = 'vertex'
		WHERE id = $1::uuid`, acct.ID); err != nil {
		t.Fatal(err)
	}
	nextPromptTokens.Store(100_000)
	nextCompletionTokens.Store(100_000)
	if _, err := disp.ChatCall(ctx, "v-tier", "", who, input); err != nil {
		t.Fatalf("proxy with pricing_provider=vertex chat: %v", err)
	}
	cost, src, est = latestLedger("google/gemini-tier-test")
	if cost == nil || *cost != 1_500_000 || src != "override" || est {
		t.Fatalf("proxy pricing_provider=vertex ledger = cost:%v src:%q est:%t, want 1500000 override false", cost, src, est)
	}

	// 7. Streamed chat with tool-call deltas + cached-token 401 recovery + exact new ledger row.
	var beforeStreamRows int
	if err := r.owner.QueryRow(ctx, `
		SELECT count(*) FROM gateway.ledger
		WHERE provider_account_id = $1::uuid AND model = 'google/gemini-tier-test'`, acct.ID).Scan(&beforeStreamRows); err != nil {
		t.Fatal(err)
	}
	forceStream401Once.Store(true)
	nextPromptTokens.Store(120_000)
	nextCompletionTokens.Store(80_000)
	sink := &vertexStreamSink{}
	if err := disp.ChatStreamCall(ctx, "v-tier", "", who, input, 0, sink); err != nil {
		t.Fatalf("ChatStreamCall: %v", err)
	}
	if sink.fail != nil || sink.done == nil || sink.text.String() != "streamed" {
		t.Fatalf("stream sink = text:%q done:%+v fail:%+v", sink.text.String(), sink.done, sink.fail)
	}
	if len(sink.tools) != 1 || sink.tools[0].ID != "vc1" || sink.tools[0].Name != "lookup" {
		t.Fatalf("stream tool deltas = %+v", sink.tools)
	}
	// 120k * $5.00 override + 80k * $10.00 catalog = 600_000 + 800_000 = 1_400_000 micro-USD.
	if sink.done.CostMicroUSD == nil || *sink.done.CostMicroUSD != 1_400_000 {
		t.Fatalf("stream done cost = %v, want 1400000", sink.done.CostMicroUSD)
	}
	if mints.Load() != 3 {
		t.Fatalf("mints after streamed 401 retry = %d, want 3", mints.Load())
	}
	var afterStreamRows, gotIn, gotOut int
	if err := r.owner.QueryRow(ctx, `
		SELECT count(*) FROM gateway.ledger
		WHERE provider_account_id = $1::uuid AND model = 'google/gemini-tier-test'`, acct.ID).Scan(&afterStreamRows); err != nil {
		t.Fatal(err)
	}
	if afterStreamRows != beforeStreamRows+1 {
		t.Fatalf("streamed ledger rows = %d, want %d", afterStreamRows, beforeStreamRows+1)
	}
	if err := r.owner.QueryRow(ctx, `
		SELECT input_tokens, output_tokens, cost_micro_usd, price_source, estimated
		FROM gateway.ledger
		WHERE provider_account_id = $1::uuid AND model = 'google/gemini-tier-test'
		ORDER BY at DESC, id DESC LIMIT 1`, acct.ID).Scan(&gotIn, &gotOut, &cost, &src, &est); err != nil {
		t.Fatal(err)
	}
	if gotIn != 120_000 || gotOut != 80_000 || cost == nil || *cost != 1_400_000 || src != "override" || est {
		t.Fatalf("streamed ledger = in:%d out:%d cost:%v src:%q est:%t, want 120000 80000 1400000 override false", gotIn, gotOut, cost, src, est)
	}

	// 8. Embed route reaching a Vertex-only link is refused without calling Vertex or writing a ledger row.
	makeRoute("v-embed", "embed", "google/gemini-tier-test", nil)
	beforeCalls := apiCalls.Load()
	_, _, err = disp.EmbedCall(ctx, "v-embed", "", who, []string{"hello"})
	if err == nil {
		t.Fatal("expected embed on Vertex-only route to fail")
	}
	if apiCalls.Load() != beforeCalls {
		t.Fatalf("Vertex API called during embed: before=%d after=%d", beforeCalls, apiCalls.Load())
	}
}

func TestFeature_UPM401_VertexDailyCheckPreservesModelsAndCatchesRevokedKey(t *testing.T) {
	r := newRig(t, masterKeys(t, 11))
	ctx := context.Background()
	saJSON := vertexTestKey(t, "check@demo-proj.iam.gserviceaccount.com", "checkkeyid0099")
	acct, err := r.svc.Add(ctx, ada, app.NewProvider{
		Provider: "vertex", Name: "Daily Vertex", Adapter: domain.AdapterVertex, PlanKind: domain.PlanMetered,
		BaseURL: "https://aiplatform.googleapis.com/v1/projects/demo-proj/locations/global/endpoints/openapi",
		APIKey:  saJSON,
		Models:  []string{"google/gemini-2.5-flash"},
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	var revoked atomic.Bool
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if revoked.Load() {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `{"error":"invalid_grant","error_description":"key disabled"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"access_token":"tok-daily","expires_in":3600}`)
	}))
	t.Cleanup(tokenSrv.Close)

	vAdapter := vertex.NewAdapter()
	vAdapter.Client = tokenSrv.Client()
	vAdapter.TokenURL = tokenSrv.URL

	var notified []string
	deps := ProviderCheckDeps{
		Owner:   r.owner,
		App:     r.app,
		Secrets: DBSecrets{Pool: r.owner, Keys: r.keys},
		Probes:  map[string]app.Prober{domain.AdapterVertex: vAdapter},
		ActiveUser: func(context.Context, string) (bool, error) {
			return true, nil
		},
		Notify: func(_ context.Context, _ pgx.Tx, _ string, title string) error {
			notified = append(notified, title)
			return nil
		},
	}

	// Run 3 consecutive healthy daily checks: manually added model must never accrue misses or retire.
	for i := range 3 {
		if err := CheckAccountOnce(ctx, deps, acct.ID); err != nil {
			t.Fatalf("CheckAccountOnce %d: %v", i, err)
		}
	}
	var status string
	var misses int
	var retired bool
	if err := r.owner.QueryRow(ctx, `
		SELECT a.status, pm.misses, pm.retired
		FROM gateway.provider_accounts a
		JOIN gateway.provider_models pm ON pm.account_id = a.id
		WHERE a.id = $1::uuid`, acct.ID).Scan(&status, &misses, &retired); err != nil {
		t.Fatal(err)
	}
	if status != domain.StatusConnected || misses != 0 || retired {
		t.Fatalf("after 3 checks: status=%q misses=%d retired=%t", status, misses, retired)
	}

	// Revoke key (400 invalid_grant): daily check bypasses cache, marks Key rejected, and notifies owner.
	revoked.Store(true)
	if err := CheckAccountOnce(ctx, deps, acct.ID); err != nil {
		t.Fatalf("CheckAccountOnce revoked: %v", err)
	}
	if err := r.owner.QueryRow(ctx, `SELECT status FROM gateway.provider_accounts WHERE id = $1::uuid`, acct.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != domain.StatusKeyRejected || len(notified) != 1 || !strings.Contains(notified[0], "API key was rejected") {
		t.Fatalf("after revoke: status=%q notified=%v", status, notified)
	}
}
