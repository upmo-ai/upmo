//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/brainops"
	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/keys"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/provision"
)

// The people of the password-verify suites, as Part B section 4 names them.
const (
	adaID        = "22222222-2222-7222-8222-222222222222"
	bobID        = "44444444-4444-7444-8444-444444444444"
	carolID      = "66666666-6666-7666-8666-666666666666"
	daveID       = "88888888-8888-7888-8888-888888888888"
	goodPassword = "correct horse battery"
	noMatch      = `{"match":false,"rehash":false}`
)

// currentPHC is password hashed with identity's current argon2id settings.
func currentPHC(password string) string {
	return phcOf(password, []byte("salt-of-16-bytes"), 19*1024, 2, 1, 32)
}

// phcOf builds the PHC string identity stores, for any settings.
func phcOf(password string, salt []byte, memory, timeCost uint32, threads uint8, keyLen uint32) string {
	key := argon2.IDKey([]byte(password), salt, timeCost, memory, threads, keyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", memory, timeCost, threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

// person stores one person with a sealed address, and when hash is set,
// a password sealed the way brain seals one: a data key the worker wraps
// for the person, the payload sealed here.
func (w *keyWorker) person(id, address, role, hash string, disabled bool) string {
	w.t.Helper()
	ctx := context.Background()
	stored, emailHash, keyID, err := brainops.NewEmailSealer(w.ring).Seal(address)
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.owner.Exec(ctx, `
		INSERT INTO identity.users (id, email, email_hash, email_hash_key_id, name, role_id, joined_at, disabled_at)
		VALUES ($1, $2, $3, $4, 'Person', (SELECT id FROM identity.roles WHERE key = $5), now(),
		        CASE WHEN $6 THEN now() END)`, id, stored, emailHash, keyID, role, disabled); err != nil {
		w.t.Fatal(err)
	}
	if hash == "" {
		return ""
	}
	user, err := identity.ParseUserID(id)
	if err != nil {
		w.t.Fatal(err)
	}
	brain := w.client(svcsig.AudienceWorkerBrain)
	sealed, err := brainops.WorkerPasswords{Keys: &brain}.Seal(ctx, user, hash)
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.owner.Exec(ctx, `UPDATE identity.users SET password_hash = $2 WHERE id = $1`, id, sealed); err != nil {
		w.t.Fatal(err)
	}
	return sealed
}

// verify posts a password-verify body as brain.
func (w *keyWorker) verify(body string) (int, string) {
	w.t.Helper()
	return w.call(svcsig.AudienceWorkerBrain, http.MethodPost, keys.PathPasswordVerify, body)
}

func verifyBody(email, password string) string {
	raw, _ := json.Marshal(map[string]string{"email": email, "password": password})
	return string(raw)
}

// window reads one address's count and start, as the owner.
func (w *keyWorker) window(address string) (int, time.Time) {
	w.t.Helper()
	_, subject, err := w.ring.IndexMAC([]byte(identity.NormalEmail(address)))
	if err != nil {
		w.t.Fatal(err)
	}
	var attempts int
	var start time.Time
	if err := w.owner.QueryRow(context.Background(), `
		SELECT attempts, window_start FROM identity.password_verify_window WHERE subject = $1`, subject).Scan(&attempts, &start); err != nil {
		w.t.Fatalf("window of %s: %v", address, err)
	}
	return attempts, start
}

// restart stops the worker and serves the same database and keys from a new
// handler, as a worker restart does: every in-memory counter starts over.
func (w *keyWorker) restart() {
	w.t.Helper()
	w.srv.Close()
	cfg := config.Config{
		WorkerBrainKey:    w.keys[svcsig.AudienceWorkerBrain],
		WorkerGatewayKey:  w.keys[svcsig.AudienceWorkerGateway],
		WorkerSandboxdKey: w.keys[svcsig.AudienceWorkerSandboxd],
		PublicURL:         "http://localhost:5173",
		AvatarsDir:        w.t.TempDir(),
	}
	h, err := workerHandler(w.owner, cfg, w.ring, []provision.Key{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		w.t.Fatal(err)
	}
	w.srv = httptest.NewServer(h)
	w.t.Cleanup(w.srv.Close)
}

// Invariant 1: brain's password is five fields under k1, the payload under
// its own data key and the wrap bound to the person; a three-field value is
// a 500 that does not echo it.
func TestKeysPasswordIsFiveFields(t *testing.T) {
	w := startKeyWorker(t)
	stored := w.person(adaID, "Ada@Example.com", "owner", currentPHC(goodPassword), false)
	parts := strings.Split(stored, ":")
	if len(parts) != 5 || parts[0] != "k1" || strings.Contains(stored, "$argon2id$") {
		t.Fatalf("stored = %s, want five fields under k1", stored)
	}
	sealed, err := gateway.ParseSealedText(stored)
	if err != nil {
		t.Fatal(err)
	}
	ada, err := identity.ParseUserID(adaID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gateway.OpenData(workerMaster, identity.PasswordRef(ada), sealed.Ciphertext, sealed.Nonce); err == nil {
		t.Fatal("the payload opened with the master as its key")
	}
	if _, err := w.ring.Unwrap(gateway.KindPassword, bobID, sealed.KeyID, sealed.WrapNonce, sealed.WrappedKey); err == nil {
		t.Fatal("the wrap opened as v1:password:<bob>")
	}
	if code, body := w.verify(verifyBody("Ada@Example.com", goodPassword)); code != http.StatusOK || strings.TrimSpace(body) != `{"match":true,"rehash":false}` {
		t.Fatalf("verify = %d %s", code, body)
	}

	// The old three-field form under the same master never opens.
	old, err := w.ring.Seal(identity.PasswordRef(ada), currentPHC(goodPassword))
	if err != nil {
		t.Fatal(err)
	}
	three := "k1:" + base64.StdEncoding.EncodeToString(old.Nonce) + ":" + base64.StdEncoding.EncodeToString(old.Ciphertext)
	if _, err := w.owner.Exec(context.Background(), `UPDATE identity.users SET password_hash = $2 WHERE id = $1`, adaID, three); err != nil {
		t.Fatal(err)
	}
	code, body := w.verify(verifyBody("Ada@Example.com", goodPassword))
	if code != http.StatusInternalServerError || body != "keys: refused" {
		t.Fatalf("three fields = %d %q, want 500 keys: refused", code, body)
	}
}

// Invariant 2: brain alone wraps a password and checks one; nobody unwraps
// one.
func TestKeysPasswordRefusals(t *testing.T) {
	w := startKeyWorker(t)
	w.person(adaID, "Ada@Example.com", "owner", currentPHC(goodPassword), false)
	unwrap := `{"kind":"password","ref":"` + adaID + `","sandbox_id":""}`
	for _, tc := range []struct{ name, audience, path, body string }{
		{"unwrap from brain", svcsig.AudienceWorkerBrain, keys.PathUnwrap, unwrap},
		{"unwrap from gateway", svcsig.AudienceWorkerGateway, keys.PathUnwrap, unwrap},
		{"unwrap from sandboxd", svcsig.AudienceWorkerSandboxd, keys.PathUnwrap, `{"kind":"password","ref":"` + adaID + `","sandbox_id":"sb-1","proxy_secret":"` + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x61}, 32)) + `"}`},
		{"verify from gateway", svcsig.AudienceWorkerGateway, keys.PathPasswordVerify, verifyBody("Ada@Example.com", goodPassword)},
		{"verify from sandboxd", svcsig.AudienceWorkerSandboxd, keys.PathPasswordVerify, verifyBody("Ada@Example.com", goodPassword)},
		{"wrap from gateway", svcsig.AudienceWorkerGateway, keys.PathWrap, `{"kind":"password","ref":"` + adaID + `"}`},
		{"wrap from sandboxd", svcsig.AudienceWorkerSandboxd, keys.PathWrap, `{"kind":"password","ref":"` + adaID + `"}`},
		{"wrap of a credential ref", svcsig.AudienceWorkerBrain, keys.PathWrap, `{"kind":"password","ref":"GITHUB"}`},
		{"wrap of an upper-case uuid", svcsig.AudienceWorkerBrain, keys.PathWrap, `{"kind":"password","ref":"` + strings.ToUpper("aaaaaaaa-2222-7222-8222-222222222222") + `"}`},
		{"wrap of a credential with a uuid", svcsig.AudienceWorkerBrain, keys.PathWrap, `{"kind":"credential","ref":"` + adaID + `"}`},
	} {
		code, body := w.call(tc.audience, http.MethodPost, tc.path, tc.body)
		if code != http.StatusForbidden || body != "keys: refused" {
			t.Errorf("%s = %d %q, want 403 keys: refused", tc.name, code, body)
		}
	}
	// A password kind never unwraps, even under a ref a credential row has:
	// every unwrap row is a denial written before any key would leave.
	w.seed("GITHUB", allowedKey)
	if code, body := w.call(svcsig.AudienceWorkerGateway, http.MethodPost, keys.PathUnwrap, `{"kind":"password","ref":"GITHUB","sandbox_id":""}`); code != http.StatusForbidden || body != "keys: refused" {
		t.Errorf("unwrap of a password under GITHUB = %d %q", code, body)
	}
	if _, outcomes := w.auditRows("keys.unwrap"); len(outcomes) != 4 || slices.ContainsFunc(outcomes, func(o string) bool { return o != "denied" }) {
		t.Errorf("unwrap audit = %v, want four denials", outcomes)
	}
	code, body := w.call(svcsig.AudienceWorkerBrain, http.MethodPost, keys.PathWrap, `{"kind":"password","ref":"`+adaID+`"}`)
	var made keys.WrapResponse
	if err := json.Unmarshal([]byte(body), &made); code != http.StatusOK || err != nil || len(made.DataKey) != 32 || made.KeyID != "k1" {
		t.Fatalf("password wrap from brain = %d %s", code, body)
	}
	if changes, outcomes := w.auditRows("keys.wrap"); len(changes) == 0 || outcomes[len(outcomes)-1] != "success" ||
		changes[len(changes)-1] != `{"ref": "`+adaID+`", "kind": "password", "audience": "worker-brain"}` {
		t.Fatalf("wrap audit = %v %v", changes, outcomes)
	}
}

// Invariant 3: the sign-in body's answers and bounds. Every path's one hash
// is pinned in identity's TestPasswordVerifySpendsOneHashOnEveryPath.
func TestKeysBrainPasswordVerify(t *testing.T) {
	w := startKeyWorker(t)
	w.person(adaID, "Ada@Example.com", "owner", currentPHC(goodPassword), false)
	weak := phcOf(goodPassword, []byte("salt-is-16-bytes"), 8, 1, 1, 16)
	w.person(bobID, "Bob@Example.com", "member", weak, false)
	for _, tc := range []struct{ name, body, want string }{
		{"match", verifyBody("Ada@Example.com", goodPassword), `{"match":true,"rehash":false}`},
		{"another casing", verifyBody("ADA@example.COM", goodPassword), `{"match":true,"rehash":false}`},
		{"wrong", verifyBody("Ada@Example.com", goodPassword+"!"), noMatch},
		{"unknown", verifyBody("nobody@example.com", goodPassword), noMatch},
		{"old settings", verifyBody("Bob@Example.com", goodPassword), `{"match":true,"rehash":true}`},
	} {
		code, body := w.verify(tc.body)
		if code != http.StatusOK || strings.TrimSpace(body) != tc.want {
			t.Errorf("%s = %d %s, want %s", tc.name, code, body, tc.want)
		}
		if strings.Contains(body, "$argon2id$") || strings.Contains(body, "m=8") || strings.Contains(body, "m=19456") {
			t.Errorf("%s body holds the hash: %s", tc.name, body)
		}
	}
	long := strings.Repeat("p", 1025)
	longEmail := strings.Repeat("a", 309) + "@example.com"
	for _, tc := range []struct{ name, body, digestOf string }{
		{"a 1025-byte password", verifyBody("Ada@Example.com", long), "Ada@Example.com"},
		{"a 321-byte address", verifyBody(longEmail, goodPassword), longEmail},
	} {
		code, body := w.verify(tc.body)
		if code != http.StatusForbidden || body != "keys: refused" {
			t.Errorf("%s = %d %q, want 403 keys: refused", tc.name, code, body)
		}
		changes, outcomes := w.auditRows("keys.password_verify")
		last := map[string]string{}
		if err := json.Unmarshal([]byte(changes[len(changes)-1]), &last); err != nil {
			t.Fatal(err)
		}
		if outcomes[len(outcomes)-1] != "denied" || last["ref_digest"] != w.ring.AuditDigest([]byte(tc.digestOf)) ||
			!regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(last["ref_digest"]) {
			t.Errorf("%s audit = %v %s", tc.name, last, outcomes[len(outcomes)-1])
		}
		if strings.Contains(changes[len(changes)-1], long) || strings.Contains(changes[len(changes)-1], longEmail) {
			t.Errorf("%s audit holds the raw bytes", tc.name)
		}
	}
	if len(longEmail) != 321 {
		t.Fatalf("the long address is %d bytes", len(longEmail))
	}
	// Exactly one proof: both, neither, a null one, or a short hash is 403.
	// A fresh worker: brain's ten checks per 10 s are spent.
	w.restart()
	hash := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	for name, body := range map[string]string{
		"both":           `{"email":"Ada@Example.com","actor_token_hash":"` + hash + `","password":"` + goodPassword + `"}`,
		"neither":        `{"password":"` + goodPassword + `"}`,
		"a null address": `{"email":null,"actor_token_hash":"` + hash + `","password":"` + goodPassword + `"}`,
		"an empty one":   `{"email":"","password":"` + goodPassword + `"}`,
		"no password":    `{"email":"Ada@Example.com"}`,
		"an extra field": `{"email":"Ada@Example.com","password":"` + goodPassword + `","user_id":"` + adaID + `"}`,
		"a short hash":   `{"actor_token_hash":"` + base64.StdEncoding.EncodeToString([]byte{7}) + `","password":"` + goodPassword + `"}`,
	} {
		if code, got := w.verify(body); code != http.StatusForbidden || got != "keys: refused" {
			t.Errorf("%s = %d %q, want 403 keys: refused", name, code, got)
		}
	}
}

// Invariant 5: five checks per address a minute, counted in the database:
// a restart keeps the count, an unknown or disabled address is refused the
// same way, and ten at once leave the count at 6 with at most five checks.
func TestKeysPasswordVerifySurvivesRestart(t *testing.T) {
	w := startKeyWorker(t)
	ctx := context.Background()
	w.person(adaID, "Ada@Example.com", "owner", currentPHC(goodPassword), false)
	w.person(bobID, "Bob@Example.com", "member", currentPHC(goodPassword), false)
	w.person(carolID, "Carol@Example.com", "member", currentPHC(goodPassword), true)
	for i := range 5 {
		if code, body := w.verify(verifyBody("Ada@Example.com", goodPassword+"!")); code != http.StatusOK || strings.TrimSpace(body) != noMatch {
			t.Fatalf("call %d = %d %s", i+1, code, body)
		}
	}
	attempts, fifth := w.window("Ada@Example.com")
	if attempts != 5 {
		t.Fatalf("attempts after five = %d", attempts)
	}

	w.restart()
	code, refused := w.verify(verifyBody("Ada@Example.com", goodPassword))
	if code != http.StatusTooManyRequests || refused != "keys: refused" {
		t.Fatalf("sixth call after a restart = %d %q, want 429 keys: refused", code, refused)
	}
	if attempts, start := w.window("Ada@Example.com"); attempts != 6 || !start.Equal(fifth) {
		t.Fatalf("window after the sixth = %d %s, want 6 %s", attempts, start, fifth)
	}
	if code, body := w.verify(verifyBody("Bob@Example.com", goodPassword+"!")); code != http.StatusOK || strings.TrimSpace(body) != noMatch {
		t.Fatalf("Bob's first = %d %s", code, body)
	}
	if attempts, _ := w.window("Bob@Example.com"); attempts != 1 {
		t.Fatalf("Bob's attempts = %d", attempts)
	}
	// An unknown address counts like a real one, and its refusal is the same.
	for i := range 5 {
		if code, body := w.verify(verifyBody("nobody@example.com", goodPassword)); code != http.StatusOK || strings.TrimSpace(body) != noMatch {
			t.Fatalf("unknown call %d = %d %s", i+1, code, body)
		}
	}
	if code, body := w.verify(verifyBody("nobody@example.com", goodPassword)); code != http.StatusTooManyRequests || body != refused {
		t.Fatalf("unknown sixth = %d %q, want %q", code, body, refused)
	}

	w.restart()
	for i := range 5 {
		if code, body := w.verify(verifyBody("Carol@Example.com", goodPassword)); code != http.StatusOK || strings.TrimSpace(body) != noMatch {
			t.Fatalf("disabled call %d = %d %s", i+1, code, body)
		}
	}
	if code, body := w.verify(verifyBody("Carol@Example.com", goodPassword)); code != http.StatusTooManyRequests || body != refused {
		t.Fatalf("disabled sixth = %d %q, want %q", code, body, refused)
	}
	// A minute on, the window starts again.
	if _, err := w.owner.Exec(ctx, `UPDATE identity.password_verify_window SET window_start = clock_timestamp() - interval '60 seconds' WHERE attempts = 6 AND subject = $1`, w.subject("Ada@Example.com")); err != nil {
		t.Fatal(err)
	}
	if code, body := w.verify(verifyBody("Ada@Example.com", goodPassword)); code != http.StatusOK || strings.TrimSpace(body) != `{"match":true,"rehash":false}` {
		t.Fatalf("after the window = %d %s", code, body)
	}
	if attempts, _ := w.window("Ada@Example.com"); attempts != 1 {
		t.Fatalf("attempts after the window = %d, want 1", attempts)
	}

	// Ten at once for one address: one statement each, so the count stops at
	// 6 and no more than five checks run.
	w.restart()
	w.person(daveID, "Dave@Example.com", "member", currentPHC(goodPassword), false)
	var mu sync.Mutex
	codes := map[int]int{}
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			code, _ := w.verify(verifyBody("Dave@Example.com", goodPassword+"!"))
			mu.Lock()
			codes[code]++
			mu.Unlock()
		})
	}
	wg.Wait()
	if attempts, _ := w.window("Dave@Example.com"); attempts != 6 {
		t.Fatalf("attempts after ten at once = %d, want 6", attempts)
	}
	if codes[http.StatusOK]+codes[http.StatusServiceUnavailable] > 5 || codes[http.StatusTooManyRequests] < 5 || len(codes) > 3 {
		t.Fatalf("ten at once = %v, want at most five checks", codes)
	}
}

