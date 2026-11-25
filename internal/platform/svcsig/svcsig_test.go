package svcsig

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func bytes32(b byte) []byte {
	return bytes.Repeat([]byte{b}, 32)
}

func keyText(audience string, b byte) string {
	return KeyPrefix + audience + "_" + base64.RawStdEncoding.EncodeToString(bytes32(b))
}

func audienceKey(t testing.TB, audience string, b byte) []byte {
	t.Helper()
	k, err := ParseAudienceKey(keyText(audience, b), audience)
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

func TestParseAudienceKey(t *testing.T) {
	raw := bytes32('a')
	for _, secret := range []string{
		KeyPrefix + "gateway_" + base64.RawStdEncoding.EncodeToString(raw),
		KeyPrefix + "gateway_" + base64.StdEncoding.EncodeToString(raw),
	} {
		got, err := ParseAudienceKey(secret, AudienceGateway)
		if err != nil || !bytes.Equal(got, raw) {
			t.Fatalf("%q = %v %v", secret, got, err)
		}
	}
	for _, tc := range []struct{ name, secret, audience, want string }{
		{"other audience", keyText(AudienceSandboxd, 'a'), AudienceGateway, "svcsig: key is not a gateway key"},
		{"raw master", KeyPrefix + base64.RawStdEncoding.EncodeToString(raw), AudienceGateway, "svcsig: a raw service master is not accepted; give the derived gateway key"},
		{"metrics token", "upmo_mtr_" + base64.RawStdEncoding.EncodeToString(raw), AudienceGateway, "svcsig: key is not upmo_svc_gateway_ followed by base64 of 32 bytes"},
		{"short", KeyPrefix + "markitdown_" + base64.RawStdEncoding.EncodeToString(raw[:31]), AudienceMarkitdown, "svcsig: key is not upmo_svc_markitdown_ followed by base64 of 32 bytes"},
		{"long", KeyPrefix + "sandboxd_" + base64.RawStdEncoding.EncodeToString(append(raw, 'b')), AudienceSandboxd, "svcsig: key is not upmo_svc_sandboxd_ followed by base64 of 32 bytes"},
		{"empty", "", AudienceSandboxd, "svcsig: key is not upmo_svc_sandboxd_ followed by base64 of 32 bytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseAudienceKey(tc.secret, tc.audience)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
	if _, err := ParseAudienceKey(keyText("brain", 'a'), "brain"); !errors.Is(err, errAudience) {
		t.Fatalf("unknown audience = %v", err)
	}
}

func TestDeriveUnknownAudience(t *testing.T) {
	if _, err := Derive(bytes32('k'), "brain"); !errors.Is(err, errAudience) {
		t.Fatalf("error = %v", err)
	}
}

func TestSignVerifyRequestV2(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	newKey := audienceKey(t, AudienceProvision, 'n')
	oldKey := audienceKey(t, AudienceProvision, 'o')
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
	v1Tok := "upmo_s2s_v1." + strings.TrimPrefix(token, BearerPrefixV2)

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
		{"personal", keys, http.MethodPost, "/api/v1/provision/members", "", body, "upmo_pat_" + strings.Repeat("A", 43), now, -1, errBearer},
		{"no keys", nil, http.MethodPost, "/api/v1/provision/members", "", body, token, now, -1, errNoKey},
		{"empty method", keys, "", "/api/v1/provision/members", "", body, token, now, -1, errMethodPath},
		{"newline in mac", keys, http.MethodPost, "/api/v1/provision/members", "", body, token[:len(token)-10] + "\n" + token[len(token)-10:], now, -1, errBearer},
		{"carriage return in mac", keys, http.MethodPost, "/api/v1/provision/members", "", body, token[:len(token)-10] + "\r" + token[len(token)-10:], now, -1, errBearer},
		{"plus sign time", keys, http.MethodPost, "/api/v1/provision/members", "", body, strings.Replace(token, ".1700000000.", ".+1700000000.", 1), now, -1, errBearer},
		{"leading zero time", keys, http.MethodPost, "/api/v1/provision/members", "", body, strings.Replace(token, ".1700000000.", ".01700000000.", 1), now, -1, errBearer},
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
}

// The markitdown vector is pinned here and in deploy/markitdown/test_main.py,
// so the Go signer and the Python verifier provably agree byte for byte.
const (
	vectorKey    = "upmo_svc_markitdown_AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA"
	vectorUnix   = 1_700_000_000
	vectorBody   = "hello"
	vectorBearer = "upmo_s2s_v2.markitdown.1700000000.AAECAwQFBgcICQoLDA0ODw.z-sGwNTWPwCw3h5aSL_nEk2K0WV6Mb41LsSZ8bIXzu0" // #nosec G101 -- fixed test vector, not a credential
	// vectorQueryBearer signs the raw query "q=\xe9": one non-ASCII byte, framed as one byte.
	vectorQuery       = "q=\xe9"
	vectorQueryBearer = "upmo_s2s_v2.markitdown.1700000000.AAECAwQFBgcICQoLDA0ODw.Vv5G-eD8auRbs5Z07w7O0v8ONT7GjI87rMLYDSaW9uo" // #nosec G101 -- fixed test vector, not a credential
)

func TestMarkitdownVector(t *testing.T) {
	key, err := ParseAudienceKey(vectorKey, AudienceMarkitdown)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(vectorUnix, 0)
	idx, nonce, err := VerifyRequestIndex([][]byte{key}, AudienceMarkitdown, http.MethodPost, "/convert", "", []byte(vectorBody), vectorBearer, now)
	if err != nil || idx != 0 || nonce != "AAECAwQFBgcICQoLDA0ODw" {
		t.Fatalf("vector = (%d, %q, %v)", idx, nonce, err)
	}
	if _, _, err := VerifyRequestIndex([][]byte{key}, AudienceMarkitdown, http.MethodPost, "/convert", "", []byte("hellO"), vectorBearer, now); !errors.Is(err, errMismatch) {
		t.Fatalf("changed body = %v", err)
	}
	idx, nonce, err = VerifyRequestIndex([][]byte{key}, AudienceMarkitdown, http.MethodPost, "/convert", vectorQuery, []byte(vectorBody), vectorQueryBearer, now)
	if err != nil || idx != 0 || nonce != "AAECAwQFBgcICQoLDA0ODw" {
		t.Fatalf("raw query vector = (%d, %q, %v)", idx, nonce, err)
	}
	if _, _, err := VerifyRequestIndex([][]byte{key}, AudienceMarkitdown, http.MethodPost, "/convert", "q=%C3%A9", []byte(vectorBody), vectorQueryBearer, now); !errors.Is(err, errMismatch) {
		t.Fatalf("re-encoded query = %v", err)
	}
}

// signed returns a v2 bearer for a request the test then sends as is.
func signed(t *testing.T, key []byte, audience, method, path, rawQuery string, body []byte, at time.Time) string {
	t.Helper()
	tok, err := SignRequest(key, audience, method, path, rawQuery, body, at)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// listener serves Handler on a real socket and counts how often the inner
// handler ran, so a refusal provably never reached it.
func listener(t *testing.T, keys [][]byte, audience string, maxBody int64) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var ran atomic.Int64
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ran.Add(1)
		got, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Content-Length", strconv.FormatInt(r.ContentLength, 10))
		_, _ = w.Write(got)
	})
	h, err := Handler(keys, audience, maxBody, inner)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv, &ran
}

func send(t *testing.T, srv *httptest.Server, method, pathQuery, bearer string, body []byte) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+pathQuery, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	got, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(got)
}

