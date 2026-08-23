package mcpauth

import (
	"context"
	"io"
	"net/http"
)

const defaultStepUp = 2

// Transport attaches an MCP access token and retries on insufficient_scope.
type Transport struct {
	Base      http.RoundTripper
	Token     func(ctx context.Context, resource, scope string) (string, error)
	Resource  string
	Scope     string
	MaxStepUp int
}

func (t *Transport) base() http.RoundTripper {
	if t.Base != nil {
		return t.Base
	}
	return http.DefaultTransport
}

func (t *Transport) maxStepUp() int {
	if t.MaxStepUp > 0 {
		return t.MaxStepUp
	}
	return defaultStepUp
}

// RoundTrip never sends an Upmo token. A 401 or 403 with insufficient_scope
// unions the challenged scopes and retries a bounded number of times.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := RefusePassthrough(req.Header.Get("Authorization")); err != nil {
		return nil, err
	}
	return t.round(req, t.Scope, 0)
}

func (t *Transport) round(req *http.Request, scope string, n int) (*http.Response, error) {
	clone := req.Clone(req.Context())
	if t.Token != nil {
		token, err := t.Token(req.Context(), t.Resource, scope)
		if err != nil {
			return nil, err
		}
		if err := RefusePassthrough(token); err != nil {
			return nil, err
		}
		if token != "" {
			clone.Header.Set("Authorization", "Bearer "+token)
		} else {
			clone.Header.Del("Authorization")
		}
	}
	res, err := t.base().RoundTrip(clone)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusUnauthorized && res.StatusCode != http.StatusForbidden {
		return res, nil
	}
	if t.Token == nil {
		return res, nil
	}
	ch, err := ParseWWWAuthenticate(res.Header.Get("WWW-Authenticate"))
	if err != nil || !NeedsStepUp(ch) {
		return res, nil
	}
	next := UnionScopes(scope, ch.Scope)
	if next == scope || n >= t.maxStepUp() {
		return res, nil
	}
	_, _ = io.Copy(io.Discard, res.Body)
	_ = res.Body.Close()
	return t.round(req, next, n+1)
}
