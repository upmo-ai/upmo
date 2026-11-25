//go:build integration

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/gateway/adapters/fake"
	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/platform/buildinfo"
	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/httpserver"
	"github.com/upmo-ai/upmo/internal/platform/metrics"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestGatewayDecideYesNoAndChoice(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := migrate.Up(ctx, owner, gateway.Schema, gateway.Migrations()); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO gateway.provider_accounts (name, adapter, credential_ref, plan_kind, house, visibility)
		VALUES ('local', 'fake', 'house', 'local', true, 'everyone')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO gateway.routes (name, capability, decision, installed) VALUES
			('memory-check', 'decide', 'yesno', true),
			('folder-file', 'decide', 'choice', true),
			('draft', 'chat', '', true),
			('embed', 'embed', '', true)
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO gateway.route_links (route_id, position, provider_account_id, model)
		SELECT id, 1, (SELECT id FROM gateway.provider_accounts WHERE name = 'local'), 'stub'
		FROM gateway.routes
	`); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UPMO_CREDENTIAL_house", "not-a-provider-secret")
	master := mustMaster(t)
	handler, err := routes(owner, config.Config{GatewayKey: master}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(httpserver.Protect(handler))
	t.Cleanup(srv.Close)

	derived := mustDerived(t, master)
	yes := postJSON(t, srv, derived, "/upmo.gateway.v1.Gateway/Decide", map[string]any{
		"route": "memory-check", "kind": "yesno", "prompt": "keep this fact?", "source": "system",
	})
	if yes["yes"] != true {
		t.Fatalf("yesno = %v", yes)
	}
	choice := postJSON(t, srv, derived, "/upmo.gateway.v1.Gateway/Decide", map[string]any{
		"route": "folder-file", "kind": "choice", "prompt": "where", "options": []string{"a", "b"}, "source": "system",
	})
	if choice["choice"] != "a" {
		t.Fatalf("choice = %v", choice)
	}
	chat := postJSON(t, srv, derived, "/upmo.gateway.v1.Gateway/Chat", map[string]any{
		"route": "draft", "prompt": "hello", "source": "system",
	})
	if chat["text"] != "ok" {
		t.Fatalf("chat = %v", chat)
	}
	embedded := postJSON(t, srv, derived, "/upmo.gateway.v1.Gateway/Embed", map[string]any{
		"route": "embed", "inputs": []string{"ada prefers tea"}, "source": "system",
	})
	if embedded["model"] != "stub" {
		t.Fatalf("model = %v", embedded["model"])
	}
	vectors, _ := embedded["vectors"].([]any)
	if len(vectors) != 1 {
		t.Fatalf("vectors = %d", len(vectors))
	}
	row, _ := vectors[0].([]any)
	if len(row) != 1024 {
		t.Fatalf("dimension = %d", len(row))
	}
	nonzero := false
	for _, n := range row {
		v, ok := n.(float64)
		if ok && v != 0 {
			nonzero = true
			break
		}
	}
	if !nonzero {
		t.Fatal("zero vector")
	}

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/upmo.gateway.v1.Gateway/Models", nil)
	if err != nil {
		t.Fatal(err)
	}
	token, err := svcsig.SignRequest(derived, svcsig.AudienceGateway, http.MethodGet, "/upmo.gateway.v1.Gateway/Models", "", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("models = %d", res.StatusCode)
	}

	bare := httptest.NewRecorder()
	handler.ServeHTTP(bare, httptest.NewRequest(http.MethodGet, "/upmo.gateway.v1.Gateway/Models", nil))
	if bare.Code != http.StatusUnauthorized {
		t.Fatalf("no bearer = %d", bare.Code)
	}

	var n, latest int
	if err := owner.QueryRow(ctx, `
		SELECT count(*), coalesce(max(version_id), 0)
		FROM gateway.goose_db_version
		WHERE is_applied AND version_id > 0`).Scan(&n, &latest); err != nil {
		t.Fatal(err)
	}
	if n != 1 || latest != 1 {
		t.Fatalf("applied versions = %d latest = %d, want 1 and 1", n, latest)
	}

	var attempts, priced int
	if err := owner.QueryRow(ctx, `SELECT count(*), count(cost_micro_usd) FROM gateway.ledger`).Scan(&attempts, &priced); err != nil {
		t.Fatal(err)
	}
	if attempts != 4 || priced != 0 {
		t.Fatalf("ledger attempts=%d priced=%d", attempts, priced)
	}
}

func TestGatewayOpensAPersonsSealedKeyForTheirAccount(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := migrate.Up(ctx, owner, gateway.Schema, gateway.Migrations()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UPMO_MASTER_KEY", base64.StdEncoding.EncodeToString(bytes32('m')))
	t.Setenv("UPMO_MASTER_KEY_ID", "k7")
	keys, err := gateway.KeyringFromEnv(os.LookupEnv, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	const ref, key = "own_wiretest", "sk-own-wire-test-key"
	sealed, err := keys.Seal(ref, key)
	if err != nil {
		t.Fatal(err)
	}
	var seen atomic.Value
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"from the person's provider"}}]}`)
	}))
	t.Cleanup(provider.Close)
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO gateway.provider_accounts (owner_user_id, name, adapter, credential_ref, plan_kind, house, base_url)
			VALUES ('11111111-1111-4111-8111-111111111111', 'mine', 'openai_compat', $1, 'local', false, $2)`, []any{ref, provider.URL}},
		{`INSERT INTO gateway.credentials (ref, ciphertext, nonce, key_id) VALUES ($1, $2, $3, $4)`, []any{ref, sealed.Ciphertext, sealed.Nonce, sealed.KeyID}},
		{`INSERT INTO gateway.routes (name, capability, installed) VALUES ('draft', 'chat', true)`, nil},
		{`INSERT INTO gateway.route_links (route_id, position, provider_account_id, model)
			SELECT r.id, 1, a.id, 'm' FROM gateway.routes r, gateway.provider_accounts a`, nil},
	} {
		if _, err := owner.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	master := mustMaster(t)
	handler, err := routes(owner, config.Config{GatewayKey: master}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(httpserver.Protect(handler))
	t.Cleanup(srv.Close)
	out := postJSON(t, srv, mustDerived(t, master), "/upmo.gateway.v1.Gateway/Chat", map[string]any{
		"route": "draft", "prompt": "hello", "credential_ref": ref,
		"user_id": "11111111-1111-4111-8111-111111111111", "source": "chat",
	})
	if out["text"] != "from the person's provider" {
		t.Fatalf("chat = %v", out)
	}
	if seen.Load() != "Bearer "+key {
		t.Fatalf("the provider was sent %q", seen.Load())
	}
}

func TestGatewayRefusesToStartWithAMalformedMasterKey(t *testing.T) {
	owner := pgtest.New(t)
	t.Setenv("UPMO_MASTER_KEY", "this-is-not-a-32-byte-key")
	_, err := routes(owner, config.Config{GatewayKey: mustMaster(t)}, nil, nil, nil)
	if err == nil || strings.Contains(err.Error(), "this-is-not-a-32-byte-key") {
		t.Fatalf("routes error = %v", err)
	}
}

func TestGatewayLedgerNullCostCases(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := migrate.Up(ctx, owner, gateway.Schema, gateway.Migrations()); err != nil {
		t.Fatal(err)
	}
	store := postgres.Store{Pool: owner}
	usage := domain.Usage{Arrived: true, Input: 1000, Output: 1000}
	price := domain.Price{Version: "v1", InputMicroUSDPerMTok: microPtr(1_000_000), OutputMicroUSDPerMTok: microPtr(2_000_000)}
	cases := []struct {
		plan     string
		usage    domain.Usage
		found    bool
		estRate  int64
		wantNull bool
	}{
		{plan: domain.PlanLocal, usage: usage, found: true, wantNull: true},
		{plan: domain.PlanMetered, usage: domain.Usage{}, found: true, estRate: 75_000_000},
		{plan: domain.PlanMetered, usage: usage, found: false, estRate: 75_000_000},
		{plan: domain.PlanMetered, usage: usage, found: false, estRate: 0, wantNull: true},
	}
	for i, tc := range cases {
		micro, estimated, priceSource := domain.MeteredCost(tc.plan, tc.usage, price, tc.found, tc.estRate, 8, 0, false)
		row := domain.Entry{
			Route: "draft", LinkPosition: 1, Model: "stub", PlanKind: tc.plan,
			Class: domain.ClassOK, Usage: tc.usage, Principal: domain.Principal{Source: domain.SourceSystem},
			CostMicroUSD: micro, Estimated: estimated, PriceSource: priceSource,
		}
		if !tc.wantNull && row.CostMicroUSD == nil {
			t.Fatalf("case %d has no metered cost", i)
		}
		if tc.wantNull && row.CostMicroUSD != nil {
			t.Fatalf("case %d cost = %d, want nil", i, *row.CostMicroUSD)
		}
		if err := store.Record(ctx, row); err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
	}
	var priced, estimated int
	if err := owner.QueryRow(ctx, `SELECT count(cost_micro_usd), count(*) FILTER (WHERE estimated) FROM gateway.ledger`).Scan(&priced, &estimated); err != nil {
		t.Fatal(err)
	}
	if priced != 2 || estimated != 2 {
		t.Fatalf("priced = %d, estimated = %d", priced, estimated)
	}
	var alertProc *string
	if err := owner.QueryRow(ctx, `SELECT to_regprocedure('gateway.cache_hit_alert(integer, integer)')::text`).Scan(&alertProc); err != nil {
		t.Fatal(err)
	}
	if alertProc != nil {
		t.Fatalf("gateway.cache_hit_alert still exists: %q", *alertProc)
	}
}

func TestGatewayVersionAndReadyzRoutes(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := migrate.Up(ctx, owner, gateway.Schema, gateway.Migrations()); err != nil {
		t.Fatal(err)
	}
	versions, err := migrate.SchemaVersions(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := routes(owner, config.Config{GatewayKey: mustMaster(t)}, nil, func(context.Context) (map[string]int64, error) {
		return maps.Clone(versions), nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	info := buildinfo.Info()

	derived := mustDerived(t, mustMaster(t))
	for _, path := range []string{"/version", "/api/version"} {
		bare := httptest.NewRecorder()
		handler.ServeHTTP(bare, httptest.NewRequest(http.MethodGet, path, nil))
		if bare.Code != http.StatusUnauthorized {
			t.Fatalf("%s without a bearer = %d, want 401", path, bare.Code)
		}
		tok, err := svcsig.SignRequest(derived, svcsig.AudienceGateway, http.MethodGet, path, "", nil, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d, want 200", path, rec.Code)
		}
		var got buildinfo.Response
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s decode: %v", path, err)
		}
		if got.Version != info.Version || got.Commit != info.Commit || got.BuildTime != info.BuildTime {
			t.Fatalf("%s info = %+v, want %+v", path, got, info)
		}
		if got.SchemaVersions[gateway.Schema] != 1 {
			t.Fatalf("%s gateway schema version = %d, want 1", path, got.SchemaVersions[gateway.Schema])
		}
		if _, hasTasks := got.SchemaVersions["tasks"]; hasTasks {
			t.Fatalf("%s unexpectedly reported unmigrated tasks schema: %v", path, got.SchemaVersions)
		}
	}

	ready := httptest.NewRecorder()
	handler.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusOK || ready.Body.String() != "ok" {
		t.Fatalf("readyz = %d %q", ready.Code, ready.Body.String())
	}
	if ready.Header().Get("X-Upmo-Version") != info.Version || ready.Header().Get("X-Upmo-Commit") != info.Commit {
		t.Fatalf("readyz headers = %q %q, want %q %q", ready.Header().Get("X-Upmo-Version"), ready.Header().Get("X-Upmo-Commit"), info.Version, info.Commit)
	}

	postRec := httptest.NewRecorder()
	handler.ServeHTTP(postRec, httptest.NewRequest(http.MethodPost, "/version", nil))
	if postRec.Code != http.StatusUnauthorized {
		t.Fatalf("POST /version without svcsig = %d, want 401", postRec.Code)
	}
}

func TestGatewayRoutesCapTheBodyAndAcceptThePreviousKey(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := migrate.Up(ctx, owner, gateway.Schema, gateway.Migrations()); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO gateway.provider_accounts (name, adapter, credential_ref, plan_kind, house, visibility)
		VALUES ('local', 'fake', 'house', 'local', true, 'everyone');
		INSERT INTO gateway.routes (name, capability, decision, installed) VALUES ('draft', 'chat', '', true);
		INSERT INTO gateway.route_links (route_id, position, provider_account_id, model)
		SELECT id, 1, (SELECT id FROM gateway.provider_accounts WHERE name = 'local'), 'stub'
		FROM gateway.routes
	`); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UPMO_CREDENTIAL_house", "not-a-provider-secret")
	current := svcsig.KeyPrefix + "gateway_" + base64.RawStdEncoding.EncodeToString(bytes32('h'))
	previous := mustMaster(t)
	handler, err := routes(owner, config.Config{GatewayKey: current, GatewayKeyPrevious: previous}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(httpserver.Protect(handler))
	t.Cleanup(srv.Close)

	for name, secret := range map[string]string{"current": current, "previous": previous} {
		chat := postJSON(t, srv, mustDerived(t, secret), "/upmo.gateway.v1.Gateway/Chat", map[string]any{
			"route": "draft", "prompt": "hello", "source": "system",
		})
		if chat["text"] != "ok" {
			t.Fatalf("%s key chat = %v", name, chat)
		}
	}

	const path = "/upmo.gateway.v1.Gateway/Chat"
	big := bytes.Repeat([]byte{'a'}, 8<<20+1)
	tok, err := svcsig.SignRequest(mustDerived(t, current), svcsig.AudienceGateway, http.MethodPost, path, "", big, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(big))
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("8 MiB + 1 byte = %d, want 413", rec.Code)
	}
}

