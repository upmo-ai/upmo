// Package webhooksig signs and verifies webhook bodies with HMAC-SHA256:
// GitHub's X-Hub-Signature-256 and Standard
// Webhooks. Every comparison is constant time.
package webhooksig

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
)

// MaxBody is the largest body, in bytes, that is read, signed or verified.
const MaxBody = 1 << 20

// tolerance is how far a Standard Webhooks timestamp may be from now, past
// or future.
const tolerance = 5 * time.Minute

var (
	errTooLarge     = errors.New("webhooksig: body larger than 1 MiB")
	errMissing      = errors.New("webhooksig: missing id, timestamp or signature")
	errNoSecret     = errors.New("webhooksig: no secret")
	errSecret       = errors.New("webhooksig: secret is not whsec_ followed by base64")
	errTimestamp    = errors.New("webhooksig: timestamp is not unix seconds")
	errStale        = errors.New("webhooksig: timestamp outside the tolerance")
	errEncoding     = errors.New("webhooksig: malformed signature")
	errNoSignature  = errors.New("webhooksig: no v1 signature")
	errMismatch     = errors.New("webhooksig: signature does not match")
	errGitHubFormat = errors.New("webhooksig: header is not sha256=<hex>")
)

// ReadBody reads all of r. More than 1 MiB is an error.
func ReadBody(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, MaxBody+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxBody {
		return nil, errTooLarge
	}
	return b, nil
}

// SignGitHub returns the X-Hub-Signature-256 value for body: "sha256=<hex>".
func SignGitHub(secret, body []byte) string {
	return "sha256=" + hex.EncodeToString(mac(secret, body))
}

// VerifyGitHub checks an X-Hub-Signature-256 header against the raw body.
func VerifyGitHub(secret, body []byte, header string) error {
	if len(secret) == 0 {
		return errNoSecret
	}
	if len(body) > MaxBody {
		return errTooLarge
	}
	h, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return errGitHubFormat
	}
	got, err := hex.DecodeString(h)
	if err != nil {
		return errGitHubFormat
	}
	if subtle.ConstantTimeCompare(got, mac(secret, body)) != 1 {
		return errMismatch
	}
	return nil
}

// Sign returns the Standard Webhooks signature, "v1,<base64>", that Upmo
// sends with its own outbound webhooks. secret is "whsec_" followed by
// base64.
func Sign(secret, id string, timestamp time.Time, body []byte) (string, error) {
	key, err := decodeSecret(secret)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", errMissing
	}
	if len(body) > MaxBody {
		return "", errTooLarge
	}
	sig := mac(key, signedContent(id, timestamp.Unix(), body))
	return "v1," + base64.StdEncoding.EncodeToString(sig), nil
}

// Verify checks a Standard Webhooks request and returns its id. It accepts
// when any secret matches any v1 signature in the space-separated signature
// header, and the timestamp is within 5 minutes of now.
func Verify(secrets []string, id, timestamp, signature string, body []byte, now time.Time) (string, error) {
	if id == "" || timestamp == "" || signature == "" {
		return "", errMissing
	}
	if len(body) > MaxBody {
		return "", errTooLarge
	}
	if len(secrets) == 0 {
		return "", errNoSecret
	}
	keys := make([][]byte, 0, len(secrets))
	for _, s := range secrets {
		k, err := decodeSecret(s)
		if err != nil {
			return "", err
		}
		keys = append(keys, k)
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return "", errTimestamp
	}
	if d := now.Sub(time.Unix(ts, 0)); d > tolerance || d < -tolerance {
		return "", errStale
	}
	sigs, err := parseSignatures(signature)
	if err != nil {
		return "", err
	}
	content := signedContent(id, ts, body)
	for _, k := range keys {
		want := mac(k, content)
		for _, s := range sigs {
			if subtle.ConstantTimeCompare(s, want) == 1 {
				return id, nil
			}
		}
	}
	return "", errMismatch
}

// parseSignatures returns the decoded v1 signatures in header. Other
// versions are skipped; a malformed entry is an error.
func parseSignatures(header string) ([][]byte, error) {
	var sigs [][]byte
	for _, f := range strings.Fields(header) {
		version, value, ok := strings.Cut(f, ",")
		if !ok {
			return nil, errEncoding
		}
		if version != "v1" {
			continue
		}
		s, err := base64.StdEncoding.Strict().DecodeString(value)
		if err != nil {
			return nil, errEncoding
		}
		sigs = append(sigs, s)
	}
	if len(sigs) == 0 {
		return nil, errNoSignature
	}
	return sigs, nil
}

// decodeSecret strips the whsec_ prefix and decodes the base64 rest, with
// or without padding.
func decodeSecret(secret string) ([]byte, error) {
	rest, ok := strings.CutPrefix(secret, "whsec_")
	if !ok {
		return nil, errSecret
	}
	key, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(rest, "="))
	if err != nil || len(key) == 0 {
		return nil, errSecret
	}
	return key, nil
}

func signedContent(id string, unix int64, body []byte) []byte {
	b := make([]byte, 0, len(id)+len(body)+22)
	b = append(b, id...)
	b = append(b, '.')
	b = strconv.AppendInt(b, unix, 10)
	b = append(b, '.')
	return append(b, body...)
}

func mac(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}
