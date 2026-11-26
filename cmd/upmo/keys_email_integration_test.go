//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/brainops"
	"github.com/upmo-ai/upmo/internal/platform/keys"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
)

// keyRingOf is a ring over one byte-filled 32-byte key per id, current id
// current.
func keyRingOf(t *testing.T, current string, fill map[string]byte) gateway.Keyring {
	t.Helper()
	text := func(b byte) string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32)) }
	var environ []string
	for id, b := range fill {
		if id != current {
			environ = append(environ, gateway.EnvMasterKeyPrevious+id+"="+text(b))
		}
	}
	ring, err := gateway.KeyringFromEnv(func(name string) (string, bool) {
		switch name {
		case gateway.EnvMasterKey:
			return text(fill[current]), true
		case gateway.EnvMasterKeyID:
			return current, true
		}
		return "", false
	}, environ)
	if err != nil {
		t.Fatal(err)
	}
	return ring
}

// macOf is the index MAC of address under ring's current key.
func macOf(t *testing.T, ring gateway.Keyring, address string) []byte {
	t.Helper()
	_, mac, err := ring.IndexMAC([]byte(address))
	if err != nil {
		t.Fatal(err)
	}
	return mac
}

// checkFiveFields fails unless stored is address sealed for hash in five
// fields under k1: the payload under its own data key for
// identity:email:<hex>, the data key wrapped for v1:email:<hex>.
func checkFiveFields(t *testing.T, ring gateway.Keyring, stored string, hash []byte, address string) {
	t.Helper()
	if parts := strings.Split(stored, ":"); len(parts) != 5 || parts[0] != "k1" {
		t.Fatalf("stored = %q, want five fields under k1", stored)
	}
	if strings.Contains(strings.ToLower(stored), strings.ToLower(address)) || strings.Contains(strings.ToLower(stored), "example") {
		t.Fatalf("stored %q carries the address", stored)
	}
	s, err := domain.ParseSealedText(stored)
	if err != nil {
		t.Fatal(err)
	}
	ref := hex.EncodeToString(hash)
	for name, kindRef := range map[string][2]string{
		"another row's hex": {domain.KindEmail, strings.Repeat("0", 64)},
		"no ref":            {domain.KindEmail, ""},
		"a credential":      {domain.KindCredential, ref},
	} {
		if _, err := ring.Unwrap(kindRef[0], kindRef[1], s.KeyID, s.WrapNonce, s.WrappedKey); !errors.Is(err, domain.ErrUnseal) {
			t.Fatalf("unwrap for %s = %v, want ErrUnseal", name, err)
		}
	}
	dataKey, err := ring.Unwrap(domain.KindEmail, ref, s.KeyID, s.WrapNonce, s.WrappedKey)
	if err != nil || len(dataKey) != 32 {
		t.Fatalf("unwrap for v1:email:<hex> = %d bytes %v", len(dataKey), err)
	}
	if _, err := domain.OpenData(workerMaster, identity.EmailRef(hash), s.Ciphertext, s.Nonce); !errors.Is(err, domain.ErrUnseal) {
		t.Fatalf("open the payload with the master = %v", err)
	}
	if _, err := domain.OpenData(dataKey, "", s.Ciphertext, s.Nonce); !errors.Is(err, domain.ErrUnseal) {
		t.Fatalf("open the payload with no ref = %v", err)
	}
	if got, err := domain.OpenData(dataKey, identity.EmailRef(hash), s.Ciphertext, s.Nonce); err != nil || got != address {
		t.Fatalf("open the payload = %q %v", got, err)
	}
}

