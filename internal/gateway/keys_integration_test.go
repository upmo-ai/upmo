//go:build integration

package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/identity"
	workerkeys "github.com/upmo-ai/upmo/internal/platform/keys"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

// keyWorker is the worker's real key service over the rig's owner pool.
type keyWorker struct {
	srv *httptest.Server
	key map[string]string
}

func startKeyWorker(t *testing.T, r *rig, ring Keyring) *keyWorker {
	t.Helper()
	w := &keyWorker{key: map[string]string{}}
	verify := KeysVerify{}
	for _, audience := range []string{svcsig.AudienceWorkerBrain, svcsig.AudienceWorkerGateway, svcsig.AudienceWorkerSandboxd} {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			t.Fatal(err)
		}
		w.key[audience] = "upmo_svc_" + audience + "_" + base64.RawStdEncoding.EncodeToString(raw)
		verify[audience] = [][]byte{raw}
	}
	// The worker applies the rig's rules for sharing, teams, member
	// providers and the local plan, read when each test runs, and resolves
	// the person from a real session row.
	providers := WorkerProviders(r.owner, nil)
	// The adapters a test points at its own listeners are the worker's too.
	providers.Probes = r.svc.Probes
	providers.Allow = func(ctx context.Context, user string) (string, error) {
		if r.svc.Allow == nil {
			return "", nil
		}
		return r.svc.Allow(ctx, user)
	}
	providers.Teams = func(ctx context.Context, user string) ([]string, error) {
		if r.svc.Teams == nil {
			return nil, nil
		}
		return r.svc.Teams(ctx, user)
	}
	providers.MemberProvidersAllowed = func(ctx context.Context, user string) (bool, error) {
		if r.svc.MemberProvidersAllowed == nil {
			return true, nil
		}
		return r.svc.MemberProvidersAllowed(ctx, user)
	}
	providers.LocalPlan = func(ctx context.Context, user string) bool {
		return r.svc.LocalPlan != nil && r.svc.LocalPlan(ctx, user)
	}
	actor := func(ctx context.Context, hash []byte) (string, bool, error) {
		return identity.ActorOfTokenHash(ctx, r.owner, hash)
	}
	h, err := KeysHandler(r.owner, ring, verify, providers, actor, noPasswords{}, noEmails{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w.srv = httptest.NewServer(h)
	t.Cleanup(w.srv.Close)
	return w
}

func (w *keyWorker) client(t *testing.T, audience string) *workerkeys.Client {
	t.Helper()
	c, err := workerkeys.Open(workerkeys.Options{URL: w.srv.URL, Key: w.key[audience], Audience: audience, Development: true})
	if err != nil {
		t.Fatal(err)
	}
	return &c
}

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// Brain saves and replaces a key through the worker's wrap as upmo_app,
// gateway opens it as upmo_gateway through the worker's unwrap, and with the
// worker gone a save is a 503, never a key under the master.
func TestBrainSavesAKeyThroughTheWorkerAndGatewayOpensIt(t *testing.T) {
	ring := masterKeys(t, 'a')
	r := newRig(t, ring)
	worker := r.worker

	prov := newFakeProvider(t)
	made := r.mustAdd(ada, body("Mine", prov.url(), adaKey))
	if code, raw := r.call(ada, http.MethodPut, "/api/providers/"+made.ID, map[string]any{"api_key": newKey}); code != http.StatusOK {
		t.Fatalf("replace the key = %d %s", code, raw)
	}
	ref := refOf(t, r, made.ID)
	var ciphertext, nonce, wrapped, wrapNonce []byte
	var keyID string
	if err := r.owner.QueryRow(context.Background(), `
		SELECT ciphertext, nonce, key_id, wrapped_key, wrap_nonce FROM gateway.credentials WHERE ref = $1`, ref).
		Scan(&ciphertext, &nonce, &keyID, &wrapped, &wrapNonce); err != nil {
		t.Fatal(err)
	}
	if keyID != "k1" || len(wrapped) != 48 || len(wrapNonce) != 12 || bytes.Contains(ciphertext, []byte(newKey)) {
		t.Fatalf("stored row: key %q wrapped %d nonce %d", keyID, len(wrapped), len(wrapNonce))
	}
	// The master does not open the payload: it is under its own data key.
	if _, err := ring.Open(ref, domain.Sealed{Ciphertext: ciphertext, Nonce: nonce, KeyID: keyID}); !errors.Is(err, domain.ErrUnseal) {
		t.Fatalf("the master opened the payload: %v", err)
	}

	linkRoute(t, r, "draft", made.ID)
	mux := http.NewServeMux()
	Mount(mux, r.owner, Chain{EnvSecrets{}, DBSecrets{Pool: pgtest.Login(t, r.owner, "upmo_gateway"), Unwrap: worker.client(t, svcsig.AudienceWorkerGateway)}}, nil)
	code, out := chatVia(t, mux, "draft", ref)
	if code != http.StatusOK || !strings.Contains(out, "hello from provider") || prov.lastAuth() != "Bearer "+newKey {
		t.Fatalf("chat = %d %s, provider saw %q", code, out, prov.lastAuth())
	}

	worker.srv.Close()
	code, raw := r.add(ada, body("Second", prov.url(), adaKey))
	if code != http.StatusServiceUnavailable || !strings.Contains(string(raw), "Provider keys cannot be saved right now.") {
		t.Fatalf("add with the worker gone = %d %s", code, raw)
	}
	if n := r.count(`SELECT count(*) FROM gateway.provider_accounts WHERE name = 'Second'`); n != 0 {
		t.Fatalf("an account was saved without its key: %d", n)
	}
	r.assertNoSecrets(adaKey, newKey)
}

// Invariant 9, brain's part: upmo_app writes a saved key and reads back only
// its ref, key id and age; a row without its wrap does not commit.
func TestBrainCannotReadASavedKeyBack(t *testing.T) {
	r := newRig(t, masterKeys(t, 'a'))
	made := r.mustAdd(ada, body("Mine", "https://127.0.0.1", adaKey))
	ref := refOf(t, r, made.ID)
	ctx := context.Background()
	who := app.Caller{User: ada}
	as := func(query string) error {
		tx, err := r.app.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := r.asPerson(ctx, tx, ada, ""); err != nil {
			t.Fatal(err)
		}
		rows, err := tx.Query(ctx, query, ref)
		if err != nil {
			return err
		}
		n := 0
		for rows.Next() {
			n++
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if n != 1 {
			t.Fatalf("%s saw %d rows", query, n)
		}
		return nil
	}
	for _, column := range []string{"ciphertext", "nonce", "wrapped_key", "wrap_nonce"} {
		if err := as(`SELECT ` + column + ` FROM gateway.credentials WHERE ref = $1`); pgCode(err) != "42501" {
			t.Errorf("upmo_app SELECT %s = %v, want 42501", column, err)
		}
	}
	if err := as(`SELECT ref, key_id, created_at FROM gateway.credentials WHERE ref = $1`); err != nil {
		t.Fatalf("upmo_app SELECT ref, key_id, created_at = %v", err)
	}
	// The real write path, with a sealed value that lacks its wrap.
	accounts := postgres.Providers{Pool: r.app}
	cur, err := accounts.Get(ctx, who, made.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = accounts.Update(ctx, who, cur, &domain.Sealed{Ciphertext: []byte("payload"), Nonce: make([]byte, 12), KeyID: "k1"}, false)
	if pgCode(err) != "23502" {
		t.Fatalf("a key without its wrap = %v, want 23502", err)
	}
}

// Invariant 5, brain's part: with no typed key, a saved key is tested by
// the worker at its saved address only. Another base URL or adapter is the
// fixed refusal before any worker call; the saved address goes through the
// worker's provider-test and stores its status.
func TestSavedKeyTestGoesOnlyToItsSavedAddress(t *testing.T) {
	r := newRig(t, masterKeys(t, 'a'))
	prov, other := newFakeProvider(t), newFakeProvider(t)
	made := r.mustAdd(ada, body("Saved", prov.url(), adaKey))
	workerCalls := func() int {
		return r.count(`SELECT count(*) FROM audit.pending WHERE action = 'keys.provider_test'`)
	}
	before := prov.hits.Load()
	for name, req := range map[string]map[string]any{
		"another base URL": {"id": made.ID, "base_url": other.url()},
		"another adapter":  {"id": made.ID, "adapter": "anthropic"},
	} {
		code, raw := r.call(ada, http.MethodPost, "/api/providers/test", req)
		res := decode[app.TestResult](t, raw)
		if code != http.StatusOK || res.OK || res.Error != "Enter the key again to test a new address." {
			t.Fatalf("%s = %d %s", name, code, raw)
		}
	}
	if n := workerCalls(); n != 0 || prov.hits.Load() != before || other.hits.Load() != 0 {
		t.Fatalf("refusals reached: worker %d, saved %d, other %d", n, prov.hits.Load()-before, other.hits.Load())
	}
	code, raw := r.call(ada, http.MethodPost, "/api/providers/test", map[string]any{"id": made.ID})
	if res := decode[app.TestResult](t, raw); code != http.StatusOK || !res.OK {
		t.Fatalf("saved address = %d %s", code, raw)
	}
	if n := workerCalls(); n != 1 || prov.hits.Load() != before+1 || prov.lastAuth() != "Bearer "+adaKey || other.hits.Load() != 0 {
		t.Fatalf("saved address: worker %d, saved %d (%q), other %d", n, prov.hits.Load()-before, prov.lastAuth(), other.hits.Load())
	}
	// A typed key still probes in brain, at any address, with no worker call.
	code, raw = r.call(ada, http.MethodPost, "/api/providers/test", map[string]any{"id": made.ID, "base_url": other.url(), "api_key": newKey})
	if res := decode[app.TestResult](t, raw); code != http.StatusOK || !res.OK || other.hits.Load() != 1 || workerCalls() != 1 {
		t.Fatalf("typed key elsewhere = %d %s, other %d, worker %d", code, raw, other.hits.Load(), workerCalls())
	}
	// Without the worker a saved-key test is the plain 503.
	r.worker.srv.Close()
	if code, raw := r.call(ada, http.MethodPost, "/api/providers/test", map[string]any{"id": made.ID}); code != http.StatusServiceUnavailable {
		t.Fatalf("saved key with the worker gone = %d %s", code, raw)
	}
	r.assertNoSecrets(adaKey, newKey)
}

// With member providers turned off, a member's saved-key test is refused by
// brain before any worker call, and by the worker when asked directly; the
// saved address is never dialed. Turned back on, both work.
func TestSavedKeyTestHonorsMemberProvidersOff(t *testing.T) {
	r := newRig(t, masterKeys(t, 'a'))
	memberProviders := true
	r.svc.MemberProvidersAllowed = func(context.Context, string) (bool, error) { return memberProviders, nil }
	r.mount(nil)
	if _, err := r.owner.Exec(context.Background(), `
		INSERT INTO identity.users (id, email, email_hash, email_hash_key_id, name, role_id, joined_at)
		VALUES
		  ($1::uuid, 'eve@example.test', sha256('eve@example.test'::bytea), 'k1', 'Eve', (SELECT id FROM identity.roles WHERE key = 'owner'), now()),
		  ($2::uuid, 'ada@example.test', sha256('ada@example.test'::bytea), 'k1', 'Ada', (SELECT id FROM identity.roles WHERE key = 'member'), now())`, eve, ada); err != nil {
		t.Fatal(err)
	}
	prov := newFakeProvider(t)
	made := r.mustAdd(ada, body("Saved", prov.url(), adaKey))
	workerCalls := func() int {
		return r.count(`SELECT count(*) FROM audit.pending WHERE action = 'keys.provider_test'`)
	}
	brain := r.worker.client(t, svcsig.AudienceWorkerBrain)
	ctx := context.Background()

	memberProviders = false
	code, raw := r.call(ada, http.MethodPost, "/api/providers/test", map[string]any{"id": made.ID})
	if code != http.StatusForbidden || message(t, raw) != "Your workspace has turned off member providers." {
		t.Fatalf("brain test with member providers off = %d %s", code, raw)
	}
	if n := workerCalls(); n != 0 || prov.hits.Load() != 0 {
		t.Fatalf("brain refusal reached: worker %d, provider %d", n, prov.hits.Load())
	}
	if res, err := brain.ProviderTest(ctx, made.ID, r.session(ada)); err == nil || err.Error() != "keys: worker unavailable" {
		t.Fatalf("worker test with member providers off = %+v %v", res, err)
	}
	if n := r.count(`SELECT count(*) FROM audit.pending WHERE action = 'keys.provider_test' AND outcome = 'denied'`); n != 1 || workerCalls() != 1 || prov.hits.Load() != 0 {
		t.Fatalf("worker refusal: %d denied of %d rows, provider %d", n, workerCalls(), prov.hits.Load())
	}

	memberProviders = true
	code, raw = r.call(ada, http.MethodPost, "/api/providers/test", map[string]any{"id": made.ID})
	if res := decode[app.TestResult](t, raw); code != http.StatusOK || !res.OK {
		t.Fatalf("brain test with member providers on = %d %s", code, raw)
	}
	if res, err := brain.ProviderTest(ctx, made.ID, r.session(ada)); err != nil || !res.OK {
		t.Fatalf("worker test with member providers on = %+v %v", res, err)
	}
	if prov.hits.Load() != 2 || prov.lastAuth() != "Bearer "+adaKey {
		t.Fatalf("provider after turning it on = %d %q", prov.hits.Load(), prov.lastAuth())
	}
	r.assertNoSecrets(adaKey)
}

// nilModels is an OK probe that lists no models at all, as Vertex does.
type nilModels struct{}

func (nilModels) Probe(context.Context, app.Attempt) ([]domain.SeenModel, domain.FailureClass, error) {
	return nil, domain.ClassOK, nil
}

// A saved-key test keeps an empty model list apart from none: a provider
// that lists no models clears the seen models, as a typed-key test does,
// and an adapter that cannot list them leaves the seen models alone.
func TestSavedKeyTestKeepsEmptyModelsApartFromNone(t *testing.T) {
	r := newRig(t, masterKeys(t, 'a'))
	prov := newFakeProvider(t)
	made := r.mustAdd(ada, body("Saved", prov.url(), adaKey))
	seen := func() int {
		return r.count(`SELECT count(*) FROM gateway.account_models_seen WHERE account_id = $1::uuid`, made.ID)
	}
	plant := func() {
		t.Helper()
		if _, err := r.owner.Exec(context.Background(), `
			INSERT INTO gateway.account_models_seen (account_id, model_id, position) VALUES ($1::uuid, 'old-model', 1)
			ON CONFLICT DO NOTHING`, made.ID); err != nil {
			t.Fatal(err)
		}
	}
	test := func() {
		t.Helper()
		code, raw := r.call(ada, http.MethodPost, "/api/providers/test", map[string]any{"id": made.ID})
		if res := decode[app.TestResult](t, raw); code != http.StatusOK || !res.OK {
			t.Fatalf("saved-key test = %d %s", code, raw)
		}
	}

	// The OpenAI-compatible probe reads {"data":[]}: an empty list.
	plant()
	test()
	if n := seen(); n != 0 || prov.hits.Load() != 1 {
		t.Fatalf("after an empty list: %d seen models, provider %d, want 0 and 1", n, prov.hits.Load())
	}

	// An OK probe with no list keeps what was seen.
	keep := r.svc.Probes[domain.AdapterOpenAI]
	r.svc.Probes[domain.AdapterOpenAI] = nilModels{}
	t.Cleanup(func() { r.svc.Probes[domain.AdapterOpenAI] = keep })
	plant()
	test()
	if n := seen(); n != 1 {
		t.Fatalf("after no list: %d seen models, want 1", n)
	}
}

// noEmails finds no stored address: these tests serve provider-test only,
// and the email route has its own suite in cmd/upmo.
type noEmails struct{}

func (noEmails) Own(context.Context, []byte) (KeysEmailRow, bool, error) {
	return KeysEmailRow{}, false, nil
}
func (noEmails) Link(context.Context, []byte) (KeysEmailRow, bool, error) {
	return KeysEmailRow{}, false, nil
}
func (noEmails) Member(context.Context, []byte, string) (KeysEmailRow, bool, error) {
	return KeysEmailRow{}, false, nil
}
func (noEmails) Directory(context.Context, []byte) ([]KeysEmailRow, bool, error) {
	return nil, false, nil
}
func (noEmails) Open(string, []byte) (string, error) { return "", ErrKeysStoredPassword }

// noPasswords refuses every password check: these tests serve provider-test
// only, and password-verify has its own suite in cmd/upmo.
type noPasswords struct{}

func (noPasswords) ByAddress(context.Context, string) (KeysPasswordAccount, error) {
	return KeysPasswordAccount{}, errors.New("no password check here")
}
func (noPasswords) BySession(context.Context, []byte) (KeysPasswordAccount, bool, error) {
	return KeysPasswordAccount{}, false, nil
}
func (noPasswords) Take(context.Context, []byte) (int, error) {
	return 0, errors.New("no password check here")
}
func (noPasswords) Check(context.Context, KeysPasswordAccount, string) (bool, bool, error) {
	return false, false, ErrKeysStoredPassword
}
