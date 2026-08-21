//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/httpserver"
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
		INSERT INTO gateway.provider_accounts (name, adapter, credential_ref, plan_kind, house)
		VALUES ('local', 'fake', 'house', 'local', true)
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO gateway.routes (name, capability, decision) VALUES
			('memory-check', 'decide', 'yesno'),
			('folder-file', 'decide', 'choice'),
			('draft', 'chat', '')
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
	handler, err := routes(owner, config.Config{ServiceKey: master})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(httpserver.Protect(handler))
	t.Cleanup(srv.Close)

	derived := mustDerived(t, master)
	yes := postJSON(t, srv, derived, "/upmo.gateway.v1.Gateway/Decide", map[string]any{
		"route": "memory-check", "kind": "yesno", "prompt": "keep this fact?",
	})
	if yes["yes"] != true {
		t.Fatalf("yesno = %v", yes)
	}
	choice := postJSON(t, srv, derived, "/upmo.gateway.v1.Gateway/Decide", map[string]any{
		"route": "folder-file", "kind": "choice", "prompt": "where", "options": []string{"a", "b"},
	})
	if choice["choice"] != "a" {
		t.Fatalf("choice = %v", choice)
	}
	chat := postJSON(t, srv, derived, "/upmo.gateway.v1.Gateway/Chat", map[string]any{
		"route": "draft", "prompt": "hello",
	})
	if chat["text"] != "ok" {
		t.Fatalf("chat = %v", chat)
	}

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/upmo.gateway.v1.Gateway/Models", nil)
	if err != nil {
		t.Fatal(err)
	}
	token, err := svcsig.Sign(derived, svcsig.AudienceGateway, http.MethodGet, "/upmo.gateway.v1.Gateway/Models", time.Now())
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
	if err := owner.QueryRow(ctx, `SELECT count(*), count(cost_usd) FROM gateway.ledger`).Scan(&attempts, &priced); err != nil {
		t.Fatal(err)
	}
	if attempts != 3 || priced != 0 {
		t.Fatalf("ledger attempts=%d priced=%d", attempts, priced)
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
	price := domain.Price{Version: "v1", InputPerMTok: ptr(1), OutputPerMTok: ptr(2)}
	cases := []struct {
		plan  string
		usage domain.Usage
		found bool
	}{
		{plan: domain.PlanLocal, usage: usage, found: true},
		{plan: domain.PlanMetered, usage: domain.Usage{}, found: true},
		{plan: domain.PlanMetered, usage: usage, found: false},
	}
	for i, tc := range cases {
		row := domain.Entry{
			Route: "draft", LinkPosition: 1, Model: "stub", PlanKind: tc.plan,
			Class: domain.ClassOK, Usage: tc.usage,
			CostUSD: domain.CostUSD(tc.plan, tc.usage, price, tc.found),
		}
		if row.CostUSD != nil {
			t.Fatalf("case %d cost = %v", i, *row.CostUSD)
		}
		if err := store.Record(ctx, row); err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
	}
	var priced int
	if err := owner.QueryRow(ctx, `SELECT count(cost_usd) FROM gateway.ledger`).Scan(&priced); err != nil {
		t.Fatal(err)
	}
	if priced != 0 {
		t.Fatalf("priced = %d", priced)
	}
	var alert bool
	if err := owner.QueryRow(ctx, `SELECT gateway.cache_hit_alert(40, 60)`).Scan(&alert); err != nil {
		t.Fatal(err)
	}
	if !alert {
		t.Fatal("expected cache hit alert")
	}
}

func ptr(v float64) *float64 { return &v }

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
	token, err := svcsig.Sign(derived, svcsig.AudienceGateway, http.MethodPost, path, time.Now())
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
	agent := "11111111-1111-4111-8111-111111111111"
	if _, err := owner.Exec(ctx, `INSERT INTO gateway.tool_bundles (name) VALUES ('core')`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO gateway.tool_bundle_tools (bundle, tool_name, position) VALUES
			('core', 'search', 1), ('core', 'write', 2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `INSERT INTO gateway.agent_bundles (agent_id, bundle) VALUES ($1, 'core')`, agent); err != nil {
		t.Fatal(err)
	}
	store := postgres.Store{Pool: owner}
	b, err := store.AgentBundle(ctx, agent)
	if err != nil {
		t.Fatal(err)
	}
	if b.Name != "core" || len(b.Tools) != 2 || b.Tools[0] != "search" || b.Tools[1] != "write" {
		t.Fatalf("bundle = %+v", b)
	}
	if _, err := store.AgentBundle(ctx, "22222222-2222-4222-8222-222222222222"); err == nil {
		t.Fatal("missing agent bundle")
	}
}

func mustMaster(t *testing.T) string {
	t.Helper()
	raw := bytes32('g')
	return svcsig.KeyPrefix + base64.RawStdEncoding.EncodeToString(raw)
}

func mustDerived(t *testing.T, secret string) []byte {
	t.Helper()
	master, err := svcsig.ParseKey(secret)
	if err != nil {
		t.Fatal(err)
	}
	d, err := svcsig.Derive(master, svcsig.AudienceGateway)
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