// Invariant 1 (4d1): brain's address, sealed through the worker on the
// bootstrap path, and the worker's own are five fields; brain and the
// worker each open both, and neither opens a moved or a three-field value.
func TestKeysEmailIsFiveFields(t *testing.T) {
	w := startKeyWorker(t)
	ctx := context.Background()
	brain := w.client(svcsig.AudienceWorkerBrain)
	brainEmails := brainops.WorkerEmails{Keys: &brain, Ring: w.ring}
	workerEmails := brainops.NewEmailSealer(w.ring)

	passkeys, err := identity.Passkeys(w.owner, brainEmails, identity.Sessions(w.owner, identity.IdleTimeout, identity.MaxAge), "http://localhost:5173", "localhost", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := passkeys.Bootstrap(ctx, "Ada@Example.com", "Ada"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	var stored, keyID string
	var hash []byte
	if err := w.owner.QueryRow(ctx, `SELECT email, email_hash, email_hash_key_id FROM identity.users`).Scan(&stored, &hash, &keyID); err != nil {
		t.Fatal(err)
	}
	if want := macOf(t, w.ring, "ada@example.com"); !bytes.Equal(hash, want) || keyID != "k1" {
		t.Fatalf("email_hash under %q is not the ring's index MAC", keyID)
	}
	checkFiveFields(t, w.ring, stored, hash, "Ada@Example.com")

	workerStored, workerHash, workerKeyID, err := workerEmails.Seal(ctx, " Grace@Example.org ")
	if err != nil || workerKeyID != "k1" || !bytes.Equal(workerHash, macOf(t, w.ring, "grace@example.org")) {
		t.Fatalf("worker seal under %q: %v", workerKeyID, err)
	}
	checkFiveFields(t, w.ring, workerStored, workerHash, "Grace@Example.org")

	old, err := w.ring.Seal(identity.EmailRef(hash), "Ada@Example.com")
	if err != nil {
		t.Fatal(err)
	}
	three := strings.Join([]string{old.KeyID, base64.StdEncoding.EncodeToString(old.Nonce), base64.StdEncoding.EncodeToString(old.Ciphertext)}, ":")
	for name, sealer := range map[string]identity.EmailSealer{"brain": brainEmails, "worker": workerEmails} {
		if got, err := sealer.Open(stored, hash); err != nil || got != "Ada@Example.com" {
			t.Errorf("%s opens brain's address to %q %v", name, got, err)
		}
		if got, err := sealer.Open(workerStored, workerHash); err != nil || got != "Grace@Example.org" {
			t.Errorf("%s opens the worker's address to %q %v", name, got, err)
		}
		for what, value := range map[string][2]string{
			"moved to grace's hash": {stored, string(workerHash)},
			"three fields":          {three, string(hash)},
		} {
			got, err := sealer.Open(value[0], []byte(value[1]))
			if !errors.Is(err, identity.ErrStoredEmail) || err.Error() != "identity: stored email address" || got != "" {
				t.Errorf("%s opens a value %s = %q %v, want ErrStoredEmail", name, what, got, err)
			}
		}
	}
}

// Invariant 2 (4d1): only brain wraps kind email, only for a 64-character
// lowercase hex ref; nobody unwraps it; the audit rows keep the ref only
// as a digest.
func TestKeysEmailWrapRefusals(t *testing.T) {
	w := startKeyWorker(t)
	ref := hex.EncodeToString(macOf(t, w.ring, "ada@example.com"))
	wrap := func(r string) string { return `{"kind":"email","ref":"` + r + `"}` }
	unwrap := `{"kind":"email","ref":"` + ref + `","sandbox_id":""}`
	for _, tc := range []struct{ name, audience, path, body string }{
		{"wrap from gateway", svcsig.AudienceWorkerGateway, keys.PathWrap, wrap(ref)},
		{"wrap from sandboxd", svcsig.AudienceWorkerSandboxd, keys.PathWrap, wrap(ref)},
		{"wrap for GITHUB", svcsig.AudienceWorkerBrain, keys.PathWrap, wrap("GITHUB")},
		{"wrap for 63 hex", svcsig.AudienceWorkerBrain, keys.PathWrap, wrap(ref[:63])},
		{"wrap for 65 hex", svcsig.AudienceWorkerBrain, keys.PathWrap, wrap(ref + "0")},
		{"wrap for upper-case hex", svcsig.AudienceWorkerBrain, keys.PathWrap, wrap(strings.ToUpper(ref))},
		{"unwrap from brain", svcsig.AudienceWorkerBrain, keys.PathUnwrap, unwrap},
		{"unwrap from gateway", svcsig.AudienceWorkerGateway, keys.PathUnwrap, unwrap},
		{"unwrap from sandboxd", svcsig.AudienceWorkerSandboxd, keys.PathUnwrap, unwrap},
	} {
		if code, body := w.call(tc.audience, http.MethodPost, tc.path, tc.body); code != http.StatusForbidden || body != "keys: refused" {
			t.Errorf("%s = %d %q, want 403 keys: refused", tc.name, code, body)
		}
	}
	code, body := w.call(svcsig.AudienceWorkerBrain, http.MethodPost, keys.PathWrap, wrap(ref))
	var made keys.WrapResponse
	if err := json.Unmarshal([]byte(body), &made); code != http.StatusOK || err != nil || made.KeyID != "k1" || len(made.DataKey) != 32 || len(made.WrapNonce) != 12 {
		t.Fatalf("wrap from brain = %d", code)
	}
	if got, err := w.ring.Unwrap(domain.KindEmail, ref, made.KeyID, made.WrapNonce, made.WrappedKey); err != nil || !bytes.Equal(got, made.DataKey) {
		t.Fatalf("the wrap is not for v1:email:<hex>: %v", err)
	}

	digest := regexp.MustCompile(`^\{"kind": "email", "audience": "worker-(brain|gateway|sandboxd)", "ref_digest": "[0-9a-f]{64}"\}$`)
	for action, want := range map[string]int{"keys.wrap": 7, "keys.unwrap": 3} {
		changes, outcomes := w.auditRows(action)
		if len(changes) != want {
			t.Fatalf("%s rows = %d, want %d", action, len(changes), want)
		}
		for i, c := range changes {
			if !digest.MatchString(c) || strings.Contains(c, ref) || strings.Contains(c, strings.ToUpper(ref)) || strings.Contains(c, "GITHUB") {
				t.Errorf("%s row %d (%s) = %s", action, i, outcomes[i], c)
			}
		}
	}
	if changes, outcomes := w.auditRows("keys.wrap"); outcomes[6] != "success" || !strings.Contains(changes[6], `"audience": "worker-brain"`) {
		t.Fatalf("the served wrap's row = %s %s", changes[6], outcomes[6])
	}
}

// Invariant 3 (4d1): index-mac answers brain alone, with the ring's MAC of
// the trimmed, lower-case address; every MAC in ring order with all; and
// refuses a bad body or address.
func TestKeysIndexMAC(t *testing.T) {
	w := startKeyWorker(t)
	ctx := context.Background()
	want := macOf(t, w.ring, "ada@example.com")
	for _, body := range []string{
		`{"address":"Ada@Example.com","all":false}`,
		`{"address":"Ada@Example.com","all":false}`,
		`{"address":"  ADA@example.COM ","all":false}`,
	} {
		code, got := w.call(svcsig.AudienceWorkerBrain, http.MethodPost, keys.PathIndexMAC, body)
		var out keys.IndexMACResponse
		if err := json.Unmarshal([]byte(got), &out); code != http.StatusOK || err != nil || out.KeyID != "k1" || len(out.MACs) != 1 || !bytes.Equal(out.MACs[0], want) {
			t.Fatalf("index-mac %s = %d %s", body, code, got)
		}
		if strings.Contains(got, "@") || strings.Contains(strings.ToLower(got), "ada") {
			t.Fatalf("index-mac answer holds the address: %s", got)
		}
	}
	longest := strings.Repeat("a", 308) + "@example.com"
	if code, _ := w.call(svcsig.AudienceWorkerBrain, http.MethodPost, keys.PathIndexMAC, `{"address":"`+longest+`","all":false}`); len(longest) != 320 || code != http.StatusOK {
		t.Fatalf("index-mac of 320 bytes = %d", code)
	}
	for _, tc := range []struct{ name, audience, body string }{
		{"321 bytes", svcsig.AudienceWorkerBrain, `{"address":"a` + longest + `","all":false}`},
		{"empty", svcsig.AudienceWorkerBrain, `{"address":"","all":false}`},
		{"no address", svcsig.AudienceWorkerBrain, `{"all":true}`},
		{"an extra field", svcsig.AudienceWorkerBrain, `{"address":"ada@example.com","all":false,"key_id":"k1"}`},
		{"two objects", svcsig.AudienceWorkerBrain, `{"address":"ada@example.com"}{"address":"ada@example.com"}`},
		{"from gateway", svcsig.AudienceWorkerGateway, `{"address":"ada@example.com","all":false}`},
		{"from sandboxd", svcsig.AudienceWorkerSandboxd, `{"address":"ada@example.com","all":false}`},
	} {
		if code, body := w.call(tc.audience, http.MethodPost, keys.PathIndexMAC, tc.body); code != http.StatusForbidden || body != "keys: refused" {
			t.Errorf("index-mac %s = %d %q, want 403 keys: refused", tc.name, code, body)
		}
	}
	brain, gw := w.client(svcsig.AudienceWorkerBrain), w.client(svcsig.AudienceWorkerGateway)
	if keyID, macs, err := brain.IndexMAC(ctx, "Ada@Example.com", false); err != nil || keyID != "k1" || len(macs) != 1 || !bytes.Equal(macs[0], want) {
		t.Fatalf("client index-mac = %q %d %v", keyID, len(macs), err)
	}
	if _, macs, err := gw.IndexMAC(ctx, "Ada@Example.com", false); !errors.Is(err, keys.ErrUnavailable) || macs != nil {
		t.Fatalf("client index-mac as gateway = %v", err)
	}

	// After a rotation: k2 current, k0 and k1 previous.
	fill := map[string]byte{"k2": 0x4c, "k1": workerMaster[0], "k0": 0x4a}
	rotated := startKeyWorkerOn(t, keyRingOf(t, "k2", fill))
	rb := rotated.client(svcsig.AudienceWorkerBrain)
	order := [][]byte{}
	for _, id := range []string{"k2", "k0", "k1"} {
		order = append(order, macOf(t, keyRingOf(t, id, map[string]byte{id: fill[id]}), "ada@example.com"))
	}
	keyID, macs, err := rb.IndexMAC(ctx, "Ada@Example.com", true)
	if err != nil || keyID != "k2" || len(macs) != 3 {
		t.Fatalf("index-mac all = %q %d %v", keyID, len(macs), err)
	}
	for i := range order {
		if !bytes.Equal(macs[i], order[i]) {
			t.Fatalf("index-mac all [%d] is not k2, k0, k1 order", i)
		}
	}
	if !bytes.Equal(macs[2], want) {
		t.Fatal("the k1 MAC differs from the k1 ring's")
	}
	if keyID, macs, err := rb.IndexMAC(ctx, "Ada@Example.com", false); err != nil || keyID != "k2" || len(macs) != 1 || !bytes.Equal(macs[0], order[0]) {
		t.Fatalf("index-mac current after a rotation = %q %d %v", keyID, len(macs), err)
	}
}

// Invariant 5 (4d1): every index-mac call writes one keys.index_mac row
// before it answers, with kind email and no ref, address or MAC; a refused
// address is kept only as its digest; a refused audit row refuses the call.
func TestKeysIndexMACAudit(t *testing.T) {
	w := startKeyWorker(t)
	ctx := context.Background()
	mac := macOf(t, w.ring, "ada@example.com")
	long := strings.Repeat("a", 309) + "@example.com"
	for _, call := range []struct {
		audience, body string
		code           int
	}{
		{svcsig.AudienceWorkerBrain, `{"address":"Ada@Example.com","all":false}`, http.StatusOK},
		{svcsig.AudienceWorkerBrain, `{"address":"Ada@Example.com","all":true}`, http.StatusOK},
		{svcsig.AudienceWorkerGateway, `{"address":"Ada@Example.com","all":false}`, http.StatusForbidden},
		{svcsig.AudienceWorkerBrain, `{"address":"` + long + `","all":false}`, http.StatusForbidden},
		{svcsig.AudienceWorkerBrain, `{"address":"Ada@Example.com","extra":1}`, http.StatusForbidden},
	} {
		if code, _ := w.call(call.audience, http.MethodPost, keys.PathIndexMAC, call.body); code != call.code {
			t.Fatalf("index-mac %s = %d, want %d", call.body, code, call.code)
		}
	}
	changes, outcomes := w.auditRows("keys.index_mac")
	want := []string{
		`{"kind": "email", "audience": "worker-brain"}`,
		`{"kind": "email", "audience": "worker-brain"}`,
		`{"kind": "email", "audience": "worker-gateway"}`,
		`{"kind": "email", "audience": "worker-brain", "ref_digest": "` + w.ring.AuditDigest([]byte(long)) + `"}`,
		`{"kind": "email", "audience": "worker-brain"}`,
	}
	wantOutcomes := []string{"success", "success", "denied", "denied", "denied"}
	if len(changes) != len(want) {
		t.Fatalf("keys.index_mac rows = %v %v", changes, outcomes)
	}
	for i := range want {
		if changes[i] != want[i] || outcomes[i] != wantOutcomes[i] {
			t.Errorf("row %d = %s %s, want %s %s", i, changes[i], outcomes[i], want[i], wantOutcomes[i])
		}
		for _, secret := range []string{"ada", "example", hex.EncodeToString(mac), base64.StdEncoding.EncodeToString(mac)} {
			if strings.Contains(strings.ToLower(changes[i]), secret) {
				t.Errorf("row %d holds %q: %s", i, secret, changes[i])
			}
		}
	}

	if _, err := w.owner.Exec(ctx, `ALTER TABLE audit.pending ADD CONSTRAINT test_no_index_mac CHECK (action <> 'keys.index_mac') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	if code, body := w.call(svcsig.AudienceWorkerBrain, http.MethodPost, keys.PathIndexMAC, `{"address":"Ada@Example.com","all":false}`); code != http.StatusForbidden || body != "keys: refused" {
		t.Fatalf("index-mac with the audit refusing = %d %q, want 403 keys: refused", code, body)
	}
}
