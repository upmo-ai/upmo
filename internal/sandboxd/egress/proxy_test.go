package egress

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/platform/netguard"
	"github.com/upmo-ai/upmo/internal/runtime"
	"github.com/upmo-ai/upmo/internal/sandboxd/inject"
)

var ghCred = runtime.Credential{Sentinel: "upmo_sentinel_gh", Ref: "env:GITHUB_TOKEN", Host: "github.com"}

// book holds one allow-list and one credential list per sandbox.
type book struct {
	allow  map[string][]string
	creds  []runtime.Credential
	source netip.Addr
}

var testProxySecret = []byte(strings.Repeat("p", 32))

func (b book) Authenticate(source netip.Addr, secret []byte) (Principal, bool) {
	have, want := sha256.Sum256(secret), sha256.Sum256(testProxySecret)
	if len(secret) != 32 || have != want {
		return Principal{}, false
	}
	allow, ok := b.allow["box-1"]
	if b.source.IsValid() && source != b.source {
		ok = false
	}
	return Principal{ID: "box-1", Allow: allow, Creds: b.creds, Lease: 1}, ok
}
func (b book) Acquire(id string, token uint64) (func(), context.Context, bool) {
	return func() {}, context.Background(), id == "box-1" && token == 1
}

type injector struct {
	mu    sync.Mutex
	hosts []string
	err   error
}

type resolvedSecret map[string]string

func (r resolvedSecret) Resolve(_ context.Context, ref string) (string, error) {
	if v, ok := r[ref]; ok {
		return v, nil
	}
	return "", inject.ErrNoBackend
}

func (i *injector) For(_ context.Context, _ string, _ []byte, host string, _ []runtime.Credential) (inject.Rewrite, error) {
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

type failingBody struct{}

func (failingBody) Read([]byte) (int, error) { return 0, errors.New("upstream read failed") }
func (failingBody) Close() error             { return nil }

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

func connect(p *Proxy, target, sandbox string) *httptest.ResponseRecorder {
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
	r.SetBasicAuth(user, base64.RawURLEncoding.EncodeToString(testProxySecret))
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Basic ")
}

func proxy(inj Injector, d *dialer) (*Proxy, *logs) {
	l := &logs{}
	p := &Proxy{
		Book:   book{allow: map[string][]string{"box-1": {"github.com"}}},
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
	waitFor(t, func() bool { return strings.Contains(l.String(), `"verdict":"allow"`) })
	out := l.String()
	if !strings.Contains(out, `"verdict":"allow"`) {
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

func TestTunnelOutlivesDialTimeout(t *testing.T) {
	addr, _ := upstream(t, func(c net.Conn) { _, _ = io.Copy(c, c) })
	p, _ := proxy(&injector{}, &dialer{target: addr})
	srv := httptest.NewServer(p)
	defer srv.Close()
	c, br, res := open(t, srv, "github.com:443", "box-1", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT = %d", res.StatusCode)
	}
	_ = c.SetDeadline(time.Now().Add(DialTimeout + time.Second))
	time.Sleep(DialTimeout + 50*time.Millisecond)
	if _, err := io.WriteString(c, "still open"); err != nil {
		t.Fatalf("tunnel closed after dial deadline: %v", err)
	}
	if got := readN(t, br, len("still open")); got != "still open" {
		t.Fatalf("tunnel echo after dial deadline = %q", got)
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
	waitFor(t, func() bool { return strings.Contains(l.String(), `"verdict":"allow"`) })
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
	p.Book = book{allow: map[string][]string{"box-1": {"github.com", "proxy.golang.org"}}}
	srv := httptest.NewServer(p)
	defer srv.Close()

	// The credential names github.com only, so proxy.golang.org tunnels.
	if _, _, res := open(t, srv, "proxy.golang.org:443", "box-1", ""); res.StatusCode != http.StatusOK {
		t.Fatalf("no credential = %d", res.StatusCode)
	}
	if a := d.dialled(); len(a) != 1 || a[0] != "proxy.golang.org:443" {
		t.Fatalf("dialled %v", a)
	}
}

func TestProxyRefusesConnectToCredentialedHost(t *testing.T) {
	d := &dialer{target: "127.0.0.1:1"}
	inj := &injector{err: inject.ErrNoBackend}
	p, l := proxy(inj, d)
	p.Book = book{allow: map[string][]string{"box-1": {"github.com"}}, creds: []runtime.Credential{ghCred}}
	rec := connect(p, "github.com:443", "box-1")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("credentialed CONNECT = %d", rec.Code)
	}
	if len(inj.hosts) != 0 {
		t.Fatalf("inject hosts = %v", inj.hosts)
	}
	if len(d.dialled()) != 0 {
		t.Fatalf("dialled after a credential failure: %v", d.dialled())
	}
	if !strings.Contains(l.String(), `"reason":"denied_connect_credential"`) {
		t.Fatalf("log = %s", l)
	}
}

func TestProxyForwardSwapsSentinelOnlyForMatchingHost(t *testing.T) {
	const live = "sk_live_proxy_test"
	sentinel := "upmo_sentinel_" + strings.Repeat("a", 64)
	cred := runtime.Credential{Sentinel: sentinel, Ref: ghCred.Ref, Host: "example.com", Methods: []string{"GET"}, Paths: []string{"/user"}}
	var got string
	hits := 0
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		got = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, "ok")
	}))
	defer up.Close()
	root := up.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	d := &dialer{target: up.Listener.Addr().String()}
	p, _ := proxy(inject.Injector{Resolver: resolvedSecret{ghCred.Ref: live}}, d)
	p.TLSRootCAs = root
	p.Book = book{allow: map[string][]string{"box-1": {"example.com", "registry.npmjs.org"}}, creds: []runtime.Credential{cred}}
	r := httptest.NewRequest(http.MethodGet, "http://example.com/user", nil)
	r.RemoteAddr = "203.0.113.9:1234"
	r.Header.Set("Authorization", "Bearer "+sentinel)
	r.Header.Set("Proxy-Authorization", "Basic "+basic("ignored"))
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Body.String() != "ok" || got != "Bearer "+live {
		t.Fatalf("status=%d body=%q upstream credential swapped=%t", w.Code, w.Body.String(), got == "Bearer "+live)
	}
	r = httptest.NewRequest(http.MethodGet, "http://registry.npmjs.org/user", nil)
	r.RemoteAddr = "203.0.113.9:1234"
	r.Header.Set("Authorization", "Bearer "+sentinel)
	r.Header.Set("Proxy-Authorization", "Basic "+basic("ignored"))
	w = httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || hits != 1 {
		t.Fatalf("foreign host status=%d upstream hits=%d", w.Code, hits)
	}
}

