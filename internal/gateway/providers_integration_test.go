//go:build integration

package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway/adapters/openai"
	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/platform/netguard"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

const (
	ada = "11111111-1111-4111-8111-111111111111"
	bob = "22222222-2222-4222-8222-222222222222"
	eve = "33333333-3333-4333-8333-333333333333"

	adaKey = "sk-ada-aaaa-aaaa-aaaa-aaaa"
	newKey = "sk-new-bbbb-bbbb-bbbb-bbbb"
)

// migrateWithIdentity brings the schemas up in the order brain does. The
// store records audit events, so audit comes first, and an account shared
// with a team points at identity's teams, so identity follows.
func migrateWithIdentity(t *testing.T, owner *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	if err := audit.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, Schema, Migrations()); err != nil {
		t.Fatal(err)
	}
}

// syncBuf is a log sink that is safe to read while handlers write.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// masterKeys builds a ring the way a process does, from configuration.
func masterKeys(t *testing.T, fill byte) Keyring {
	t.Helper()
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32))
	ring, err := KeyringFromEnv(func(k string) (string, bool) {
		if k == EnvMasterKey {
			return key, true
		}
		return "", false
	}, nil)
	if err != nil || !ring.Ready() {
		t.Fatalf("keyring: ready=%v err=%v", ring.Ready(), err)
	}
	return ring
}

// rig is a migrated database, the provider routes as brain mounts them, and a
// record of every response body and log line, so a test can prove no key
// appeared in any of them.
type rig struct {
	t       *testing.T
	owner   *pgxpool.Pool
	app     *pgxpool.Pool
	keys    Keyring
	svc     app.Providers
	handler http.Handler
	logs    *syncBuf
	mu      sync.Mutex
	bodies  [][]byte
}

// newRig tests with a limit on tests loose enough that only the rate limit
// test meets it.
func newRig(t *testing.T, keys Keyring) *rig { return newRigLimited(t, keys, 1000) }

// newRigLimited allows each person max tests a minute. Zero keeps the limit
// NewProviders sets, which is the one that ships.
func newRigLimited(t *testing.T, keys Keyring, max int) *rig {
	t.Helper()
	owner := pgtest.New(t)
	migrateWithIdentity(t, owner)
	r := &rig{t: t, owner: owner, app: pgtest.App(t, owner), keys: keys, logs: &syncBuf{}}
	log := slog.New(slog.NewJSONHandler(r.logs, nil))
	r.svc = NewProviders(r.app, keys, log)
	// The rig stands in for the brain's wiring: here every person may use a
	// local-plan account.
	r.svc.LocalPlan = func(context.Context, string) bool { return true }
	if max > 0 {
		r.svc.Limiter = app.NewTestLimiter(max, time.Minute)
	}
	r.mount(log)
	return r
}

// mount serves r.svc the way brain does. A test that changes r.svc mounts
// again.
func (r *rig) mount(log *slog.Logger) {
	mux := http.NewServeMux()
	ProvidersHandler{
		Service: r.svc,
		Log:     log,
		// The X-User header stands in for the session brain resolves.
		Access: func(req *http.Request) (string, bool) {
			user := req.Header.Get("X-User")
			return user, user != ""
		},
	}.MountProviders(mux, nil)
	r.handler = mux
}

func (r *rig) call(user, method, path string, body any) (int, []byte) {
	r.t.Helper()
	var reader io.Reader = http.NoBody
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			r.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	if user != "" {
		req.Header.Set("X-User", user)
	}
	rec := httptest.NewRecorder()
	r.handler.ServeHTTP(rec, req)
	out := rec.Body.Bytes()
	r.mu.Lock()
	r.bodies = append(r.bodies, out)
	r.mu.Unlock()
	return rec.Code, out
}

// assertNoSecrets fails if any recorded response or log line holds a secret.
func (r *rig) assertNoSecrets(secrets ...string) {
	r.t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range secrets {
		for _, b := range r.bodies {
			if bytes.Contains(b, []byte(s)) {
				r.t.Errorf("a response holds %q: %s", s, b)
			}
		}
		if strings.Contains(r.logs.String(), s) {
			r.t.Errorf("the log holds %q:\n%s", s, r.logs.String())
		}
	}
}

type account struct {
	ID           string   `json:"id"`
	Provider     string   `json:"provider"`
	Name         string   `json:"name"`
	Adapter      string   `json:"adapter"`
	PlanKind     string   `json:"plan_kind"`
	BaseURL      string   `json:"base_url"`
	Status       string   `json:"status"`
	Models       []string `json:"models"`
	Visibility   string   `json:"visibility"`
	CanUse       bool     `json:"can_use"`
	Usable       bool     `json:"usable"`
	UsableModels []string `json:"usable_models"`
	TeamIDs      []string `json:"team_ids"`
	TeamNames    []string `json:"team_names"`
	CanManage    bool     `json:"can_manage"`
	IsOwner      bool     `json:"is_owner"`
}

func decode[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	return out
}

func message(t *testing.T, raw []byte) string {
	t.Helper()
	return decode[map[string]string](t, raw)["error"]
}

func (r *rig) add(user string, body map[string]any) (int, []byte) {
	r.t.Helper()
	return r.call(user, http.MethodPost, "/api/providers", body)
}

func (r *rig) mustAdd(user string, body map[string]any) account {
	r.t.Helper()
	code, raw := r.add(user, body)
	if code != http.StatusCreated {
		r.t.Fatalf("add = %d %s", code, raw)
	}
	return decode[account](r.t, raw)
}

func (r *rig) list(user string) []account {
	r.t.Helper()
	code, raw := r.call(user, http.MethodGet, "/api/providers", nil)
	if code != http.StatusOK {
		r.t.Fatalf("list = %d %s", code, raw)
	}
	return decode[[]account](r.t, raw)
}

func (r *rig) count(query string, args ...any) int {
	r.t.Helper()
	var n int
	if err := r.owner.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

// body is a valid local account on baseURL; tests change what they test.
func body(name, baseURL, key string) map[string]any {
	b := map[string]any{
		"provider": "custom", "name": name, "adapter": "openai_compat",
		"plan_kind": "local", "base_url": baseURL, "models": []string{"m1", "m2"},
	}
	if key != "" {
		b["api_key"] = key
	}
	return b
}

// globalAccount inserts a workspace account the way a seed would.
func (r *rig) globalAccount(name string) {
	r.t.Helper()
	ctx := context.Background()
	var id string
	err := r.owner.QueryRow(ctx, `
		INSERT INTO gateway.provider_accounts (name, adapter, credential_ref, plan_kind, house, base_url, provider, visibility)
		VALUES ($1, 'openai_compat', 'house_secret_ref', 'metered', true, 'https://api.example.com', 'openai', 'everyone')
		RETURNING id::text`, name).Scan(&id)
	if err != nil {
		r.t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `INSERT INTO gateway.provider_models (account_id, model, position) VALUES ($1::uuid, 'gpt-x', 1)`, id); err != nil {
		r.t.Fatal(err)
	}
}

// fakeProvider is a provider on loopback. It records the key it was sent and
// answers with the status a test sets, echoing the key in error bodies so a
// leak into a response would show.
type fakeProvider struct {
	srv    *httptest.Server
	status atomic.Int32
	hits   atomic.Int32
	mu     sync.Mutex
	auth   string
}

func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()
	p := &fakeProvider{}
	p.status.Store(http.StatusOK)
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		p.hits.Add(1)
		p.mu.Lock()
		p.auth = req.Header.Get("Authorization")
		p.mu.Unlock()
		if code := int(p.status.Load()); code != http.StatusOK {
			w.WriteHeader(code)
			_, _ = fmt.Fprintf(w, `{"error":"bad key %s"}`, req.Header.Get("Authorization"))
			return
		}
		if req.URL.Path == "/v1/chat/completions" {
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"hello from provider"}}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *fakeProvider) url() string { return p.srv.URL }

func (p *fakeProvider) lastAuth() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.auth
}

// closedURL is a loopback address nothing listens on.
func closedURL(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return "http://" + addr
}

