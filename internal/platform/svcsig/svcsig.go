// Package svcsig signs and verifies process-to-process HMAC bearers: a
// short-lived v2 token bound to one audience, method, path, raw query string
// and the SHA-256 of the body, carrying a nonce the server refuses to accept
// twice. Each process holds only the audience keys it signs or verifies.
// Every comparison is constant time.
package svcsig

import (
	"bytes"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Audiences a bearer may name. Each process verifies only its own.
const (
	AudienceGateway    = "gateway"
	AudienceSandboxd   = "sandboxd"
	AudienceMarkitdown = "markitdown"
	AudienceProvision  = "provision"
	// The worker's key routes: each caller signs with its own audience.
	AudienceWorkerBrain    = "worker-brain"
	AudienceWorkerGateway  = "worker-gateway"
	AudienceWorkerSandboxd = "worker-sandboxd"
)

const (
	// KeyPrefix starts every service key, so it cannot collide with a
	// personal API token.
	KeyPrefix = "upmo_svc_"
	// BearerPrefixV2 starts the bearers whose MAC covers audience, method,
	// path, the raw query string and the SHA-256 of the body.
	BearerPrefixV2 = "upmo_s2s_v2." // #nosec G101 -- published prefix, not a credential
)

const (
	keyBytes   = 32
	nonceBytes = 16
	macBytes   = 32
	tolerance  = 30 * time.Second
	version2   = "v2"

	// NonceRetention is how long a consumed v2 nonce must stay recorded
	// before it may be swept: twice the timestamp tolerance, so a swept
	// nonce belongs to a bearer that can no longer verify anyway.
	NonceRetention = 2 * tolerance
)

// ErrStale is a bearer whose MAC verified but whose timestamp is outside the
// tolerance.
var ErrStale = errors.New("svcsig: timestamp outside the tolerance")

var (
	errNoKey      = errors.New("svcsig: no key")
	errKey        = errors.New("svcsig: key is not upmo_svc_ followed by at least 32 bytes of base64")
	errAudience   = errors.New("svcsig: unknown audience")
	errBearer     = errors.New("svcsig: malformed bearer")
	errMismatch   = errors.New("svcsig: signature does not match")
	errMethodPath = errors.New("svcsig: method or path is empty")
)

// ParseKey accepts upmo_svc_ plus unpadded or padded base64, at least 32
// bytes. Only the provision audience still derives from such a master.
func ParseKey(secret string) ([]byte, error) {
	rest, ok := strings.CutPrefix(secret, KeyPrefix)
	if !ok {
		return nil, errKey
	}
	key, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(rest, "="))
	if err != nil || len(key) < keyBytes {
		return nil, errKey
	}
	return key, nil
}

// ParseAudienceKey accepts upmo_svc_<audience>_ plus unpadded or padded
// base64 of exactly 32 bytes and returns those bytes as the HMAC key. A key
// for another audience and an untagged service master are refused.
func ParseAudienceKey(secret, audience string) ([]byte, error) {
	if !knownAudience(audience) {
		return nil, errAudience
	}
	rest, ok := strings.CutPrefix(secret, KeyPrefix)
	if !ok {
		return nil, fmt.Errorf("svcsig: key is not upmo_svc_%s_ followed by base64 of 32 bytes", audience)
	}
	// base64 has no underscore, so an untagged master never splits here.
	tag, text, ok := strings.Cut(rest, "_")
	if !ok {
		return nil, fmt.Errorf("svcsig: a raw service master is not accepted; give the derived %s key", audience)
	}
	if tag != audience {
		return nil, fmt.Errorf("svcsig: key is not a %s key", audience)
	}
	key, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(text, "="))
	if err != nil || len(key) != keyBytes {
		return nil, fmt.Errorf("svcsig: key is not upmo_svc_%s_ followed by base64 of 32 bytes", audience)
	}
	return key, nil
}

// Derive returns the 32-byte HMAC key for one audience.
func Derive(master []byte, audience string) ([]byte, error) {
	if len(master) < keyBytes {
		return nil, errNoKey
	}
	if !knownAudience(audience) {
		return nil, errAudience
	}
	return hkdf.Key(sha256.New, master, []byte("upmo-s2s-v1"), audience, keyBytes)
}

