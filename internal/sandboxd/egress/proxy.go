// Package egress is the only route out of a sandbox: a CONNECT
// proxy that matches the sandbox allow-list by host name, without TLS
// interception, and logs every decision. The tunnel itself is not built yet,
// so an allowed host is answered 501 and nothing leaves.
package egress

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/upmo-ai/upmo/internal/runtime"
	"github.com/upmo-ai/upmo/internal/sandboxd/inject"
)

// Book returns the allow-list and credentials of a live sandbox.
type Book interface {
	Egress(id string) (allow []string, creds []runtime.Credential, ok bool)
}

// Injector resolves the credentials of a sandbox for one host.
type Injector interface {
	For(ctx context.Context, host string, creds []runtime.Credential) (inject.Rewrite, error)
}

// Proxy answers CONNECT requests from sandboxes.
type Proxy struct {
	Book   Book
	Inject Injector
	Log    *slog.Logger
}

// ServeHTTP refuses anything but CONNECT, then admits the target host
// through the runtime egress rules.
func (p Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		w.Header().Set("Allow", http.MethodConnect)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// The sandbox names itself in the proxy user name until drivers bind the
	// proxy to each sandbox network.
	id, _, ok := proxyAuth(r)
	allow, creds, live := []string(nil), []runtime.Credential(nil), false
	if ok && id != "" {
		allow, creds, live = p.Book.Egress(id)
	}
	if !live {
		w.Header().Set("Proxy-Authenticate", `Basic realm="sandboxd"`)
		http.Error(w, "unknown sandbox", http.StatusProxyAuthRequired)
		return
	}

	decision, err := runtime.AdmitEgress(r.Host, allow)
	if err != nil {
		p.Log.Warn("egress", "sandbox", id, "verdict", runtime.VerdictRefuse, "reason", "malformed")
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	note, err := runtime.Note(decision)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	p.Log.Info("egress", "sandbox", id, "host", note.Host, "port", note.Port, "verdict", note.Verdict, "reason", note.Reason)
	if note.Verdict != runtime.VerdictAllow {
		http.Error(w, "refused", http.StatusForbidden)
		return
	}

	if _, err := p.Inject.For(r.Context(), note.Host, creds); err != nil {
		p.Log.Warn("egress credential", "sandbox", id, "host", note.Host, "err", err)
		http.Error(w, "credential unavailable", http.StatusBadGateway)
		return
	}
	http.Error(w, "tunnel is not built yet", http.StatusNotImplemented)
}

// proxyAuth reads Basic credentials from Proxy-Authorization.
func proxyAuth(r *http.Request) (string, string, bool) {
	h := r.Header.Get("Proxy-Authorization")
	if h == "" {
		return "", "", false
	}
	q := &http.Request{Header: http.Header{"Authorization": {h}}}
	return q.BasicAuth()
}
