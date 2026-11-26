//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/platform/keys"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

// unwrapForSandbox is the body sandboxd sends for sb with the 32-byte proxy
// secret of fill.
func unwrapForSandbox(sb string, fill byte) string {
	return `{"kind":"credential","ref":"GITHUB","sandbox_id":"` + sb + `","proxy_secret":"` +
		base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32)) + `"}`
}

// Invariant 4, sandboxd's part (SandboxSecretAndLiveness): the worker
// unwraps for sandboxd only while gateway.sandbox_may_open holds for the
// SHA-256 of the proxy secret it sends. The clock is the database's, so the
// expiry is set from now: live is 9 minutes ahead, expired 1 minute behind.
func TestKeysSandboxSecretAndLiveness(t *testing.T) {
	w := startKeyWorker(t)
	ctx := context.Background()
	w.seed("GITHUB", allowedKey)
	app := pgtest.AppLogin(t, w.owner)
	sandbox := pgtest.Login(t, w.owner, "upmo_sandbox")
	attachPending(t, w.owner, app, sandbox, time.Now().Add(9*time.Minute))
	if _, err := w.owner.Exec(ctx, `INSERT INTO gateway.sandbox_credential_refs (sandbox_id, ref) VALUES ('sb-1', 'GITHUB')`); err != nil {
		t.Fatal(err)
	}
	setTask := func(status string, paused bool) {
		t.Helper()
		tx, err := w.owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		// Straight to each status: the transition triggers would only let a
		// task reach it through its events.
		if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM tasks.tasks WHERE id = $1`, sandboxTask); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO tasks.tasks (id, owner_user_id, goal, status, paused)
			VALUES ($1, $2, 'ship', $3, $4)`, sandboxTask, sandboxOwner, status, paused); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	expires := func(d time.Duration) {
		t.Helper()
		if _, err := w.owner.Exec(ctx, `UPDATE runtime.task_sandboxes SET expires_at = $2 WHERE task_id = $1`, sandboxTask, time.Now().Add(d)); err != nil {
			t.Fatal(err)
		}
	}
	unwrap := func(body string) (int, string) {
		t.Helper()
		return w.call(svcsig.AudienceWorkerSandboxd, http.MethodPost, keys.PathUnwrap, body)
	}
	served := func(name, body string) {
		t.Helper()
		code, out := unwrap(body)
		var got map[string][]byte
		if err := json.Unmarshal([]byte(out), &got); code != http.StatusOK || err != nil || len(got) != 1 || len(got["data_key"]) != 32 {
			t.Fatalf("%s = %d %s, want 32 key bytes", name, code, out)
		}
	}
	refused := func(name, body string) {
		t.Helper()
		if code, out := unwrap(body); code != http.StatusForbidden || out != "keys: refused" {
			t.Errorf("%s = %d %q, want 403 keys: refused", name, code, out)
		}
	}

	setTask("Working", false)
	served("live Working with the 0x61 secret", unwrapForSandbox("sb-1", 0x61))
	// The client sandboxd will use gets the same key gateway does.
	sbx := w.client(svcsig.AudienceWorkerSandboxd)
	viaClient, err := sbx.UnwrapForSandbox(ctx, "sb-1", "GITHUB", bytes.Repeat([]byte{0x61}, 32))
	gw := w.client(svcsig.AudienceWorkerGateway)
	viaGateway, gerr := gw.UnwrapCredential(ctx, "GITHUB")
	if err != nil || gerr != nil || len(viaClient) != 32 || !bytes.Equal(viaClient, viaGateway) {
		t.Fatalf("UnwrapForSandbox = %d bytes %v, gateway %v", len(viaClient), err, gerr)
	}
	if _, err := sbx.UnwrapForSandbox(ctx, "sb-1", "GITHUB", bytes.Repeat([]byte{0x62}, 32)); err == nil || err.Error() != "keys: worker unavailable" {
		t.Fatalf("UnwrapForSandbox with the wrong secret = %v", err)
	}
	refused("the 0x62 secret", unwrapForSandbox("sb-1", 0x62))
	refused("sb-2 with sb-1's secret", unwrapForSandbox("sb-2", 0x61))
	refused("no proxy secret", `{"kind":"credential","ref":"GITHUB","sandbox_id":"sb-1"}`)
	refused("no sandbox", `{"kind":"credential","ref":"GITHUB","sandbox_id":"","proxy_secret":"`+base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x61}, 32))+`"}`)
	refused("a 31-byte secret", `{"kind":"credential","ref":"GITHUB","sandbox_id":"sb-1","proxy_secret":"`+base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x61}, 31))+`"}`)
	refused("an unbound ref", `{"kind":"credential","ref":"OTHER","sandbox_id":"sb-1","proxy_secret":"`+base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x61}, 32))+`"}`)
	refused("a password", `{"kind":"password","ref":"GITHUB","sandbox_id":"sb-1","proxy_secret":"`+base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x61}, 32))+`"}`)
	refused("the stored hash as the secret", `{"kind":"credential","ref":"GITHUB","sandbox_id":"sb-1","proxy_secret":"`+base64.StdEncoding.EncodeToString(sandboxSecret(0x61))+`"}`)
	for _, tc := range []struct {
		status string
		paused bool
	}{{"Done", false}, {"Needs approval", false}, {"Working", true}} {
		setTask(tc.status, tc.paused)
		refused(tc.status+map[bool]string{true: " paused", false: ""}[tc.paused], unwrapForSandbox("sb-1", 0x61))
	}
	setTask("Working", false)
	expires(-time.Minute)
	refused("an expired sandbox", unwrapForSandbox("sb-1", 0x61))
	expires(9 * time.Minute)
	setTask("Verifying", false)
	served("live Verifying", unwrapForSandbox("sb-1", 0x61))

	// There is no route that hands sandboxd its secret.
	if code, _ := w.call(svcsig.AudienceWorkerSandboxd, http.MethodPost, "/internal/keys/sandbox-secret", `{}`); code != http.StatusNotFound {
		t.Fatalf("sandbox-secret = %d, want 404", code)
	}
	// Each sandboxd unwrap left one row naming the sandbox, never a secret.
	changes, outcomes := w.auditRows("keys.unwrap")
	sawSandbox := false
	for i, c := range changes {
		if strings.Contains(c, "proxy_secret") || strings.Contains(c, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x61}, 32))) {
			t.Fatalf("audit row %d holds the secret: %s", i, c)
		}
		if c == `{"ref": "GITHUB", "kind": "credential", "audience": "worker-sandboxd", "sandbox_id": "sb-1"}` && outcomes[i] == "success" {
			sawSandbox = true
		}
	}
	if !sawSandbox {
		t.Fatalf("no sandboxd success row: %v", changes)
	}
}