func TestProxyRefusesKeyOnWrongMethodAndPath(t *testing.T) {
	inj := &injector{}
	d := &dialer{err: errors.New("must not dial")}
	p, l := proxy(inj, d)
	sentinel := "upmo_sentinel_" + strings.Repeat("b", 64)
	p.Book = book{allow: map[string][]string{"box-1": {"github.com"}}, creds: []runtime.Credential{{Sentinel: sentinel, Ref: ghCred.Ref, Host: "github.com", Methods: []string{"GET"}, Paths: []string{"/user"}}}}
	for _, tc := range []struct {
		method, target string
		status         int
		reason         string
	}{{http.MethodPost, "http://github.com/user", http.StatusForbidden, "denied_credential_method"}, {http.MethodGet, "http://github.com/repos", http.StatusForbidden, "denied_credential_path"}, {http.MethodGet, "http://github.com/user/../repos", http.StatusBadRequest, "denied_credential_path"}, {http.MethodGet, "http://github.com/user/%2e%2e/repos", http.StatusBadRequest, "denied_credential_path"}} {
		r := httptest.NewRequest(tc.method, tc.target, nil)
		r.RemoteAddr = "203.0.113.9:1234"
		r.Header.Set("Authorization", "Bearer "+sentinel)
		r.Header.Set("Proxy-Authorization", "Basic "+basic("box-1"))
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.target, w.Code, tc.status)
		}
		if !strings.Contains(l.String(), `"reason":"`+tc.reason+`"`) {
			t.Errorf("%s %s log = %s", tc.method, tc.target, l)
		}
	}
	if len(inj.hosts) != 0 || len(d.dialled()) != 0 {
		t.Fatalf("invalid credential requests reached For or upstream: For=%v dials=%v", inj.hosts, d.dialled())
	}
}

func TestProxyRefusesKeyOnPort80And8443(t *testing.T) {
	inj := &injector{}
	d := &dialer{err: errors.New("must not dial")}
	p, _ := proxy(inj, d)
	p.Book = book{allow: map[string][]string{"box-1": {"github.com"}}}
	for _, port := range []string{"80", "8443"} {
		r := httptest.NewRequest(http.MethodGet, "http://github.com:"+port+"/user", nil)
		r.RemoteAddr = "203.0.113.9:1234"
		r.Header.Set("Proxy-Authorization", "Basic "+basic("box-1"))
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("port %s = %d", port, w.Code)
		}
	}
	if len(inj.hosts) != 0 || len(d.dialled()) != 0 {
		t.Fatalf("non-443 requests reached For or upstream: For=%v dials=%v", inj.hosts, d.dialled())
	}
}

func TestProxyRejectsExplicitRawPath(t *testing.T) {
	p, _ := proxy(&injector{}, nil)
	r := httptest.NewRequest(http.MethodGet, "http://github.com/user", nil)
	r.URL.RawPath = "/user"
	r.RemoteAddr = "203.0.113.9:1234"
	r.Header.Set("Proxy-Authorization", "Basic "+basic("box-1"))
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("RawPath status=%d body=%q", w.Code, w.Body.String())
	}
}

func TestReadResponseReportsUpstreamReadFailure(t *testing.T) {
	_, _, err := readResponse(&http.Response{Body: failingBody{}})
	if err == nil || err.Error() != "upstream_read_failed" {
		t.Fatalf("readResponse err=%v", err)
	}
}