func ptr(v float64) *float64 { return &v }

func microPtr(v int64) *int64 { return &v }

func postJSON(t *testing.T, srv *httptest.Server, derived []byte, path string, body map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	token, err := svcsig.SignRequest(derived, svcsig.AudienceGateway, http.MethodPost, path, "", raw, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("%s = %d %s", path, res.StatusCode, b)
	}
	out := map[string]any{}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestGatewayToolBundles(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := migrate.Up(ctx, owner, gateway.Schema, gateway.Migrations()); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO gateway.tool_bundles (name) VALUES ('core')`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO gateway.tool_bundle_tools (bundle, tool_name, position) VALUES
			('core', 'search', 1), ('core', 'write', 2)`); err != nil {
		t.Fatal(err)
	}
	store := postgres.Store{Pool: owner}
	b, err := store.Bundle(ctx, "core")
	if err != nil {
		t.Fatal(err)
	}
	if b.Name != "core" || len(b.Tools) != 2 || b.Tools[0] != "search" || b.Tools[1] != "write" {
		t.Fatalf("bundle = %+v", b)
	}
	if _, err := store.Bundle(ctx, "missing"); err == nil {
		t.Fatal("missing bundle")
	}
}

func TestGatewayRPCToolCallsUsageAndContextWindow(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := migrate.Up(ctx, owner, gateway.Schema, gateway.Migrations()); err != nil {
		t.Fatal(err)
	}
	store := postgres.Store{Pool: owner}
	rate := 1.0
	if err := store.ReplaceCatalog(ctx, "v1", []domain.CatalogRow{{
		Model:         "fake-tool-call",
		Version:       "v1",
		Caps:          []string{domain.CapTools},
		ContextWindow: 200000,
		Price: domain.Price{
			Version:        "v1",
			InputPerMTok:   &rate,
			OutputPerMTok:  &rate,
			CacheReadMTok:  &rate,
			CacheWriteMTok: &rate,
			ContextWindow:  200000,
		},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO gateway.provider_accounts (name, adapter, credential_ref, plan_kind, house, visibility)
		VALUES ('metered-fake', 'fake', 'house', 'metered', true, 'everyone')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO gateway.routes (name, capability, decision, installed) VALUES
			('tool-chat', 'chat', '', true),
			('tool-decide', 'decide', 'yesno', true)
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO gateway.route_links (route_id, position, provider_account_id, model)
		SELECT id, 1, (SELECT id FROM gateway.provider_accounts WHERE name = 'metered-fake'), 'fake-tool-call'
		FROM gateway.routes
	`); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UPMO_CREDENTIAL_house", "not-a-secret")
	master := mustMaster(t)
	handler, err := routes(owner, config.Config{GatewayKey: master}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(httpserver.Protect(handler))
	t.Cleanup(srv.Close)
	derived := mustDerived(t, master)

	// Decide returns full usage including cost_micro_usd and context_window.
	dec := postJSON(t, srv, derived, "/upmo.gateway.v1.Gateway/Decide", map[string]any{
		"route": "tool-decide", "kind": "yesno", "prompt": "keep?", "source": "system",
	})
	decUsage, _ := dec["usage"].(map[string]any)
	if decUsage["arrived"] != true || decUsage["context_window"] != float64(200000) || decUsage["cost_micro_usd"] != float64(2) {
		t.Fatalf("decide usage = %v", decUsage)
	}

	// Turn 0 Chat returns tool_calls, max_output_tokens respected, full usage + context_window.
	turn0 := postJSON(t, srv, derived, "/upmo.gateway.v1.Gateway/Chat", map[string]any{
		"route":             "tool-chat",
		"prompt":            "search for go",
		"max_output_tokens": 512,
		"tool_choice":       "auto",
		"tools":             []map[string]any{{"name": "search", "description": "search docs"}},
		"source":            "system",
	})
	toolCalls, _ := turn0["tool_calls"].([]any)
	if len(toolCalls) != 1 {
		t.Fatalf("turn0 tool_calls = %v", turn0)
	}
	firstCall, _ := toolCalls[0].(map[string]any)
	if firstCall["name"] != "search" || turn0["context_window"] != float64(200000) || turn0["cost_micro_usd"] != float64(2) {
		t.Fatalf("turn0 = %v", turn0)
	}

	// Turn 1 Chat with assistant tool_calls + error RoleTool message.
	turn1 := postJSON(t, srv, derived, "/upmo.gateway.v1.Gateway/Chat", map[string]any{
		"route": "tool-chat",
		"tools": []map[string]any{{"name": "search"}},
		"messages": []map[string]any{
			{"role": "user", "text": "search for go"},
			{"role": "assistant", "tool_calls": toolCalls},
			{"role": "tool", "tool_id": firstCall["id"], "tool_name": "search", "text": "timeout", "is_error": true},
		},
		"source": "system",
	})
	if turn1["text"] != "tool_error: timeout" {
		t.Fatalf("turn1 = %v", turn1)
	}

	// ChatStream emits tool_call frames and full done metadata.
	raw, _ := json.Marshal(map[string]any{
		"route":  "tool-chat",
		"prompt": "stream tool call",
		"tools":  []map[string]any{{"name": "search"}},
		"source": "system",
	})
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/upmo.gateway.v1.Gateway/ChatStream", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := svcsig.SignRequest(derived, svcsig.AudienceGateway, http.MethodPost, "/upmo.gateway.v1.Gateway/ChatStream", "", raw, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", res.StatusCode)
	}
	var toolFrames int
	var doneFrame map[string]any
	scan := bufio.NewScanner(res.Body)
	for scan.Scan() {
		var line map[string]any
		if err := json.Unmarshal(scan.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		if _, ok := line["tool_call"]; ok {
			toolFrames++
		}
		if d, ok := line["done"].(map[string]any); ok {
			doneFrame = d
		}
	}
	if toolFrames != 2 || doneFrame == nil {
		t.Fatalf("toolFrames=%d done=%v", toolFrames, doneFrame)
	}
	if doneFrame["context_window"] != float64(200000) || doneFrame["arrived"] != true || doneFrame["cost_micro_usd"] != float64(2) {
		t.Fatalf("done frame = %v", doneFrame)
	}
	if calls, _ := doneFrame["tool_calls"].([]any); len(calls) != 1 {
		t.Fatalf("done tool_calls = %v", doneFrame)
	}
}

func TestGatewayChatUsageAndMaxOutputTokensReachCallerAndLedger(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := migrate.Up(ctx, owner, gateway.Schema, gateway.Migrations()); err != nil {
		t.Fatal(err)
	}
	rate := 1.0
	if err := (postgres.Store{Pool: owner}).ReplaceCatalog(ctx, "v1", []domain.CatalogRow{{
		Model:         fake.ModelUsage,
		Version:       "v1",
		ContextWindow: 200000,
		Price: domain.Price{
			Version: "v1", InputPerMTok: &rate, OutputPerMTok: &rate,
			CacheReadMTok: &rate, CacheWriteMTok: &rate, ContextWindow: 200000,
		},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO gateway.provider_accounts (name, adapter, credential_ref, plan_kind, house, visibility)
		VALUES ('metered-fake', 'fake', 'house', 'metered', true, 'everyone')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO gateway.routes (name, capability, decision, installed) VALUES ('usage-chat', 'chat', '', true), ('usage-decide', 'decide', 'yesno', true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO gateway.route_links (route_id, position, provider_account_id, model)
		SELECT id, 1, (SELECT id FROM gateway.provider_accounts WHERE name = 'metered-fake'), $1
		FROM gateway.routes
	`, fake.ModelUsage); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UPMO_CREDENTIAL_house", "not-a-secret")
	master := mustMaster(t)
	handler, err := routes(owner, config.Config{GatewayKey: master}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(httpserver.Protect(handler))
	t.Cleanup(srv.Close)
	derived := mustDerived(t, master)
	body := map[string]any{
		"route": "usage-chat", "prompt": "hello", "max_output_tokens": 512,
		"tool_choice": map[string]any{"mode": "tool", "name": "search"},
		"tools":       []map[string]any{{"name": "search", "schema": map[string]any{"type": "object"}}},
		"source":      "system",
	}
	const asked = "max_output_tokens=512 tool_choice=tool:search"

	chat := postJSON(t, srv, derived, "/upmo.gateway.v1.Gateway/Chat", body)
	if chat["text"] != asked {
		t.Fatalf("adapter was not asked for 512 output tokens: %v", chat)
	}
	u, _ := chat["usage"].(map[string]any)
	if u["input_tokens"] != float64(100) || u["output_tokens"] != float64(50) || u["cache_read"] != float64(25) ||
		u["cache_write"] != float64(10) || u["reasoning"] != float64(5) || u["arrived"] != true {
		t.Fatalf("chat usage = %v", u)
	}
	if chat["cost_micro_usd"] != float64(185) || chat["context_window"] != float64(200000) || chat["model"] != fake.ModelUsage {
		t.Fatalf("chat metadata = %v", chat)
	}

	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/upmo.gateway.v1.Gateway/ChatStream", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := svcsig.SignRequest(derived, svcsig.AudienceGateway, http.MethodPost, "/upmo.gateway.v1.Gateway/ChatStream", "", raw, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var streamed strings.Builder
	var done map[string]any
	scan := bufio.NewScanner(res.Body)
	for scan.Scan() {
		var line map[string]any
		if err := json.Unmarshal(scan.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		if d, ok := line["delta"].(string); ok {
			streamed.WriteString(d)
		}
		if d, ok := line["done"].(map[string]any); ok {
			done = d
		}
	}
	if strings.TrimSpace(streamed.String()) != asked {
		t.Fatalf("stream was not asked for 512 output tokens: %q", streamed.String())
	}
	if done["input_tokens"] != float64(100) || done["output_tokens"] != float64(50) || done["cache_read"] != float64(25) ||
		done["cache_write"] != float64(10) || done["reasoning"] != float64(5) || done["arrived"] != true {
		t.Fatalf("done frame = %v", done)
	}
	if done["cost_micro_usd"] != float64(185) || done["context_window"] != float64(200000) {
		t.Fatalf("done metadata = %v", done)
	}

	dec := postJSON(t, srv, derived, "/upmo.gateway.v1.Gateway/Decide", map[string]any{"route": "usage-decide", "kind": "yesno", "prompt": "ok?", "source": "system"})
	du, _ := dec["usage"].(map[string]any)
	if du["cache_read"] != float64(25) || du["cache_write"] != float64(10) || du["reasoning"] != float64(5) ||
		du["cost_micro_usd"] != float64(185) || du["context_window"] != float64(200000) || du["arrived"] != true {
		t.Fatalf("decide usage = %v", du)
	}

	// Every call is in the ledger with every counter stored.
	rows, err := owner.Query(ctx, `
		SELECT input_tokens, output_tokens, cache_read, cache_write, reasoning, usage_arrived
		FROM gateway.ledger WHERE route IN ('usage-chat', 'usage-decide')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var in, out, cr, cw, rs int
		var arrived bool
		if err := rows.Scan(&in, &out, &cr, &cw, &rs, &arrived); err != nil {
			t.Fatal(err)
		}
		if in != 100 || out != 50 || cr != 25 || cw != 10 || rs != 5 || !arrived {
			t.Fatalf("ledger row = %d %d %d %d %d %v", in, out, cr, cw, rs, arrived)
		}
		n++
	}
	if n != 3 {
		t.Fatalf("ledger rows = %d, want 3 (chat, stream, decide)", n)
	}
}

func TestGatewayMetricsInternalOnlyAndRecordsCalls(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := migrate.Up(ctx, owner, gateway.Schema, gateway.Migrations()); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO gateway.provider_accounts (name, provider, adapter, credential_ref, plan_kind, house, visibility)
		VALUES ('local', 'fake', 'fake', 'house', 'local', true, 'everyone')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO gateway.routes (name, capability, decision, installed) VALUES ('draft', 'chat', '', true)
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO gateway.route_links (route_id, position, provider_account_id, model)
		SELECT id, 1, (SELECT id FROM gateway.provider_accounts WHERE name = 'local'), 'stub'
		FROM gateway.routes
	`); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UPMO_CREDENTIAL_house", "not-a-provider-secret")
	master := mustMaster(t)
	reg := metrics.NewRegistry()
	handler, err := routes(owner, config.Config{GatewayKey: master}, nil, nil, reg)
	if err != nil {
		t.Fatal(err)
	}
	public := httptest.NewServer(httpserver.Protect(handler))
	t.Cleanup(public.Close)
	internal := httptest.NewServer(metrics.Server("127.0.0.1:0", reg, scrapeToken).Handler)
	t.Cleanup(internal.Close)

	// /metrics must not be exposed on the public gateway server.
	pubRes, err := http.Get(public.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	_ = pubRes.Body.Close()
	if pubRes.StatusCode != http.StatusUnauthorized && pubRes.StatusCode != http.StatusNotFound {
		t.Fatalf("public GET /metrics = %d, want 401 or 404", pubRes.StatusCode)
	}

	derived := mustDerived(t, master)
	chat := postJSON(t, public, derived, "/upmo.gateway.v1.Gateway/Chat", map[string]any{
		"route": "draft", "prompt": "hello", "source": "system",
	})
	if chat["text"] != "ok" {
		t.Fatalf("chat = %v", chat)
	}

	gatewayBearer, err := svcsig.SignRequest(derived, svcsig.AudienceGateway, http.MethodGet, "/metrics", "", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for name, auth := range map[string]string{"no bearer": "", "gateway bearer": "Bearer " + gatewayBearer} {
		if code := scrapeStatus(t, internal.URL+"/metrics", auth); code != http.StatusUnauthorized {
			t.Fatalf("internal GET /metrics with %s = %d, want 401", name, code)
		}
	}
	req, err := http.NewRequest(http.MethodGet, internal.URL+"/metrics", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+scrapeToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("internal GET /metrics = %d", res.StatusCode)
	}
	bodyBytes, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(bodyBytes)
	for _, want := range []string{
		`upmo_http_requests_total{service="gateway",route="POST /upmo.gateway.v1.Gateway/Chat",status="200"} 1`,
		`upmo_http_requests_total{service="gateway",route="unmatched",status="401"} 1`,
		`upmo_gateway_calls_total{provider="fake",model="stub",class="ok"} 1`,
		`upmo_gateway_call_duration_seconds_count{provider="fake",model="stub"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in internal /metrics:\n%s", want, body)
		}
	}
}

const scrapeToken = "upmo_mtr_Z2F0ZXdheS1tZXRyaWNzLXNjcmFwZS10b2tlbi10ZXN0" // #nosec G101 -- fixed test token, not a credential

func scrapeStatus(t *testing.T, url, auth string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	return res.StatusCode
}

func mustMaster(t *testing.T) string {
	t.Helper()
	raw := bytes32('g')
	return svcsig.KeyPrefix + "gateway_" + base64.RawStdEncoding.EncodeToString(raw)
}

func mustDerived(t *testing.T, secret string) []byte {
	t.Helper()
	d, err := svcsig.ParseAudienceKey(secret, svcsig.AudienceGateway)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func bytes32(b byte) []byte {
	out := make([]byte, 32)
	for i := range out {
		out[i] = b
	}
	return out
}