// SignRequest returns a v2 bearer for this audience, method, path, raw query
// string and body. The MAC covers the hex SHA-256 of the body, so nothing
// signed can travel with another query or body; an empty body hashes the
// empty string.
func SignRequest(derived []byte, audience, method, path, rawQuery string, body []byte, now time.Time) (string, error) {
	if len(derived) != keyBytes {
		return "", errNoKey
	}
	if !knownAudience(audience) {
		return "", errAudience
	}
	if method == "" || path == "" {
		return "", errMethodPath
	}
	nonce := make([]byte, nonceBytes)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("svcsig: nonce: %w", err)
	}
	sum := sha256.Sum256(body)
	unix := now.Unix()
	nonceText := base64.RawURLEncoding.EncodeToString(nonce)
	mac := macOverV2(derived, audience, unix, nonceText, method, path, rawQuery, hex.EncodeToString(sum[:]))
	return BearerPrefixV2 + audience + "." + strconv.FormatInt(unix, 10) + "." + nonceText + "." + base64.RawURLEncoding.EncodeToString(mac), nil
}

// VerifyRequestIndex checks a v2 bearer against any derived key and returns
// the index of the matching key and the bearer's nonce, so the caller can
// refuse a replay of a nonce it has already recorded. The MAC must cover the
// raw query string and the SHA-256 of the body. The signature is checked
// before the timestamp, and an invalid signature never yields a nonce. A
// stale bearer still returns the matching key's index with ErrStale, so the
// caller can attribute the refusal to a key.
func VerifyRequestIndex(derived [][]byte, audience, method, path, rawQuery string, body []byte, bearer string, now time.Time) (int, string, error) {
	if err := checkVerify(derived, audience, method, path); err != nil {
		return -1, "", err
	}
	gotAudience, unix, nonceText, gotMAC, err := parseBearer(bearer)
	if err != nil {
		return -1, "", err
	}
	if gotAudience != audience {
		return -1, "", errAudience
	}
	bodySum := sha256.Sum256(body)
	matched := -1
	for i, k := range derived {
		candidate := macOverV2(k, gotAudience, unix, nonceText, method, path, rawQuery, hex.EncodeToString(bodySum[:]))
		if subtle.ConstantTimeCompare(gotMAC, candidate) == 1 && matched == -1 {
			matched = i
		}
	}
	if matched < 0 {
		return -1, "", errMismatch
	}
	if d := now.Sub(time.Unix(unix, 0)); d > tolerance || d < -tolerance {
		return matched, "", ErrStale
	}
	return matched, nonceText, nil
}

// Transport adds a v2 bearer on every client request, over the raw query
// string and the body. An empty key or unknown audience is an error, so a
// caller cannot start a client that will never authenticate.
func Transport(base http.RoundTripper, derived []byte, audience string) (http.RoundTripper, error) {
	if len(derived) != keyBytes {
		return nil, errNoKey
	}
	if !knownAudience(audience) {
		return nil, errAudience
	}
	if base == nil {
		base = http.DefaultTransport
	}
	return roundTripper{base: base, derived: derived, audience: audience}, nil
}

// Handler refuses every path except /healthz and /readyz unless a v2 bearer
// verifies for this audience, method, path, raw query and body, and its
// nonce was not seen before. The body is read up to maxBody bytes before it
// is hashed; a larger body is 413. An empty key list, an unknown audience or
// a cap below one byte is an error, so a caller cannot start a server that
// fails closed by accident.
func Handler(derived [][]byte, audience string, maxBody int64, inner http.Handler) (http.Handler, error) {
	if len(derived) == 0 {
		return nil, errNoKey
	}
	for _, k := range derived {
		if len(k) != keyBytes {
			return nil, errNoKey
		}
	}
	if !knownAudience(audience) {
		return nil, errAudience
	}
	if maxBody < 1 {
		return nil, errors.New("svcsig: body cap must be positive")
	}
	seen := &nonceCache{at: map[string]time.Time{}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			inner.ServeHTTP(w, r)
			return
		}
		secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || secret == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		now := time.Now()
		_, nonce, err := VerifyRequestIndex(derived, audience, r.Method, r.URL.Path, r.URL.RawQuery, raw, secret, now)
		if err != nil || !seen.add(nonce, now) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		inner.ServeHTTP(w, r)
	}), nil
}

// nonceCache remembers the nonces of verified bearers for NonceRetention.
type nonceCache struct {
	mu    sync.Mutex
	at    map[string]time.Time
	swept time.Time
}

// add records nonce and reports whether it was new. Expired entries are
// swept at most once a second, so a busy listener does not scan every call.
func (c *nonceCache) add(nonce string, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if now.Sub(c.swept) >= time.Second {
		for n, at := range c.at {
			if now.Sub(at) >= NonceRetention {
				delete(c.at, n)
			}
		}
		c.swept = now
	}
	if _, dup := c.at[nonce]; dup {
		return false
	}
	c.at[nonce] = now
	return true
}

