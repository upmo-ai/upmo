// Package inject swaps the sentinel values a sandbox sees for the real
// credential values, only toward the host each credential names.
// It runs in sandboxd and never in brain, so brain never holds a real value.
package inject

import (
	"context"
	"errors"
	"net/http"
	"os"
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

// Env resolves refs of the form env:NAME from the process environment.
// Other schemes, a blank name, or a missing or empty value
// report ErrNoBackend.
type Env struct{}

// Resolve returns the value of NAME when ref is env:NAME and that name is
// set to a non-empty value in the process environment.
func (Env) Resolve(_ context.Context, ref string) (string, error) {
	name, ok := strings.CutPrefix(strings.TrimSpace(ref), "env:")
	if !ok || !envName(name) {
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
}

// Rewrite maps each sentinel to its real value for one host. It has no
// String method, so a log line cannot print a value by accident.
type Rewrite struct {
	pairs []string
}

// For resolves every credential whose host is host. A credential for another
// host is never resolved, so its value cannot leave toward the wrong host.
// A host with no credential gets an empty rewrite and no error, even with no
// resolver; only a credential the host names that cannot be resolved fails.
func (i Injector) For(ctx context.Context, host string, creds []runtime.Credential) (Rewrite, error) {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	var pairs []string
	for _, c := range creds {
		if c.Host != host {
			continue
		}
		if i.Resolver == nil {
			return Rewrite{}, ErrNoBackend
		}
		value, err := i.Resolver.Resolve(ctx, c.Ref)
		if err != nil {
			return Rewrite{}, err
		}
		if value == "" {
			return Rewrite{}, ErrNoBackend
		}
		pairs = append(pairs, c.Sentinel, value)
	}
	return Rewrite{pairs: pairs}, nil
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
	r := strings.NewReplacer(rw.pairs...)
	for k, values := range out {
		for j, v := range values {
			out[k][j] = r.Replace(v)
		}
	}
	return out
}
