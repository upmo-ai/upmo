//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/keys"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/provision"
)

const allowedKey = "sk-test-allowed"

// workerKeyConfig is a worker config with one fresh key per worker audience,
// and those keys by audience.
func workerKeyConfig(t *testing.T) (config.Config, map[string]string) {
	t.Helper()
	keys := map[string]string{}
	for _, audience := range []string{svcsig.AudienceWorkerBrain, svcsig.AudienceWorkerGateway, svcsig.AudienceWorkerSandboxd} {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			t.Fatal(err)
		}
		keys[audience] = "upmo_svc_" + audience + "_" + base64.RawStdEncoding.EncodeToString(raw)
	}
	return config.Config{
		WorkerBrainKey:    keys[svcsig.AudienceWorkerBrain],
		WorkerGatewayKey:  keys[svcsig.AudienceWorkerGateway],
		WorkerSandboxdKey: keys[svcsig.AudienceWorkerSandboxd],
	}, keys
}

// workerMaster is the raw master key workerRing uses.
var workerMaster = bytes.Repeat([]byte{0x4b}, 32)

// workerRing is a ring over workerMaster, current id id.
func workerRing(t *testing.T, id string) gateway.Keyring {
	t.Helper()
	ring, err := gateway.KeyringFromEnv(func(name string) (string, bool) {
		switch name {
		case gateway.EnvMasterKey:
			return base64.StdEncoding.EncodeToString(workerMaster), true
		case gateway.EnvMasterKeyID:
			return id, true
		}
		return "", false
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return ring
}

// keyWorker is the real worker listener: workerHandler as the worker serves it.
type keyWorker struct {
	t     *testing.T
	owner *pgxpool.Pool
	ring  gateway.Keyring
	keys  map[string]string
	srv   *httptest.Server
}

func startKeyWorker(t *testing.T) *keyWorker {
	t.Helper()
	return startKeyWorkerOn(t, workerRing(t, "k1"))
}

// startKeyWorkerOn is startKeyWorker over ring.
func startKeyWorkerOn(t *testing.T, ring gateway.Keyring) *keyWorker {
	t.Helper()
	owner := migrated(t)
	cfg, keyByAudience := workerKeyConfig(t)
	cfg.PublicURL, cfg.AvatarsDir = "http://localhost:5173", t.TempDir()
	h, err := workerHandler(owner, cfg, ring, []provision.Key{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &keyWorker{t: t, owner: owner, ring: ring, keys: keyByAudience, srv: srv}
}

func (w *keyWorker) client(audience string) keys.Client {
	w.t.Helper()
	c, err := keys.Open(keys.Options{URL: w.srv.URL, Key: w.keys[audience], Audience: audience, Development: true})
	if err != nil {
		w.t.Fatal(err)
	}
	return c
}

// signed is one bearer for audience over method, path and body.
func (w *keyWorker) signed(audience, method, path, body string) string {
	w.t.Helper()
	raw, err := svcsig.ParseAudienceKey(w.keys[audience], audience)
	if err != nil {
		w.t.Fatal(err)
	}
	token, err := svcsig.SignRequest(raw, audience, method, path, "", []byte(body), time.Now())
	if err != nil {
		w.t.Fatal(err)
	}
	return token
}

// send posts body with bearer, or none when bearer is empty.
func (w *keyWorker) send(method, path, bearer, body string) (int, string) {
	w.t.Helper()
	req, err := http.NewRequest(method, w.srv.URL+path, strings.NewReader(body))
	if err != nil {
		w.t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	got, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(got)
}

// call signs for audience and sends.
func (w *keyWorker) call(audience, method, path, body string) (int, string) {
	w.t.Helper()
	return w.send(method, path, w.signed(audience, method, path, body), body)
}

// seed stores ref with plaintext the way brain does: a data key wrapped by
// the worker, the payload sealed with it in the caller.
func (w *keyWorker) seed(ref, plaintext string) gateway.Sealed {
	w.t.Helper()
	brain := w.client(svcsig.AudienceWorkerBrain)
	sealed, err := gateway.WorkerSealer{Keys: &brain}.SealCredential(context.Background(), ref, plaintext)
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.owner.Exec(context.Background(), `
		INSERT INTO gateway.credentials (ref, ciphertext, nonce, key_id, wrapped_key, wrap_nonce)
		VALUES ($1, $2, $3, $4, $5, $6)`, ref, sealed.Ciphertext, sealed.Nonce, sealed.KeyID, sealed.WrappedKey, sealed.WrapNonce); err != nil {
		w.t.Fatal(err)
	}
	return sealed
}

// auditRows returns the outcomes of the pending rows for action, and their
// changes as one flat object of each value's "to", which is every value the
// row holds: the worker stores no "from".
func (w *keyWorker) auditRows(action string) (changes []string, outcomes []string) {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `
		SELECT COALESCE((SELECT jsonb_object_agg(k, v->'to') FROM jsonb_each(changes->'values') AS e(k, v)), '{}'::jsonb)::text,
		       outcome, service, actor_type, credential_type
		FROM audit.pending WHERE action = $1 ORDER BY occurred_at, id`, action)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var c, o, service, actor, credential string
		if err := rows.Scan(&c, &o, &service, &actor, &credential); err != nil {
			w.t.Fatal(err)
		}
		if actor != "system" || credential != "service" {
			w.t.Fatalf("%s row actor %q credential %q", action, actor, credential)
		}
		changes = append(changes, c)
		outcomes = append(outcomes, o)
	}
	return changes, outcomes
}

const unwrapGITHUB = `{"kind":"credential","ref":"GITHUB","sandbox_id":""}`

// Invariant 1: the payload is sealed under its own data key, bound to the
// ref, and the data key is wrapped for v1:credential:<ref>.
func TestKeysPayloadUnderItsOwnDataKey(t *testing.T) {
	w := startKeyWorker(t)
	sealed := w.seed("GITHUB", allowedKey)
	if bytes.Contains(sealed.Ciphertext, []byte(allowedKey)) {
		t.Fatal("the ciphertext holds the plaintext")
	}
	if sealed.KeyID != "k1" || len(sealed.WrapNonce) != 12 || len(sealed.WrappedKey) != 48 {
		t.Fatalf("wrap = key %q nonce %d wrapped %d", sealed.KeyID, len(sealed.WrapNonce), len(sealed.WrappedKey))
	}
	// The master is not the payload key.
	if _, err := domain.OpenData(workerMaster, "GITHUB", sealed.Ciphertext, sealed.Nonce); err == nil || err.Error() != "gateway: cannot open credential" {
		t.Fatalf("open with the master as payload key = %v", err)
	}
	gw := w.client(svcsig.AudienceWorkerGateway)
	dataKey, err := gw.UnwrapCredential(context.Background(), "GITHUB")
	if err != nil || len(dataKey) != 32 {
		t.Fatalf("unwrap GITHUB = %d bytes %v", len(dataKey), err)
	}
	plain, err := domain.OpenData(dataKey, "GITHUB", sealed.Ciphertext, sealed.Nonce)
	if err != nil || plain != allowedKey {
		t.Fatalf("open with the unwrapped key = %q %v", plain, err)
	}
	// The payload is bound to its ref, and the wrap to v1:credential:<ref>.
	if _, err := domain.OpenData(dataKey, "OTHER", sealed.Ciphertext, sealed.Nonce); !errors.Is(err, domain.ErrUnseal) {
		t.Fatalf("open the payload for OTHER = %v", err)
	}
	if _, err := w.ring.Unwrap(domain.KindCredential, "OTHER", sealed.KeyID, sealed.WrapNonce, sealed.WrappedKey); !errors.Is(err, domain.ErrUnseal) {
		t.Fatalf("unwrap with v1:credential:OTHER = %v", err)
	}
	if got, err := w.ring.Unwrap(domain.KindCredential, "GITHUB", sealed.KeyID, sealed.WrapNonce, sealed.WrappedKey); err != nil || !bytes.Equal(got, dataKey) {
		t.Fatalf("unwrap with v1:credential:GITHUB = %v", err)
	}
}

// Invariant 3: only a fresh v2 bearer of a worker audience gets in.
func TestKeysRoutesNeedAWorkerAudienceBearer(t *testing.T) {
	w := startKeyWorker(t)
	w.seed("GITHUB", allowedKey)
	wrapBody := `{"kind":"credential","ref":"GITHUB"}`
	for _, route := range []struct{ method, path, body string }{
		{http.MethodGet, keys.PathReady, ""},
		{http.MethodPost, keys.PathWrap, wrapBody},
		{http.MethodPost, keys.PathUnwrap, unwrapGITHUB},
		{http.MethodPost, keys.PathProviderTest, `{"account_id":"018f3b2c-7a10-7c4a-8e11-777777777777","actor_token_hash":"AAAA"}`},
	} {
		if code, _ := w.send(route.method, route.path, "", route.body); code != http.StatusUnauthorized {
			t.Errorf("unsigned %s = %d, want 401", route.path, code)
		}
		// A provision bearer, and a bearer of the gateway service audience,
		// each signed correctly for their own audience.
		provisionRaw := bytes.Repeat([]byte{'p'}, 32)
		derived, err := svcsig.Derive(provisionRaw, svcsig.AudienceProvision)
		if err != nil {
			t.Fatal(err)
		}
		gatewayRaw := bytes.Repeat([]byte{'g'}, 32)
		// The worker-gateway key itself, signing for the gateway service
		// audience: a key never crosses audiences.
		workerGatewayRaw, err := svcsig.ParseAudienceKey(w.keys[svcsig.AudienceWorkerGateway], svcsig.AudienceWorkerGateway)
		if err != nil {
			t.Fatal(err)
		}
		for name, bearer := range map[string]struct {
			key      []byte
			audience string
		}{
			"provision":                         {derived, svcsig.AudienceProvision},
			"gateway service":                   {gatewayRaw, svcsig.AudienceGateway},
			"gateway service with a worker key": {workerGatewayRaw, svcsig.AudienceGateway},
		} {
			token, err := svcsig.SignRequest(bearer.key, bearer.audience, route.method, route.path, "", []byte(route.body), time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if code, _ := w.send(route.method, route.path, token, route.body); code != http.StatusUnauthorized {
				t.Errorf("%s bearer on %s = %d, want 401", name, route.path, code)
			}
		}
		// A worker bearer signed with a key the worker does not hold.
		stranger := bytes.Repeat([]byte{'z'}, 32)
		token, err := svcsig.SignRequest(stranger, svcsig.AudienceWorkerGateway, route.method, route.path, "", []byte(route.body), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if code, _ := w.send(route.method, route.path, token, route.body); code != http.StatusUnauthorized {
			t.Errorf("unknown worker-gateway key on %s = %d, want 401", route.path, code)
		}
	}
	// A replayed bearer is 401 the second time.
	token := w.signed(svcsig.AudienceWorkerGateway, http.MethodPost, keys.PathUnwrap, unwrapGITHUB)
	if code, _ := w.send(http.MethodPost, keys.PathUnwrap, token, unwrapGITHUB); code != http.StatusOK {
		t.Fatalf("first use = %d", code)
	}
	if code, body := w.send(http.MethodPost, keys.PathUnwrap, token, unwrapGITHUB); code != http.StatusUnauthorized || strings.Contains(body, "data_key") {
		t.Fatalf("replay = %d %s, want 401", code, body)
	}
	// Ready answers each worker audience, with no key bytes.
	for _, audience := range []string{svcsig.AudienceWorkerBrain, svcsig.AudienceWorkerGateway, svcsig.AudienceWorkerSandboxd} {
		if code, body := w.call(audience, http.MethodGet, keys.PathReady, ""); code != http.StatusOK || body != "ok" {
			t.Errorf("ready as %s = %d %q", audience, code, body)
		}
	}
	// The compose healthcheck still reads /readyz unsigned.
	if code, _ := w.send(http.MethodGet, "/readyz", "", ""); code != http.StatusOK {
		t.Fatalf("unsigned /readyz = %d", code)
	}
}

// Invariant 4, the gateway part: brain wraps, gateway unwraps, and every
// other combination is 403 keys: refused with no key bytes. Sandboxd's live
// sandbox cases are TestKeysSandboxSecretAndLiveness.
func TestKeysRoutesRefuseEveryOtherCaller(t *testing.T) {
	w := startKeyWorker(t)
	w.seed("GITHUB", allowedKey)
	wrapBody := `{"kind":"credential","ref":"GITHUB"}`
	for _, tc := range []struct {
		name, audience, path, body string
	}{
		{"wrap from gateway", svcsig.AudienceWorkerGateway, keys.PathWrap, wrapBody},
		{"wrap from sandboxd", svcsig.AudienceWorkerSandboxd, keys.PathWrap, wrapBody},
		{"wrap of a password", svcsig.AudienceWorkerBrain, keys.PathWrap, `{"kind":"password","ref":"GITHUB"}`},
		{"wrap with no ref", svcsig.AudienceWorkerBrain, keys.PathWrap, `{"kind":"credential"}`},
		{"unwrap from brain", svcsig.AudienceWorkerBrain, keys.PathUnwrap, unwrapGITHUB},
		{"unwrap of a password", svcsig.AudienceWorkerGateway, keys.PathUnwrap, `{"kind":"password","ref":"GITHUB","sandbox_id":""}`},
		{"unwrap with no ref", svcsig.AudienceWorkerGateway, keys.PathUnwrap, `{"kind":"credential","sandbox_id":""}`},
		{"unwrap of a missing row", svcsig.AudienceWorkerGateway, keys.PathUnwrap, `{"kind":"credential","ref":"NOPE","sandbox_id":""}`},
		{"gateway naming a sandbox", svcsig.AudienceWorkerGateway, keys.PathUnwrap, `{"kind":"credential","ref":"GITHUB","sandbox_id":"sb-1"}`},
		{"gateway with a proxy secret", svcsig.AudienceWorkerGateway, keys.PathUnwrap, `{"kind":"credential","ref":"GITHUB","sandbox_id":"","proxy_secret":"` + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x61}, 32)) + `"}`},
		{"gateway sending a wrapped key", svcsig.AudienceWorkerGateway, keys.PathUnwrap, `{"kind":"credential","ref":"GITHUB","sandbox_id":"","wrapped_key":"AAAA"}`},
		{"sandboxd with no live sandbox", svcsig.AudienceWorkerSandboxd, keys.PathUnwrap, `{"kind":"credential","ref":"GITHUB","sandbox_id":"sb-1","proxy_secret":"` + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x61}, 32)) + `"}`},
		{"two objects", svcsig.AudienceWorkerGateway, keys.PathUnwrap, unwrapGITHUB + unwrapGITHUB},
		{"an object then junk", svcsig.AudienceWorkerGateway, keys.PathUnwrap, unwrapGITHUB + " junk"},
	} {
		code, body := w.call(tc.audience, http.MethodPost, tc.path, tc.body)
		if code != http.StatusForbidden || body != "keys: refused" {
			t.Errorf("%s = %d %q, want 403 keys: refused", tc.name, code, body)
		}
	}
	// Brain gets a fresh 32-byte key; gateway gets the stored row's key.
	code, body := w.call(svcsig.AudienceWorkerBrain, http.MethodPost, keys.PathWrap, wrapBody)
	var made keys.WrapResponse
	if err := json.Unmarshal([]byte(body), &made); code != http.StatusOK || err != nil || len(made.DataKey) != 32 || made.KeyID != "k1" || len(made.WrapNonce) != 12 {
		t.Fatalf("wrap from brain = %d %s", code, body)
	}
	code, body = w.call(svcsig.AudienceWorkerGateway, http.MethodPost, keys.PathUnwrap, unwrapGITHUB)
	var got map[string][]byte
	if err := json.Unmarshal([]byte(body), &got); code != http.StatusOK || err != nil || len(got) != 1 || len(got["data_key"]) != 32 {
		t.Fatalf("unwrap from gateway = %d %s", code, body)
	}
}