type roundTripper struct {
	base     http.RoundTripper
	derived  []byte
	audience string
}

func (t roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := requestBody(req)
	if err != nil {
		return nil, err
	}
	token, err := SignRequest(t.derived, t.audience, req.Method, req.URL.Path, req.URL.RawQuery, body, time.Now())
	if err != nil {
		return nil, err
	}
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		clone.Body = io.NopCloser(bytes.NewReader(body))
		clone.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
		clone.ContentLength = int64(len(body))
	}
	return t.base.RoundTrip(clone)
}

// requestBody reads the whole request body, from GetBody when the request
// can replay it. The original body is always closed, as RoundTrip must.
func requestBody(req *http.Request) ([]byte, error) {
	if req.Body == nil || req.Body == http.NoBody {
		return nil, nil
	}
	defer func() { _ = req.Body.Close() }()
	src := req.Body
	if req.GetBody != nil {
		rc, err := req.GetBody()
		if err != nil {
			return nil, fmt.Errorf("svcsig: body: %w", err)
		}
		defer func() { _ = rc.Close() }()
		src = rc
	}
	raw, err := io.ReadAll(src)
	if err != nil {
		return nil, fmt.Errorf("svcsig: body: %w", err)
	}
	return raw, nil
}

// checkVerify holds the checks every verifier runs before touching a bearer.
func checkVerify(derived [][]byte, audience, method, path string) error {
	if len(derived) == 0 {
		return errNoKey
	}
	for _, k := range derived {
		if len(k) != keyBytes {
			return errNoKey
		}
	}
	if !knownAudience(audience) {
		return errAudience
	}
	if method == "" || path == "" {
		return errMethodPath
	}
	return nil
}

func parseBearer(bearer string) (audience string, unix int64, nonce string, mac []byte, err error) {
	// The base64 decoder skips \r and \n, so without this a bearer with a
	// line break in its MAC would verify as a second spelling of the same token.
	if strings.ContainsAny(bearer, "\r\n") {
		return "", 0, "", nil, errBearer
	}
	rest, ok := strings.CutPrefix(bearer, BearerPrefixV2)
	if !ok {
		return "", 0, "", nil, errBearer
	}
	audience, rest, ok = strings.Cut(rest, ".")
	if !ok || !knownAudience(audience) {
		return "", 0, "", nil, errBearer
	}
	unixText, rest, ok := strings.Cut(rest, ".")
	if !ok {
		return "", 0, "", nil, errBearer
	}
	unix, err = strconv.ParseInt(unixText, 10, 64)
	// The MAC covers the canonical number, so "+1700000000" or "01700000000"
	// would otherwise verify too.
	if err != nil || strconv.FormatInt(unix, 10) != unixText {
		return "", 0, "", nil, errBearer
	}
	nonce, macText, ok := strings.Cut(rest, ".")
	if !ok || nonce == "" || macText == "" || strings.Contains(macText, ".") {
		return "", 0, "", nil, errBearer
	}
	got, err := base64.RawURLEncoding.Strict().DecodeString(macText)
	if err != nil || len(got) != macBytes {
		return "", 0, "", nil, errBearer
	}
	rawNonce, err := base64.RawURLEncoding.Strict().DecodeString(nonce)
	if err != nil || len(rawNonce) != nonceBytes {
		return "", 0, "", nil, errBearer
	}
	return audience, unix, nonce, got, nil
}

// macOverV2 covers everything a v2 bearer binds. Each field is length
// prefixed, so no two different field lists can produce the same bytes even
// when a path or query string carries the separator characters other
// schemes rely on.
func macOverV2(derived []byte, audience string, unix int64, nonce, method, path, rawQuery, bodySum string) []byte {
	h := hmac.New(sha256.New, derived)
	for _, field := range []string{
		version2,
		audience,
		strconv.FormatInt(unix, 10),
		nonce,
		method,
		path,
		rawQuery,
		bodySum,
	} {
		h.Write([]byte(strconv.Itoa(len(field))))
		h.Write([]byte{':'})
		h.Write([]byte(field))
	}
	return h.Sum(nil)
}

func knownAudience(audience string) bool {
	switch audience {
	case AudienceGateway, AudienceSandboxd, AudienceMarkitdown, AudienceProvision,
		AudienceWorkerBrain, AudienceWorkerGateway, AudienceWorkerSandboxd:
		return true
	}
	return false
}