func TestProxyAcceptsAbsoluteFormOnly(t *testing.T) {
	p, _ := proxy(&injector{}, &dialer{err: errors.New("should not dial")})
	r := httptest.NewRequest(http.MethodGet, "http://github.com/user", nil)
	r.RequestURI = "/user"
	r.RemoteAddr = "203.0.113.9:1234"
	r.Header.Set("Proxy-Authorization", "Basic "+basic("box-1"))
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("origin-form = %d", w.Code)
	}
}

func TestProxyRejectsDisagreeingTarget(t *testing.T) {
	p, _ := proxy(&injector{}, &dialer{err: errors.New("should not dial")})
	r := httptest.NewRequest(http.MethodGet, "http://github.com/user", nil)
	r.Host = "registry.npmjs.org"
	r.RemoteAddr = "203.0.113.9:1234"
	r.Header.Set("Proxy-Authorization", "Basic "+basic("box-1"))
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("disagreeing Host = %d", w.Code)
	}
}

func TestProxyCredentialsIgnoreProxyAuthorization(t *testing.T) {
	sentinel := "upmo_sentinel_" + strings.Repeat("8", 64)
	cred := runtime.Credential{Sentinel: sentinel, Ref: "github_token", Host: "example.com", Methods: []string{"GET"}, Paths: []string{"/user"}}
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer up.Close()
	inj := &injector{}
	p, _ := proxy(inj, &dialer{target: up.Listener.Addr().String()})
	p.TLSRootCAs = up.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	p.Book = book{allow: map[string][]string{"box-1": {"example.com"}}, creds: []runtime.Credential{cred}}
	for _, user := range []string{sentinel, "upmo_sentinel_" + strings.Repeat("9", 64)} {
		r := httptest.NewRequest(http.MethodGet, "http://example.com/user", nil)
		r.RemoteAddr = "203.0.113.9:1234"
		r.Header.Set("Proxy-Authorization", "Basic "+basic(user))
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		if w.Code != http.StatusNoContent {
			t.Fatalf("proxy username %q status=%d body=%q", user, w.Code, w.Body.String())
		}
	}
	if len(inj.hosts) != 0 {
		t.Fatalf("Proxy-Authorization triggered For: %v", inj.hosts)
	}
}

func TestUnpresentedRequestMatchesOneOfMultipleCredentialScopes(t *testing.T) {
	creds := []runtime.Credential{
		{Sentinel: "upmo_sentinel_" + strings.Repeat("a", 64), Ref: "repos", Host: "example.com", Methods: []string{"GET"}, Paths: []string{"/repos"}},
		{Sentinel: "upmo_sentinel_" + strings.Repeat("b", 64), Ref: "issues", Host: "example.com", Methods: []string{"POST"}, Paths: []string{"/issues"}},
	}
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer up.Close()
	inj := &injector{}
	p, _ := proxy(inj, &dialer{target: up.Listener.Addr().String()})
	p.TLSRootCAs = up.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	p.Book = book{allow: map[string][]string{"box-1": {"example.com"}}, creds: creds}
	r := httptest.NewRequest(http.MethodGet, "http://example.com/repos", nil)
	r.RemoteAddr = "203.0.113.9:1234"
	r.Header.Set("Proxy-Authorization", "Basic "+basic("box-1"))
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent || len(inj.hosts) != 0 {
		t.Fatalf("unpresented matching scope status=%d For=%v body=%q", w.Code, inj.hosts, w.Body.String())
	}
}

func TestTwoPresentedSentinelsSharingRefBothSwap(t *testing.T) {
	live := strings.Repeat("0123456789abcdef", 2)
	sa, sb := "upmo_sentinel_"+strings.Repeat("a", 64), "upmo_sentinel_"+strings.Repeat("b", 64)
	creds := []runtime.Credential{{Sentinel: sa, Ref: "shared", Host: "example.com", Methods: []string{"GET"}, Paths: []string{"/user"}}, {Sentinel: sb, Ref: "shared", Host: "example.com", Methods: []string{"GET"}, Paths: []string{"/user"}}}
	var gotA, gotB string
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotA, gotB = r.Header.Get("Authorization"), r.Header.Get("X-Other")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer up.Close()
	p, _ := proxy(inject.Injector{Resolver: resolvedSecret{"shared": live}}, &dialer{target: up.Listener.Addr().String()})
	p.TLSRootCAs = up.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	p.Book = book{allow: map[string][]string{"box-1": {"example.com"}}, creds: creds}
	r := httptest.NewRequest(http.MethodGet, "http://example.com/user", nil)
	r.RemoteAddr = "203.0.113.9:1234"
	r.Header.Set("Authorization", "Bearer "+sa)
	r.Header.Set("X-Other", "Token "+sb)
	r.Header.Set("Proxy-Authorization", "Basic "+basic("box-1"))
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent || gotA != "Bearer "+live || gotB != "Token "+live {
		t.Fatalf("status=%d Authorization=%q X-Other=%q", w.Code, gotA, gotB)
	}
}

