package egress

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/runtime"
	"github.com/upmo-ai/upmo/internal/sandboxd/inject"
)

var ghCred = runtime.Credential{Sentinel: "upmo_sentinel_gh", Ref: "env:GITHUB_TOKEN", Host: "github.com"}

// book holds one allow-list and one credential list per sandbox.
type book struct {
	allow map[string][]string
	creds []runtime.Credential
}

func (b book) Egress(id string) ([]string, []runtime.Credential, bool) {
	allow, ok := b.allow[id]
	return allow, b.creds, ok
}

type injector struct {
	mu    sync.Mutex
	hosts []string
	err   error
}

func (i *injector) For(_ context.Context, host string, _ []runtime.Credential) (inject.Rewrite, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.hosts = append(i.hosts, host)
	return inject.Rewrite{}, i.err
}

// logs is a JSON log buffer safe to read while a handler still writes.
type logs struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// dialer records each upstream address and connects to target instead.
type dialer struct {
	mu     sync.Mutex
	target string
	err    error
	addrs  []string
}

func (d *dialer) dial(ctx context.Context, network, address string) (net.Conn, error) {
	d.mu.Lock()
	d.addrs = append(d.addrs, address)
	target, err := d.target, d.err
	d.mu.Unlock()
	if err != nil {
		return nil, err
	}
	var nd net.Dialer
	return nd.DialContext(ctx, network, target)
}

func (d *dialer) dialled() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.addrs...)
}

func connect(p Proxy, target, sandbox string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodConnect, target, nil)
	if sandbox != "" {
		r.Header.Set("Proxy-Authorization", "Basic "+basic(sandbox))
	}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, r)
	return rec
}

func basic(user string) string {
	r := &http.Request{Header: http.Header{}}
	r.SetBasicAuth(user, "")
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Basic ")
}

func proxy(inj Injector, d *dialer) (Proxy, *logs) {
	l := &logs{}
	p := Proxy{
		Book:   book{allow: map[string][]string{"box-1": {"github.com"}}, creds: []runtime.Credential{ghCred}},
		Inject: inj,
		Log:    slog.New(slog.NewJSONHandler(l, nil)),
	}
	if d != nil {
		p.Dial = d.dial
	}
	return p, l
}

// upstream serves one connection with handle and reports when it returns.
func upstream(t *testing.T, handle func(net.Conn)) (string, <-chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		handle(c)
	}()
	return ln.Addr().String(), done
}

// open sends a CONNECT for target as sandbox, with extra bytes in the same
// write, and returns the connection, its reader and the proxy's response.
func open(t *testing.T, srv *httptest.Server, target, sandbox, extra string) (*net.TCPConn, *bufio.Reader, *http.Response) {
	t.Helper()
	c, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	head := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n"
	if sandbox != "" {
		head += "Proxy-Authorization: Basic " + basic(sandbox) + "\r\n"
	}
	if _, err := io.WriteString(c, head+"\r\n"+extra); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	res, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	return c.(*net.TCPConn), br, res
}

func readN(t *testing.T, r io.Reader, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		t.Fatalf("read %d: %v", n, err)
	}
	return string(b)
}

func TestTunnelRelaysBothWays(t *testing.T) {
	got := make(chan string, 1)
	addr, served := upstream(t, func(c net.Conn) {
		// Echo until the client half-closes, then say goodbye.
		var seen bytes.Buffer
		_, _ = io.Copy(io.MultiWriter(c, &seen), c)
		got <- seen.String()
		_, _ = io.WriteString(c, "bye")
	})
	d := &dialer{target: addr}
	p, l := proxy(inject.Injector{Resolver: inject.Refuse{}}, d)
	p.Book = book{allow: map[string][]string{"box-1": {"github.com"}}}
	srv := httptest.NewServer(p)
	defer srv.Close()

	// "early" rides in the same write as the CONNECT head, as an eager TLS
	// ClientHello would, and must still reach the upstream.
	c, br, res := open(t, srv, "github.com:443", "box-1", "early")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT = %d", res.StatusCode)
	}
	if got := readN(t, br, 5); got != "early" {
		t.Fatalf("echo of the buffered bytes = %q", got)
	}
	if _, err := io.WriteString(c, "ping"); err != nil {
		t.Fatal(err)
	}
	if got := readN(t, br, 4); got != "ping" {
		t.Fatalf("echo = %q", got)
	}

	// Half-close: the upstream sees the end and can still answer.
	if err := c.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if rest, err := io.ReadAll(br); err != nil || string(rest) != "bye" {
		t.Fatalf("after half-close = %q %v", rest, err)
	}
	if seen := <-got; seen != "earlyping" {
		t.Fatalf("upstream saw %q", seen)
	}
	<-served

	if a := d.dialled(); len(a) != 1 || a[0] != "github.com:443" {
		t.Fatalf("dialled %v", a)
	}
	waitFor(t, func() bool { return strings.Contains(l.String(), "egress tunnel closed") })
	out := l.String()
	if !strings.Contains(out, `"verdict":"allow"`) || !strings.Contains(out, `"sent":9`) || !strings.Contains(out, `"received":12`) {
		t.Fatalf("log = %s", out)
	}
}