func TestOwnerAddsListsUpdatesAndRemoves(t *testing.T) {
	r := newRig(t, masterKeys(t, 'a'))
	prov := newFakeProvider(t)

	made := r.mustAdd(ada, map[string]any{
		"provider": "openai", "name": "  Work OpenAI ", "adapter": "openai_compat",
		"plan_kind": "metered", "base_url": "https://api.example.com/", "api_key": adaKey,
		"models": []string{" gpt-a ", "gpt-b"},
	})
	if made.ID == "" || made.Name != "Work OpenAI" || made.Visibility != "private" || !made.CanUse || made.Status != "Not connected" ||
		made.Provider != "openai" || made.Adapter != "openai_compat" || made.PlanKind != "metered" ||
		made.BaseURL != "https://api.example.com" || strings.Join(made.Models, ",") != "gpt-a,gpt-b" {
		t.Fatalf("made = %+v", made)
	}
	local := r.mustAdd(ada, body("Alpha local", prov.url(), ""))
	if local.PlanKind != "local" || local.BaseURL != prov.url() {
		t.Fatalf("local = %+v", local)
	}

	rows := r.list(ada)
	if len(rows) != 2 || rows[0].Name != "Alpha local" || rows[1].Name != "Work OpenAI" {
		t.Fatalf("list = %+v", rows)
	}
	for _, row := range rows {
		if row.Models == nil {
			t.Fatalf("models is null: %+v", row)
		}
	}

	code, raw := r.call(ada, http.MethodPut, "/api/providers/"+made.ID, map[string]any{
		"name": "Renamed", "models": []string{"z", "y", "x"},
	})
	if code != http.StatusOK {
		t.Fatalf("update = %d %s", code, raw)
	}
	updated := decode[account](t, raw)
	if updated.Name != "Renamed" || strings.Join(updated.Models, ",") != "z,y,x" || updated.BaseURL != "https://api.example.com" {
		t.Fatalf("updated = %+v", updated)
	}

	// An empty body, and an empty key, change nothing.
	code, raw = r.call(ada, http.MethodPut, "/api/providers/"+made.ID, map[string]any{"api_key": ""})
	if code != http.StatusOK || decode[account](t, raw).Name != "Renamed" {
		t.Fatalf("empty update = %d %s", code, raw)
	}

	if code, raw = r.call(ada, http.MethodDelete, "/api/providers/"+made.ID, nil); code != http.StatusNoContent || len(raw) != 0 {
		t.Fatalf("remove = %d %s", code, raw)
	}
	if rows := r.list(ada); len(rows) != 1 || rows[0].Name != "Alpha local" {
		t.Fatalf("list after remove = %+v", rows)
	}
	if code, _ = r.call(ada, http.MethodDelete, "/api/providers/"+made.ID, nil); code != http.StatusNotFound {
		t.Fatalf("second remove = %d", code)
	}
	if code, _ = r.call(ada, http.MethodPut, "/api/providers/not-a-uuid", map[string]any{}); code != http.StatusNotFound {
		t.Fatalf("bad id = %d", code)
	}

	for _, line := range []string{`"provider added"`, `"provider updated"`, `"provider removed"`} {
		if !strings.Contains(r.logs.String(), line) {
			t.Errorf("log has no %s line:\n%s", line, r.logs.String())
		}
	}
	if !strings.Contains(r.logs.String(), made.ID) || !strings.Contains(r.logs.String(), ada) {
		t.Errorf("log lines lack the account and owner ids:\n%s", r.logs.String())
	}
	r.assertNoSecrets(adaKey)
}

func TestKeyIsSealedAndNeverReturnedOrLogged(t *testing.T) {
	r := newRig(t, masterKeys(t, 'a'))
	made := r.mustAdd(ada, map[string]any{
		"provider": "openai", "name": "Work", "adapter": "openai_compat",
		"plan_kind": "metered", "base_url": "https://api.example.com", "api_key": adaKey, "models": []string{"m"},
	})

	var ref string
	var ciphertext []byte
	var keyID string
	if err := r.owner.QueryRow(context.Background(), `
		SELECT a.credential_ref, c.ciphertext, c.key_id
		FROM gateway.provider_accounts a JOIN gateway.credentials c ON c.ref = a.credential_ref
		WHERE a.id = $1::uuid`, made.ID).Scan(&ref, &ciphertext, &keyID); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte(adaKey)) || keyID != "k1" {
		t.Fatalf("stored credential: key_id %q, plain key in ciphertext: %v", keyID, bytes.Contains(ciphertext, []byte(adaKey)))
	}
	var plainAnywhere int
	if err := r.owner.QueryRow(context.Background(), `
		SELECT count(*) FROM gateway.provider_accounts a WHERE a::text LIKE '%' || $1 || '%'`, adaKey).Scan(&plainAnywhere); err != nil || plainAnywhere != 0 {
		t.Fatalf("plain key in an account row: %d, %v", plainAnywhere, err)
	}
	// The gateway's reader opens it, and only under the ref it was sealed for.
	secrets := DBSecrets{Pool: r.owner, Keys: r.keys}
	if got, err := secrets.Get(ref); err != nil || got != adaKey {
		t.Fatalf("DBSecrets.Get = %q, %v", got, err)
	}
	if _, err := r.owner.Exec(context.Background(), `UPDATE gateway.credentials SET ref = 'own_moved' WHERE ref = $1`, ref); err != nil {
		t.Fatal(err)
	}
	if got, err := secrets.Get("own_moved"); !errors.Is(err, ErrMissing) || got != "" {
		t.Fatalf("a credential moved to another ref opened: %q, %v", got, err)
	}
	if _, err := r.owner.Exec(context.Background(), `UPDATE gateway.credentials SET ref = $1 WHERE ref = 'own_moved'`, ref); err != nil {
		t.Fatal(err)
	}
	if got, err := (DBSecrets{Pool: r.owner, Keys: masterKeys(t, 'b')}).Get(ref); !errors.Is(err, ErrMissing) || got != "" {
		t.Fatalf("a wrong master key opened it: %q, %v", got, err)
	}
	if got, err := (DBSecrets{Pool: r.owner}).Get(ref); !errors.Is(err, ErrMissing) || got != "" {
		t.Fatalf("no master key opened it: %q, %v", got, err)
	}
	if _, err := r.owner.Exec(context.Background(), `UPDATE gateway.credentials SET ciphertext = set_byte(ciphertext, 0, get_byte(ciphertext, 0) # 1) WHERE ref = $1`, ref); err != nil {
		t.Fatal(err)
	}
	if got, err := secrets.Get(ref); !errors.Is(err, ErrMissing) || got != "" {
		t.Fatalf("tampered ciphertext opened: %q, %v", got, err)
	}
	if _, err := secrets.Get("bad ref!"); err == nil {
		t.Fatal("a bad ref was looked up")
	}

	// Every route, including failures.
	r.call(ada, http.MethodGet, "/api/providers", nil)
	r.call(ada, http.MethodPut, "/api/providers/"+made.ID, map[string]any{"api_key": newKey, "name": ""})
	r.call(ada, http.MethodPut, "/api/providers/"+made.ID, map[string]any{"api_key": newKey})
	r.add(ada, map[string]any{"provider": "p", "name": "Work", "adapter": "openai_compat", "plan_kind": "metered", "base_url": "http://x.example.com", "api_key": adaKey})
	r.add(ada, map[string]any{"provider": "p", "name": "X", "adapter": "nope", "plan_kind": "metered", "api_key": adaKey})
	r.add(ada, map[string]any{"provider": "p", "name": "Work", "adapter": "openai_compat", "plan_kind": "metered", "base_url": "https://api.example.com", "api_key": adaKey})
	r.call(ada, http.MethodPost, "/api/providers/test", map[string]any{"adapter": "openai_compat", "plan_kind": "metered", "base_url": "https://127.0.0.1", "api_key": adaKey})
	r.call(ada, http.MethodPost, "/api/providers/test", map[string]any{"id": made.ID})
	r.call(bob, http.MethodPut, "/api/providers/"+made.ID, map[string]any{"api_key": newKey})
	r.call(ada, http.MethodDelete, "/api/providers/"+made.ID, nil)
	r.assertNoSecrets(adaKey, newKey)
}

