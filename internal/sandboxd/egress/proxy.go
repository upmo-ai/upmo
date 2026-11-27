// Package egress is the only route out of a sandbox: a CONNECT
// proxy that matches the sandbox allow-list by host name, without TLS
// interception, and logs every decision. An allowed CONNECT dials the host
// and relays bytes both ways until either side closes or the request
// context ends.
package egress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"syscall"
	"time"

	"github.com/upmo-ai/upmo/internal/platform/netguard"
	"github.com/upmo-ai/upmo/internal/runtime"
	"github.com/upmo-ai/upmo/internal/sandboxd/inject"
)

const (
	// DialTimeout bounds the dial to an allowed host.
	DialTimeout = 10 * time.Second
	// defaultPort is the port of a CONNECT target that names none.
	defaultPort = "443"
	// established is the reply that opens the tunnel.
	established = "HTTP/1.1 200 Connection Established\r\n\r\n"
)

// ErrResolved reports an allowed name that resolved to an address a sandbox
// must never reach: the host, its local network or a reserved range.
var ErrResolved = errors.New("egress: name resolved to a non-public address")

// Book returns the allow-list and credentials of a live sandbox.
type Book interface {
	Egress(id string) (allow []string, creds []runtime.Credential, ok bool)
}

// Injector resolves the credentials of a sandbox for one host.
type Injector interface {
	For(ctx context.Context, host string, creds []runtime.Credential) (inject.Rewrite, error)
}

// DialFunc opens the upstream connection of a tunnel.
type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// Proxy answers CONNECT requests from sandboxes.
type Proxy struct {
	Book   Book
	Inject Injector
	Log    *slog.Logger
	// Dial opens the upstream connection; nil means PublicDialer, which
	// refuses a name that resolves to a non-public address.
	Dial DialFunc
}

// ServeHTTP refuses anything but CONNECT, admits the target host through
// the runtime egress rules, then tunnels to it.
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

	// A host with no credential resolves to an empty rewrite; only a
	// credential the host needs and no backend can resolve stops it. The
	// rewrite is not applied: without TLS interception the proxy cannot see
	// inside the stream.
	if _, err := p.Inject.For(r.Context(), note.Host, creds); err != nil {
		p.Log.Warn("egress credential", "sandbox", id, "host", note.Host, "err", err)
		http.Error(w, "credential unavailable", http.StatusBadGateway)
		return
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		p.Log.Error("egress tunnel", "sandbox", id, "host", note.Host, "err", "connection cannot be hijacked")
		http.Error(w, "tunnel unavailable", http.StatusInternalServerError)
		return
	}
	port := note.Port
	if port == "" {
		port = defaultPort
	}
	dial := p.Dial
	if dial == nil {
		dial = PublicDialer()
	}
	dctx, cancel := context.WithTimeout(r.Context(), DialTimeout)
	upstream, err := dial(dctx, "tcp", net.JoinHostPort(note.Host, port))
	cancel()
	if err != nil {
		if errors.Is(err, ErrResolved) {
			p.Log.Warn("egress", "sandbox", id, "host", note.Host, "port", port, "verdict", runtime.VerdictRefuse, "reason", "resolved")
			http.Error(w, "refused", http.StatusForbidden)
			return
		}
		p.Log.Warn("egress dial", "sandbox", id, "host", note.Host, "port", port, "err", err)
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}

	client, buf, err := hj.Hijack()
	if err != nil {
		_ = upstream.Close()
		p.Log.Error("egress tunnel", "sandbox", id, "host", note.Host, "err", err)
		return
	}
	// The server may have set deadlines for an ordinary request; a tunnel
	// lives as long as both sides keep it open.
	_ = client.SetDeadline(time.Time{})
	if _, err := io.WriteString(client, established); err != nil {
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	// Bytes the client sent after the CONNECT head, such as an eager TLS
	// ClientHello, are already in the server's buffer.
	var early int64
	if n := buf.Reader.Buffered(); n > 0 {
		head, _ := buf.Peek(n)
		if _, err := upstream.Write(head); err != nil {
			_ = client.Close()
			_ = upstream.Close()
			return
		}
		early = int64(n)
	}

	start := time.Now()
	up, down := Relay(r.Context(), client, upstream)
	p.Log.Info("egress tunnel closed", "sandbox", id, "host", note.Host, "port", port,
		"sent", early+up, "received", down, "ms", time.Since(start).Milliseconds())
}

// Relay copies a to b and b to a until both directions end or ctx is done,
// then closes both. It returns the bytes copied each way. When one side
// finishes sending, the other is half-closed so a reply can still arrive.
func Relay(ctx context.Context, a, b net.Conn) (aToB, bToA int64) {
	var once sync.Once
	closeBoth := func() {
		once.Do(func() {
			_ = a.Close()
			_ = b.Close()
		})
	}
	stop := context.AfterFunc(ctx, closeBoth)
	defer stop()
	defer closeBoth()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		aToB = pipe(b, a)
	}()
	go func() {
		defer wg.Done()
		bToA = pipe(a, b)
	}()
	wg.Wait()
	return aToB, bToA
}

// pipe copies src to dst, then half-closes dst when it can, or closes it
// when it cannot, so the far side sees the end of the stream.
func pipe(dst, src net.Conn) int64 {
	n, _ := io.Copy(dst, src)
	if cw, ok := dst.(interface{ CloseWrite() error }); ok {
		if cw.CloseWrite() == nil {
			return n
		}
	}
	_ = dst.Close()
	_ = src.Close()
	return n
}

// PublicDialer dials TCP and refuses, after resolution and before the
// connection opens, any address that is not public unicast. It closes the
// gap between the name check at CONNECT and the address the name resolves
// to, so an allowed name cannot be pointed at the host or its network.
func PublicDialer() DialFunc {
	d := &net.Dialer{Timeout: DialTimeout, ControlContext: refuseBlocked}
	return d.DialContext
}

// refuseBlocked is the dialer's control hook. netguard judges NAT64, 6to4
// and mapped addresses by the IPv4 address inside them.
func refuseBlocked(_ context.Context, _, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrResolved, address)
	}
	if reason := netguard.BlockedAddr(ap.Addr()); reason != "" {
		return fmt.Errorf("%w: %s (%s)", ErrResolved, ap.Addr(), reason)
	}
	return nil
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