const gatewayCap = 8 << 20

func TestHandlerBindsTheBody(t *testing.T) {
	key := audienceKey(t, AudienceGateway, 'g')
	srv, ran := listener(t, [][]byte{key}, AudienceGateway, gatewayCap)
	tok := signed(t, key, AudienceGateway, http.MethodPost, "/chat", "", []byte(`{"n":1}`), time.Now())
	if code, _ := send(t, srv, http.MethodPost, "/chat", tok, []byte(`{"n":2}`)); code != http.StatusUnauthorized {
		t.Fatalf("swapped body = %d, want 401", code)
	}
	if ran.Load() != 0 {
		t.Fatalf("inner ran %d times, want 0", ran.Load())
	}
	// The body it was signed over still goes through, and arrives intact.
	tok = signed(t, key, AudienceGateway, http.MethodPost, "/chat", "", []byte(`{"n":1}`), time.Now())
	if code, got := send(t, srv, http.MethodPost, "/chat", tok, []byte(`{"n":1}`)); code != http.StatusOK || got != `{"n":1}` {
		t.Fatalf("signed body = %d %q, want 200 and the body", code, got)
	}
}

func TestHandlerRefusesAReplay(t *testing.T) {
	key := audienceKey(t, AudienceGateway, 'g')
	srv, ran := listener(t, [][]byte{key}, AudienceGateway, gatewayCap)
	body := []byte(`{"n":1}`)
	tok := signed(t, key, AudienceGateway, http.MethodPost, "/chat", "", body, time.Now())
	code, got := send(t, srv, http.MethodPost, "/chat", tok, body)
	if code != http.StatusOK || got != `{"n":1}` {
		t.Fatalf("first = %d %q, want 200 and the body", code, got)
	}
	if code, _ := send(t, srv, http.MethodPost, "/chat", tok, body); code != http.StatusUnauthorized {
		t.Fatalf("replay = %d, want 401", code)
	}
	if ran.Load() != 1 {
		t.Fatalf("inner ran %d times, want 1", ran.Load())
	}
}

