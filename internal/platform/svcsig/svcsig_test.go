package svcsig

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func master(t *testing.T) []byte {
	t.Helper()
	key, err := ParseKey(KeyPrefix + base64.RawStdEncoding.EncodeToString(bytes32('k')))
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func bytes32(b byte) []byte {
	out := make([]byte, 32)
	for i := range out {
		out[i] = b
	}
	return out
}

func derived(t *testing.T, audience string) []byte {
	t.Helper()
	k, err := Derive(master(t), audience)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestParseKey(t *testing.T) {
	raw := bytes32('a')
	ok := KeyPrefix + base64.RawStdEncoding.EncodeToString(raw)
	padded := KeyPrefix + base64.StdEncoding.EncodeToString(raw)
	got, err := ParseKey(ok)
	if err != nil || string(got) != string(raw) {
		t.Fatalf("raw = %v %v", got, err)
	}
	got, err = ParseKey(padded)
	if err != nil || string(got) != string(raw) {
		t.Fatalf("padded = %v %v", got, err)
	}
	for _, secret := range []string{"", "upmo_pat_abc", KeyPrefix, KeyPrefix + "!!!!", KeyPrefix + base64.RawStdEncoding.EncodeToString(make([]byte, 31))} {
		if _, err := ParseKey(secret); !errors.Is(err, errKey) {
			t.Fatalf("%q error = %v", secret, err)
		}
	}
}

func TestSignVerify(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cases := []struct {
		audience, method, path string
	}{
		{AudienceGateway, http.MethodPost, "/upmo.gateway.v1.Gateway/Chat"},
		{AudienceGateway, http.MethodGet, "/upmo.gateway.v1.Gateway/Models"},
		{AudienceSandboxd, http.MethodPost, "/upmo.sandboxd.v1.Runtime/Create"},
		{AudienceSandboxd, http.MethodGet, "/upmo.sandboxd.v1.Runtime/Wait"},
		{AudienceProvision, http.MethodPost, "/api/v1/provision/members"},
		{AudienceProvision, http.MethodGet, "/api/v1/provision/status"},
	}
	for _, tc := range cases {
		t.Run(tc.audience+" "+tc.method+" "+tc.path, func(t *testing.T) {
			key := derived(t, tc.audience)
			token, err := Sign(key, tc.audience, tc.method, tc.path, now)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(token, BearerPrefix+tc.audience+".") {
				t.Fatalf("token = %s", token)
			}
			if err := Verify([][]byte{key}, tc.audience, tc.method, tc.path, token, now); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestVerifyRejects(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	gw := derived(t, AudienceGateway)
	sbx := derived(t, AudienceSandboxd)
	token, err := Sign(gw, AudienceGateway, http.MethodPost, "/chat", now)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		keys     [][]byte
		audience string
		method   string
		path     string
		bearer   string
		now      time.Time
		want     error
	}{
		{"audience", [][]byte{sbx}, AudienceSandboxd, http.MethodPost, "/chat", token, now, errAudience},
		{"method", [][]byte{gw}, AudienceGateway, http.MethodGet, "/chat", token, now, errMismatch},
		{"path", [][]byte{gw}, AudienceGateway, http.MethodPost, "/embed", token, now, errMismatch},
		{"stale", [][]byte{gw}, AudienceGateway, http.MethodPost, "/chat", token, now.Add(31 * time.Second), ErrStale},
		{"future", [][]byte{gw}, AudienceGateway, http.MethodPost, "/chat", token, now.Add(-31 * time.Second), ErrStale},
		{"personal", [][]byte{gw}, AudienceGateway, http.MethodPost, "/chat", "upmo_pat_" + strings.Repeat("A", 43), now, errBearer},
		{"truncated", [][]byte{gw}, AudienceGateway, http.MethodPost, "/chat", token[:len(token)-1], now, errBearer},
		{"empty", [][]byte{gw}, AudienceGateway, http.MethodPost, "/chat", "", now, errBearer},
		{"no keys", nil, AudienceGateway, http.MethodPost, "/chat", token, now, errNoKey},
		{"empty method", [][]byte{gw}, AudienceGateway, "", "/chat", token, now, errMethodPath},
		{"newline in mac", [][]byte{gw}, AudienceGateway, http.MethodPost, "/chat", token[:len(token)-10] + "\n" + token[len(token)-10:], now, errBearer},
		{"carriage return in mac", [][]byte{gw}, AudienceGateway, http.MethodPost, "/chat", token[:len(token)-10] + "\r" + token[len(token)-10:], now, errBearer},
		{"plus sign time", [][]byte{gw}, AudienceGateway, http.MethodPost, "/chat", strings.Replace(token, ".1700000000.", ".+1700000000.", 1), now, errBearer},
		{"leading zero time", [][]byte{gw}, AudienceGateway, http.MethodPost, "/chat", strings.Replace(token, ".1700000000.", ".01700000000.", 1), now, errBearer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Verify(tc.keys, tc.audience, tc.method, tc.path, tc.bearer, tc.now)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestRotation(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	oldMaster := bytes32('o')
	newMaster := bytes32('n')
	oldKey, err := Derive(oldMaster, AudienceGateway)
	if err != nil {
		t.Fatal(err)
	}
	newKey, err := Derive(newMaster, AudienceGateway)
	if err != nil {
		t.Fatal(err)
	}
	oldToken, err := Sign(oldKey, AudienceGateway, http.MethodPost, "/chat", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify([][]byte{newKey, oldKey}, AudienceGateway, http.MethodPost, "/chat", oldToken, now); err != nil {
		t.Fatalf("previous key refused: %v", err)
	}
	newToken, err := Sign(newKey, AudienceGateway, http.MethodPost, "/chat", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify([][]byte{newKey}, AudienceGateway, http.MethodPost, "/chat", newToken, now); err != nil {
		t.Fatal(err)
	}
	if err := Verify([][]byte{newKey}, AudienceGateway, http.MethodPost, "/chat", oldToken, now); !errors.Is(err, errMismatch) {
		t.Fatalf("dropped previous still verified: %v", err)
	}
}

func TestHTTPRoundTrip(t *testing.T) {
	key := derived(t, AudienceGateway)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	h, err := Handler([][]byte{key}, AudienceGateway, inner)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	rt, err := Transport(srv.Client().Transport, key, AudienceGateway)
	if err != nil {
		t.Fatal(err)
	}
	client := srv.Client()
	client.Transport = rt
	res, err := client.Post(srv.URL+"/upmo.gateway.v1.Gateway/Chat", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", res.StatusCode)
	}
}

func TestHandlerSkipsHealth(t *testing.T) {
	key := derived(t, AudienceSandboxd)
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h, err := Handler([][]byte{key}, AudienceSandboxd, inner)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/healthz", "/readyz"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d", path, rec.Code)
		}
	}
	for _, path := range []string{"/upmo.sandboxd.v1.Runtime/Create", "/healthz/", "/readyz/live"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s = %d", path, rec.Code)
		}
	}
}

func TestConstructRequiresKey(t *testing.T) {
	inner := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	if _, err := Transport(nil, nil, AudienceGateway); !errors.Is(err, errNoKey) {
		t.Fatalf("transport = %v", err)
	}
	if _, err := Handler(nil, AudienceGateway, inner); !errors.Is(err, errNoKey) {
		t.Fatalf("handler = %v", err)
	}
	if _, err := Handler([][]byte{derived(t, AudienceGateway)}, "brain", inner); !errors.Is(err, errAudience) {
		t.Fatalf("audience = %v", err)
	}
}

func TestDeriveUnknownAudience(t *testing.T) {
	if _, err := Derive(master(t), "brain"); !errors.Is(err, errAudience) {
		t.Fatalf("error = %v", err)
	}
}

// FuzzVerify never panics and never accepts a bearer whose MAC was not
// computed independently from the same inputs.
func FuzzVerify(f *testing.F) {
	now := time.Unix(1_700_000_000, 0)
	key := mustDerive(bytes32('k'), AudienceGateway)
	good, err := Sign(key, AudienceGateway, http.MethodPost, "/chat", now)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(good, AudienceGateway, http.MethodPost, "/chat", now.Unix())
	// CI found this one: a line break inside the MAC.
	f.Add(good[:len(good)-10]+"\n"+good[len(good)-10:], AudienceGateway, http.MethodPost, "/chat", now.Unix())
	f.Add("", AudienceGateway, http.MethodGet, "/", int64(0))
	f.Add("upmo_pat_abc", "sandboxd", "PUT", "/x", int64(-1))
	f.Fuzz(func(t *testing.T, bearer, audience, method, path string, unix int64) {
		defer func() {
			if recover() != nil {
				t.Fatal("parser panicked")
			}
		}()
		now := time.Unix(unix, 0)
		err := Verify([][]byte{key}, audience, method, path, bearer, now)
		want, ok := independentMAC(key, audience, method, path, bearer, now)
		if ok && err != nil {
			t.Fatalf("rejected an independently computed signature: %v", err)
		}
		if !ok && err == nil {
			t.Fatalf("verified a forged signature %q (independent %q)", bearer, want)
		}
	})
}

func mustDerive(master []byte, audience string) []byte {
	k, err := Derive(master, audience)
	if err != nil {
		panic(err)
	}
	return k
}

func independentMAC(key []byte, audience, method, path, bearer string, now time.Time) (string, bool) {
	rest, ok := strings.CutPrefix(bearer, BearerPrefix)
	if !ok {
		return "", false
	}
	parts := strings.Split(rest, ".")
	if len(parts) != 4 {
		return "", false
	}
	gotAudience, unixText, nonce, macText := parts[0], parts[1], parts[2], parts[3]
	if gotAudience != audience || method == "" || path == "" || !knownAudience(audience) {
		return "", false
	}
	unix, err := strconv.ParseInt(unixText, 10, 64)
	if err != nil {
		return "", false
	}
	if d := now.Sub(time.Unix(unix, 0)); d > tolerance || d < -tolerance {
		return "", false
	}
	rawNonce, err := base64.RawURLEncoding.Strict().DecodeString(nonce)
	if err != nil || len(rawNonce) != nonceBytes {
		return "", false
	}
	gotMAC, err := base64.RawURLEncoding.Strict().DecodeString(macText)
	if err != nil || len(gotMAC) != macBytes {
		return "", false
	}
	h := hmac.New(sha256.New, key)
	h.Write([]byte(version + "." + gotAudience + "." + unixText + "." + nonce + "." + method + "." + path))
	want := base64.RawURLEncoding.EncodeToString(h.Sum(nil))
	return want, macText == want
}

func TestVerifyIndexProvisionRotation(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	k1 := mustDerive(bytes32('1'), AudienceProvision)
	k2 := mustDerive(bytes32('2'), AudienceProvision)
	keys := [][]byte{k1, k2}

	tok1, err := Sign(k1, AudienceProvision, http.MethodPost, "/api/v1/provision/members", now)
	if err != nil {
		t.Fatal(err)
	}
	tok2, err := Sign(k2, AudienceProvision, http.MethodPut, "/api/v1/provision/quotas", now)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := VerifyIndex(keys, AudienceProvision, http.MethodPost, "/api/v1/provision/members", tok1, now)
	if err != nil || idx != 0 {
		t.Fatalf("VerifyIndex(k1) = (%d, %v), want (0, nil)", idx, err)
	}
	idx, err = VerifyIndex(keys, AudienceProvision, http.MethodPut, "/api/v1/provision/quotas", tok2, now)
	if err != nil || idx != 1 {
		t.Fatalf("VerifyIndex(k2) = (%d, %v), want (1, nil)", idx, err)
	}
}

func TestSignVerifyRequestV2(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	newKey := mustDerive(bytes32('n'), AudienceProvision)
	oldKey := mustDerive(bytes32('o'), AudienceProvision)
	keys := [][]byte{newKey, oldKey}
	body := []byte(`{"external_id":"m-1","email":"ada@example.com"}`)

	sign := func(key []byte, method, path, rawQuery string, b []byte, at time.Time) string {
		t.Helper()
		tok, err := SignRequest(key, AudienceProvision, method, path, rawQuery, b, at)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}

	token := sign(newKey, http.MethodPost, "/api/v1/provision/members", "", body, now)
	emptyTok := sign(newKey, http.MethodGet, "/api/v1/provision/status", "", nil, now)
	queryTok := sign(newKey, http.MethodGet, "/api/v1/provision/usage", "month=2026-11", nil, now)
	oldTok := sign(oldKey, http.MethodPost, "/api/v1/provision/members", "", body, now)
	v1Tok, err := Sign(newKey, AudienceProvision, http.MethodPost, "/api/v1/provision/members", now)
	if err != nil {
		t.Fatal(err)
	}

	// The bearer's third segment is the nonce the verifier must hand back,
	// so the caller records exactly what was authenticated.
	wantNonce := strings.Split(strings.TrimPrefix(token, BearerPrefixV2), ".")[2]

	cases := []struct {
		name     string
		keys     [][]byte
		method   string
		path     string
		rawQuery string
		body     []byte
		bearer   string
		now      time.Time
		wantIdx  int
		wantErr  error
	}{
		{"body", keys, http.MethodPost, "/api/v1/provision/members", "", body, token, now, 0, nil},
		{"empty body", keys, http.MethodGet, "/api/v1/provision/status", "", nil, emptyTok, now, 0, nil},
		{"query", keys, http.MethodGet, "/api/v1/provision/usage", "month=2026-11", nil, queryTok, now, 0, nil},
		{"body changed", keys, http.MethodPost, "/api/v1/provision/members", "", []byte(`{"external_id":"m-2"}`), token, now, -1, errMismatch},
		{"body added to empty", keys, http.MethodGet, "/api/v1/provision/status", "", []byte("{}"), emptyTok, now, -1, errMismatch},
		{"query changed", keys, http.MethodGet, "/api/v1/provision/usage", "month=2026-10", nil, queryTok, now, -1, errMismatch},
		{"query dropped", keys, http.MethodGet, "/api/v1/provision/usage", "", nil, queryTok, now, -1, errMismatch},
		{"method changed", keys, http.MethodPut, "/api/v1/provision/members", "", body, token, now, -1, errMismatch},
		{"path changed", keys, http.MethodPost, "/api/v1/provision/teams", "", body, token, now, -1, errMismatch},
		{"rotation second key", keys, http.MethodPost, "/api/v1/provision/members", "", body, oldTok, now, 1, nil},
		{"rotation dropped key", [][]byte{newKey}, http.MethodPost, "/api/v1/provision/members", "", body, oldTok, now, -1, errMismatch},
		{"stale", keys, http.MethodPost, "/api/v1/provision/members", "", body, token, now.Add(31 * time.Second), 0, ErrStale},
		{"future", keys, http.MethodPost, "/api/v1/provision/members", "", body, token, now.Add(-31 * time.Second), 0, ErrStale},
		{"v1 bearer refused", keys, http.MethodPost, "/api/v1/provision/members", "", body, v1Tok, now, -1, errBearer},
		{"truncated", keys, http.MethodPost, "/api/v1/provision/members", "", body, token[:len(token)-1], now, -1, errBearer},
		{"empty bearer", keys, http.MethodPost, "/api/v1/provision/members", "", body, "", now, -1, errBearer},
		{"no keys", nil, http.MethodPost, "/api/v1/provision/members", "", body, token, now, -1, errNoKey},
		{"empty method", keys, "", "/api/v1/provision/members", "", body, token, now, -1, errMethodPath},
		{"newline in mac", keys, http.MethodPost, "/api/v1/provision/members", "", body, token[:len(token)-10] + "\n" + token[len(token)-10:], now, -1, errBearer},
		{"plus sign time", keys, http.MethodPost, "/api/v1/provision/members", "", body, strings.Replace(token, ".1700000000.", ".+1700000000.", 1), now, -1, errBearer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idx, nonce, err := VerifyRequestIndex(tc.keys, AudienceProvision, tc.method, tc.path, tc.rawQuery, tc.body, tc.bearer, tc.now)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if err != nil {
				if idx != tc.wantIdx || nonce != "" {
					t.Fatalf("failure returned (%d, %q), want (%d, \"\")", idx, nonce, tc.wantIdx)
				}
				return
			}
			if idx != tc.wantIdx {
				t.Fatalf("index = %d, want %d", idx, tc.wantIdx)
			}
			if tc.name == "body" && nonce != wantNonce {
				t.Fatalf("nonce = %q, want the bearer's nonce %q", nonce, wantNonce)
			}
		})
	}

	// The v1 verifier refuses a v2 bearer just as firmly.
	if _, err := VerifyIndex(keys, AudienceProvision, http.MethodPost, "/api/v1/provision/members", token, now); !errors.Is(err, errBearer) {
		t.Fatalf("v1 verifier accepted a v2 bearer: %v", err)
	}
}
