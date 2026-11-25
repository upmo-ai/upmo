package runjwt

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func keys(t *testing.T) Keys {
	t.Helper()
	k, err := ParseKeys(KeyPrefix+base64.RawStdEncoding.EncodeToString(bytes32('k')), "")
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func bytes32(b byte) []byte {
	out := make([]byte, 32)
	for i := range out {
		out[i] = b
	}
	return out
}

func TestSignVerify(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	k := keys(t)
	token, err := k.Sign(AudienceConnectors, "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222", now)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(token, ".") != 2 {
		t.Fatalf("token = %s", token)
	}
	got, err := k.Verify(AudienceConnectors, token, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.User != "11111111-1111-1111-1111-111111111111" || got.Run != "22222222-2222-2222-2222-222222222222" {
		t.Fatalf("claims = %+v", got)
	}
}

func TestVerifyRejects(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	k := keys(t)
	token, err := k.Sign(AudienceConnectors, "u", "r", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.Verify("sandboxd", token, now); !errors.Is(err, errClaims) {
		t.Fatalf("audience = %v", err)
	}
	if _, err := k.Verify(AudienceConnectors, token, now.Add(lifetime+time.Minute)); !errors.Is(err, errStale) {
		t.Fatalf("stale = %v", err)
	}
	if _, err := k.Verify(AudienceConnectors, "upmo_pat_"+strings.Repeat("A", 43), now); !errors.Is(err, errToken) {
		t.Fatalf("personal = %v", err)
	}
	if _, err := k.Verify(AudienceConnectors, "upmo_s2s_v1.gateway.1.a.b", now); !errors.Is(err, errToken) {
		t.Fatalf("service = %v", err)
	}
	if _, err := New(nil); !errors.Is(err, ErrNoKey) {
		t.Fatalf("empty = %v", err)
	}
	for _, brk := range []string{"\n", "\r"} {
		split := token[:len(token)-10] + brk + token[len(token)-10:]
		if _, err := k.Verify(AudienceConnectors, split, now); !errors.Is(err, errToken) {
			t.Fatalf("line break %q in signature = %v", brk, err)
		}
	}
}

func TestRotation(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	old, cur := bytes32('o'), bytes32('n')
	oldKeys, err := New([][]byte{old})
	if err != nil {
		t.Fatal(err)
	}
	both, err := New([][]byte{cur, old})
	if err != nil {
		t.Fatal(err)
	}
	token, err := oldKeys.Sign(AudienceConnectors, "u", "r", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := both.Verify(AudienceConnectors, token, now); err != nil {
		t.Fatalf("previous refused: %v", err)
	}
	currentOnly, err := New([][]byte{cur})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := currentOnly.Verify(AudienceConnectors, token, now); !errors.Is(err, errMismatch) {
		t.Fatalf("dropped previous = %v", err)
	}
}

func FuzzVerify(f *testing.F) {
	now := time.Unix(1_700_000_000, 0)
	k := mustKeys()
	good, err := k.Sign(AudienceConnectors, "u", "r", now)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(good, AudienceConnectors, now.Unix())
	f.Add("", "sandboxd", int64(0))
	f.Add("upmo_pat_abc", AudienceConnectors, int64(-1))
	f.Fuzz(func(t *testing.T, token, audience string, unix int64) {
		defer func() {
			if recover() != nil {
				t.Fatal("parser panicked")
			}
		}()
		now := time.Unix(unix, 0)
		_, err := k.Verify(audience, token, now)
		wantOK := independentMAC(k.current, audience, token, now)
		if wantOK && err != nil {
			t.Fatalf("rejected an independently computed signature: %v", err)
		}
		if !wantOK && err == nil {
			t.Fatalf("verified a forged token %q", token)
		}
	})
}

func mustKeys() Keys {
	k, err := New([][]byte{bytes32('k')})
	if err != nil {
		panic(err)
	}
	return k
}

func independentMAC(key []byte, audience, token string, now time.Time) bool {
	if strings.HasPrefix(token, "upmo_pat_") || strings.HasPrefix(token, "upmo_s2s_") {
		return false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != headerB64 {
		return false
	}
	got, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	// Only the canonical spelling of the signature counts.
	if err != nil || len(got) != sha256.Size || base64.RawURLEncoding.EncodeToString(got) != parts[2] {
		return false
	}
	body := parts[0] + "." + parts[1]
	h := hmac.New(sha256.New, key)
	h.Write([]byte(body))
	if !hmac.Equal(got, h.Sum(nil)) {
		return false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil {
		return false
	}
	if !strings.Contains(string(raw), `"iss":"upmo"`) || !strings.Contains(string(raw), `"aud":"`+audience+`"`) {
		return false
	}
	if audience == "" || strings.ContainsAny(audience, " \t\n.") {
		return false
	}
	var payload struct {
		Sub string `json:"sub"`
		Run string `json:"run"`
		Nbf int64  `json:"nbf"`
		Exp int64  `json:"exp"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || payload.Sub == "" || payload.Run == "" {
		return false
	}
	if now.Add(leeway).Before(time.Unix(payload.Nbf, 0)) || now.Add(-leeway).After(time.Unix(payload.Exp, 0)) {
		return false
	}
	return true
}

func TestParseKey(t *testing.T) {
	raw := bytes32('a')
	for _, secret := range []string{KeyPrefix + base64.RawStdEncoding.EncodeToString(raw), KeyPrefix + base64.StdEncoding.EncodeToString(raw)} {
		got, err := ParseKey(secret)
		if err != nil || string(got) != string(raw) {
			t.Fatalf("%q = %v %v", secret, got, err)
		}
	}
	for _, secret := range []string{
		"",
		"upmo_svc_" + base64.RawStdEncoding.EncodeToString(raw),
		KeyPrefix + base64.RawStdEncoding.EncodeToString(raw[:31]),
		KeyPrefix + base64.RawStdEncoding.EncodeToString(append(raw, 'b')),
	} {
		if _, err := ParseKey(secret); err == nil || err.Error() != "runjwt: key is not upmo_rnk_ followed by base64 of 32 bytes" {
			t.Fatalf("%q error = %v", secret, err)
		}
	}
	if _, err := ParseKeys("", ""); !errors.Is(err, ErrNoKey) {
		t.Fatalf("no key = %v", err)
	}
}