func TestProxyLogsNoSecret(t *testing.T) {
	const live = "ghp_live_secret_406"
	sentinel := "upmo_sentinel_" + strings.Repeat("d", 64)
	cred := runtime.Credential{Sentinel: sentinel, Ref: "github_token", Host: "example.com", Methods: []string{"GET"}, Paths: []string{"/user"}}
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Echo-Auth", r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, "ok")
	}))
	defer up.Close()
	p, logs := proxy(inject.Injector{Resolver: resolvedSecret{cred.Ref: live}}, &dialer{target: up.Listener.Addr().String()})
	p.TLSRootCAs = up.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	p.Book = book{allow: map[string][]string{"box-1": {"example.com"}}, creds: []runtime.Credential{cred}}
	r := httptest.NewRequest(http.MethodGet, "http://example.com/user", nil)
	r.RemoteAddr = "203.0.113.9:1234"
	r.Header.Set("Authorization", "Bearer "+sentinel)
	r.Header.Set("Proxy-Authorization", "Basic "+basic("box-1"))
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(logs.String(), cred.Ref) || strings.Contains(logs.String(), live) || strings.Contains(logs.String(), sentinel) {
		t.Fatalf("status=%d log=%s", w.Code, logs.String())
	}
}

func TestResolvedSecretIsNotLeftInProcessEnv(t *testing.T) {
	const live = "ghp_live_secret_406"
	for _, value := range os.Environ() {
		if strings.Contains(value, live) || strings.HasPrefix(value, "UPMO_MASTER_KEY=") {
			t.Fatalf("credential material remains in process environment")
		}
	}
}

func TestProxyFailsClosedOnRemainingContentEncoding(t *testing.T) {
	const live = "ghp_live_secret_406"
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "br")
		_, _ = io.WriteString(w, "compressed payload "+live)
	}))
	defer up.Close()
	p, _ := proxy(&injector{}, &dialer{target: up.Listener.Addr().String()})
	p.TLSRootCAs = up.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	p.Book = book{allow: map[string][]string{"box-1": {"example.com"}}}
	r := httptest.NewRequest(http.MethodGet, "http://example.com/user", nil)
	r.RemoteAddr = "203.0.113.9:1234"
	r.Header.Set("Proxy-Authorization", "Basic "+basic("box-1"))
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), live) || strings.Contains(w.Body.String(), "compressed payload") {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
}

func TestProxyDropsTrailersAndBoundsGzip(t *testing.T) {
	const live = "ghp_live_secret_406"
	compressed := func(t *testing.T, body string) []byte {
		t.Helper()
		var b bytes.Buffer
		gz := gzip.NewWriter(&b)
		if _, err := io.WriteString(gz, body); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	serve := func(t *testing.T, handler http.HandlerFunc) (*httptest.ResponseRecorder, http.Header) {
		t.Helper()
		sentinel := "upmo_sentinel_" + strings.Repeat("7", 64)
		cred := runtime.Credential{Sentinel: sentinel, Ref: "github_token", Host: "example.com", Methods: []string{"GET"}, Paths: []string{"/user"}}
		up := httptest.NewTLSServer(handler)
		t.Cleanup(up.Close)
		p, _ := proxy(inject.Injector{Resolver: resolvedSecret{cred.Ref: live}}, &dialer{target: up.Listener.Addr().String()})
		p.TLSRootCAs = up.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
		p.Book = book{allow: map[string][]string{"box-1": {"example.com"}}, creds: []runtime.Credential{cred}}
		r := httptest.NewRequest(http.MethodGet, "http://example.com/user", nil)
		r.RemoteAddr = "203.0.113.9:1234"
		r.Header.Set("Authorization", "Bearer "+sentinel)
		r.Header.Set("Proxy-Authorization", "Basic "+basic("box-1"))
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		return w, w.Header()
	}
	t.Run("declared trailer and decoded gzip", func(t *testing.T) {
		body := compressed(t, "echo "+live)
		w, h := serve(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Encoding", "GZIP")
			w.Header().Add("Trailer", "X-Echo-Auth")
			_, _ = w.Write(body)
			w.Header().Set("X-Echo-Auth", live)
		})
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), live) || !strings.Contains(w.Body.String(), "upmo_sentinel_"+strings.Repeat("7", 64)) || h.Get("Content-Encoding") != "" || h.Get("Trailer") != "" || h.Get("X-Echo-Auth") != "" {
			t.Fatalf("status=%d headers=%v body has live=%t", w.Code, h, strings.Contains(w.Body.String(), live))
		}
	})
	t.Run("duplicate gzip", func(t *testing.T) {
		w, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Add("Content-Encoding", "gzip")
			w.Header().Add("Content-Encoding", "gzip")
			_, _ = w.Write(compressed(t, "small"))
		})
		if w.Code != http.StatusBadGateway {
			t.Fatalf("duplicate gzip status=%d", w.Code)
		}
	})
	t.Run("decoded bound", func(t *testing.T) {
		body := compressed(t, strings.Repeat("A", int(MaxResponseBytes+1)))
		w, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write(body)
		})
		if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "upstream_response_too_large") || len(w.Body.Bytes()) != len("upstream_response_too_large\n") {
			t.Fatalf("oversize status=%d body=%q", w.Code, w.Body.String())
		}
	})
	t.Run("compressed input bound", func(t *testing.T) {
		w, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Encoding", "gzip")
			gz := gzip.NewWriter(w)
			if _, err := io.CopyN(gz, rand.Reader, MaxResponseBytes-1); err != nil {
				t.Errorf("write compressed payload: %v", err)
			}
			if err := gz.Close(); err != nil {
				t.Errorf("close gzip: %v", err)
			}
		})
		if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "upstream_response_too_large") {
			t.Fatalf("compressed oversize status=%d body=%q", w.Code, w.Body.String())
		}
	})
}