// subject is an address's window key.
func (w *keyWorker) subject(address string) []byte {
	w.t.Helper()
	_, mac, err := w.ring.IndexMAC([]byte(identity.NormalEmail(address)))
	if err != nil {
		w.t.Fatal(err)
	}
	return mac
}

// Invariant 6: brain may check ten passwords per 10 s, counted apart from
// the 100 key calls.
func TestKeysPasswordVerifyAudienceLimit(t *testing.T) {
	w := startKeyWorker(t)
	w.person(adaID, "Ada@Example.com", "owner", currentPHC(goodPassword), false)
	for i := range 10 {
		if code, body := w.verify(verifyBody(fmt.Sprintf("person%d@example.com", i), goodPassword)); code != http.StatusOK || strings.TrimSpace(body) != noMatch {
			t.Fatalf("call %d = %d %s", i+1, code, body)
		}
	}
	if code, body := w.verify(verifyBody("person10@example.com", goodPassword)); code != http.StatusTooManyRequests || body != "keys: refused" {
		t.Fatalf("call 11 = %d %q, want 429 keys: refused", code, body)
	}
	if code, body := w.call(svcsig.AudienceWorkerBrain, http.MethodPost, keys.PathWrap, `{"kind":"password","ref":"`+adaID+`"}`); code != http.StatusOK {
		t.Fatalf("a wrap after ten checks = %d %s", code, body)
	}
}