// listener counts the calls one fake provider gets and the key each saw.
type listener struct {
	srv  *httptest.Server
	hits atomic.Int32
	auth atomic.Value
}

func newListener(t *testing.T) *listener {
	t.Helper()
	l := &listener{}
	l.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		l.hits.Add(1)
		l.auth.Store(r.Header.Get("Authorization"))
		_, _ = rw.Write([]byte(`{"data":[{"id":"m1"}]}`))
	}))
	t.Cleanup(l.srv.Close)
	return l
}

// person adds a joined person with role and a live session, and returns the
// session's token hash.
func person(t *testing.T, w *keyWorker, id, role string) []byte {
	t.Helper()
	ctx := context.Background()
	email := id + "@example.test"
	if _, err := w.owner.Exec(ctx, `
		INSERT INTO identity.users (id, email, email_hash, email_hash_key_id, name, role_id, joined_at)
		VALUES ($1::uuid, $2::text, sha256(convert_to($2::text, 'UTF8')), 'k1', 'Person', (SELECT id FROM identity.roles WHERE key = $3), now())`, id, email, role); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("session of " + id))
	if _, err := w.owner.Exec(ctx, `
		INSERT INTO identity.sessions (user_id, token_hash, created_at, last_seen_at, expires_at)
		VALUES ($1::uuid, $2, now(), now(), now() + interval '1 hour')`, id, hash[:]); err != nil {
		t.Fatal(err)
	}
	return hash[:]
}