func TestAnotherPersonSeesNothingOfIt(t *testing.T) {
	r := newRig(t, masterKeys(t, 'a'))
	prov := newFakeProvider(t)
	made := r.mustAdd(ada, body("Ada private", prov.url(), adaKey))

	if rows := r.list(bob); len(rows) != 0 {
		t.Fatalf("bob sees %+v", rows)
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodPut, "/api/providers/" + made.ID},
		{http.MethodDelete, "/api/providers/" + made.ID},
	} {
		code, raw := r.call(bob, c.method, c.path, map[string]any{"name": "mine now", "api_key": newKey})
		if code != http.StatusNotFound {
			t.Fatalf("bob %s = %d %s", c.method, code, raw)
		}
	}
	code, raw := r.call(bob, http.MethodPost, "/api/providers/test", map[string]any{"id": made.ID})
	if code != http.StatusNotFound {
		t.Fatalf("bob test = %d %s", code, raw)
	}
	if prov.hits.Load() != 0 {
		t.Fatal("bob's test reached the provider with ada's key")
	}
	// Nothing changed for ada.
	if rows := r.list(ada); len(rows) != 1 || rows[0].Name != "Ada private" {
		t.Fatalf("ada's list = %+v", rows)
	}

	// Row security itself, below the handler: bob's session sees no account
	// and no sealed key; ada's sees both; no session sees neither.
	visible := func(user string) (accounts, credentials, models int) {
		tx, err := r.app.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if _, err := tx.Exec(context.Background(), `SELECT set_config('upmo.user_id', $1, true)`, user); err != nil {
			t.Fatal(err)
		}
		for q, dst := range map[string]*int{
			`SELECT count(*) FROM gateway.provider_accounts`: &accounts,
			`SELECT count(*) FROM gateway.credentials`:       &credentials,
			`SELECT count(*) FROM gateway.provider_models`:   &models,
		} {
			if err := tx.QueryRow(context.Background(), q).Scan(dst); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	if a, c, m := visible(bob); a != 0 || c != 0 || m != 0 {
		t.Fatalf("bob sees accounts=%d credentials=%d models=%d", a, c, m)
	}
	if a, c, m := visible(ada); a != 1 || c != 1 || m != 2 {
		t.Fatalf("ada sees accounts=%d credentials=%d models=%d", a, c, m)
	}
	if a, c, m := visible(""); a != 0 || c != 0 || m != 0 {
		t.Fatalf("no person sees accounts=%d credentials=%d models=%d", a, c, m)
	}
	// And bob cannot write around the handler.
	tx, err := r.app.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(context.Background(), `SELECT set_config('upmo.user_id', $1, true)`, bob); err != nil {
		t.Fatal(err)
	}
	tag, err := tx.Exec(context.Background(), `UPDATE gateway.provider_accounts SET name = 'taken'`)
	if err != nil || tag.RowsAffected() != 0 {
		t.Fatalf("bob updated %d rows: %v", tag.RowsAffected(), err)
	}
	if _, err := tx.Exec(context.Background(), `
		INSERT INTO gateway.provider_accounts (owner_user_id, name, adapter, credential_ref, plan_kind, house)
		VALUES ($1::uuid, 'forged', 'fake', 'own_forged', 'local', false)`, ada); err == nil {
		t.Fatal("bob inserted an account owned by ada")
	}
	r.assertNoSecrets(adaKey, newKey)
}

func TestGlobalAccountsAreListedAndReadOnly(t *testing.T) {
	r := newRig(t, masterKeys(t, 'a'))
	prov := newFakeProvider(t)
	r.globalAccount("House")
	r.mustAdd(ada, body("Zed own", prov.url(), ""))
	// Own accounts come first, then global ones, each by name; a name a global
	// account uses is free for a person.
	r.mustAdd(ada, body("House", prov.url(), ""))

	rows := r.list(ada)
	if len(rows) != 3 || rows[0].Visibility != "private" || rows[1].Visibility != "private" || rows[2].Visibility != "everyone" ||
		rows[0].Name != "House" || rows[1].Name != "Zed own" {
		t.Fatalf("list = %+v", rows)
	}
	global := rows[2]
	if global.Provider != "openai" || global.PlanKind != "metered" || global.Status != "Not connected" ||
		strings.Join(global.Models, ",") != "gpt-x" {
		t.Fatalf("global = %+v", global)
	}
	if len(r.list(bob)) != 1 {
		t.Fatalf("bob should see just the global account: %+v", r.list(bob))
	}
	// A global account that was never given a vendor label shows its adapter.
	if _, err := r.owner.Exec(context.Background(), `UPDATE gateway.provider_accounts SET provider = '' WHERE house`); err != nil {
		t.Fatal(err)
	}
	if got := r.list(bob)[0].Provider; got != "openai_compat" {
		t.Fatalf("provider fallback = %q", got)
	}

	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPut, "/api/providers/" + global.ID, map[string]any{"name": "mine"}},
		{http.MethodPut, "/api/providers/" + global.ID, map[string]any{"api_key": newKey}},
		{http.MethodDelete, "/api/providers/" + global.ID, nil},
		{http.MethodPost, "/api/providers/test", map[string]any{"id": global.ID}},
	} {
		if code, raw := r.call(ada, c.method, c.path, c.body); code != http.StatusForbidden {
			t.Errorf("%s %s = %d %s", c.method, c.path, code, raw)
		}
	}
	if n := r.count(`SELECT count(*) FROM gateway.provider_accounts WHERE house AND name = 'House'`); n != 1 {
		t.Fatalf("the global account changed: %d", n)
	}
	r.assertNoSecrets("house_secret_ref", newKey)
}

func TestWhoMayCall(t *testing.T) {
	r := newRig(t, masterKeys(t, 'a'))
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/providers"},
		{http.MethodPost, "/api/providers"},
		{http.MethodPut, "/api/providers/" + ada},
		{http.MethodDelete, "/api/providers/" + ada},
		{http.MethodPost, "/api/providers/test"},
	} {
		if code, _ := r.call("", c.method, c.path, map[string]any{}); code != http.StatusUnauthorized {
			t.Errorf("no session: %s %s = %d", c.method, c.path, code)
		}
		req := httptest.NewRequest(c.method, c.path, strings.NewReader("{}"))
		req.Header.Set("X-User", ada)
		req.Header.Set("Authorization", "Bearer upmo_pat_secret")
		rec := httptest.NewRecorder()
		r.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("bearer token: %s %s = %d", c.method, c.path, rec.Code)
		}
	}
	if n := r.count(`SELECT count(*) FROM gateway.provider_accounts`); n != 0 {
		t.Fatalf("a refused call wrote %d accounts", n)
	}
}