func TestProxyScrubsAccidentalEcho(t *testing.T) {
	const live = "ghp_live_secret_406"
	sentinel := "upmo_sentinel_" + strings.Repeat("e", 64)
	cred := runtime.Credential{Sentinel: sentinel, Ref: "github_token", Host: "example.com", Methods: []string{"GET"}, Paths: []string{"/user", "/redirect"}}
	var requests int
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path == "/redirect" {
			w.Header().Set("Location", "https://evil.example.com/?leak="+live)
			w.WriteHeader(http.StatusFound)
			return
		}
		w.Header().Set("X-Echo-Auth", r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, live)
	}))
	defer up.Close()
	d := &dialer{target: up.Listener.Addr().String()}
	p, _ := proxy(inject.Injector{Resolver: resolvedSecret{cred.Ref: live}}, d)
	p.TLSRootCAs = up.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	p.Book = book{allow: map[string][]string{"box-1": {"example.com"}}, creds: []runtime.Credential{cred}}
	for _, path := range []string{"/user", "/redirect"} {
		r := httptest.NewRequest(http.MethodGet, "http://example.com"+path, nil)
		r.RemoteAddr = "203.0.113.9:1234"
		r.Header.Set("Authorization", "Bearer "+sentinel)
		r.Header.Set("Proxy-Authorization", "Basic "+basic("box-1"))
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		if w.Code != http.StatusOK && (path != "/redirect" || w.Code != http.StatusFound) {
			t.Fatalf("path %s status=%d", path, w.Code)
		}
		if strings.Contains(w.Body.String(), live) || strings.Contains(w.Header().Get("X-Echo-Auth"), live) || strings.Contains(w.Header().Get("Location"), live) {
			t.Fatalf("secret leaked for %s", path)
		}
		if path == "/user" && (!strings.Contains(w.Body.String(), sentinel) || !strings.Contains(w.Header().Get("X-Echo-Auth"), sentinel)) {
			t.Fatal("echo was not scrubbed to sentinel")
		}
		if path == "/redirect" && !strings.Contains(w.Header().Get("Location"), sentinel) {
			t.Fatal("redirect Location was not scrubbed")
		}
	}
	if requests != 2 || len(d.dialled()) != 2 {
		t.Fatalf("redirect was followed: handlers=%d dials=%v", requests, d.dialled())
	}
}

func TestProxySendsKeyOnlyAfterVerifiedTLS(t *testing.T) {
	const live = "ghp_live_secret_406"
	sentinel := "upmo_sentinel_" + strings.Repeat("f", 64)
	cred := runtime.Credential{Sentinel: sentinel, Ref: ghCred.Ref, Host: "example.com", Methods: []string{"GET"}, Paths: []string{"/user"}}
	hits := 0
	up := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer up.Close()
	p, _ := proxy(inject.Injector{Resolver: resolvedSecret{cred.Ref: live}}, &dialer{target: up.Listener.Addr().String()})
	p.Book = book{allow: map[string][]string{"box-1": {"example.com"}}, creds: []runtime.Credential{cred}}
	r := httptest.NewRequest(http.MethodGet, "http://example.com/user", nil)
	r.RemoteAddr = "203.0.113.9:1234"
	r.Header.Set("Authorization", "Bearer "+sentinel)
	r.Header.Set("Proxy-Authorization", "Basic "+basic("box-1"))
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != http.StatusBadGateway || hits != 0 {
		t.Fatalf("wrong-SAN TLS status=%d handler hits=%d", w.Code, hits)
	}
}

