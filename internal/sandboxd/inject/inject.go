// Package inject swaps the sentinel values a sandbox sees for the real
// credential values, only toward the host each credential names.
// It runs in sandboxd and never in brain, so brain never holds a real value.
package inject

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"unicode"

	"github.com/upmo-ai/upmo/internal/runtime"
)

// ErrNoBackend reports a reference no backend can resolve.
var ErrNoBackend = errors.New("inject: no credential backend")

// Resolver turns a credential reference into its real value.
type Resolver interface {
	Resolve(ctx context.Context, ref string) (string, error)
}

// Refuse is the stub resolver: it resolves nothing, so no real value is ever
// injected until a backend is wired.
type Refuse struct{}

// Resolve always reports ErrNoBackend.
func (Refuse) Resolve(context.Context, string) (string, error) {
	return "", ErrNoBackend
}

// Env resolves refs of the form env:NAME from the process environment, only
// for a NAME on Allow (exact match). A name with the UPMO_ prefix is never
// resolved, even when listed, so sandboxd's own secrets cannot leave. The
// zero Env resolves nothing. Other schemes, a blank or unlisted name, or a
// missing or empty value report ErrNoBackend.
type Env struct {
	// Allow lists the variable names Resolve may read.
	Allow []string
}

// Resolve returns the value of NAME when ref is env:NAME, NAME is on Allow,
// does not start with UPMO_, and is set to a non-empty value.
func (e Env) Resolve(_ context.Context, ref string) (string, error) {
	name, ok := strings.CutPrefix(strings.TrimSpace(ref), "env:")
	if !ok || !envName(name) || strings.HasPrefix(name, "UPMO_") || !slices.Contains(e.Allow, name) {
		return "", ErrNoBackend
	}
	value, ok := os.LookupEnv(name)
	if !ok || value == "" {
		return "", ErrNoBackend
	}
	return value, nil
}

// envName reports whether name is a portable environment variable name:
// letters, digits and underscore, not starting with a digit.
func envName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_', unicode.IsLetter(r):
		case unicode.IsDigit(r):
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// Injector resolves the credentials of one sandbox for one host.
type Injector struct {
	Resolver Resolver
	Sandbox  SandboxResolver
}

// SandboxResolver resolves a credential only for the sandbox holding it.
type SandboxResolver interface {
	ResolveForSandbox(ctx context.Context, sandboxID string, proxySecret []byte, ref string) ([]byte, error)
}

// Rewrite maps each sentinel to its real value for one host. It has no
// String method, so a log line cannot print a value by accident.
type Rewrite struct {
	pairs []string
	refs  []string
}

// For resolves every credential whose host is host. A credential for another
// host is never resolved, so its value cannot leave toward the wrong host.
// A host with no credential gets an empty rewrite and no error, even with no
// resolver; only a credential the host names that cannot be resolved fails.
func (i Injector) For(ctx context.Context, sandboxID string, proxySecret []byte, host string, creds []runtime.Credential) (Rewrite, error) {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	var rw Rewrite
	for _, c := range creds {
		if c.Host != host {
			continue
		}
		var value string
		if i.Sandbox != nil {
			plain, err := i.Sandbox.ResolveForSandbox(ctx, sandboxID, proxySecret, c.Ref)
			if err != nil {
				return Rewrite{}, err
			}
			if len(plain) == 0 || strings.ContainsAny(string(plain), "\r\n\x00") || strings.HasPrefix(string(plain), runtime.SentinelPrefix) {
				clear(plain)
				return Rewrite{}, ErrNoBackend
			}
			value = string(plain)
			clear(plain)
		} else {
			if i.Resolver == nil {
				return Rewrite{}, ErrNoBackend
			}
			var err error
			value, err = i.Resolver.Resolve(ctx, c.Ref)
			if err != nil {
				return Rewrite{}, err
			}
		}
		if value == "" || strings.ContainsAny(value, "\r\n\x00") || strings.HasPrefix(value, runtime.SentinelPrefix) {
			return Rewrite{}, ErrNoBackend
		}
		rw.pairs = append(rw.pairs, c.Sentinel, value)
		rw.refs = append(rw.refs, c.Ref)
	}
	return rw, nil
}

// Empty reports whether the rewrite changes nothing.
func (rw Rewrite) Empty() bool {
	return len(rw.pairs) == 0
}

// Apply returns a copy of header with every sentinel replaced.
func (rw Rewrite) Apply(header http.Header) http.Header {
	out := header.Clone()
	if out == nil || rw.Empty() {
		return out
	}
	for k, values := range out {
		for j, v := range values {
			out[k][j] = rw.transform(v, false)
		}
	}
	return out
}

// ScrubHeader returns a copy with exact resolved values replaced by sentinels.
func (rw Rewrite) ScrubHeader(header http.Header) http.Header {
	out := header.Clone()
	if out == nil || rw.Empty() {
		return out
	}
	for k, values := range out {
		for j, v := range values {
			out[k][j] = rw.transform(v, true)
		}
	}
	return out
}

// Scrub replaces exact resolved values in body in place when no replacement is needed.
func (rw Rewrite) Scrub(body []byte) []byte {
	if rw.Empty() {
		return body
	}
	out := body
	for i := 0; i < len(rw.pairs); i += 2 {
		secret := []byte(rw.pairs[i+1])
		if bytes.Contains(out, secret) {
			out = bytes.ReplaceAll(out, secret, []byte(rw.pairs[i]))
		}
	}
	return out
}

func (rw Rewrite) transform(value string, scrub bool) string {
	for i := 0; i < len(rw.pairs); i += 2 {
		from, to := rw.pairs[i], rw.pairs[i+1]
		if scrub {
			from, to = to, from
		}
		if len(value) >= 6 && strings.EqualFold(value[:6], "Basic ") {
			decoded, err := base64.StdEncoding.DecodeString(value[6:])
			if err == nil {
				changed := strings.ReplaceAll(string(decoded), from, to)
				if changed != string(decoded) {
					value = "Basic " + base64.StdEncoding.EncodeToString([]byte(changed))
					continue
				}
			}
		}
		value = strings.ReplaceAll(value, from, to)
	}
	return value
}

// String reports only the credential refs represented by this rewrite.
func (rw Rewrite) String() string { return fmt.Sprintf("Rewrite{refs:%q}", rw.refs) }

// LogValue reports only the credential refs represented by this rewrite.
func (rw Rewrite) LogValue() slog.Value { return slog.AnyValue(rw.refs) }