func TestHandlerRefusesAStaleBearer(t *testing.T) {
	key := audienceKey(t, AudienceGateway, 'g')
	srv, ran := listener(t, [][]byte{key}, AudienceGateway, gatewayCap)
	tok := signed(t, key, AudienceGateway, http.MethodGet, "/version", "", nil, time.Now().Add(-61*time.Second))
	if code, _ := send(t, srv, http.MethodGet, "/version", tok, nil); code != http.StatusUnauthorized {
		t.Fatalf("stale = %d, want 401", code)
	}
	if ran.Load() != 0 {
		t.Fatalf("inner ran %d times, want 0", ran.Load())
	}
}

func TestHandlerRefusesAnotherAudience(t *testing.T) {
	gw := audienceKey(t, AudienceGateway, 'g')
	sbx := audienceKey(t, AudienceSandboxd, 's')
	srv, ran := listener(t, [][]byte{gw}, AudienceGateway, gatewayCap)
	for _, tok := range []string{
		signed(t, sbx, AudienceSandboxd, http.MethodGet, "/version", "", nil, time.Now()),
		// The gateway key used under the sandboxd name is refused too.
		signed(t, gw, AudienceSandboxd, http.MethodGet, "/version", "", nil, time.Now()),
	} {
		if code, _ := send(t, srv, http.MethodGet, "/version", tok, nil); code != http.StatusUnauthorized {
			t.Fatalf("sandboxd bearer = %d, want 401", code)
		}
	}
	if ran.Load() != 0 {
		t.Fatalf("inner ran %d times, want 0", ran.Load())
	}
}

func TestHandlerCapsTheBody(t *testing.T) {
	key := audienceKey(t, AudienceGateway, 'g')
	srv, ran := listener(t, [][]byte{key}, AudienceGateway, gatewayCap)
	big := bytes.Repeat([]byte{'a'}, gatewayCap+1)
	tok := signed(t, key, AudienceGateway, http.MethodPost, "/chat", "", big, time.Now())
	if code, _ := send(t, srv, http.MethodPost, "/chat", tok, big); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("over the cap = %d, want 413", code)
	}
	if ran.Load() != 0 {
		t.Fatalf("inner ran %d times, want 0", ran.Load())
	}
	exact := big[:gatewayCap]
	tok = signed(t, key, AudienceGateway, http.MethodPost, "/chat", "", exact, time.Now())
	if code, got := send(t, srv, http.MethodPost, "/chat", tok, exact); code != http.StatusOK || len(got) != gatewayCap {
		t.Fatalf("at the cap = %d with %d bytes, want 200 and %d", code, len(got), gatewayCap)
	}
}

func TestHandlerRefusesV1(t *testing.T) {
	for _, audience := range []string{AudienceGateway, AudienceSandboxd} {
		t.Run(audience, func(t *testing.T) {
			key := audienceKey(t, audience, 'v')
			srv, ran := listener(t, [][]byte{key}, audience, gatewayCap)
			v2 := signed(t, key, audience, http.MethodGet, "/x", "", nil, time.Now())
			v1 := "upmo_s2s_v1." + strings.TrimPrefix(v2, BearerPrefixV2)
			if code, _ := send(t, srv, http.MethodGet, "/x", v1, nil); code != http.StatusUnauthorized {
				t.Fatalf("v1 = %d, want 401", code)
			}
			if ran.Load() != 0 {
				t.Fatalf("inner ran %d times, want 0", ran.Load())
			}
		})
	}
}

func TestHandlerBindsTheQuery(t *testing.T) {
	key := audienceKey(t, AudienceGateway, 'g')
	srv, ran := listener(t, [][]byte{key}, AudienceGateway, gatewayCap)
	tok := signed(t, key, AudienceGateway, http.MethodGet, "/models", "a=1", nil, time.Now())
	if code, _ := send(t, srv, http.MethodGet, "/models?a=2", tok, nil); code != http.StatusUnauthorized {
		t.Fatalf("changed query = %d, want 401", code)
	}
	if ran.Load() != 0 {
		t.Fatalf("inner ran %d times, want 0", ran.Load())
	}
}

