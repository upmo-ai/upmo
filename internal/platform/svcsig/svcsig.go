// Package svcsig signs and verifies process-to-process HMAC bearers:
// a short-lived token derived from the install master key,
// bound to one audience, method and path. Every comparison is constant time.
package svcsig

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Audiences a bearer may name. Each process verifies only its own.
const (
	AudienceGateway   = "gateway"
	AudienceSandboxd  = "sandboxd"
	AudienceProvision = "provision"
)

// Prefix is the env and bearer prefix that must not collide with a
// personal API token.
const (
	KeyPrefix    = "upmo_svc_"
	BearerPrefix = "upmo_s2s_v1." // #nosec G101 -- published prefix, not a credential
)

const (
	keyBytes   = 32
	nonceBytes = 16
	macBytes   = 32
	tolerance  = 30 * time.Second
	version    = "v1"
)

var (
	errNoKey      = errors.New("svcsig: no key")
	errKey        = errors.New("svcsig: key is not upmo_svc_ followed by at least 32 bytes of base64")
	errAudience   = errors.New("svcsig: unknown audience")
	errBearer     = errors.New("svcsig: malformed bearer")
	errMismatch   = errors.New("svcsig: signature does not match")
	errStale      = errors.New("svcsig: timestamp outside the tolerance")
	errMethodPath = errors.New("svcsig: method or path is empty")
)

// ParseKey accepts upmo_svc_ plus unpadded or padded base64, at least 32 bytes.
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

// Sign returns an Authorization bearer for this audience, method and path.
func Sign(derived []byte, audience, method, path string, now time.Time) (string, error) {
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
	unix := now.Unix()
	nonceText := base64.RawURLEncoding.EncodeToString(nonce)
	mac := macOver(derived, audience, unix, nonceText, method, path)
	return BearerPrefix + audience + "." + strconv.FormatInt(unix, 10) + "." + nonceText + "." + base64.RawURLEncoding.EncodeToString(mac), nil
}

// Verify checks a bearer against any derived key. The first matching key wins.
func Verify(derived [][]byte, audience, method, path, bearer string, now time.Time) error {
	_, err := VerifyIndex(derived, audience, method, path, bearer, now)
	return err
}

// VerifyIndex checks a bearer against any derived key and returns the index of
// the matching key on success.
func VerifyIndex(derived [][]byte, audience, method, path, bearer string, now time.Time) (int, error) {
	if len(derived) == 0 {
		return -1, errNoKey
	}
	for _, k := range derived {
		if len(k) != keyBytes {
			return -1, errNoKey
		}
	}
	if !knownAudience(audience) {
		return -1, errAudience
	}
	if method == "" || path == "" {
		return -1, errMethodPath
	}
	gotAudience, unix, nonceText, gotMAC, err := parseBearer(bearer)
	if err != nil {
		return -1, err
	}
	if gotAudience != audience {
		return -1, errAudience
	}
	matched := -1
	for i, k := range derived {
		candidate := macOver(k, gotAudience, unix, nonceText, method, path)
		if subtle.ConstantTimeCompare(gotMAC, candidate) == 1 && matched == -1 {
			matched = i
		}
	}
	if matched < 0 {
		return -1, errMismatch
	}
	if d := now.Sub(time.Unix(unix, 0)); d > tolerance || d < -tolerance {
		return -1, errStale
	}
	return matched, nil
}

// Transport adds a bearer on every client request. An empty key or unknown
// audience is an error, so a caller cannot start a client that will never
// authenticate.
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

// Handler refuses every path except /healthz and /readyz unless the bearer
// verifies for this audience, method and path. An empty key list or unknown
// audience is an error, so a caller cannot start a server that fails closed
// by accident.
func Handler(derived [][]byte, audience string, inner http.Handler) (http.Handler, error) {
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			inner.ServeHTTP(w, r)
			return
		}
		secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || Verify(derived, audience, r.Method, r.URL.Path, secret, time.Now()) != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		inner.ServeHTTP(w, r)
	}), nil
}

type roundTripper struct {
	base     http.RoundTripper
	derived  []byte
	audience string
}

func (t roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := Sign(t.derived, t.audience, req.Method, req.URL.Path, time.Now())
	if err != nil {
		return nil, err
	}
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	clone.Header.Set("Authorization", "Bearer "+token)
	return t.base.RoundTrip(clone)
}

func parseBearer(bearer string) (audience string, unix int64, nonce string, mac []byte, err error) {
	rest, ok := strings.CutPrefix(bearer, BearerPrefix)
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
	if err != nil {
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

func macOver(derived []byte, audience string, unix int64, nonce, method, path string) []byte {
	h := hmac.New(sha256.New, derived)
	h.Write([]byte(version))
	h.Write([]byte{'.'})
	h.Write([]byte(audience))
	h.Write([]byte{'.'})
	h.Write([]byte(strconv.FormatInt(unix, 10)))
	h.Write([]byte{'.'})
	h.Write([]byte(nonce))
	h.Write([]byte{'.'})
	h.Write([]byte(method))
	h.Write([]byte{'.'})
	h.Write([]byte(path))
	return h.Sum(nil)
}

func knownAudience(audience string) bool {
	return audience == AudienceGateway || audience == AudienceSandboxd || audience == AudienceProvision
}
