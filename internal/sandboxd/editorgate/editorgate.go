// Package editorgate verifies editor JWTs and reverse-proxies to OpenVSCode.
package editorgate

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/upmo-ai/upmo/internal/platform/editorjwt"
)

// DefaultBaseURL is the public editor gate origin when nothing is configured.
const DefaultBaseURL = "http://127.0.0.1:8084"

// Book resolves a sandbox editor upstream and refreshes idle touch.
type Book interface {
	EditorRoute(ctx context.Context, sandboxID string) (Route, error)
	TouchEditor(ctx context.Context, sandboxID string) error
}

// Route is the upstream OpenVSCode target for one sandbox.
type Route struct {
	Upstream  *url.URL
	ConnToken string
	TaskID    string
}

// PublicURL returns the browser-facing editor URL for sandboxID.
func PublicURL(base, sandboxID string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	id := strings.TrimSpace(sandboxID)
	if base == "" {
		base = DefaultBaseURL
	}
	return base + "/e/" + id + "/"
}

// Handler gates editor HTTP on a bound JWT.
type Handler struct {
	Book Book
	Keys editorjwt.Keys
	Now  func() time.Time
	Log  *slog.Logger
}

// ServeHTTP implements http.Handler.
func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sandboxID, tail, ok := parsePath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if tail == "" && !strings.HasSuffix(r.URL.Path, "/") {
		http.Redirect(w, r, "/e/"+sandboxID+"/", http.StatusPermanentRedirect) //nolint:gosec // same-origin path redirect; sandboxID holds no slash
		return
	}
	token := clientToken(r)
	if token == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	now := time.Now
	if h.Now != nil {
		now = h.Now
	}
	claims, err := h.Keys.Verify(token, now())
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	route, err := h.Book.EditorRoute(r.Context(), sandboxID)
	if err != nil {
		if h.Log != nil {
			h.Log.Debug("editor gate route", "sandbox", sandboxID, "err", err)
		}
		http.NotFound(w, r)
		return
	}
	if route.TaskID != "" && claims.Task != route.TaskID {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := h.Book.TouchEditor(r.Context(), sandboxID); err != nil && h.Log != nil {
		h.Log.Warn("editor gate touch", "sandbox", sandboxID, "err", err)
	}
	connToken := route.ConnToken
	proxy := &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.SetURL(route.Upstream)
		pr.Out.Host = pr.In.Host
		pr.SetXForwarded()
		pr.Out.URL.Path = singleJoin(route.Upstream.Path, tail)
		q := pr.Out.URL.Query()
		q.Del("access_token")
		q.Del("token")
		if connToken != "" {
			q.Set("tkn", connToken)
		}
		pr.Out.URL.RawQuery = q.Encode()
		pr.Out.Header.Del("Authorization")
	}}
	proxy.ErrorHandler = func(_ http.ResponseWriter, _ *http.Request, err error) {
		if h.Log != nil {
			h.Log.Warn("editor gate proxy", "sandbox", sandboxID, "err", err)
		}
	}
	proxy.ServeHTTP(w, r)
}

func parsePath(path string) (sandboxID, tail string, ok bool) {
	path = strings.TrimPrefix(path, "/")
	if !strings.HasPrefix(path, "e/") {
		return "", "", false
	}
	rest := strings.TrimPrefix(path, "e/")
	sandboxID, tail, _ = strings.Cut(rest, "/")
	if sandboxID == "" {
		return "", "", false
	}
	return sandboxID, tail, true
}

func clientToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		const p = "Bearer "
		if len(h) >= len(p) && strings.EqualFold(h[:len(p)], p) {
			return strings.TrimSpace(h[len(p):])
		}
	}
	q := r.URL.Query()
	for _, name := range []string{"access_token", "token"} {
		if v := strings.TrimSpace(q.Get(name)); v != "" {
			return v
		}
	}
	return ""
}

func singleJoin(base, add string) string {
	switch {
	case base == "" || base == "/":
		if add == "" {
			return "/"
		}
		if !strings.HasPrefix(add, "/") {
			return "/" + add
		}
		return add
	case add == "":
		return base
	default:
		return strings.TrimRight(base, "/") + "/" + strings.TrimPrefix(add, "/")
	}
}

// ValidateBaseURL checks UPMO_EDITOR_BASE_URL shape.
func ValidateBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultBaseURL, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.Hostname() == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", fmt.Errorf("config: UPMO_EDITOR_BASE_URL must be http://host[:port]")
	}
	return u.Scheme + "://" + u.Host, nil
}