func TestProxyUsesDialAndTestRoots(t *testing.T) {
	const live = "ghp_live_secret_406"
	sentinel := "upmo_sentinel_" + strings.Repeat("1", 64)
	cred := runtime.Credential{Sentinel: sentinel, Ref: ghCred.Ref, Host: "example.com", Methods: []string{"GET"}, Paths: []string{"/user"}}
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+live {
			t.Errorf("authorization=%q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer up.Close()
	d := &dialer{target: up.Listener.Addr().String()}
	p, _ := proxy(inject.Injector{Resolver: resolvedSecret{cred.Ref: live}}, d)
	p.TLSRootCAs = up.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	p.Book = book{allow: map[string][]string{"box-1": {"example.com"}}, creds: []runtime.Credential{cred}}
	r := httptest.NewRequest(http.MethodGet, "http://example.com/user", nil)
	r.RemoteAddr = "203.0.113.9:1234"
	r.Header.Set("Authorization", "Bearer "+sentinel)
	r.Header.Set("Proxy-Authorization", "Basic "+basic("box-1"))
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent || len(d.dialled()) != 1 || d.dialled()[0] != "example.com:443" {
		t.Fatalf("status=%d dials=%v", w.Code, d.dialled())
	}
}

type revokeBook struct {
	mu       sync.Mutex
	cond     *sync.Cond
	ctx      context.Context
	cancel   context.CancelFunc
	inFlight int
	token    uint64
	order    []string
	log      *slog.Logger
}

func newRevokeBook(log *slog.Logger) *revokeBook {
	ctx, cancel := context.WithCancel(context.Background())
	b := &revokeBook{ctx: ctx, cancel: cancel, token: 7, log: log}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func (b *revokeBook) Authenticate(source netip.Addr, secret []byte) (Principal, bool) {
	return Principal{ID: "sb-406", Allow: []string{"github.com"}, Lease: b.token}, source == netip.MustParseAddr("203.0.113.10") && len(secret) == 32
}
func (b *revokeBook) Acquire(id string, token uint64) (func(), context.Context, bool) {
	b.mu.Lock()
	if id != "sb-406" || token == 0 || token != b.token {
		b.mu.Unlock()
		return nil, nil, false
	}
	b.inFlight++
	ctx := b.ctx
	b.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			b.inFlight--
			b.order = append(b.order, "released")
			b.cond.Broadcast()
			b.mu.Unlock()
		})
	}, ctx, true
}
func (b *revokeBook) Pause() error {
	b.mu.Lock()
	b.token = 0
	b.cancel()
	b.order = append(b.order, "canceled")
	b.mu.Unlock()
	timer := time.AfterFunc(RevokeTimeout, func() { b.mu.Lock(); b.cond.Broadcast(); b.mu.Unlock() })
	defer timer.Stop()
	deadline := time.Now().Add(RevokeTimeout)
	b.mu.Lock()
	for b.inFlight > 0 && time.Now().Before(deadline) {
		b.cond.Wait()
	}
	if b.inFlight > 0 {
		b.order = append(b.order, "pause")
		b.mu.Unlock()
		b.log.Error("lease_revoke_timeout", "sandbox", "sb-406", "reason", "lease_revoke_timeout")
		return ErrRevokeTimeout
	}
	b.order = append(b.order, "pause")
	b.mu.Unlock()
	return nil
}

func TestProxyPauseWaitsForHeldAcquire(t *testing.T) {
	for _, tc := range []struct {
		name      string
		release   bool
		wantErr   error
		wantOrder []string
	}{
		{name: "release before timeout", release: true, wantOrder: []string{"canceled", "released", "pause"}},
		{name: "real timeout", wantErr: ErrRevokeTimeout, wantOrder: []string{"canceled", "pause", "released"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = ln.Close() }()
			accepted := make(chan net.Conn, 1)
			go func() {
				c, e := ln.Accept()
				if e == nil {
					accepted <- c
				}
			}()
			var lb logs
			logger := slog.New(slog.NewJSONHandler(&lb, nil))
			b := newRevokeBook(logger)
			p := &Proxy{Book: b, Inject: inject.Injector{Resolver: inject.Refuse{}}, Log: logger}
			entered, continueDial := make(chan struct{}), make(chan struct{})
			p.Dial = func(ctx context.Context, network, address string) (net.Conn, error) {
				close(entered)
				<-continueDial
				var d net.Dialer
				c, e := d.DialContext(ctx, network, ln.Addr().String())
				if e == nil && ctx.Err() != nil {
					_ = c.Close()
					return nil, ctx.Err()
				}
				return c, e
			}
			r := httptest.NewRequest(http.MethodGet, "http://github.com/user", nil)
			r.RemoteAddr = "203.0.113.10:4321"
			r.Header.Set("Proxy-Authorization", "Basic "+basic("sb-406"))
			w := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { p.ServeHTTP(w, r); close(done) }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("dial was not entered")
			}
			pauseDone := make(chan error, 1)
			go func() { pauseDone <- b.Pause() }()
			select {
			case <-b.ctx.Done():
			case <-time.After(time.Second):
				t.Fatal("held context was not canceled")
			}
			if tc.release {
				close(continueDial)
				select {
				case err := <-pauseDone:
					if err != nil {
						t.Fatalf("Pause=%v", err)
					}
				case <-time.After(time.Second):
					t.Fatal("Pause did not wait for release")
				}
			} else {
				select {
				case err := <-pauseDone:
					if !errors.Is(err, tc.wantErr) {
						t.Fatalf("Pause=%v", err)
					}
				case <-time.After(RevokeTimeout + time.Second):
					t.Fatal("Pause did not time out")
				}
				if !strings.Contains(lb.String(), `"msg":"lease_revoke_timeout"`) {
					t.Fatalf("timeout log=%s", lb.String())
				}
				close(continueDial)
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("forward did not finish")
			}
			select {
			case c := <-accepted:
				defer func() { _ = c.Close() }()
				_ = c.SetReadDeadline(time.Now().Add(time.Second))
				payload, _ := io.ReadAll(c)
				if len(payload) != 0 {
					t.Fatalf("parked dial wrote %d upstream bytes", len(payload))
				}
			case <-time.After(time.Second):
			}
			b.mu.Lock()
			order := append([]string(nil), b.order...)
			b.mu.Unlock()
			if len(order) != len(tc.wantOrder) {
				t.Fatalf("order=%v", order)
			}
			for i := range order {
				if order[i] != tc.wantOrder[i] {
					t.Fatalf("order=%v want %v", order, tc.wantOrder)
				}
			}
		})
	}
}