func TestHandlerAcceptsThePreviousKey(t *testing.T) {
	current := audienceKey(t, AudienceSandboxd, 'n')
	previous := audienceKey(t, AudienceSandboxd, 'o')
	srv, _ := listener(t, [][]byte{current, previous}, AudienceSandboxd, gatewayCap)
	tok := signed(t, previous, AudienceSandboxd, http.MethodGet, "/x", "", nil, time.Now())
	if code, _ := send(t, srv, http.MethodGet, "/x", tok, nil); code != http.StatusOK {
		t.Fatalf("previous key = %d, want 200", code)
	}
}

func TestTransportSignsTheBody(t *testing.T) {
	key := audienceKey(t, AudienceGateway, 'g')
	srv, ran := listener(t, [][]byte{key}, AudienceGateway, gatewayCap)
	rt, err := Transport(srv.Client().Transport, key, AudienceGateway)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: rt}
	res, err := client.Post(srv.URL+"/upmo.gateway.v1.Gateway/Chat?route=chat", "application/json", strings.NewReader(`{"n":1}`))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || string(got) != `{"n":1}` || res.Header.Get("X-Content-Length") != "7" {
		t.Fatalf("transport = %d %q length %s, want 200 and the body", res.StatusCode, got, res.Header.Get("X-Content-Length"))
	}
	// A body with no GetBody, as a pipe gives, is read and signed too.
	pr, pw := io.Pipe()
	go func() {
		_, _ = pw.Write([]byte(`{"n":2}`))
		_ = pw.Close()
	}()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/chat", pr)
	if err != nil {
		t.Fatal(err)
	}
	res, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || string(got) != `{"n":2}` {
		t.Fatalf("pipe = %d %q, want 200 and the body", res.StatusCode, got)
	}
	if ran.Load() != 2 {
		t.Fatalf("inner ran %d times, want 2", ran.Load())
	}
}

// retryOnce sends a request, then sends it again from GetBody, as a client
// retry does, and keeps the second status.
type retryOnce struct {
	base   http.RoundTripper
	second int
	err    error
}

func (r *retryOnce) RoundTrip(req *http.Request) (*http.Response, error) {
	first, err := r.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if req.GetBody == nil {
		r.err = errors.New("signed request has no GetBody")
		return first, nil
	}
	again := req.Clone(req.Context())
	if again.Body, r.err = req.GetBody(); r.err != nil {
		return first, nil
	}
	res, err := r.base.RoundTrip(again)
	if err != nil {
		r.err = err
		return first, nil
	}
	r.second = res.StatusCode
	_ = res.Body.Close()
	return first, nil
}

func TestTransportRetrySendsTheBodyAndIsAReplay(t *testing.T) {
	key := audienceKey(t, AudienceGateway, 'g')
	h, err := Handler([][]byte{key}, AudienceGateway, gatewayCap, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ := io.ReadAll(r.Body)
		_, _ = w.Write(got)
	}))
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var arrived []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		arrived = append(arrived, string(raw))
		mu.Unlock()
		r.Body = io.NopCloser(bytes.NewReader(raw))
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	retry := &retryOnce{base: srv.Client().Transport}
	rt, err := Transport(retry, key, AudienceGateway)
	if err != nil {
		t.Fatal(err)
	}
	// A body with no GetBody of its own, so only Transport can make the retry possible.
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/chat", io.NopCloser(strings.NewReader(`{"n":1}`)))
	if err != nil {
		t.Fatal(err)
	}
	res, err := (&http.Client{Transport: rt}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if retry.err != nil {
		t.Fatal(retry.err)
	}
	if res.StatusCode != http.StatusOK || string(got) != `{"n":1}` || retry.second != http.StatusUnauthorized {
		t.Fatalf("first = %d %q, second = %d, want 200 with the body, then 401 as a replay", res.StatusCode, got, retry.second)
	}
	if len(arrived) != 2 || arrived[0] != `{"n":1}` || arrived[1] != `{"n":1}` {
		t.Fatalf("bodies that arrived = %q, want the full body twice", arrived)
	}
}

func TestTransportSignsAGetWithoutBody(t *testing.T) {
	key := audienceKey(t, AudienceGateway, 'g')
	srv, ran := listener(t, [][]byte{key}, AudienceGateway, gatewayCap)
	rt, err := Transport(srv.Client().Transport, key, AudienceGateway)
	if err != nil {
		t.Fatal(err)
	}
	res, err := (&http.Client{Transport: rt}).Get(srv.URL + "/version?a=1")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || ran.Load() != 1 {
		t.Fatalf("GET = %d, inner ran %d times, want 200 and 1", res.StatusCode, ran.Load())
	}
}