// Invariant 5 (ProviderTestKeepsTheKey): the worker probes a saved account
// with its saved key at its stored address only, for a person who manages
// it, and the answer never carries the key.
func TestKeysProviderTestKeepsTheKey(t *testing.T) {
	w := startKeyWorker(t)
	ctx := context.Background()
	provider, evil := newListener(t), newListener(t)
	manager := person(t, w, sandboxOwner, "owner")
	member := person(t, w, otherPerson, "member")
	w.seed("own_provider_test", allowedKey)
	var account string
	if err := w.owner.QueryRow(ctx, `
		INSERT INTO gateway.provider_accounts (owner_user_id, name, provider, adapter, credential_ref, plan_kind, house, base_url)
		VALUES ($1::uuid, 'Saved', 'custom', 'openai_compat', 'own_provider_test', 'local', false, $2)
		RETURNING id::text`, sandboxOwner, provider.srv.URL).Scan(&account); err != nil {
		t.Fatal(err)
	}
	body := func(hash []byte, extra string) string {
		return `{"account_id":"` + account + `","actor_token_hash":"` + base64.StdEncoding.EncodeToString(hash) + `"` + extra + `}`
	}
	test := func(hash []byte, extra string) (int, string) {
		t.Helper()
		return w.call(svcsig.AudienceWorkerBrain, http.MethodPost, keys.PathProviderTest, body(hash, extra))
	}

	// A body that names an address is refused before anything is dialed.
	if code, out := test(manager, `,"base_url":"`+evil.srv.URL+`"`); code != http.StatusForbidden || out != "keys: refused" {
		t.Fatalf("extra base_url = %d %q", code, out)
	}
	// A person who does not manage the account, an unknown session, an
	// expired one, and every audience but brain are refused.
	if code, out := test(member, ""); code != http.StatusForbidden || out != "keys: refused" {
		t.Fatalf("a member = %d %q", code, out)
	}
	unknown := sha256.Sum256([]byte("nobody"))
	if code, _ := test(unknown[:], ""); code != http.StatusForbidden {
		t.Fatalf("an unknown session = %d", code)
	}
	for _, audience := range []string{svcsig.AudienceWorkerGateway, svcsig.AudienceWorkerSandboxd} {
		if code, out := w.call(audience, http.MethodPost, keys.PathProviderTest, body(manager, "")); code != http.StatusForbidden || out != "keys: refused" {
			t.Fatalf("provider-test from %s = %d %q", audience, code, out)
		}
	}
	if provider.hits.Load() != 0 || evil.hits.Load() != 0 {
		t.Fatalf("refusals dialed: provider %d evil %d", provider.hits.Load(), evil.hits.Load())
	}

	// The manager's test reaches the stored address with the saved key.
	code, out := test(manager, "")
	var got keys.ProviderTestResponse
	if err := json.Unmarshal([]byte(out), &got); code != http.StatusOK || err != nil || !got.OK || got.Error != "" || len(got.Models) != 1 {
		t.Fatalf("manager test = %d %s", code, out)
	}
	if strings.Contains(out, allowedKey) {
		t.Fatalf("the answer holds the key: %s", out)
	}
	if provider.hits.Load() != 1 || evil.hits.Load() != 0 || provider.auth.Load() != "Bearer "+allowedKey {
		t.Fatalf("listeners: provider %d (%v) evil %d", provider.hits.Load(), provider.auth.Load(), evil.hits.Load())
	}
	// The client brain uses reads the same answer.
	brain := w.client(svcsig.AudienceWorkerBrain)
	if res, err := brain.ProviderTest(ctx, account, manager); err != nil || !res.OK || len(res.Models) != 1 {
		t.Fatalf("client ProviderTest = %+v %v", res, err)
	}
	if _, err := brain.ProviderTest(ctx, account, member); err == nil || err.Error() != "keys: worker unavailable" {
		t.Fatalf("client ProviderTest as a member = %v", err)
	}
	// An expired session is no person.
	if _, err := w.owner.Exec(ctx, `UPDATE identity.sessions SET expires_at = now() - interval '1 second' WHERE token_hash = $1`, manager); err != nil {
		t.Fatal(err)
	}
	if code, _ := test(manager, ""); code != http.StatusForbidden {
		t.Fatalf("an expired session = %d", code)
	}
	if provider.hits.Load() != 2 || evil.hits.Load() != 0 {
		t.Fatalf("listeners after all: provider %d evil %d", provider.hits.Load(), evil.hits.Load())
	}
	// One audit row per call: success only for the two served tests.
	changes, outcomes := w.auditRows("keys.provider_test")
	success := 0
	for i, c := range changes {
		if strings.Contains(c, allowedKey) || strings.Contains(c, base64.StdEncoding.EncodeToString(manager)) {
			t.Fatalf("audit row %d holds a secret: %s", i, c)
		}
		if outcomes[i] == "success" {
			success++
		}
	}
	if len(changes) != 9 || success != 2 {
		t.Fatalf("provider_test audit = %d rows, %d success: %v %v", len(changes), success, changes, outcomes)
	}
}