func TestValidation(t *testing.T) {
	r := newRig(t, masterKeys(t, 'a'))
	valid := func(mod func(map[string]any)) map[string]any {
		b := map[string]any{
			"provider": "openai", "name": "Valid", "adapter": "openai_compat", "plan_kind": "metered",
			"base_url": "https://api.example.com", "api_key": adaKey, "models": []string{"m"},
		}
		mod(b)
		return b
	}
	many := func(n, size int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("%0*d", size, i)
		}
		return out
	}
	tests := []struct {
		name string
		mod  func(map[string]any)
		want int
	}{
		{"valid metered", func(b map[string]any) { b["name"] = "ok1" }, http.StatusCreated},
		{"valid subscription", func(b map[string]any) { b["name"] = "ok2"; b["plan_kind"] = "subscription" }, http.StatusCreated},
		{"valid local without key", func(b map[string]any) {
			b["name"] = "ok3"
			b["plan_kind"] = "local"
			b["base_url"] = "http://127.0.0.1:11434"
			delete(b, "api_key")
		}, http.StatusCreated},
		{"valid local private http", func(b map[string]any) {
			b["name"] = "ok4"
			b["plan_kind"] = "local"
			b["base_url"] = "http://192.168.1.20:8000"
		}, http.StatusCreated},
		{"valid without models", func(b map[string]any) { b["name"] = "ok5"; delete(b, "models") }, http.StatusCreated},
		{"valid 40 models", func(b map[string]any) { b["name"] = "ok6"; b["models"] = many(40, 3) }, http.StatusCreated},
		{"valid 60 char name", func(b map[string]any) { b["name"] = strings.Repeat("n", 60) }, http.StatusCreated},
		{"valid 120 char model", func(b map[string]any) { b["name"] = "ok8"; b["models"] = []string{strings.Repeat("m", 120)} }, http.StatusCreated},
		{"valid 4096 byte key", func(b map[string]any) { b["name"] = "ok9"; b["api_key"] = strings.Repeat("k", 4096) }, http.StatusCreated},

		{"no provider", func(b map[string]any) { delete(b, "provider") }, http.StatusUnprocessableEntity},
		{"no name", func(b map[string]any) { delete(b, "name") }, http.StatusUnprocessableEntity},
		{"blank name", func(b map[string]any) { b["name"] = "   " }, http.StatusUnprocessableEntity},
		{"61 char name", func(b map[string]any) { b["name"] = strings.Repeat("n", 61) }, http.StatusUnprocessableEntity},
		{"control char name", func(b map[string]any) { b["name"] = "a\x01b" }, http.StatusUnprocessableEntity},
		{"unknown adapter", func(b map[string]any) { b["adapter"] = "cohere" }, http.StatusUnprocessableEntity},
		{"no adapter", func(b map[string]any) { delete(b, "adapter") }, http.StatusUnprocessableEntity},
		{"unknown plan", func(b map[string]any) { b["plan_kind"] = "free" }, http.StatusUnprocessableEntity},
		{"no plan", func(b map[string]any) { delete(b, "plan_kind") }, http.StatusUnprocessableEntity},
		{"metered without key", func(b map[string]any) { delete(b, "api_key") }, http.StatusUnprocessableEntity},
		{"subscription with blank key", func(b map[string]any) { b["plan_kind"] = "subscription"; b["api_key"] = "  " }, http.StatusUnprocessableEntity},
		{"4097 byte key", func(b map[string]any) { b["api_key"] = strings.Repeat("k", 4097) }, http.StatusUnprocessableEntity},
		{"key with newline inside", func(b map[string]any) { b["api_key"] = "sk-a\nb" }, http.StatusUnprocessableEntity},
		{"key with nul", func(b map[string]any) { b["api_key"] = "sk-a\x00b" }, http.StatusUnprocessableEntity},
		{"41 models", func(b map[string]any) { b["models"] = many(41, 3) }, http.StatusUnprocessableEntity},
		{"121 char model", func(b map[string]any) { b["models"] = []string{strings.Repeat("m", 121)} }, http.StatusUnprocessableEntity},
		{"empty model", func(b map[string]any) { b["models"] = []string{"a", ""} }, http.StatusUnprocessableEntity},
		{"duplicate model", func(b map[string]any) { b["models"] = []string{"a", "b", "a"} }, http.StatusUnprocessableEntity},
		{"http for metered", func(b map[string]any) { b["base_url"] = "http://api.example.com" }, http.StatusUnprocessableEntity},
		{"http for subscription", func(b map[string]any) { b["plan_kind"] = "subscription"; b["base_url"] = "http://api.example.com" }, http.StatusUnprocessableEntity},
		{"userinfo", func(b map[string]any) { b["base_url"] = "https://user:pw@api.example.com" }, http.StatusUnprocessableEntity},
		{"userinfo local", func(b map[string]any) { b["plan_kind"] = "local"; b["base_url"] = "http://user@127.0.0.1" }, http.StatusUnprocessableEntity},
		{"metadata address", func(b map[string]any) { b["base_url"] = "https://169.254.169.254/latest" }, http.StatusUnprocessableEntity},
		{"metadata address local", func(b map[string]any) { b["plan_kind"] = "local"; b["base_url"] = "http://169.254.169.254" }, http.StatusUnprocessableEntity},
		{"link-local v6 local", func(b map[string]any) { b["plan_kind"] = "local"; b["base_url"] = "http://[fe80::1]" }, http.StatusUnprocessableEntity},
		{"metadata name", func(b map[string]any) { b["base_url"] = "https://metadata.google.internal" }, http.StatusUnprocessableEntity},
		{"metadata name local", func(b map[string]any) { b["plan_kind"] = "local"; b["base_url"] = "http://metadata.google.internal" }, http.StatusUnprocessableEntity},
		{"loopback for metered", func(b map[string]any) { b["base_url"] = "https://127.0.0.1" }, http.StatusUnprocessableEntity},
		{"private for subscription", func(b map[string]any) { b["plan_kind"] = "subscription"; b["base_url"] = "https://10.0.0.8" }, http.StatusUnprocessableEntity},
		{"localhost for metered", func(b map[string]any) { b["base_url"] = "https://localhost" }, http.StatusUnprocessableEntity},
		{"garbage url", func(b map[string]any) { b["base_url"] = "not a url" }, http.StatusUnprocessableEntity},
	}
	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// A new person each time keeps the 20 account cap out of the way.
			user := fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
			b := valid(tt.mod)
			code, raw := r.add(user, b)
			if code != tt.want {
				t.Fatalf("status %d, want %d: %s", code, tt.want, raw)
			}
			if code == http.StatusUnprocessableEntity {
				if msg := message(t, raw); msg == "" || strings.Contains(msg, adaKey) {
					t.Fatalf("message = %q", msg)
				}
			}
		})
	}
	r.assertNoSecrets(adaKey)

	// The same rules hold on update.
	prov := newFakeProvider(t)
	local := r.mustAdd(ada, body("Local", prov.url(), ""))
	for name, patch := range map[string]map[string]any{
		"to metered with no key":   {"plan_kind": "metered"},
		"bad plan":                 {"plan_kind": "free"},
		"blank name":               {"name": " "},
		"http for metered":         {"plan_kind": "metered", "api_key": "k", "base_url": "http://api.example.com"},
		"metadata":                 {"base_url": "http://169.254.169.254"},
		"41 models":                {"models": many(41, 3)},
		"duplicate models":         {"models": []string{"a", "a"}},
		"key too long":             {"api_key": strings.Repeat("k", 4097)},
		"key with control":         {"api_key": "a\nb"},
		"userinfo":                 {"base_url": "http://u:p@127.0.0.1"},
		"saved http url, new plan": {"plan_kind": "subscription", "api_key": "k"},
	} {
		if code, raw := r.call(ada, http.MethodPut, "/api/providers/"+local.ID, patch); code != http.StatusUnprocessableEntity {
			t.Errorf("update %s = %d %s", name, code, raw)
		}
	}
	if got := r.list(ada)[0]; got.PlanKind != "local" || got.Name != "Local" || got.BaseURL != prov.url() {
		t.Fatalf("a refused update changed the account: %+v", got)
	}
	if code, raw := r.call(ada, http.MethodPut, "/api/providers/"+local.ID, map[string]any{
		"plan_kind": "subscription", "api_key": "k", "base_url": "https://api.example.com",
	}); code != http.StatusOK {
		t.Fatalf("a valid plan change = %d %s", code, raw)
	}
	r.assertNoSecrets(adaKey)
}

func TestScriptedModelIdsStayOnTheFakeAdapter(t *testing.T) {
	r := newRig(t, masterKeys(t, 'a'))
	real := body("real", "http://127.0.0.1:11434", "")
	real["models"] = []string{"m1", "fake-ok"}
	if code, raw := r.add(ada, real); code != http.StatusUnprocessableEntity {
		t.Fatalf("a scripted id on a real adapter = %d %s", code, raw)
	}
	made := r.mustAdd(ada, body("real", "http://127.0.0.1:11434", ""))
	code, raw := r.call(ada, http.MethodPut, "/api/providers/"+made.ID, map[string]any{"models": []string{"fake-slow"}})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("a scripted id added to a real account = %d %s", code, raw)
	}
	if got := r.list(ada)[0].Models; len(got) != 2 || got[0] != "m1" {
		t.Fatalf("a refused change left models %v", got)
	}
	scripted := map[string]any{
		"provider": "fake", "name": "scripted", "adapter": "fake", "plan_kind": "local",
		"models": []string{"fake-ok", "fake-slow"},
	}
	if code, raw := r.add(ada, scripted); code != http.StatusCreated {
		t.Fatalf("scripted ids on the fake adapter = %d %s", code, raw)
	}
}

func TestDuplicateNamesPerOwner(t *testing.T) {
	r := newRig(t, masterKeys(t, 'a'))
	prov := newFakeProvider(t)
	first := r.mustAdd(ada, body("Work", prov.url(), ""))
	other := r.mustAdd(ada, body("Other", prov.url(), ""))
	if code, raw := r.add(ada, body("Work", prov.url(), "")); code != http.StatusConflict || message(t, raw) == "" {
		t.Fatalf("duplicate add = %d %s", code, raw)
	}
	if code, raw := r.add(ada, body(" Work ", prov.url(), "")); code != http.StatusConflict {
		t.Fatalf("duplicate add after trim = %d %s", code, raw)
	}
	r.mustAdd(bob, body("Work", prov.url(), ""))
	if code, raw := r.call(ada, http.MethodPut, "/api/providers/"+other.ID, map[string]any{"name": "Work"}); code != http.StatusConflict {
		t.Fatalf("duplicate rename = %d %s", code, raw)
	}
	if code, _ := r.call(ada, http.MethodPut, "/api/providers/"+first.ID, map[string]any{"name": "Work"}); code != http.StatusOK {
		t.Fatalf("keeping the same name = %d", code)
	}
	// Two global accounts cannot share a name either.
	r.globalAccount("House")
	_, err := r.owner.Exec(context.Background(), `
		INSERT INTO gateway.provider_accounts (name, adapter, credential_ref, plan_kind, house, visibility)
		VALUES ('House', 'fake', 'other', 'local', true, 'everyone')`)
	if err == nil {
		t.Fatal("two global accounts share a name")
	}
}

