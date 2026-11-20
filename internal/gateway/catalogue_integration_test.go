//go:build integration

package gateway

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

//go:embed domain/testdata/models_dev.json
var testModelsDevFixture []byte

type recordingChatter struct {
	calls int
	usage domain.Usage
}

func (c *recordingChatter) Chat(_ context.Context, attempt app.Attempt, _ domain.Request) (domain.ChatResult, domain.FailureClass, error) {
	c.calls++
	return domain.ChatResult{
		Text:  "ok",
		Model: attempt.Model,
		Usage: c.usage,
	}, domain.ClassOK, nil
}

type staticSecrets map[string]string

func (s staticSecrets) Get(ref string) (string, error) {
	v, ok := s[ref]
	if !ok {
		return "", errors.New("missing secret")
	}
	return v, nil
}

func loadFixtureCatalog(t *testing.T, ctx context.Context, store postgres.Store, version string) {
	t.Helper()
	rows, err := domain.ParseCatalog(bytes.NewReader(testModelsDevFixture), version)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceCatalog(ctx, version, rows); err != nil {
		t.Fatal(err)
	}
}

func insertProviderAndRoute(t *testing.T, r *rig, routeName, vendor, acctName, ref, model string, connected bool) string {
	t.Helper()
	ctx := context.Background()
	status := domain.StatusNotConnected
	if connected {
		status = domain.StatusConnected
	}
	adapter := domain.AdapterFake
	baseURL := "https://api.example.com"
	if v, ok := domain.VendorFor(vendor); ok && !v.Local {
		adapter = v.Adapter
		baseURL = v.BaseURL
	}
	var acctID string
	if err := r.owner.QueryRow(ctx, `
		INSERT INTO gateway.provider_accounts
			(owner_user_id, provider, name, adapter, credential_ref, plan_kind, house, base_url, status, visibility)
		VALUES ($1::uuid, $2, $3, $4, $5, 'metered', false, $6, $7, 'private')
		RETURNING id::text`,
		ada, vendor, acctName, adapter, ref, baseURL, status,
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
		INSERT INTO gateway.routes (name, capability, installed)
		VALUES ($1, 'chat', true)
		RETURNING id::text`, routeName).Scan(&routeID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `
		INSERT INTO gateway.route_links (route_id, position, provider_account_id, model, retry_budget)
		VALUES ($1::uuid, 1, $2::uuid, $3, 0)`, routeID, acctID, model); err != nil {
		t.Fatal(err)
	}
	return acctID
}

func TestFeature_UPM363_SameModelDifferentProviderPrices(t *testing.T) {
	// Given a refreshed catalogue with gpt-4o priced differently under openai ($2.50/$10)
	// and openrouter ($2.75/$11), and two metered accounts pointing at those two vendors.
	ctx := context.Background()
	r := newRig(t, masterKeys(t, 'a'))
	store := postgres.Store{Pool: r.owner}
	loadFixtureCatalog(t, ctx, store, "cat-v1")

	insertProviderAndRoute(t, r, "route-openai", "openai", "OpenAI Account", "ref_oai", "gpt-4o", true)
	insertProviderAndRoute(t, r, "route-openrouter", "openrouter", "OpenRouter Account", "ref_or", "gpt-4o", true)

	chatter := &recordingChatter{
		usage: domain.Usage{Arrived: true, Input: 1_000_000, Output: 1_000_000},
	}
	disp := app.Dispatch{
		Routes:  store,
		Teams:   store,
		Prices:  store,
		Ledger:  store,
		Secrets: staticSecrets{"ref_oai": "k1", "ref_or": "k2"},
		Chat:    map[string]app.Chatter{domain.AdapterOpenAI: chatter, domain.AdapterAnthropic: chatter, domain.AdapterFake: chatter},
	}
	in := domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}}

	// When one call runs through each route via Dispatch.
	resOAI, err := disp.ChatCall(ctx, "route-openai", "", domain.Principal{User: ada, Source: domain.SourceChat}, in)
	if err != nil {
		t.Fatalf("openai ChatCall: %v", err)
	}
	resOR, err := disp.ChatCall(ctx, "route-openrouter", "", domain.Principal{User: ada, Source: domain.SourceChat}, in)
	if err != nil {
		t.Fatalf("openrouter ChatCall: %v", err)
	}

	// Then each call records its own vendor's cost in gateway.ledger and returns it.
	if resOAI.CostUSD == nil || *resOAI.CostUSD != 12.5 {
		t.Fatalf("openai cost = %v, want 12.5", resOAI.CostUSD)
	}
	if resOR.CostUSD == nil || *resOR.CostUSD != 13.75 {
		t.Fatalf("openrouter cost = %v, want 13.75", resOR.CostUSD)
	}
	rows, err := r.owner.Query(ctx, `SELECT route, cost_micro_usd FROM gateway.ledger ORDER BY route`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]int64{}
	for rows.Next() {
		var route string
		var cost int64
		if err := rows.Scan(&route, &cost); err != nil {
			t.Fatal(err)
		}
		got[route] = cost
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if got["route-openai"] != 12_500_000 || got["route-openrouter"] != 13_750_000 {
		t.Fatalf("ledger costs = %v, want openai=12500000 openrouter=13750000", got)
	}
}

func TestFeature_UPM363_UnknownVendorFallsBackToModelOnlyRow(t *testing.T) {
	// Given a catalogue where "bedrock-only-model" comes from a models.dev provider
	// with no picker vendor (so only the '' fallback row exists at $2/$8 per MTok),
	// and an account whose vendor label ("custom-proxy") is unknown to models.dev.
	ctx := context.Background()
	r := newRig(t, masterKeys(t, 'a'))
	store := postgres.Store{Pool: r.owner}
	loadFixtureCatalog(t, ctx, store, "cat-v1")

	insertProviderAndRoute(t, r, "route-custom", "custom-proxy", "Custom Proxy", "ref_custom", "bedrock-only-model", true)

	chatter := &recordingChatter{
		usage: domain.Usage{Arrived: true, Input: 1_000_000, Output: 500_000},
	}
	disp := app.Dispatch{
		Routes:  store,
		Teams:   store,
		Prices:  store,
		Ledger:  store,
		Secrets: staticSecrets{"ref_custom": "k1"},
		Chat:    map[string]app.Chatter{domain.AdapterFake: chatter},
	}

	// When a chat call runs through Dispatch.
	res, err := disp.ChatCall(ctx, "route-custom", "", domain.Principal{User: ada, Source: domain.SourceChat}, domain.Input{
		Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("custom ChatCall: %v", err)
	}

	// Then it prices from the model-only fallback row ($2 + 0.5*$8 = $6.00).
	if res.CostUSD == nil || *res.CostUSD != 6.0 {
		t.Fatalf("fallback cost = %v, want 6.0", res.CostUSD)
	}
	var ledgerCost int64
	if err := r.owner.QueryRow(ctx, `SELECT cost_micro_usd FROM gateway.ledger WHERE route = 'route-custom'`).Scan(&ledgerCost); err != nil {
		t.Fatal(err)
	}
	if ledgerCost != 6_000_000 {
		t.Fatalf("ledger cost = %d, want 6000000", ledgerCost)
	}
}

func TestFeature_UPM363_MergeSourcePrecedenceRead(t *testing.T) {
	// Given a catalogue row for gpt-4o under openrouter, a stored account_models_seen
	// entry for the same model with provider pricing and context_length, and an
	// optional override on input_usd_per_mtok.
	ctx := context.Background()
	r := newRig(t, masterKeys(t, 'a'))
	store := postgres.Store{Pool: r.owner}
	loadFixtureCatalog(t, ctx, store, "cat-v1")

	acctID := insertProviderAndRoute(t, r, "route-merge", "openrouter", "OpenRouter Merge", "ref_m", "gpt-4o", true)
	if _, err := r.owner.Exec(ctx, `
		INSERT INTO gateway.account_models_seen (account_id, model_id, position, context_length, pricing, modalities)
		VALUES ($1::uuid, 'gpt-4o', 1, 64000,
			'{"prompt":"0.000004","completion":"0.000016"}'::jsonb,
			'{"input":["text","image","pdf"],"output":["text"]}'::jsonb)`, acctID); err != nil {
		t.Fatal(err)
	}

	// When reading the merged model from Postgres without an override.
	merged, err := r.svc.MergedModel(ctx, ada, acctID, "gpt-4o", nil)
	if err != nil {
		t.Fatalf("MergedModel without override: %v", err)
	}

	// Then provider fields beat models_dev while models_dev fills fields the provider omitted.
	if merged.Sources.ContextWindow != domain.SourceProvider || merged.ContextWindow != 64000 {
		t.Fatalf("context window = %d (%s), want 64000 (provider)", merged.ContextWindow, merged.Sources.ContextWindow)
	}
	if merged.Sources.InputPerMTok != domain.SourceProvider || *merged.InputPerMTok != 4.0 {
		t.Fatalf("input rate = %v (%s), want 4.0 (provider)", merged.InputPerMTok, merged.Sources.InputPerMTok)
	}
	if merged.Sources.Inputs != domain.SourceProvider || !slices.Contains(merged.Inputs, domain.InputFile) {
		t.Fatalf("inputs = %v (%s), want file from provider", merged.Inputs, merged.Sources.Inputs)
	}
	if merged.Sources.Kind != domain.SourceModelsDev || merged.Kind != domain.KindChat ||
		merged.Sources.MaxOutput != domain.SourceModelsDev || *merged.MaxOutput != 16384 {
		t.Fatalf("models_dev fields = %+v (sources %+v)", merged, merged.Sources)
	}

	// When an override is supplied for input_usd_per_mtok.
	ovIn := 1.5
	withOv, err := r.svc.MergedModel(ctx, ada, acctID, "gpt-4o", &domain.ModelOverride{InputPerMTok: &ovIn})
	if err != nil {
		t.Fatalf("MergedModel with override: %v", err)
	}

	// Then the override beats the provider's list.
	if withOv.Sources.InputPerMTok != domain.SourceOverride || *withOv.InputPerMTok != 1.5 {
		t.Fatalf("override input rate = %v (%s), want 1.5 (override)", withOv.InputPerMTok, withOv.Sources.InputPerMTok)
	}
	if withOv.Sources.OutputPerMTok != domain.SourceProvider || *withOv.OutputPerMTok != 16.0 {
		t.Fatalf("non-overridden output rate = %v (%s), want 16.0 (provider)", withOv.OutputPerMTok, withOv.Sources.OutputPerMTok)
	}
}

func TestFeature_UPM363_EmbeddingExcludedAndUnsupportedImageRefused(t *testing.T) {
	// Given a refreshed catalogue with text-embedding-3-small (embedding), gpt-3.5-turbo
	// (text-only chat), and gpt-4o (image-input chat), and a Connected openai account
	// listing all three models.
	ctx := context.Background()
	r := newRig(t, masterKeys(t, 'a'))
	store := postgres.Store{Pool: r.owner}
	loadFixtureCatalog(t, ctx, store, "cat-v1")

	var acctID string
	if err := r.owner.QueryRow(ctx, `
		INSERT INTO gateway.provider_accounts
			(owner_user_id, provider, name, adapter, credential_ref, plan_kind, house, base_url, status, visibility)
		VALUES ($1::uuid, 'openai', 'OpenAI Mixed', 'fake', 'ref_mix', 'metered', false, 'https://api.openai.com', 'Connected', 'private')
		RETURNING id::text`, ada).Scan(&acctID); err != nil {
		t.Fatal(err)
	}
	for i, m := range []string{"text-embedding-3-small", "gpt-3.5-turbo", "gpt-4o"} {
		if _, err := r.owner.Exec(ctx, `
			INSERT INTO gateway.provider_models (account_id, model, position, listed)
			VALUES ($1::uuid, $2, $3, true)`, acctID, m, i+1); err != nil {
			t.Fatal(err)
		}
	}
	var routeID string
	if err := r.owner.QueryRow(ctx, `
		INSERT INTO gateway.routes (name, capability, installed)
		VALUES ('text-only-route', 'chat', true)
		RETURNING id::text`).Scan(&routeID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `
		INSERT INTO gateway.route_links (route_id, position, provider_account_id, model, retry_budget)
		VALUES ($1::uuid, 1, $2::uuid, 'gpt-3.5-turbo', 0)`, routeID, acctID); err != nil {
		t.Fatal(err)
	}

	// When listing providers for pickers and checking UsableAgent.
	code, raw := r.call(ada, http.MethodGet, "/api/providers", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/providers = %d %s", code, raw)
	}
	listed := decode[[]account](t, raw)
	if len(listed) != 1 {
		t.Fatalf("listed accounts = %d, want 1", len(listed))
	}

	// Then the embedding model is excluded from chat usable_models while chat models remain.
	if slices.Contains(listed[0].UsableModels, "text-embedding-3-small") {
		t.Fatalf("embedding model offered to chat: %v", listed[0].UsableModels)
	}
	if !slices.Equal(listed[0].UsableModels, []string{"gpt-3.5-turbo", "gpt-4o"}) {
		t.Fatalf("usable_models = %v, want [gpt-3.5-turbo gpt-4o]", listed[0].UsableModels)
	}
	accounts, err := r.svc.List(ctx, ada)
	if err != nil {
		t.Fatal(err)
	}
	if UsableAgent(true, "", "openai", "text-embedding-3-small", nil, accounts, "") {
		t.Fatal("UsableAgent accepted an embedding model")
	}

	// When sending an image part to the text-only chat model through Dispatch.
	chatter := &recordingChatter{}
	disp := app.Dispatch{
		Routes:  store,
		Teams:   store,
		Prices:  store,
		Ledger:  store,
		Secrets: staticSecrets{"ref_mix": "k1"},
		Chat:    map[string]app.Chatter{domain.AdapterFake: chatter},
	}
	_, err = disp.ChatCall(ctx, "text-only-route", "", domain.Principal{User: ada}, domain.Input{
		Messages: []domain.Message{{
			Role:  domain.RoleUser,
			Parts: []domain.Part{{Kind: domain.InputImage, MIMEType: "image/png", Data: "base64"}},
		}},
	})

	// Then it is refused with a typed error naming gpt-4o before any provider call.
	var unsupported domain.UnsupportedInputError
	if !errors.As(err, &unsupported) {
		t.Fatalf("err = %v, want UnsupportedInputError", err)
	}
	if unsupported.Model != "gpt-3.5-turbo" || unsupported.Input != domain.InputImage || unsupported.Suggestion != "gpt-4o" {
		t.Fatalf("unsupported = %+v", unsupported)
	}
	if !strings.Contains(err.Error(), "gpt-4o") {
		t.Fatalf("error message %q does not name usable vision model gpt-4o", err.Error())
	}
	if chatter.calls != 0 {
		t.Fatalf("provider was called %d times, want 0", chatter.calls)
	}
}

func TestFeature_UPM363_VendorAndCatalogRoutes(t *testing.T) {
	// Given a migrated database with the recorded models.dev fixture loaded.
	ctx := context.Background()
	r := newRig(t, masterKeys(t, 'a'))
	loadFixtureCatalog(t, ctx, postgres.Store{Pool: r.owner}, "cat-v1")

	// When an unauthenticated or bearer request hits either route.
	for _, path := range []string{"/api/providers/vendors", "/api/providers/catalog?vendor=openai"} {
		if code, _ := r.call("", http.MethodGet, path, nil); code != http.StatusUnauthorized {
			t.Fatalf("%s unauthenticated = %d, want 401", path, code)
		}
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer tok")
		rec := httptest.NewRecorder()
		r.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s bearer = %d, want 403", path, rec.Code)
		}
	}

	// When a signed-in person requests GET /api/providers/vendors.
	code, raw := r.call(ada, http.MethodGet, "/api/providers/vendors", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/providers/vendors = %d %s", code, raw)
	}
	type vendorResp struct {
		Key            string `json:"key"`
		Name           string `json:"name"`
		Adapter        string `json:"adapter"`
		DefaultBaseURL string `json:"default_base_url"`
		SuggestedPlan  string `json:"suggested_plan"`
	}
	vendors := decode[[]vendorResp](t, raw)
	if len(vendors) != len(domain.Vendors()) || vendors[0].Key != "anthropic" || vendors[0].Name != "Anthropic" ||
		vendors[0].Adapter != domain.AdapterAnthropic || vendors[0].DefaultBaseURL != "https://api.anthropic.com" ||
		vendors[0].SuggestedPlan != domain.PlanMetered {
		t.Fatalf("vendors = %+v", vendors)
	}

	// When a signed-in person requests GET /api/providers/catalog?vendor=openai via the app pool.
	code, raw = r.call(ada, http.MethodGet, "/api/providers/catalog?vendor=openai", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/providers/catalog = %d %s", code, raw)
	}
	type catalogResp struct {
		Vendor         string   `json:"vendor"`
		Model          string   `json:"model"`
		Kind           string   `json:"kind"`
		Inputs         []string `json:"inputs"`
		Outputs        []string `json:"outputs"`
		Tools          bool     `json:"tools"`
		Structured     bool     `json:"structured"`
		ContextWindow  int      `json:"context_window"`
		MaxOutput      *int     `json:"max_output"`
		Lifecycle      string   `json:"lifecycle"`
		RetiresAt      string   `json:"retires_at"`
		InputPerMTok   *float64 `json:"input_usd_per_mtok"`
		OutputPerMTok  *float64 `json:"output_usd_per_mtok"`
		CacheReadMTok  *float64 `json:"cache_read_usd_per_mtok"`
		CacheWriteMTok *float64 `json:"cache_write_usd_per_mtok"`
	}
	rows := decode[[]catalogResp](t, raw)
	if len(rows) != 3 {
		t.Fatalf("openai catalog rows = %d: %s", len(rows), raw)
	}
	byModel := map[string]catalogResp{}
	for _, row := range rows {
		byModel[row.Model] = row
	}
	gpt4o := byModel["gpt-4o"]
	if gpt4o.Kind != "chat" || !slices.Equal(gpt4o.Inputs, []string{"text", "image"}) ||
		!slices.Equal(gpt4o.Outputs, []string{"text"}) || !gpt4o.Tools || !gpt4o.Structured ||
		gpt4o.ContextWindow != 128000 || gpt4o.MaxOutput == nil || *gpt4o.MaxOutput != 16384 ||
		gpt4o.InputPerMTok == nil || *gpt4o.InputPerMTok != 2.5 ||
		gpt4o.OutputPerMTok == nil || *gpt4o.OutputPerMTok != 10.0 ||
		gpt4o.CacheReadMTok == nil || *gpt4o.CacheReadMTok != 1.25 {
		t.Fatalf("gpt-4o catalog row = %+v", gpt4o)
	}
	dep := byModel["gpt-3.5-turbo"]
	if dep.Lifecycle != "deprecated" || dep.RetiresAt != "2026-12-31" {
		t.Fatalf("gpt-3.5-turbo lifecycle = %+v", dep)
	}
	emb := byModel["text-embedding-3-small"]
	if emb.Kind != "embedding" || len(emb.Outputs) != 0 {
		t.Fatalf("embedding row = %+v", emb)
	}

	// Empty or unknown vendor returns an empty array (never the '' fallback rows).
	for _, q := range []string{"", "unknown-vendor"} {
		code, raw = r.call(ada, http.MethodGet, "/api/providers/catalog?vendor="+q, nil)
		if code != http.StatusOK || strings.TrimSpace(string(raw)) != "[]" {
			t.Fatalf("catalog?vendor=%q = %d %s, want []", q, code, raw)
		}
	}
}

func TestFeature_UPM363_EstimateUsesVendorHighestRate(t *testing.T) {
	// Given two vendors seeded at different top rates, plus a high '' fallback row
	// that must never be used for vendor HighestRate estimation, and a third vendor
	// with no catalog rows.
	ctx := context.Background()
	r := newRig(t, masterKeys(t, 'a'))
	store := postgres.Store{Pool: r.owner}

	ptr := func(v float64) *float64 { return &v }
	if err := store.ReplaceCatalog(ctx, "cat-est-v1", []domain.CatalogRow{
		{
			Vendor:  "openai",
			Model:   "priced-openai",
			Version: "cat-est-v1",
			Price:   domain.Price{Version: "cat-est-v1", InputPerMTok: ptr(10.0), OutputPerMTok: ptr(20.0)},
		},
		{
			Vendor:  "anthropic",
			Model:   "priced-anthropic",
			Version: "cat-est-v1",
			Price:   domain.Price{Version: "cat-est-v1", InputPerMTok: ptr(3.0), OutputPerMTok: ptr(15.0)},
		},
		{
			Vendor:  "",
			Model:   "fallback-only",
			Version: "cat-est-v1",
			Price:   domain.Price{Version: "cat-est-v1", InputPerMTok: ptr(99.0), OutputPerMTok: ptr(99.0)},
		},
	}); err != nil {
		t.Fatal(err)
	}

	insertProviderAndRoute(t, r, "route-est-openai", "OpenAI", "OpenAI Est", "ref_oai", "unpriced-model", true)
	insertProviderAndRoute(t, r, "route-est-anthropic", "anthropic", "Anthropic Est", "ref_ant", "unpriced-model", true)
	insertProviderAndRoute(t, r, "route-est-none", "custom-no-rows", "Custom Est", "ref_none", "unpriced-model", true)

	chatter := &recordingChatter{}
	disp := app.Dispatch{
		Routes:  store,
		Teams:   store,
		Prices:  store,
		Ledger:  store,
		Secrets: staticSecrets{"ref_oai": "k1", "ref_ant": "k2", "ref_none": "k3"},
		Chat:    map[string]app.Chatter{domain.AdapterOpenAI: chatter, domain.AdapterAnthropic: chatter, domain.AdapterFake: chatter},
	}
	// 8 input bytes -> ceil(8/4) = 2 estimated tokens.
	in := domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "12345678"}}}

	cases := []struct {
		route      string
		wantCost   *int64 // 2 tokens * rate / 1_000_000
		wantEst    bool
		wantSource string
	}{
		{"route-est-openai", ptrInt64(40), true, "estimate"},    // 2 * 20_000_000 / 1_000_000
		{"route-est-anthropic", ptrInt64(30), true, "estimate"}, // 2 * 15_000_000 / 1_000_000
		{"route-est-none", nil, false, "unknown"},               // nothing known -> unknown
	}
	for _, tc := range cases {
		res, err := disp.ChatCall(ctx, tc.route, "", domain.Principal{User: ada, Source: domain.SourceChat}, in)
		if err != nil {
			t.Fatalf("%s ChatCall: %v", tc.route, err)
		}
		if (res.CostMicroUSD == nil) != (tc.wantCost == nil) || (tc.wantCost != nil && *res.CostMicroUSD != *tc.wantCost) {
			t.Fatalf("%s CostMicroUSD = %v, want %v", tc.route, res.CostMicroUSD, tc.wantCost)
		}
	}

	rows, err := r.owner.Query(ctx, `SELECT route, cost_micro_usd, estimated, price_source FROM gateway.ledger ORDER BY route`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type ledgerRow struct {
		cost      *int64
		estimated bool
		source    string
	}
	got := map[string]ledgerRow{}
	for rows.Next() {
		var route string
		var lr ledgerRow
		if err := rows.Scan(&route, &lr.cost, &lr.estimated, &lr.source); err != nil {
			t.Fatal(err)
		}
		got[route] = lr
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		lr, ok := got[tc.route]
		costMatch := (lr.cost == nil) == (tc.wantCost == nil) && (tc.wantCost == nil || *lr.cost == *tc.wantCost)
		if !ok || !costMatch || lr.estimated != tc.wantEst || lr.source != tc.wantSource {
			t.Fatalf("%s ledger = %+v (ok=%v), want cost=%v estimated=%t source=%s", tc.route, lr, ok, tc.wantCost, tc.wantEst, tc.wantSource)
		}
	}
}