func TestHandlerSkipsHealth(t *testing.T) {
	key := audienceKey(t, AudienceSandboxd, 's')
	srv, ran := listener(t, [][]byte{key}, AudienceSandboxd, gatewayCap)
	for _, path := range []string{"/healthz", "/readyz"} {
		if code, _ := send(t, srv, http.MethodGet, path, "", nil); code != http.StatusOK {
			t.Fatalf("%s = %d", path, code)
		}
	}
	for _, path := range []string{"/upmo.sandboxd.v1.Runtime/Create", "/healthz/", "/readyz/live"} {
		if code, _ := send(t, srv, http.MethodGet, path, "", nil); code != http.StatusUnauthorized {
			t.Fatalf("%s = %d", path, code)
		}
	}
	if ran.Load() != 2 {
		t.Fatalf("inner ran %d times, want 2", ran.Load())
	}
}

func TestNonceCacheSweeps(t *testing.T) {
	c := &nonceCache{at: map[string]time.Time{}}
	now := time.Unix(1_700_000_000, 0)
	if !c.add("a", now) || c.add("a", now.Add(NonceRetention-time.Second)) {
		t.Fatal("a nonce inside the retention was accepted twice")
	}
	if !c.add("b", now.Add(NonceRetention)) || len(c.at) != 1 {
		t.Fatalf("expired nonce kept: %v", c.at)
	}
}

func TestConstructRequiresKey(t *testing.T) {
	inner := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	gw := audienceKey(t, AudienceGateway, 'g')
	if _, err := Transport(nil, nil, AudienceGateway); !errors.Is(err, errNoKey) {
		t.Fatalf("transport = %v", err)
	}
	if _, err := Handler(nil, AudienceGateway, gatewayCap, inner); !errors.Is(err, errNoKey) {
		t.Fatalf("handler = %v", err)
	}
	if _, err := Handler([][]byte{gw}, "brain", gatewayCap, inner); !errors.Is(err, errAudience) {
		t.Fatalf("audience = %v", err)
	}
	if _, err := Handler([][]byte{gw}, AudienceGateway, 0, inner); err == nil {
		t.Fatal("zero cap accepted")
	}
}

// FuzzVerify never panics and never accepts a bearer whose MAC was not
// computed independently from the same inputs.
func FuzzVerify(f *testing.F) {
	now := time.Unix(1_700_000_000, 0)
	key := audienceKey(f, AudienceGateway, 'k')
	good, err := SignRequest(key, AudienceGateway, http.MethodPost, "/chat", "a=1", []byte("{}"), now)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(good, AudienceGateway, http.MethodPost, "/chat", "a=1", []byte("{}"), now.Unix())
	f.Add(good[:len(good)-10]+"\n"+good[len(good)-10:], AudienceGateway, http.MethodPost, "/chat", "a=1", []byte("{}"), now.Unix())
	f.Add("", AudienceGateway, http.MethodGet, "/", "", []byte(nil), int64(0))
	f.Add("upmo_pat_abc", "sandboxd", "PUT", "/x", "", []byte("x"), int64(-1))
	f.Fuzz(func(t *testing.T, bearer, audience, method, path, rawQuery string, body []byte, unix int64) {
		defer func() {
			if recover() != nil {
				t.Fatal("parser panicked")
			}
		}()
		now := time.Unix(unix, 0)
		_, _, err := VerifyRequestIndex([][]byte{key}, audience, method, path, rawQuery, body, bearer, now)
		want, ok := independentMAC(key, audience, method, path, rawQuery, body, bearer, now)
		if ok && err != nil {
			t.Fatalf("rejected an independently computed signature: %v", err)
		}
		if !ok && err == nil {
			t.Fatalf("verified a forged signature %q (independent %q)", bearer, want)
		}
	})
}

func independentMAC(key []byte, audience, method, path, rawQuery string, body []byte, bearer string, now time.Time) (string, bool) {
	if strings.ContainsAny(bearer, "\r\n") {
		return "", false
	}
	rest, ok := strings.CutPrefix(bearer, BearerPrefixV2)
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
	if err != nil || strconv.FormatInt(unix, 10) != unixText {
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
	sum := sha256.Sum256(body)
	h := hmac.New(sha256.New, key)
	for _, field := range []string{"v2", gotAudience, unixText, nonce, method, path, rawQuery, hex.EncodeToString(sum[:])} {
		h.Write([]byte(strconv.Itoa(len(field)) + ":" + field))
	}
	want := base64.RawURLEncoding.EncodeToString(h.Sum(nil))
	return want, macText == want
}