func TestTunnelDefaultsToPort443(t *testing.T) {
	addr, _ := upstream(t, func(net.Conn) {})
	d := &dialer{target: addr}
	p, _ := proxy(&injector{}, d)
	srv := httptest.NewServer(p)
	defer srv.Close()
	if _, _, res := open(t, srv, "github.com", "box-1", ""); res.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT = %d", res.StatusCode)
	}
	if a := d.dialled(); len(a) != 1 || a[0] != "github.com:443" {
		t.Fatalf("dialled %v", a)
	}
}

func TestTunnelClosesBothSidesWhenTheContextEnds(t *testing.T) {
	closed := make(chan error, 1)
	addr, _ := upstream(t, func(c net.Conn) {
		_, err := c.Read(make([]byte, 1)) // blocks until the proxy closes
		closed <- err
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p, l := proxy(&injector{}, &dialer{target: addr})
	srv := httptest.NewUnstartedServer(p)
	srv.Config.BaseContext = func(net.Listener) context.Context { return ctx }
	srv.Start()
	defer srv.Close()

	_, br, res := open(t, srv, "github.com:443", "box-1", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT = %d", res.StatusCode)
	}
	cancel()
	if _, err := br.ReadByte(); !errors.Is(err, io.EOF) {
		t.Fatalf("client read after cancel = %v, want EOF", err)
	}
	select {
	case err := <-closed:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("upstream read after cancel = %v, want EOF", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("upstream stayed open after cancel")
	}
	waitFor(t, func() bool { return strings.Contains(l.String(), "egress tunnel closed") })
}

func TestRelayCancelClosesBoth(t *testing.T) {
	a, aPeer := net.Pipe()
	b, bPeer := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		Relay(ctx, a, b)
		close(done)
	}()
	go func() { _, _ = aPeer.Write([]byte("x")) }()
	if got := readN(t, bPeer, 1); got != "x" {
		t.Fatalf("relayed %q", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Relay did not return after cancel")
	}
	if _, err := aPeer.Read(make([]byte, 1)); err == nil {
		t.Fatal("a still open")
	}
	if _, err := bPeer.Read(make([]byte, 1)); err == nil {
		t.Fatal("b still open")
	}
}

func TestRelayEndsWhenOneSideCloses(t *testing.T) {
	a, aPeer := net.Pipe()
	b, bPeer := net.Pipe()
	done := make(chan struct{})
	go func() {
		Relay(context.Background(), a, b)
		close(done)
	}()
	_ = aPeer.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Relay did not return after a side closed")
	}
	if _, err := bPeer.Read(make([]byte, 1)); err == nil {
		t.Fatal("b still open")
	}
}

func TestNoCredentialStillTunnels(t *testing.T) {
	addr, _ := upstream(t, func(net.Conn) {})
	d := &dialer{target: addr}
	p, _ := proxy(inject.Injector{Resolver: inject.Refuse{}}, d)
	p.Book = book{allow: map[string][]string{"box-1": {"github.com", "proxy.golang.org"}}, creds: []runtime.Credential{ghCred}}
	srv := httptest.NewServer(p)
	defer srv.Close()

	// The credential names github.com only, so proxy.golang.org tunnels.
	if _, _, res := open(t, srv, "proxy.golang.org:443", "box-1", ""); res.StatusCode != http.StatusOK {
		t.Fatalf("no credential = %d", res.StatusCode)
	}
	// github.com needs a credential the stub backend cannot resolve.
	if _, _, res := open(t, srv, "github.com:443", "box-1", ""); res.StatusCode != http.StatusBadGateway {
		t.Fatalf("unresolved credential = %d", res.StatusCode)
	}
	if a := d.dialled(); len(a) != 1 || a[0] != "proxy.golang.org:443" {
		t.Fatalf("dialled %v", a)
	}
}

func TestCredentialFailureStopsBeforeTheDial(t *testing.T) {
	d := &dialer{target: "127.0.0.1:1"}
	inj := &injector{err: inject.ErrNoBackend}
	p, l := proxy(inj, d)
	rec := connect(p, "github.com:443", "box-1")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("no backend = %d", rec.Code)
	}
	if len(inj.hosts) != 1 || inj.hosts[0] != "github.com" {
		t.Fatalf("inject hosts = %v", inj.hosts)
	}
	if len(d.dialled()) != 0 {
		t.Fatalf("dialled after a credential failure: %v", d.dialled())
	}
	if !strings.Contains(l.String(), `"verdict":"allow"`) || !strings.Contains(l.String(), `"port":"443"`) {
		t.Fatalf("log = %s", l)
	}
}

func TestDialFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		code   int
		reason string
	}{
		"unreachable": {errors.New("connection refused"), http.StatusBadGateway, "egress dial"},
		"resolved":    {&net.OpError{Op: "dial", Net: "tcp", Err: ErrResolved}, http.StatusForbidden, `"reason":"resolved"`},
	} {
		p, l := proxy(&injector{}, &dialer{err: tc.err})
		srv := httptest.NewServer(p)
		_, _, res := open(t, srv, "github.com:443", "box-1", "")
		srv.Close()
		if res.StatusCode != tc.code {
			t.Errorf("%s = %d, want %d", name, res.StatusCode, tc.code)
		}
		if !strings.Contains(l.String(), tc.reason) {
			t.Errorf("%s log = %s", name, l)
		}
	}
}

