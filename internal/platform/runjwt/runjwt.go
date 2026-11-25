// Package runjwt signs and verifies a 15-minute audience-bound run JWT.
// The HMAC key is its own upmo_rnk_ key (UPMO_RUN_KEY), used as is. The
// token carries a user and a run, never a secret.
package runjwt

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// AudienceConnectors is the audience E9 will pass when the catalog is an MCP server.
const AudienceConnectors = "connectors"

const (
	issuer    = "upmo"
	lifetime  = 15 * time.Minute
	leeway    = 30 * time.Second
	keyBytes  = 32
	headerB64 = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9" // {"alg":"HS256","typ":"JWT"}
)

var (
	// ErrNoKey is a missing or short HMAC key.
	ErrNoKey    = errors.New("runjwt: no key")
	errToken    = errors.New("runjwt: malformed token")
	errAudience = errors.New("runjwt: unknown audience")
	errMismatch = errors.New("runjwt: signature does not match")
	errStale    = errors.New("runjwt: timestamp outside the lifetime")
	errClaims   = errors.New("runjwt: claims do not match")
)

// KeyPrefix starts every run JWT key.
const KeyPrefix = "upmo_rnk_"

// ParseKey accepts upmo_rnk_ plus unpadded or padded base64 of exactly 32
// bytes and returns those bytes as the HMAC key.
func ParseKey(secret string) ([]byte, error) {
	rest, ok := strings.CutPrefix(secret, KeyPrefix)
	if ok {
		key, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(rest, "="))
		if err == nil && len(key) == keyBytes {
			return key, nil
		}
	}
	return nil, errors.New("runjwt: key is not upmo_rnk_ followed by base64 of 32 bytes")
}

// ParseKeys parses the current key and an optional previous one for
// rotation. The current key signs; both verify.
func ParseKeys(current, previous string) (Keys, error) {
	if current == "" {
		return Keys{}, ErrNoKey
	}
	var keys [][]byte
	for _, secret := range []string{current, previous} {
		if secret == "" {
			continue
		}
		key, err := ParseKey(secret)
		if err != nil {
			return Keys{}, err
		}
		keys = append(keys, key)
	}
	return New(keys)
}

// Keys signs with the current key and verifies against every key.
type Keys struct {
	current  []byte
	previous [][]byte
}

// New requires at least one 32-byte key.
func New(keys [][]byte) (Keys, error) {
	if len(keys) == 0 {
		return Keys{}, ErrNoKey
	}
	for _, k := range keys {
		if len(k) != keyBytes {
			return Keys{}, ErrNoKey
		}
	}
	return Keys{current: keys[0], previous: keys[1:]}, nil
}

// Sign returns a compact HS256 JWT for this audience, user and run.
func (k Keys) Sign(audience, user, run string, now time.Time) (string, error) {
	if len(k.current) != keyBytes {
		return "", ErrNoKey
	}
	if err := checkIDs(audience, user, run); err != nil {
		return "", err
	}
	unix := now.Unix()
	payload, err := json.Marshal(map[string]any{
		"iss": issuer,
		"aud": audience,
		"sub": user,
		"run": run,
		"iat": unix,
		"nbf": unix,
		"exp": now.Add(lifetime).Unix(),
	})
	if err != nil {
		return "", err
	}
	body := headerB64 + "." + base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + base64.RawURLEncoding.EncodeToString(mac(k.current, body)), nil
}

// Claims are the verified user and run for one audience.
type Claims struct {
	User string
	Run  string
}

// Verify checks a JWT against any key.
func (k Keys) Verify(audience, token string, now time.Time) (Claims, error) {
	if len(k.current) != keyBytes {
		return Claims{}, ErrNoKey
	}
	if err := checkAudience(audience); err != nil {
		return Claims{}, err
	}
	if strings.HasPrefix(token, "upmo_pat_") || strings.HasPrefix(token, "upmo_s2s_") {
		return Claims{}, errToken
	}
	parts := strings.Split(token, ".")
	// The base64 decoder skips \r and \n, so a line break in the signature
	// would otherwise verify as a second spelling of the same token.
	if len(parts) != 3 || parts[0] != headerB64 || parts[1] == "" || parts[2] == "" || strings.ContainsAny(token, "\r\n") {
		return Claims{}, errToken
	}
	got, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	if err != nil || len(got) != sha256.Size {
		return Claims{}, errToken
	}
	body := parts[0] + "." + parts[1]
	match := subtle.ConstantTimeCompare(got, mac(k.current, body)) == 1
	for _, alt := range k.previous {
		if subtle.ConstantTimeCompare(got, mac(alt, body)) == 1 {
			match = true
		}
	}
	if !match {
		return Claims{}, errMismatch
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if err != nil {
		return Claims{}, errToken
	}
	var payload struct {
		Iss string `json:"iss"`
		Aud string `json:"aud"`
		Sub string `json:"sub"`
		Run string `json:"run"`
		Iat int64  `json:"iat"`
		Nbf int64  `json:"nbf"`
		Exp int64  `json:"exp"`
		Alg string `json:"alg"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return Claims{}, errToken
	}
	if payload.Iss != issuer || payload.Aud != audience || payload.Alg != "" {
		return Claims{}, errClaims
	}
	if err := checkIDs(payload.Aud, payload.Sub, payload.Run); err != nil {
		return Claims{}, err
	}
	if now.Add(leeway).Before(time.Unix(payload.Nbf, 0)) || now.Add(-leeway).After(time.Unix(payload.Exp, 0)) {
		return Claims{}, errStale
	}
	return Claims{User: payload.Sub, Run: payload.Run}, nil
}

func mac(key []byte, body string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(body))
	return h.Sum(nil)
}

func checkIDs(audience, user, run string) error {
	if err := checkAudience(audience); err != nil {
		return err
	}
	if user == "" || run == "" {
		return errClaims
	}
	return nil
}

func checkAudience(audience string) error {
	if audience == "" || strings.ContainsAny(audience, " \t\n.") {
		return errAudience
	}
	return nil
}