func TestTwentyAccountsAtMost(t *testing.T) {
	r := newRig(t, masterKeys(t, 'a'))
	prov := newFakeProvider(t)
	for i := range 20 {
		r.mustAdd(ada, body(fmt.Sprintf("Account %02d", i), prov.url(), ""))
	}
	code, raw := r.add(ada, body("One too many", prov.url(), ""))
	if code != http.StatusUnprocessableEntity || !strings.Contains(message(t, raw), "20") {
		t.Fatalf("21st account = %d %s", code, raw)
	}
	r.mustAdd(bob, body("Bob's first", prov.url(), ""))
	// Removing one makes room.
	rows := r.list(ada)
	if code, _ := r.call(ada, http.MethodDelete, "/api/providers/"+rows[0].ID, nil); code != http.StatusNoContent {
		t.Fatalf("remove = %d", code)
	}
	r.mustAdd(ada, body("Back to twenty", prov.url(), ""))
}

func TestTheCapHoldsUnderConcurrentAdds(t *testing.T) {
	r := newRig(t, masterKeys(t, 'a'))
	prov := newFakeProvider(t)
	for i := range 15 {
		r.mustAdd(ada, body(fmt.Sprintf("Account %02d", i), prov.url(), ""))
	}
	var wg sync.WaitGroup
	var made atomic.Int32
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if code, _ := r.add(ada, body(fmt.Sprintf("Racer %02d", i), prov.url(), "")); code == http.StatusCreated {
				made.Add(1)
			}
		}()
	}
	wg.Wait()
	if made.Load() != 5 || r.count(`SELECT count(*) FROM gateway.provider_accounts WHERE owner_user_id = $1::uuid`, ada) != 20 {
		t.Fatalf("racers made %d accounts", made.Load())
	}
}