func TestRefusesUnlistedAndLogs(t *testing.T) {
	inj := &injector{}
	d := &dialer{target: "127.0.0.1:1"}
	for target, reason := range map[string]string{
		"example.com:443":      "allowlist",
		"169.254.169.254:80":   "address",
		"metadata.internal:80": "host",
	} {
		p, l := proxy(inj, d)
		rec := connect(p, target, "box-1")
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s = %d", target, rec.Code)
		}
		if !strings.Contains(l.String(), `"verdict":"refuse"`) || !strings.Contains(l.String(), `"reason":"`+reason+`"`) {
			t.Errorf("%s log = %s", target, l)
		}
	}
	if len(inj.hosts) != 0 {
		t.Fatalf("credentials resolved for a refused host: %v", inj.hosts)
	}
	if len(d.dialled()) != 0 {
		t.Fatalf("dialled a refused host: %v", d.dialled())
	}
}

func TestRefusesUnknownSandboxAndOtherMethods(t *testing.T) {
	p, _ := proxy(&injector{}, nil)
	if rec := connect(p, "github.com:443", ""); rec.Code != http.StatusProxyAuthRequired {
		t.Errorf("anonymous = %d", rec.Code)
	}
	if rec := connect(p, "github.com:443", "box-2"); rec.Code != http.StatusProxyAuthRequired {
		t.Errorf("unknown = %d", rec.Code)
	}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://github.com/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET = %d", rec.Code)
	}
}

func TestPublic(t *testing.T) {
	for addr, want := range map[string]bool{
		"140.82.121.4":        true,
		"2606:4700::1111":     true,
		"127.0.0.1":           false,
		"10.0.0.1":            false,
		"172.17.0.1":          false,
		"192.168.5.2":         false,
		"169.254.169.254":     false,
		"100.100.100.200":     false,
		"0.0.0.0":             false,
		"255.255.255.255":     false,
		"224.0.0.1":           false,
		"::1":                 false,
		"fe80::1":             false,
		"fd00::1":             false,
		"::ffff:127.0.0.1":    false,
		"::ffff:140.82.121.4": true,
	} {
		if got := Public(netip.MustParseAddr(addr)); got != want {
			t.Errorf("Public(%s) = %t", addr, got)
		}
	}
}

func TestPublicDialerRefusesLocalAddresses(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	dial := PublicDialer()
	for _, host := range []string{"127.0.0.1", "localhost"} {
		c, err := dial(context.Background(), "tcp", net.JoinHostPort(host, port))
		if err == nil {
			_ = c.Close()
			t.Fatalf("%s: dialled a loopback address", host)
		}
		if !errors.Is(err, ErrResolved) {
			t.Fatalf("%s: err = %v, want ErrResolved", host, err)
		}
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