func TestProxyForwardsOnlyPresentedSentinels(t *testing.T) {
	const a, b = "ghp_live_a_406", "ghp_live_b_406"
	sa, sb := "upmo_sentinel_"+strings.Repeat("a", 64), "upmo_sentinel_"+strings.Repeat("b", 64)
	creds := []runtime.Credential{{Sentinel: sa, Ref: "a", Host: "example.com", Methods: []string{"GET"}, Paths: []string{"/user"}}, {Sentinel: sb, Ref: "b", Host: "example.com", Methods: []string{"GET"}, Paths: []string{"/user"}}}
	var got string
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, got)
	}))
	defer up.Close()
	d := &dialer{target: up.Listener.Addr().String()}
	inj := &injector{}
	p, _ := proxy(inj, d)
	p.Inject = inject.Injector{Resolver: resolvedSecret{"a": a}}
	p.TLSRootCAs = up.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	p.Book = book{allow: map[string][]string{"box-1": {"example.com"}}, creds: creds}
	r := httptest.NewRequest(http.MethodGet, "http://example.com/user", nil)
	r.RemoteAddr = "203.0.113.9:1234"
	r.Header.Set("Authorization", "Bearer "+sa)
	r.Header.Set("Proxy-Authorization", "Basic "+basic("box-1"))
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != http.StatusOK || got != "Bearer "+a || strings.Contains(got, b) || !strings.Contains(w.Body.String(), sa) || strings.Contains(w.Body.String(), a) || strings.Contains(w.Body.String(), b) {
		t.Fatalf("status=%d upstream auth=%q body=%q", w.Code, got, w.Body.String())
	}
	if len(d.dialled()) != 1 {
		t.Fatalf("dials=%v", d.dialled())
	}
}

func TestTwoSandboxesCarryDifferentSentinels(t *testing.T) {
	a, err := runtime.NewSentinel()
	if err != nil {
		t.Fatal(err)
	}
	b, err := runtime.NewSentinel()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("sentinels are not unique")
	}
	p, _ := proxy(&injector{}, &dialer{err: errors.New("should not dial")})
	p.Book = book{allow: map[string][]string{"box-1": {"github.com"}}, creds: []runtime.Credential{{Sentinel: a, Ref: "a", Host: "github.com", Methods: []string{"GET"}, Paths: []string{"/user"}}}}
	r := httptest.NewRequest(http.MethodGet, "http://github.com/user", nil)
	r.RemoteAddr = "203.0.113.9:1234"
	r.Header.Set("Authorization", "Bearer "+b)
	r.Header.Set("Proxy-Authorization", "Basic "+basic("box-1"))
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("foreign sentinel status=%d", w.Code)
	}
}

func TestProxyAgreesHostAfterCanonicalParse(t *testing.T) {
	for _, target := range []struct{ url, host string }{
		{"http://API.GITHUB.COM/user", "api.github.com."},
		{"http://api.github.com:443/user", "api.github.com"},
	} {
		d := &dialer{err: errors.New("dial reached canonical admitted host")}
		p, _ := proxy(&injector{}, d)
		p.Book = book{allow: map[string][]string{"box-1": {"api.github.com"}}}
		r := httptest.NewRequest(http.MethodGet, target.url, nil)
		r.Host = target.host
		r.RemoteAddr = "203.0.113.9:1234"
		r.Header.Set("Proxy-Authorization", "Basic "+basic("box-1"))
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		if w.Code != http.StatusBadGateway || len(d.dialled()) != 1 || d.dialled()[0] != "api.github.com:443" {
			t.Errorf("target=%q Host=%q status=%d dial=%v", target.url, target.host, w.Code, d.dialled())
		}
	}
	d := &dialer{}
	p, _ := proxy(&injector{}, d)
	r := httptest.NewRequest(http.MethodGet, "http://[2001:db8::1]/user", nil)
	r.Host = "[2001:0db8:0:0:0:0:0:1]"
	r.RemoteAddr = "203.0.113.9:1234"
	r.Header.Set("Proxy-Authorization", "Basic "+basic("box-1"))
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "address") || len(d.dialled()) != 0 {
		t.Fatalf("IPv6 target status=%d dial=%v", w.Code, d.dialled())
	}
}