// Invariant 7: one keys.password_verify row per call before the answer,
// success when the check ran and denied when refused before it, holding the
// audience and kind and never the address, the password or a hash. A failed
// audit insert is a refusal.
func TestKeysPasswordVerifyAudit(t *testing.T) {
	w := startKeyWorker(t)
	ctx := context.Background()
	w.person(adaID, "Ada@Example.com", "owner", currentPHC(goodPassword), false)
	if _, err := w.owner.Exec(ctx, `DELETE FROM audit.pending`); err != nil {
		t.Fatal(err)
	}
	if code, _ := w.verify(verifyBody("Ada@Example.com", goodPassword)); code != http.StatusOK {
		t.Fatalf("match = %d", code)
	}
	if code, _ := w.verify(verifyBody("Ada@Example.com", "tide-copper-leaflet")); code != http.StatusOK {
		t.Fatalf("no match = %d", code)
	}
	if code, _ := w.verify(`{"password":"tide-copper-leaflet"}`); code != http.StatusForbidden {
		t.Fatalf("bad body = %d", code)
	}
	for range 3 {
		w.verify(verifyBody("Ada@Example.com", "tide-copper-leaflet"))
	}
	if code, _ := w.verify(verifyBody("Ada@Example.com", "tide-copper-leaflet")); code != http.StatusTooManyRequests {
		t.Fatalf("sixth = %d", code)
	}
	changes, outcomes := w.auditRows("keys.password_verify")
	want := []string{"success", "success", "denied", "success", "success", "success", "denied"}
	if strings.Join(outcomes, ",") != strings.Join(want, ",") {
		t.Fatalf("outcomes = %v, want %v", outcomes, want)
	}
	for i, c := range changes {
		got := map[string]string{}
		if err := json.Unmarshal([]byte(c), &got); err != nil {
			t.Fatal(err)
		}
		if got["audience"] != "worker-brain" || got["kind"] != "password" || (len(got) != 2 && i != 2) {
			t.Errorf("row %d changes = %v", i+1, got)
		}
	}

	var ocsf bytes.Buffer
	if _, err := audit.SealOnce(ctx, w.owner, &ocsf); err != nil {
		t.Fatal(err)
	}
	var sealed int
	if err := w.owner.QueryRow(ctx, `
		SELECT count(*) FROM audit.events
		WHERE action = 'keys.password_verify'
		  AND changes->'values'->'audience'->>'to' = 'worker-brain'
		  AND changes->'values'->'kind'->>'to' = 'password'`).Scan(&sealed); err != nil || sealed != len(want) {
		t.Fatalf("sealed rows with audience and kind = %d %v, want %d", sealed, err, len(want))
	}
	var dump string
	if err := w.owner.QueryRow(ctx, `
		SELECT coalesce(string_agg(e::text, ' '), '') FROM audit.events AS e WHERE action = 'keys.password_verify'`).Scan(&dump); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"Ada@Example.com", "ada@example.com", goodPassword, "tide-copper-leaflet", "$argon2id$"} {
		if strings.Contains(dump, secret) || strings.Contains(ocsf.String(), secret) {
			t.Errorf("an audit row holds %q", secret)
		}
	}

	// The audit insert fails for real: no answer leaves.
	if _, err := w.owner.Exec(ctx, `ALTER TABLE audit.pending ADD CONSTRAINT test_no_verify CHECK (action <> 'keys.password_verify') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	w.restart()
	if code, body := w.verify(verifyBody("Bob@Example.com", goodPassword)); code != http.StatusForbidden || body != "keys: refused" {
		t.Fatalf("verify with the audit refusing = %d %q, want 403 keys: refused", code, body)
	}
}