func TestWithoutAMasterKeyWritesAreRefusedAndListStillWorks(t *testing.T) {
	r := newRig(t, Keyring{})
	prov := newFakeProvider(t)
	r.globalAccount("House")
	if rows := r.list(ada); len(rows) != 1 || rows[0].Visibility != "everyone" || !rows[0].CanUse {
		t.Fatalf("list = %+v", rows)
	}
	// An account made while a key was set, then the key went away.
	if _, err := r.owner.Exec(context.Background(), `
		INSERT INTO gateway.provider_accounts (owner_user_id, name, adapter, credential_ref, plan_kind, house)
		VALUES ($1::uuid, 'Earlier', 'openai_compat', 'own_earlier', 'local', false)`, ada); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := r.owner.QueryRow(context.Background(), `SELECT id::text FROM gateway.provider_accounts WHERE name = 'Earlier'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/api/providers", body("New", prov.url(), "")},
		{http.MethodPost, "/api/providers", body("New", prov.url(), adaKey)},
		{http.MethodPut, "/api/providers/" + id, map[string]any{"name": "x"}},
		{http.MethodDelete, "/api/providers/" + id, nil},
	} {
		code, raw := r.call(ada, c.method, c.path, c.body)
		if code != http.StatusServiceUnavailable || message(t, raw) == "" {
			t.Errorf("%s %s = %d %s", c.method, c.path, code, raw)
		}
	}
	if rows := r.list(ada); len(rows) != 2 {
		t.Fatalf("list after refusals = %+v", rows)
	}
	// A test with a key in the request needs nothing sealed.
	code, raw := r.call(ada, http.MethodPost, "/api/providers/test", map[string]any{
		"adapter": "openai_compat", "plan_kind": "local", "base_url": prov.url(), "api_key": adaKey,
	})
	if code != http.StatusOK || !decode[app.TestResult](t, raw).OK {
		t.Fatalf("test = %d %s", code, raw)
	}
	r.assertNoSecrets(adaKey)
}

func TestNewKeyReplacesTheSealedOne(t *testing.T) {
	r := newRig(t, masterKeys(t, 'a'))
	prov := newFakeProvider(t)
	made := r.mustAdd(ada, body("Keyed", prov.url(), adaKey))
	var ref string
	if err := r.owner.QueryRow(context.Background(), `SELECT credential_ref FROM gateway.provider_accounts WHERE id = $1::uuid`, made.ID).Scan(&ref); err != nil {
		t.Fatal(err)
	}
	secrets := DBSecrets{Pool: r.owner, Keys: r.keys}
	if got, _ := secrets.Get(ref); got != adaKey {
		t.Fatalf("stored key = %q", got)
	}

	// A test marks it Connected; a new key puts it back to untested.
	if code, raw := r.call(ada, http.MethodPost, "/api/providers/test", map[string]any{"id": made.ID}); code != http.StatusOK || !decode[app.TestResult](t, raw).OK {
		t.Fatalf("test = %d %s", code, raw)
	}
	if got := r.list(ada)[0].Status; got != "Connected" {
		t.Fatalf("status = %q", got)
	}
	code, raw := r.call(ada, http.MethodPut, "/api/providers/"+made.ID, map[string]any{"api_key": "  " + newKey + "\n"})
	if code != http.StatusOK {
		t.Fatalf("update = %d %s", code, raw)
	}
	if got := decode[account](t, raw).Status; got != "Not connected" {
		t.Fatalf("status after a new key = %q", got)
	}
	if got, err := secrets.Get(ref); err != nil || got != newKey {
		t.Fatalf("stored key = %q, %v", got, err)
	}
	if n := r.count(`SELECT count(*) FROM gateway.credentials`); n != 1 {
		t.Fatalf("credential rows = %d", n)
	}
	// The next test sends the new key.
	if code, _ := r.call(ada, http.MethodPost, "/api/providers/test", map[string]any{"id": made.ID}); code != http.StatusOK || prov.lastAuth() != "Bearer "+newKey {
		t.Fatalf("test sent %q", prov.lastAuth())
	}
	// A key can be added to an account that had none.
	bare := r.mustAdd(ada, body("Bare", prov.url(), ""))
	if code, raw := r.call(ada, http.MethodPut, "/api/providers/"+bare.ID, map[string]any{"api_key": adaKey}); code != http.StatusOK {
		t.Fatalf("add key = %d %s", code, raw)
	}
	if n := r.count(`SELECT count(*) FROM gateway.credentials`); n != 2 {
		t.Fatalf("credential rows = %d", n)
	}
	r.assertNoSecrets(adaKey, newKey)
}

func TestRotationOldKeyReadsNewKeyWrites(t *testing.T) {
	r := newRig(t, masterKeys(t, 'a'))
	prov := newFakeProvider(t)
	made := r.mustAdd(ada, body("Old", prov.url(), adaKey))
	var ref string
	if err := r.owner.QueryRow(context.Background(), `SELECT credential_ref FROM gateway.provider_accounts WHERE id = $1::uuid`, made.ID).Scan(&ref); err != nil {
		t.Fatal(err)
	}

	// The process restarts with a new current key and the old one as previous.
	oldKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'a'}, 32))
	newKeyB64 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'b'}, 32))
	rotated, err := KeyringFromEnv(func(k string) (string, bool) {
		return map[string]string{EnvMasterKey: newKeyB64, EnvMasterKeyID: "k2"}[k], k == EnvMasterKey || k == EnvMasterKeyID
	}, []string{EnvMasterKeyPrevious + "k1=" + oldKey})
	if err != nil {
		t.Fatal(err)
	}
	secrets := DBSecrets{Pool: r.owner, Keys: rotated}
	if got, err := secrets.Get(ref); err != nil || got != adaKey {
		t.Fatalf("old value under the rotated ring: %q, %v", got, err)
	}
	rotatedRig := &rig{t: t, owner: r.owner, app: r.app, keys: rotated, logs: r.logs}
	rotatedRig.svc = NewProviders(r.app, rotated, nil)
	rotatedRig.svc.LocalPlan = func(context.Context, string) bool { return true }
	made2, err := rotatedRig.svc.Add(context.Background(), ada, app.NewProvider{
		Provider: "p", Name: "New", Adapter: "openai_compat", PlanKind: "local", BaseURL: prov.url(), APIKey: newKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	var keyID string
	if err := r.owner.QueryRow(context.Background(), `
		SELECT c.key_id FROM gateway.credentials c JOIN gateway.provider_accounts a ON a.credential_ref = c.ref WHERE a.id = $1::uuid`, made2.ID).Scan(&keyID); err != nil || keyID != "k2" {
		t.Fatalf("new value sealed with %q, %v", keyID, err)
	}
	// The old ring cannot read what the new key sealed.
	if got, err := (DBSecrets{Pool: r.owner, Keys: r.keys}).Get(made2.CredentialRef); !errors.Is(err, ErrMissing) || got != "" {
		t.Fatalf("old ring read a new value: %q, %v", got, err)
	}
	// And a ring that lost the old key cannot read the old value: it fails closed.
	onlyNew := masterKeys(t, 'b')
	if got, err := (DBSecrets{Pool: r.owner, Keys: onlyNew}).Get(ref); !errors.Is(err, ErrMissing) || got != "" {
		t.Fatalf("a ring without the old key read it: %q, %v", got, err)
	}
}

func TestRemoveDeletesModelsAndSealedKey(t *testing.T) {
	r := newRig(t, masterKeys(t, 'a'))
	prov := newFakeProvider(t)
	made := r.mustAdd(ada, body("Gone", prov.url(), adaKey))
	keep := r.mustAdd(ada, body("Kept", prov.url(), newKey))
	if r.count(`SELECT count(*) FROM gateway.credentials`) != 2 || r.count(`SELECT count(*) FROM gateway.provider_models`) != 4 {
		t.Fatal("setup rows are missing")
	}
	if code, raw := r.call(ada, http.MethodDelete, "/api/providers/"+made.ID, nil); code != http.StatusNoContent {
		t.Fatalf("remove = %d %s", code, raw)
	}
	if n := r.count(`SELECT count(*) FROM gateway.credentials`); n != 1 {
		t.Fatalf("credentials left = %d", n)
	}
	if n := r.count(`SELECT count(*) FROM gateway.provider_models`); n != 2 {
		t.Fatalf("models left = %d", n)
	}
	if n := r.count(`SELECT count(*) FROM gateway.provider_models WHERE account_id = $1::uuid`, made.ID); n != 0 {
		t.Fatalf("models of the removed account left = %d", n)
	}
	if n := r.count(`SELECT count(*) FROM gateway.provider_accounts WHERE id = $1::uuid`, keep.ID); n != 1 {
		t.Fatal("the other account went too")
	}
}

func TestRemoveIsBlockedWhileARouteUsesTheAccount(t *testing.T) {
	r := newRig(t, masterKeys(t, 'a'))
	prov := newFakeProvider(t)
	made := r.mustAdd(ada, body("Linked", prov.url(), adaKey))
	ctx := context.Background()
	for _, name := range []string{"nightly", "chat-main", "alpha"} {
		if _, err := r.owner.Exec(ctx, `INSERT INTO gateway.routes (name, capability) VALUES ($1, 'chat')`, name); err != nil {
			t.Fatal(err)
		}
		if _, err := r.owner.Exec(ctx, `
			INSERT INTO gateway.route_links (route_id, position, provider_account_id, model)
			SELECT id, 1, $2::uuid, 'm1' FROM gateway.routes WHERE name = $1`, name, made.ID); err != nil {
			t.Fatal(err)
		}
	}
	code, raw := r.call(ada, http.MethodDelete, "/api/providers/"+made.ID, nil)
	if code != http.StatusConflict || message(t, raw) != "Used by route alpha, chat-main, nightly" {
		t.Fatalf("remove = %d %s", code, raw)
	}
	if r.count(`SELECT count(*) FROM gateway.provider_accounts WHERE id = $1::uuid`, made.ID) != 1 ||
		r.count(`SELECT count(*) FROM gateway.credentials`) != 1 ||
		r.count(`SELECT count(*) FROM gateway.provider_models WHERE account_id = $1::uuid`, made.ID) != 2 {
		t.Fatal("a blocked remove deleted something")
	}
	if _, err := r.owner.Exec(ctx, `DELETE FROM gateway.route_links`); err != nil {
		t.Fatal(err)
	}
	if code, raw = r.call(ada, http.MethodDelete, "/api/providers/"+made.ID, nil); code != http.StatusNoContent {
		t.Fatalf("remove after unlinking = %d %s", code, raw)
	}
}

func TestTestRoute(t *testing.T) {
	r := newRig(t, masterKeys(t, 'a'))
	prov := newFakeProvider(t)
	type result = app.TestResult
	run := func(user string, req map[string]any) (int, result, []byte) {
		t.Helper()
		code, raw := r.call(user, http.MethodPost, "/api/providers/test", req)
		var res result
		if code == http.StatusOK {
			res = decode[result](t, raw)
		}
		return code, res, raw
	}
	probe := func(extra map[string]any) map[string]any {
		b := map[string]any{"provider": "custom", "adapter": "openai_compat", "plan_kind": "local", "base_url": prov.url(), "api_key": adaKey, "model": "m1"}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}

	t.Run("works", func(t *testing.T) {
		prov.status.Store(200)
		code, res, raw := run(ada, probe(nil))
		if code != 200 || !res.OK || res.Error != "" {
			t.Fatalf("%d %s", code, raw)
		}
		if prov.lastAuth() != "Bearer "+adaKey {
			t.Fatalf("provider saw %q", prov.lastAuth())
		}
	})
	for status, want := range map[int]string{
		401: "The key was rejected.",
		403: "The key was rejected.",
		429: "Rate limited.",
		500: "Could not reach the provider.",
		503: "Could not reach the provider.",
	} {
		t.Run(fmt.Sprintf("provider says %d", status), func(t *testing.T) {
			prov.status.Store(int32(status))
			code, res, raw := run(ada, probe(nil))
			if code != 200 || res.OK || res.Error != want {
				t.Fatalf("%d %s", code, raw)
			}
		})
	}
	t.Run("nothing listening", func(t *testing.T) {
		code, res, raw := run(ada, probe(map[string]any{"base_url": closedURL(t)}))
		if code != 200 || res.OK || res.Error != "Could not reach the provider." {
			t.Fatalf("%d %s", code, raw)
		}
	})
	t.Run("input the person can fix", func(t *testing.T) {
		prov.status.Store(200)
		before := prov.hits.Load()
		for name, extra := range map[string]map[string]any{
			"unknown adapter":         {"adapter": "cohere"},
			"no adapter":              {"adapter": ""},
			"bad plan":                {"plan_kind": "free"},
			"no key for metered":      {"plan_kind": "metered", "base_url": "https://api.example.com", "api_key": ""},
			"http for metered":        {"plan_kind": "metered", "base_url": "http://api.example.com"},
			"private for metered":     {"plan_kind": "metered", "base_url": "https://127.0.0.1:" + strings.Split(prov.url(), ":")[2]},
			"metadata for local":      {"base_url": "http://169.254.169.254"},
			"userinfo":                {"base_url": "http://u:p@127.0.0.1"},
			"no base url":             {"base_url": ""},
			"key with a control char": {"api_key": "a\nb"},
		} {
			code, res, raw := run(ada, probe(extra))
			if code != 200 || res.OK || res.Error == "" {
				t.Errorf("%s: %d %s", name, code, raw)
			}
		}
		if prov.hits.Load() != before {
			t.Errorf("a refused test reached the provider")
		}
	})
	t.Run("bad body", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/providers/test", strings.NewReader("{nope"))
		req.Header.Set("X-User", ada)
		rec := httptest.NewRecorder()
		r.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status %d", rec.Code)
		}
	})
	t.Run("fake adapter", func(t *testing.T) {
		code, res, raw := run(ada, map[string]any{"adapter": "fake", "plan_kind": "metered", "api_key": adaKey})
		if code != 200 || !res.OK {
			t.Fatalf("%d %s", code, raw)
		}
	})
	r.assertNoSecrets(adaKey)
}

func TestTestRouteWithASavedAccountUsesItsKeyAndStoresTheStatus(t *testing.T) {
	r := newRig(t, masterKeys(t, 'a'))
	prov := newFakeProvider(t)
	made := r.mustAdd(ada, body("Saved", prov.url(), adaKey))
	status := func() (string, bool) {
		var s string
		var checked bool
		if err := r.owner.QueryRow(context.Background(), `SELECT status, checked_at IS NOT NULL FROM gateway.provider_accounts WHERE id = $1::uuid`, made.ID).Scan(&s, &checked); err != nil {
			t.Fatal(err)
		}
		return s, checked
	}
	if s, checked := status(); s != "Not connected" || checked {
		t.Fatalf("new account: %q, %v", s, checked)
	}
	for _, c := range []struct {
		provider int32
		want     string
		message  string
	}{
		{200, "Connected", ""},
		{401, "Key rejected", "The key was rejected."},
		{429, "Rate limited", "Rate limited."},
		{503, "Offline", "Could not reach the provider."},
	} {
		prov.status.Store(c.provider)
		code, raw := r.call(ada, http.MethodPost, "/api/providers/test", map[string]any{"id": made.ID})
		res := decode[app.TestResult](t, raw)
		if code != 200 || res.OK != (c.provider == 200) || res.Error != c.message {
			t.Fatalf("provider %d: %d %s", c.provider, code, raw)
		}
		if prov.lastAuth() != "Bearer "+adaKey {
			t.Fatalf("the saved key was not used: %q", prov.lastAuth())
		}
		if s, checked := status(); s != c.want || !checked {
			t.Fatalf("provider %d: status %q checked %v", c.provider, s, checked)
		}
		if got := r.list(ada)[0].Status; got != c.want {
			t.Fatalf("list shows %q", got)
		}
	}
	// A key in the request wins over the saved one, and the saved one stays.
	prov.status.Store(200)
	if code, _ := r.call(ada, http.MethodPost, "/api/providers/test", map[string]any{"id": made.ID, "api_key": newKey}); code != 200 || prov.lastAuth() != "Bearer "+newKey {
		t.Fatalf("test with a new key sent %q", prov.lastAuth())
	}
	var ref string
	_ = r.owner.QueryRow(context.Background(), `SELECT credential_ref FROM gateway.provider_accounts WHERE id = $1::uuid`, made.ID).Scan(&ref)
	if got, _ := (DBSecrets{Pool: r.owner, Keys: r.keys}).Get(ref); got != adaKey {
		t.Fatalf("a test replaced the saved key with %q", got)
	}
	// The saved key cannot be opened by the wrong master key: say so, plainly.
	other := NewProviders(r.app, masterKeys(t, 'b'), nil)
	other.LocalPlan = func(context.Context, string) bool { return true }
	res, err := other.Test(context.Background(), ada, app.TestRequest{ID: made.ID})
	if err != nil || res.OK || res.Error == "" || strings.Contains(res.Error, adaKey) {
		t.Fatalf("wrong master key: %+v, %v", res, err)
	}
	r.assertNoSecrets(adaKey, newKey)
}

func TestTestRouteIsRateLimited(t *testing.T) {
	r := newRigLimited(t, masterKeys(t, 'a'), 0)
	test := func(user string) int {
		code, _ := r.call(user, http.MethodPost, "/api/providers/test", map[string]any{"adapter": "fake", "plan_kind": "metered", "api_key": "k"})
		return code
	}
	for i := range 10 {
		if code := test(ada); code != 200 {
			t.Fatalf("test %d = %d", i+1, code)
		}
	}
	code, raw := r.call(ada, http.MethodPost, "/api/providers/test", map[string]any{"adapter": "fake", "plan_kind": "metered", "api_key": "k"})
	if code != http.StatusTooManyRequests || message(t, raw) == "" {
		t.Fatalf("11th test = %d %s", code, raw)
	}
	if code := test(bob); code != 200 {
		t.Fatalf("bob's first test = %d", code)
	}
	// Other routes are not counted.
	if code, _ := r.call(ada, http.MethodGet, "/api/providers", nil); code != 200 {
		t.Fatalf("list = %d", code)
	}
}

func TestADatabaseDownIsAPlainError(t *testing.T) {
	r := newRig(t, masterKeys(t, 'a'))
	r.app.Close()
	code, raw := r.call(ada, http.MethodGet, "/api/providers", nil)
	if code != http.StatusInternalServerError || message(t, raw) != "providers" {
		t.Fatalf("status %d %s", code, raw)
	}
}

// chatVia calls the gateway's chat route the way brain does.
func chatVia(t *testing.T, mux http.Handler, route, ref string) (int, string) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"route": route, "prompt": "hi", "credential_ref": ref})
	req := httptest.NewRequest(http.MethodPost, "/upmo.gateway.v1.Gateway/Chat", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// linkRoute makes a chat route that uses the account.
func linkRoute(t *testing.T, r *rig, route, accountID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := r.owner.Exec(ctx, `INSERT INTO gateway.routes (name, capability) VALUES ($1, 'chat')`, route); err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(ctx, `
		INSERT INTO gateway.route_links (route_id, position, provider_account_id, model, retry_budget)
		SELECT id, 1, $2::uuid, 'm1', 0 FROM gateway.routes WHERE name = $1`, route, accountID); err != nil {
		t.Fatal(err)
	}
}

func refOf(t *testing.T, r *rig, id string) string {
	t.Helper()
	var ref string
	if err := r.owner.QueryRow(context.Background(), `SELECT credential_ref FROM gateway.provider_accounts WHERE id = $1::uuid`, id).Scan(&ref); err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestDispatcherUsesAnOwnAccountsKeyFromTheDatabase(t *testing.T) {
	r := newRig(t, masterKeys(t, 'a'))
	prov := newFakeProvider(t)
	made := r.mustAdd(ada, body("Mine", prov.url(), adaKey))
	linkRoute(t, r, "draft", made.ID)
	ref := refOf(t, r, made.ID)

	mux := http.NewServeMux()
	Mount(mux, r.owner, Chain{EnvSecrets{}, DBSecrets{Pool: r.owner, Keys: r.keys}}, nil)
	code, out := chatVia(t, mux, "draft", ref)
	if code != 200 || !strings.Contains(out, "hello from provider") {
		t.Fatalf("chat = %d %s", code, out)
	}
	if prov.lastAuth() != "Bearer "+adaKey {
		t.Fatalf("the provider saw %q", prov.lastAuth())
	}
	// The outcome is stored on the account, off the calling path.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if r.count(`SELECT count(*) FROM gateway.provider_accounts WHERE id = $1::uuid AND status = 'Connected' AND checked_at IS NOT NULL`, made.ID) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the dispatcher never stored the Connected status")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// A key the provider refuses is stored as Key rejected.
	prov.status.Store(401)
	hits := prov.hits.Load()
	code, out = chatVia(t, mux, "draft", ref)
	if code == 200 || strings.Contains(out, adaKey) {
		t.Fatalf("a refused key: %d %s", code, out)
	}
	for r.count(`SELECT count(*) FROM gateway.provider_accounts WHERE id = $1::uuid AND status = 'Key rejected'`, made.ID) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("the dispatcher never stored Key rejected")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if prov.hits.Load() == hits {
		t.Fatal("the second call never reached the provider")
	}

	// With the wrong master key the key is missing: the call fails closed and
	// the provider is not called at all.
	prov.status.Store(200)
	hits = prov.hits.Load()
	wrong := http.NewServeMux()
	Mount(wrong, r.owner, Chain{EnvSecrets{}, DBSecrets{Pool: r.owner, Keys: masterKeys(t, 'z')}}, nil)
	code, out = chatVia(t, wrong, "draft", ref)
	if code == 200 || prov.hits.Load() != hits || strings.Contains(out, adaKey) {
		t.Fatalf("wrong master key: %d %s (hits %d -> %d)", code, out, hits, prov.hits.Load())
	}
	// With no database reader in the chain, the same.
	bare := http.NewServeMux()
	Mount(bare, r.owner, Chain{EnvSecrets{}}, nil)
	if code, _ = chatVia(t, bare, "draft", ref); code == 200 || prov.hits.Load() != hits {
		t.Fatalf("no reader: %d", code)
	}
	// An env variable for the same ref is not how a person's key is found, and
	// a person's ref is not the house's.
	if code, _ = chatVia(t, mux, "draft", ""); code == 200 {
		t.Fatal("a house call used a person's account")
	}
}

func TestACallToANameRepointedAtAnInternalAddressNeverDials(t *testing.T) {
	r := newRig(t, masterKeys(t, 'a'))
	prov := newFakeProvider(t)
	port := strings.Split(prov.url(), ":")[2]
	answer := map[string]netip.Addr{}
	var mu sync.Mutex
	lookup := func(_ context.Context, _, host string) ([]netip.Addr, error) {
		mu.Lock()
		defer mu.Unlock()
		if ip, ok := answer[host]; ok {
			return []netip.Addr{ip}, nil
		}
		return nil, errors.New("no such host")
	}
	repoint := func(host, ip string) {
		mu.Lock()
		defer mu.Unlock()
		answer[host] = netip.MustParseAddr(ip)
	}
	store := postgres.Store{Pool: r.owner}
	dispatch := app.Dispatch{
		Routes:  store,
		Secrets: Chain{DBSecrets{Pool: r.owner, Keys: r.keys}},
		Chat:    map[string]app.Chatter{domain.AdapterOpenAI: openai.Adapter{Lookup: lookup}},
	}
	call := func(route, ref string) error {
		_, err := dispatch.ChatCall(context.Background(), route, ref, domain.Principal{},
			domain.Input{Messages: []domain.Message{{Role: domain.RoleUser, Text: "hi"}}})
		return err
	}

	// A local provider on a name: fine while the name points at loopback, and
	// refused the moment it points at a metadata address.
	local := r.mustAdd(ada, body("Local on a name", "http://gpu.test:"+port, adaKey))
	linkRoute(t, r, "local-route", local.ID)
	repoint("gpu.test", "127.0.0.1")
	if err := call("local-route", refOf(t, r, local.ID)); err != nil {
		t.Fatalf("local provider on a name: %v", err)
	}
	if prov.hits.Load() != 1 {
		t.Fatalf("hits = %d", prov.hits.Load())
	}
	for _, ip := range []string{"169.254.169.254", "fe80::1", "100.100.100.200", "0.0.0.0"} {
		repoint("gpu.test", ip)
		if err := call("local-route", refOf(t, r, local.ID)); !errors.Is(err, netguard.ErrBlocked) {
			t.Fatalf("a name re-pointed at %s: error = %v, want the dial refused", ip, err)
		}
	}
	if prov.hits.Load() != 1 {
		t.Fatalf("a refused call reached the provider: hits = %d", prov.hits.Load())
	}

	// A metered account may never reach a private or loopback address, even
	// through a name that looked public when it was saved.
	metered := r.mustAdd(ada, map[string]any{
		"provider": "p", "name": "Metered on a name", "adapter": "openai_compat", "plan_kind": "metered",
		"base_url": "https://api.test:" + port, "api_key": newKey, "models": []string{"m1"},
	})
	linkRoute(t, r, "metered-route", metered.ID)
	for _, ip := range []string{"127.0.0.1", "10.0.0.5", "192.168.1.1", "169.254.169.254", "::1"} {
		repoint("api.test", ip)
		if err := call("metered-route", refOf(t, r, metered.ID)); !errors.Is(err, netguard.ErrBlocked) {
			t.Fatalf("a metered account on %s: error = %v, want the dial refused", ip, err)
		}
	}
	if prov.hits.Load() != 1 {
		t.Fatalf("a refused call reached the provider: hits = %d", prov.hits.Load())
	}
}

// TestGatewayProcessReadsEveryAccountUnderForcedRowSecurity runs the gateway's
// reads as a role that is not a superuser and owns the tables, which is how it
// runs where the database user is not one. Row security is forced for that
// role, so the service policy is what lets it in.
func TestGatewayProcessReadsEveryAccountUnderForcedRowSecurity(t *testing.T) {
	r := newRig(t, masterKeys(t, 'a'))
	prov := newFakeProvider(t)
	made := r.mustAdd(ada, body("Mine", prov.url(), adaKey))
	other := r.mustAdd(bob, body("Hers", prov.url(), newKey))
	r.globalAccount("House")
	ctx := context.Background()

	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	role := "gw_test_" + hex.EncodeToString(suffix[:])
	if _, err := r.owner.Exec(ctx, `CREATE ROLE `+role+` NOLOGIN`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, stmt := range []string{`REASSIGN OWNED BY ` + role + ` TO CURRENT_USER`, `DROP OWNED BY ` + role, `DROP ROLE ` + role} {
			_, _ = r.owner.Exec(context.Background(), stmt)
		}
	})
	for _, stmt := range []string{
		`ALTER TABLE gateway.provider_accounts OWNER TO ` + role,
		`ALTER TABLE gateway.provider_models OWNER TO ` + role,
		`ALTER TABLE gateway.credentials OWNER TO ` + role,
		`GRANT USAGE ON SCHEMA gateway TO ` + role,
		`GRANT ALL ON ALL TABLES IN SCHEMA gateway TO ` + role,
		// The real gateway owns these functions.
		`GRANT EXECUTE ON FUNCTION identity.may_use(uuid, text, uuid[]), identity.may_manage(uuid, text), identity.can_use(uuid, uuid[], uuid, text, uuid[]) TO ` + role,
	} {
		if _, err := r.owner.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	svc := servicePool(t, r.owner, role)

	var who string
	var forced bool
	if err := svc.QueryRow(ctx, `SELECT current_user, (SELECT relforcerowsecurity FROM pg_class WHERE oid = 'gateway.provider_accounts'::regclass)`).Scan(&who, &forced); err != nil || who != role || !forced {
		t.Fatalf("service pool runs as %q, forced=%v, err=%v", who, forced, err)
	}
	var accounts int
	if err := svc.QueryRow(ctx, `SELECT count(*) FROM gateway.provider_accounts`).Scan(&accounts); err != nil || accounts != 3 {
		t.Fatalf("the gateway process sees %d accounts, want all 3: %v", accounts, err)
	}
	for ref, want := range map[string]string{refOf(t, r, made.ID): adaKey, refOf(t, r, other.ID): newKey} {
		if got, err := (DBSecrets{Pool: svc, Keys: r.keys}).Get(ref); err != nil || got != want {
			t.Fatalf("gateway read of %s: %q, %v", ref, got, err)
		}
	}
	// It can record a status, and the route reads still work through RLS.
	if err := (postgres.Store{Pool: svc}).RecordStatus(ctx, refOf(t, r, made.ID), "Offline", time.Now()); err != nil {
		t.Fatal(err)
	}
	if r.count(`SELECT count(*) FROM gateway.provider_accounts WHERE status = 'Offline'`) != 1 {
		t.Fatal("the status was not stored")
	}
	linkRoute(t, r, "draft", made.ID)
	route, err := (postgres.Store{Pool: svc}).ByName(ctx, "draft")
	if err != nil || len(route.Links) != 1 || route.Links[0].Account.CredentialRef != refOf(t, r, made.ID) {
		t.Fatalf("route read: %+v, %v", route, err)
	}
	// A route link says whose account it is, which is how a streamed reply
	// finds a person's own links.
	if route.Links[0].Account.OwnerUserID != ada || route.Links[0].Account.House {
		t.Fatalf("link owner = %q house %v, want %q", route.Links[0].Account.OwnerUserID, route.Links[0].Account.House, ada)
	}

	// The request role (upmo_app) with no person sees none of it.
	var credentials int
	if err := r.app.QueryRow(ctx, `SELECT count(*) FROM gateway.credentials`).Scan(&credentials); err != nil || credentials != 0 {
		t.Fatalf("a request with no person sees %d credentials: %v", credentials, err)
	}
	if err := r.app.QueryRow(ctx, `SELECT count(*) FROM gateway.provider_accounts`).Scan(&accounts); err != nil || accounts != 0 {
		t.Fatalf("a request with no person sees %d accounts, want none: %v", accounts, err)
	}
}

// servicePool opens connections that run as role, a database user that is
// not a superuser.
func servicePool(t *testing.T, like *pgxpool.Pool, role string) *pgxpool.Pool {
	t.Helper()
	cfg := like.Config()
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET ROLE "+role)
		return err
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestMigrationLeavesExistingAccountsGlobal(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	migrateWithIdentity(t, owner)
	var version int
	if err := owner.QueryRow(ctx, `SELECT max(version_id) FROM gateway.goose_db_version WHERE is_applied`).Scan(&version); err != nil || version != 7 {
		t.Fatalf("version %d, %v", version, err)
	}
	var n int
	if err := owner.QueryRow(ctx, `
		SELECT count(*) FROM pg_policies WHERE schemaname = 'gateway'
		AND tablename IN ('provider_accounts', 'provider_models', 'credentials')`).Scan(&n); err != nil || n != 11 {
		t.Fatalf("policies = %d, %v", n, err)
	}
	var forced int
	if err := owner.QueryRow(ctx, `
		SELECT count(*) FROM pg_class WHERE oid IN ('gateway.provider_accounts'::regclass, 'gateway.provider_models'::regclass, 'gateway.credentials'::regclass)
		AND relrowsecurity AND relforcerowsecurity`).Scan(&forced); err != nil || forced != 3 {
		t.Fatalf("tables with forced row security = %d, %v", forced, err)
	}
	// A house row is ownerless, visible to everyone, and not connected until a call says so.
	if _, err := owner.Exec(ctx, `
		INSERT INTO gateway.provider_accounts (name, adapter, credential_ref, plan_kind, house, visibility)
		VALUES ('local', 'fake', 'house', 'local', true, 'everyone')`); err != nil {
		t.Fatal(err)
	}
	var global bool
	if err := owner.QueryRow(ctx, `
		SELECT owner_user_id IS NULL AND house AND visibility = 'everyone' AND status = 'Not connected'
		FROM gateway.provider_accounts`).Scan(&global); err != nil || !global {
		t.Fatalf("existing-shaped row: global=%v err=%v", global, err)
	}
	if err := migrate.Down(ctx, owner, Schema, Migrations()); err != nil {
		t.Fatalf("down: %v", err)
	}
	if err := migrate.Up(ctx, owner, Schema, Migrations()); err != nil {
		t.Fatalf("up after down: %v", err)
	}
	if err := owner.QueryRow(ctx, `SELECT max(version_id) FROM gateway.goose_db_version WHERE is_applied`).Scan(&version); err != nil || version != 7 {
		t.Fatalf("version after re-up = %d, %v", version, err)
	}
}