// Invariant 6: every call writes one audit row through audit.record_keys
// before a key leaves, and the row never holds key material or a raw ref
// that missed its pattern.
func TestKeysAuditRowsComeFirst(t *testing.T) {
	w := startKeyWorker(t)
	ctx := context.Background()
	w.seed("GITHUB", allowedKey)

	// KeysAuditAllowList: the definer is the allow list.
	var before int
	if err := w.owner.QueryRow(ctx, `SELECT count(*) FROM audit.pending`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := w.owner.Exec(ctx, `SELECT audit.record_keys('keys.unwrap', 'success', 'sandboxd', '{"fields":["audience","kind","ref"],"values":{"audience":{"from":null,"to":"worker-sandboxd"},"kind":{"from":null,"to":"credential"},"ref":{"from":null,"to":"GITHUB"}}}')`); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		`SELECT audit.record_keys('auth.sign_in', 'success', 'brain', '{}')`,
		`SELECT audit.record_keys('keys.unwrap', 'failure', 'gateway', '{}')`,
		`SELECT audit.record_keys('keys.unwrap', 'success', 'worker', '{}')`,
		`SELECT audit.record_keys('keys.unwrap', 'success', 'gateway', '{"fields":["data_key"],"values":{"data_key":{"from":null,"to":"x"}}}')`,
		`SELECT audit.record_keys('keys.unwrap', 'success', 'gateway', '{"fields":["proxy_secret"],"values":{"proxy_secret":{"from":null,"to":"x"}}}')`,
		`SELECT audit.record_keys('keys.unwrap', 'success', 'gateway', '{"fields":["secret"],"values":{}}')`,
		`SELECT audit.record_keys('keys.unwrap', 'success', 'gateway', '[]')`,
		`SELECT audit.record_keys('keys.unwrap', 'success', 'gateway', '{"audience":"worker-gateway"}')`,
		`SELECT audit.record_keys('keys.unwrap', 'success', 'gateway', '{"fields":[],"values":{},"ref":"GITHUB"}')`,
		`SELECT audit.record_keys('keys.unwrap', 'success', 'gateway', jsonb_build_object('fields', '["ref"]'::jsonb, 'values', jsonb_build_object('ref', jsonb_build_object('from', null, 'to', repeat('a', 1100)))))`,
	} {
		if _, err := w.owner.Exec(ctx, bad); err == nil {
			t.Errorf("%s inserted", bad)
		}
	}
	var after int
	if err := w.owner.QueryRow(ctx, `SELECT count(*) FROM audit.pending`).Scan(&after); err != nil || after != before+1 {
		t.Fatalf("pending rows %d -> %d, want one more", before, after)
	}
	var public bool
	if err := w.owner.QueryRow(ctx, `SELECT has_function_privilege('upmo_app', 'audit.record_keys(text, text, text, jsonb)', 'EXECUTE')`).Scan(&public); err != nil || public {
		t.Fatalf("upmo_app may execute audit.record_keys = %t %v", public, err)
	}
	if _, err := w.owner.Exec(ctx, `DELETE FROM audit.pending`); err != nil {
		t.Fatal(err)
	}

	// A served unwrap: one success row, no key bytes in it.
	code, body := w.call(svcsig.AudienceWorkerGateway, http.MethodPost, keys.PathUnwrap, unwrapGITHUB)
	if code != http.StatusOK {
		t.Fatalf("unwrap = %d %s", code, body)
	}
	var got keys.UnwrapResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	changes, outcomes := w.auditRows("keys.unwrap")
	if len(changes) != 1 || outcomes[0] != "success" ||
		changes[0] != `{"ref": "GITHUB", "kind": "credential", "audience": "worker-gateway"}` {
		t.Fatalf("unwrap audit = %v %v", changes, outcomes)
	}
	if strings.Contains(changes[0], base64.StdEncoding.EncodeToString(got.DataKey)) || strings.Contains(changes[0], allowedKey) {
		t.Fatalf("the audit row holds key material: %s", changes[0])
	}

	// KeysAuditDigest: a ref that misses its pattern is stored as a digest.
	bad := `{"kind":"credential","ref":"not a ref","sandbox_id":""}`
	for range 2 {
		if code, body := w.call(svcsig.AudienceWorkerGateway, http.MethodPost, keys.PathUnwrap, bad); code != http.StatusForbidden || body != "keys: refused" {
			t.Fatalf("unwrap of not a ref = %d %q", code, body)
		}
	}
	changes, outcomes = w.auditRows("keys.unwrap")
	if len(changes) != 3 || outcomes[1] != "denied" || outcomes[2] != "denied" {
		t.Fatalf("audit after two bad refs = %v %v", changes, outcomes)
	}
	digest := regexp.MustCompile(`"ref_digest": "([0-9a-f]{64})"`)
	first, second := digest.FindStringSubmatch(changes[1]), digest.FindStringSubmatch(changes[2])
	if first == nil || second == nil || first[1] != second[1] || strings.Contains(changes[1], "not a ref") || strings.Contains(changes[1], `"ref":`) {
		t.Fatalf("bad ref audit = %s / %s", changes[1], changes[2])
	}

	// A wrap writes its row too.
	if code, _ := w.call(svcsig.AudienceWorkerBrain, http.MethodPost, keys.PathWrap, `{"kind":"credential","ref":"GITHUB"}`); code != http.StatusOK {
		t.Fatalf("wrap = %d", code)
	}
	if changes, outcomes := w.auditRows("keys.wrap"); len(changes) != 1 || outcomes[0] != "success" ||
		changes[0] != `{"ref": "GITHUB", "kind": "credential", "audience": "worker-brain"}` {
		t.Fatalf("wrap audit = %v %v", changes, outcomes)
	}

	// The audit insert fails for real: nothing is unwrapped.
	if _, err := w.owner.Exec(ctx, `ALTER TABLE audit.pending ADD CONSTRAINT test_no_unwrap CHECK (action <> 'keys.unwrap') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	if code, body := w.call(svcsig.AudienceWorkerGateway, http.MethodPost, keys.PathUnwrap, unwrapGITHUB); code != http.StatusForbidden || body != "keys: refused" {
		t.Fatalf("unwrap with the audit refusing = %d %q, want 403 keys: refused", code, body)
	}
}

// Invariant 7: 100 calls per audience per 10 seconds across the key routes.
func TestKeysRateIsPerAudienceAcrossRoutes(t *testing.T) {
	w := startKeyWorker(t)
	w.seed("GITHUB", allowedKey)
	wrapBody := `{"kind":"credential","ref":"GITHUB"}`
	// Gateway's refused wraps count against it like its unwraps do.
	for i := range 50 {
		if code, _ := w.call(svcsig.AudienceWorkerGateway, http.MethodPost, keys.PathWrap, wrapBody); code != http.StatusForbidden {
			t.Fatalf("gateway wrap %d = %d", i+1, code)
		}
	}
	for i := range 50 {
		if code, _ := w.call(svcsig.AudienceWorkerGateway, http.MethodPost, keys.PathUnwrap, unwrapGITHUB); code != http.StatusOK {
			t.Fatalf("unwrap %d = %d", i+1, code)
		}
	}
	code, body := w.call(svcsig.AudienceWorkerGateway, http.MethodPost, keys.PathUnwrap, unwrapGITHUB)
	if code != http.StatusTooManyRequests || body != "keys: refused" {
		t.Fatalf("call 101 = %d %q, want 429 keys: refused", code, body)
	}
	changes, outcomes := w.auditRows("keys.unwrap")
	if len(changes) != 51 || outcomes[50] != "denied" || strings.Contains(changes[50], "data_key") {
		t.Fatalf("audit of call 101 = %d rows, last %v %v", len(changes), changes[len(changes)-1], outcomes[len(outcomes)-1])
	}
	// Ready is not counted, and brain's count is its own.
	if code, _ := w.call(svcsig.AudienceWorkerGateway, http.MethodGet, keys.PathReady, ""); code != http.StatusOK {
		t.Fatalf("ready after the limit = %d", code)
	}
	if code, _ := w.call(svcsig.AudienceWorkerBrain, http.MethodPost, keys.PathWrap, wrapBody); code != http.StatusOK {
		t.Fatalf("brain wrap while gateway is limited = %d", code)
	}
}

// Invariant 8: no cache, and a worker that is gone is one plain error.
func TestKeysClientFailsClosedWhenTheWorkerStops(t *testing.T) {
	w := startKeyWorker(t)
	sealed := w.seed("GITHUB", allowedKey)
	gw := w.client(svcsig.AudienceWorkerGateway)
	ctx := context.Background()
	dataKey, err := gw.UnwrapCredential(ctx, "GITHUB")
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := domain.OpenData(dataKey, "GITHUB", sealed.Ciphertext, sealed.Nonce); err != nil || plain != allowedKey {
		t.Fatalf("open = %q %v", plain, err)
	}
	if err := gw.Ready(ctx); err != nil {
		t.Fatalf("ready = %v", err)
	}
	w.srv.Close()
	for name, call := range map[string]func() error{
		"unwrap": func() error { _, err := gw.UnwrapCredential(ctx, "GITHUB"); return err },
		"ready":  func() error { return gw.Ready(ctx) },
	} {
		if err := call(); !errors.Is(err, keys.ErrUnavailable) || err.Error() != "keys: worker unavailable" {
			t.Errorf("%s with the worker stopped = %v", name, err)
		}
	}
}

// The worker refuses to start without each of its three audience keys.
func TestWorkerNeedsEveryWorkerKey(t *testing.T) {
	cfg, _ := workerKeyConfig(t)
	if _, err := workerVerifyKeys(cfg); err != nil {
		t.Fatal(err)
	}
	for name, drop := range map[string]func(*config.Config){
		"UPMO_WORKER_BRAIN_KEY":    func(c *config.Config) { c.WorkerBrainKey = "" },
		"UPMO_WORKER_GATEWAY_KEY":  func(c *config.Config) { c.WorkerGatewayKey = "" },
		"UPMO_WORKER_SANDBOXD_KEY": func(c *config.Config) { c.WorkerSandboxdKey = "" },
	} {
		c := cfg
		drop(&c)
		if _, err := workerVerifyKeys(c); err == nil || err.Error() != "config: "+name+" is required" {
			t.Errorf("without %s = %v", name, err)
		}
	}
}
