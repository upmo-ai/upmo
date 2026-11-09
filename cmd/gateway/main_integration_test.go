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
	"os"
	"strings"
	"sync/atomic"
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
			('draft', 'chat', ''),
			('embed', 'embed', '')
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
	handler, err := routes(owner, config.Config{ServiceKey: master}, nil)
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
	embedded := postJSON(t, srv, derived, "/upmo.gateway.v1.Gateway/Embed", map[string]any{
		"route": "embed", "inputs": []string{"ada prefers tea"},
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
	if n != 2 || latest != 2 {
		t.Fatalf("applied versions = %d latest = %d, want 2 and 2", n, latest)
	}

	var attempts, priced int
	if err := owner.QueryRow(ctx, `SELECT count(*), count(cost_usd) FROM gateway.ledger`).Scan(&attempts, &priced); err != nil {
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
		{`INSERT INTO gateway.routes (name, capability) VALUES ('draft', 'chat')`, nil},
		{`INSERT INTO gateway.route_links (route_id, position, provider_account_id, model)
			SELECT r.id, 1, a.id, 'm' FROM gateway.routes r, gateway.provider_accounts a`, nil},
	} {
		if _, err := owner.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	master := mustMaster(t)
	handler, err := routes(owner, config.Config{ServiceKey: master}, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(httpserver.Protect(handler))
	t.Cleanup(srv.Close)
	out := postJSON(t, srv, mustDerived(t, master), "/upmo.gateway.v1.Gateway/Chat", map[string]any{
		"route": "draft", "prompt": "hello", "credential_ref": ref,
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
	_, err := routes(owner, config.Config{ServiceKey: mustMaster(t)}, nil)
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