func TestProxyRejectsDuplicateAuthorizationAndSplitSentinels(t *testing.T) {
	const live = "ghp_live_secret_406"
	sentinel := "upmo_sentinel_" + strings.Repeat("c", 64)
	cred := runtime.Credential{Sentinel: sentinel, Ref: ghCred.Ref, Host: "example.com", Methods: []string{"GET"}, Paths: []string{"/user"}}
	var upstreamAuth string
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()
	d := &dialer{target: up.Listener.Addr().String()}
	inj := &injector{}
	p, _ := proxy(inj, d)
	p.TLSRootCAs = up.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	p.Book = book{allow: map[string][]string{"box-1": {"example.com"}}, creds: []runtime.Credential{cred}}
	r := httptest.NewRequest(http.MethodGet, "http://example.com/user", nil)
	r.RemoteAddr = "203.0.113.9:1234"
	r.Header.Add("Authorization", "Bearer "+sentinel)
	r.Header.Add("Authorization", "Other")
	r.Header.Set("Proxy-Authorization", "Basic "+basic("box-1"))
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || len(inj.hosts) != 0 {
		t.Fatalf("duplicate Authorization status=%d For=%v", w.Code, inj.hosts)
	}
	before := len(inj.hosts)
	r = httptest.NewRequest(http.MethodGet, "http://example.com/user", nil)
	r.RemoteAddr = "203.0.113.9:1234"
	r.Header.Add("X-Other", sentinel[:31])
	r.Header.Add("X-Other", sentinel[31:])
	r.Header.Set("Proxy-Authorization", "Basic "+basic("box-1"))
	w = httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != http.StatusOK || len(inj.hosts) != before || upstreamAuth != "" || strings.Contains(w.Body.String(), live) {
		t.Fatalf("split sentinel status=%d For=%v auth=%q body=%q", w.Code, inj.hosts, upstreamAuth, w.Body.String())
	}
}

func TestDialFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		code   int
		reason string
	}{
		"unreachable": {errors.New("connection refused"), http.StatusBadGateway, "upstream_dial_failed"},
		"resolved":    {&net.OpError{Op: "dial", Net: "tcp", Err: ErrResolved}, http.StatusForbidden, `"reason":"address"`},
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
	// The proxy username never selects the sandbox identity.
	p.Dial = func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("test dial") }
	if rec := connect(p, "github.com:443", "box-2"); rec.Code != http.StatusBadGateway {
		t.Errorf("username changed valid credentials = %d", rec.Code)
	}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("TRACE", "http://github.com/", nil)
	r.Header.Set("Proxy-Authorization", "Basic "+basic("box-1"))
	p.ServeHTTP(rec, r)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET = %d", rec.Code)
	}
}

func TestProxyRejectsImpersonatedSandbox(t *testing.T) {
	d := &dialer{target: "127.0.0.1:1"}
	p, _ := proxy(inject.Injector{Resolver: inject.Refuse{}}, d)
	p.Book = book{allow: map[string][]string{"box-1": {"github.com"}}, source: netip.MustParseAddr("203.0.113.8")}
	r := httptest.NewRequest(http.MethodConnect, "github.com:443", nil)
	r.RemoteAddr = "203.0.113.9:1200"
	r.Header.Set("Proxy-Authorization", "Basic "+basic("box-2"))
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != http.StatusProxyAuthRequired || len(d.dialled()) != 0 {
		t.Fatalf("status=%d dials=%v", w.Code, d.dialled())
	}
}

func TestRefuseBlocked(t *testing.T) {
	for addr, want := range map[string]bool{
		"140.82.121.4":         true,
		"2606:4700::1111":      true,
		"::ffff:140.82.121.4":  true,
		"127.0.0.1":            false,
		"10.0.0.1":             false,
		"172.17.0.1":           false,
		"192.168.5.2":          false,
		"192.0.2.1":            false,
		"169.254.169.254":      false,
		"100.100.100.200":      false,
		"0.0.0.0":              false,
		"255.255.255.255":      false,
		"224.0.0.1":            false,
		"::1":                  false,
		"fe80::1":              false,
		"fd00::1":              false,
		"::ffff:127.0.0.1":     false,
		"64:ff9b::a9fe:a9fe":   false,
		"64:ff9b::7f00:1":      false,
		"64:ff9b:1::a9fe:a9fe": false,
		"2002:a9fe:a9fe::":     false,
		"198.18.0.1":           false,
	} {
		reason := netguard.BlockedAddr(netip.MustParseAddr(addr))
		if (reason == "") != want {
			t.Errorf("BlockedAddr(%s) = %q, want allowed=%t", addr, reason, want)
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
	dial := (&netguard.Guard{Lookup: func(_ context.Context, _, host string) ([]netip.Addr, error) {
		if host == "localhost" {
			host = "127.0.0.1"
		}
		return []netip.Addr{netip.MustParseAddr(host)}, nil
	}}).Dial
	for _, host := range []string{"127.0.0.1", "localhost"} {
		c, err := dial(context.Background(), "tcp", net.JoinHostPort(host, port))
		if err == nil {
			_ = c.Close()
			t.Fatalf("%s: dialled a loopback address", host)
		}
		if !errors.Is(err, netguard.ErrBlocked) {
			t.Fatalf("%s: err = %v, want netguard.ErrBlocked", host, err)
		}
	}
	for _, ip := range []string{"192.0.2.1", "64:ff9b::7f00:1"} {
		if reason := netguard.BlockedAddr(netip.MustParseAddr(ip)); reason == "" {
			t.Errorf("BlockedAddr(%s) allowed a non-public address", ip)
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
