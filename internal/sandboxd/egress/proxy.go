// Package egress is the only network path available to a sandbox.
package egress

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/upmo-ai/upmo/internal/platform/netguard"
	"github.com/upmo-ai/upmo/internal/runtime"
	"github.com/upmo-ai/upmo/internal/sandboxd/inject"
)

const (
	DialTimeout                 = 10 * time.Second
	MaxResponseBytes      int64 = 32 << 20
	MaxConcurrentForwards       = 4
	RevokeTimeout               = 2 * time.Second
	defaultPort                 = "443"
	established                 = "HTTP/1.1 200 Connection Established\r\n\r\n"
)

var (
	ErrResolved      = errors.New("egress: name resolved to a non-public address")
	ErrRevokeTimeout = errors.New("egress: revoke timed out")
	sentinelPattern  = regexp.MustCompile(`upmo_sentinel_[0-9a-f]{64}`)
)

type Principal struct {
	ID    string
	Allow []string
	Creds []runtime.Credential
	Lease uint64
}

type Book interface {
	Authenticate(source netip.Addr, secret []byte) (Principal, bool)
	Acquire(id string, token uint64) (release func(), ctx context.Context, ok bool)
}

type Injector interface {
	For(ctx context.Context, sandboxID string, proxySecret []byte, host string, creds []runtime.Credential) (inject.Rewrite, error)
}

type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

type Proxy struct {
	Book       Book
	Inject     Injector
	Log        *slog.Logger
	Dial       DialFunc
	TLSRootCAs *x509.CertPool
	semOnce    sync.Once
	sem        chan struct{}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	src, _ := remoteIP(r.RemoteAddr)
	_, pass, hasAuth := proxyAuth(r)
	secret, decodeErr := base64.RawURLEncoding.DecodeString(pass)
	if !hasAuth || decodeErr != nil || len(secret) != 32 {
		clear(secret)
		secret = nil
	}
	defer clear(secret)
	principal, ok := p.Book.Authenticate(src, secret)
	if !ok {
		w.Header().Set("Proxy-Authenticate", `Basic realm="sandboxd"`)
		p.log(r, "", "", "", "refuse", "proxy_auth", nil)
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
		return
	}
	if r.Method == http.MethodConnect {
		p.connect(w, r, principal)
		return
	}
	p.forward(w, r, principal, secret)
}

func (p *Proxy) connect(w http.ResponseWriter, r *http.Request, principal Principal) {
	if r.RequestURI == "" || r.RequestURI != r.Host || strings.ContainsAny(r.RequestURI, "/?#@") || strings.Contains(r.RequestURI, "://") {
		p.reject(w, r, principal, "", "", "malformed", http.StatusBadRequest)
		return
	}
	decision, err := runtime.AdmitEgress(r.Host, principal.Allow)
	if err != nil {
		p.reject(w, r, principal, "", "", "malformed", http.StatusBadRequest)
		return
	}
	note, err := runtime.Note(decision)
	if err != nil || note.Verdict != runtime.VerdictAllow {
		reason := "refused"
		if err == nil && note.Reason != "" {
			reason = note.Reason
		}
		p.reject(w, r, principal, "", "", reason, http.StatusForbidden)
		return
	}
	for _, c := range principal.Creds {
		if c.Host == note.Host {
			p.reject(w, r, principal, note.Host, note.Port, "denied_connect_credential", http.StatusForbidden)
			return
		}
	}
	port := note.Port
	if port == "" {
		port = defaultPort
	}
	release, held, ok := p.Book.Acquire(principal.ID, principal.Lease)
	if !ok {
		p.reject(w, r, principal, note.Host, port, "denied_revoked", http.StatusForbidden)
		return
	}
	defer release()
	tunnelCtx, tunnelCancel := context.WithCancel(held)
	stop := context.AfterFunc(r.Context(), tunnelCancel)
	defer func() { stop(); tunnelCancel() }()
	dialCtx, dialCancel := context.WithTimeout(tunnelCtx, DialTimeout)
	dial := p.dial()
	upstream, err := dial(dialCtx, "tcp", net.JoinHostPort(note.Host, port))
	dialErr := dialCtx.Err()
	dialCancel()
	if err != nil || dialErr != nil || tunnelCtx.Err() != nil {
		if upstream != nil {
			_ = upstream.Close()
		}
		if errors.Is(err, ErrResolved) || errors.Is(err, netguard.ErrBlocked) {
			p.reject(w, r, principal, note.Host, port, "address", http.StatusForbidden)
			return
		}
		p.reject(w, r, principal, note.Host, port, "upstream_dial_failed", http.StatusBadGateway)
		return
	}
	p.log(r, principal.ID, note.Host, port, "allow", "", nil)
	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(w, "tunnel unavailable", http.StatusInternalServerError)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	_ = client.SetDeadline(time.Time{})
	if _, err := io.WriteString(client, established); err != nil {
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	if n := buf.Reader.Buffered(); n > 0 {
		b, _ := buf.Peek(n)
		if _, err := upstream.Write(b); err != nil {
			_ = client.Close()
			_ = upstream.Close()
			return
		}
	}
	Relay(tunnelCtx, client, upstream)
}

func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, principal Principal, secret []byte) {
	if !slices.Contains([]string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}, r.Method) {
		p.reject(w, r, principal, "", "", "method_not_allowed", http.StatusMethodNotAllowed)
		return
	}
	target, err := url.ParseRequestURI(r.RequestURI)
	if err != nil || target.Scheme != "http" || target.Host == "" || target.User != nil || target.Fragment != "" || target.Opaque != "" {
		p.reject(w, r, principal, "", "", "malformed", http.StatusBadRequest)
		return
	}
	if !sameTarget(target, r.URL, r.Host) {
		p.reject(w, r, principal, "", "", "target_mismatch", http.StatusBadRequest)
		return
	}
	decision, err := runtime.AdmitEgress(target.Host, principal.Allow)
	if err != nil {
		p.reject(w, r, principal, "", "", "malformed", http.StatusBadRequest)
		return
	}
	note, err := runtime.Note(decision)
	if err != nil || note.Verdict != runtime.VerdictAllow {
		reason := "refused"
		if err == nil && note.Reason != "" {
			reason = note.Reason
		}
		p.reject(w, r, principal, "", "", reason, http.StatusForbidden)
		return
	}
	port := note.Port
	if port == "" {
		port = defaultPort
	}
	if port != "443" {
		p.reject(w, r, principal, note.Host, port, "denied_credential_port", http.StatusForbidden)
		return
	}
	rawPath := target.EscapedPath()
	if rawPath == "" {
		rawPath = "/"
	}
	if !canonicalPath(rawPath, target.Path) || target.RawPath != "" || r.URL.RawPath != "" {
		p.reject(w, r, principal, note.Host, port, "denied_credential_path", http.StatusBadRequest)
		return
	}
	if sentinelPattern.MatchString(rawPath) || sentinelPattern.MatchString(target.RawQuery) {
		p.reject(w, r, principal, note.Host, port, "denied_sentinel_in_target", http.StatusForbidden)
		return
	}
	query, err := url.QueryUnescape(target.RawQuery)
	if err != nil {
		p.reject(w, r, principal, note.Host, port, "malformed", http.StatusBadRequest)
		return
	}
	if sentinelPattern.MatchString(query) {
		p.reject(w, r, principal, note.Host, port, "denied_sentinel_in_target", http.StatusForbidden)
		return
	}
	forwarded := stripHeaders(r.Header)
	selected, refs, reason := selectCredentials(forwarded, principal.Creds, note.Host, r.Method, target.Path)
	if reason != "" {
		p.reject(w, r, principal, note.Host, port, reason, http.StatusForbidden)
		return
	}
	p.semOnce.Do(func() { p.sem = make(chan struct{}, MaxConcurrentForwards) })
	select {
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
	default:
		p.reject(w, r, principal, note.Host, port, "proxy_busy", http.StatusServiceUnavailable)
		return
	}
	release, held, ok := p.Book.Acquire(principal.ID, principal.Lease)
	if !ok {
		p.reject(w, r, principal, note.Host, port, "denied_revoked", http.StatusForbidden)
		return
	}
	defer release()
	ctx, cancel := context.WithCancel(held)
	stop := context.AfterFunc(r.Context(), cancel)
	defer func() { stop(); cancel() }()
	var rw inject.Rewrite
	if len(selected) > 0 {
		if p.Inject == nil {
			p.reject(w, r, principal, note.Host, port, "denied_credential_unresolved", http.StatusBadGateway)
			return
		}
		rw, err = p.Inject.For(ctx, principal.ID, secret, note.Host, selected)
		if err != nil {
			p.reject(w, r, principal, note.Host, port, "denied_credential_unresolved", http.StatusBadGateway, refs)
			return
		}
	}
	// The allow-list decision and guarded dial constrain this request target.
	out, err := http.NewRequestWithContext(ctx, r.Method, "https://"+net.JoinHostPort(note.Host, "443")+target.RequestURI(), r.Body) // #nosec G704
	if err != nil {
		p.reject(w, r, principal, note.Host, port, "malformed", http.StatusBadRequest)
		return
	}
	out.URL.Host = note.Host
	out.Host = note.Host
	out.Header = forwarded
	out.Header = rw.Apply(out.Header)
	out.Header.Set("Accept-Encoding", "identity")
	tr := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{RootCAs: p.TLSRootCAs, ServerName: note.Host, MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second, IdleConnTimeout: 90 * time.Second}
	defer tr.CloseIdleConnections()
	baseDial := p.dial()
	var dialMu sync.Mutex
	dialStarted := false
	dialDone := make(chan struct{})
	var dialOnce sync.Once
	tr.DialContext = func(c context.Context, network, address string) (net.Conn, error) {
		dialMu.Lock()
		dialStarted = true
		dialMu.Unlock()
		defer dialOnce.Do(func() { close(dialDone) })
		conn, err := baseDial(c, network, net.JoinHostPort(note.Host, "443"))
		if ctx.Err() != nil {
			if conn != nil {
				_ = conn.Close()
			}
			return nil, ctx.Err()
		}
		return conn, err
	}
	res, err := tr.RoundTrip(out)
	if err != nil {
		if ctx.Err() != nil {
			dialMu.Lock()
			started := dialStarted
			dialMu.Unlock()
			if started {
				<-dialDone
			}
		}
		p.reject(w, r, principal, note.Host, port, "upstream_dial_failed", http.StatusBadGateway, refs)
		return
	}
	defer func() { _ = res.Body.Close() }()
	body, encoding, err := readResponse(res)
	if err != nil {
		p.reject(w, r, principal, note.Host, port, err.Error(), http.StatusBadGateway, refs)
		return
	}
	res.Header.Del("Trailer")
	res.Trailer = nil
	res.Header = rw.ScrubHeader(res.Header)
	body = rw.Scrub(body)
	res.Header.Set("Content-Length", strconv.Itoa(len(body)))
	for k, values := range res.Header {
		for _, value := range values {
			w.Header().Add(k, value)
		}
	}
	if encoding {
		w.Header().Del("Content-Encoding")
	}
	w.WriteHeader(res.StatusCode)
	_, _ = w.Write(body)
	p.log(r, principal.ID, note.Host, port, "allow", "", refs)
}

func readResponse(res *http.Response) ([]byte, bool, error) {
	var gzipEncoding bool
	var enc []string
	for _, line := range res.Header.Values("Content-Encoding") {
		for _, part := range strings.Split(line, ",") {
			if v := strings.TrimSpace(part); v != "" {
				enc = append(enc, strings.ToLower(v))
			}
		}
	}
	raw := &io.LimitedReader{R: res.Body, N: MaxResponseBytes + 1}
	reader := io.Reader(raw)
	if len(enc) > 1 || len(enc) == 1 && enc[0] != "gzip" {
		return nil, false, errors.New("upstream_content_encoding")
	}
	if len(enc) == 1 {
		gz, err := gzip.NewReader(reader)
		if err != nil {
			return nil, false, errors.New("upstream_content_encoding")
		}
		defer func() { _ = gz.Close() }()
		reader = io.LimitReader(gz, MaxResponseBytes+1)
		gzipEncoding = true
	}
	body, err := io.ReadAll(reader)
	if gzipEncoding && raw.N == 0 {
		return nil, false, errors.New("upstream_response_too_large")
	}
	if err != nil {
		if gzipEncoding {
			return nil, false, errors.New("upstream_content_encoding")
		}
		return nil, false, errors.New("upstream_read_failed")
	}
	if int64(len(body)) > MaxResponseBytes {
		return nil, false, errors.New("upstream_response_too_large")
	}
	return body, gzipEncoding, nil
}

func selectCredentials(headers http.Header, creds []runtime.Credential, host, method, requestPath string) ([]runtime.Credential, []string, string) {
	var found []string
	for _, values := range headers {
		for _, value := range values {
			if strings.HasPrefix(strings.ToLower(value), "basic ") {
				if raw, err := base64.StdEncoding.DecodeString(value[6:]); err == nil {
					found = append(found, sentinelPattern.FindAllString(string(raw), -1)...)
					continue
				}
			}
			found = append(found, sentinelPattern.FindAllString(value, -1)...)
		}
	}
	if len(headers.Values("Authorization")) > 1 && len(found) > 0 {
		return nil, nil, "denied_duplicate_authorization"
	}
	var selected []runtime.Credential
	var refs []string
	for _, sentinel := range found {
		var c *runtime.Credential
		for i := range creds {
			if creds[i].Sentinel == sentinel {
				c = &creds[i]
				break
			}
		}
		if c == nil {
			return nil, nil, "denied_wrong_sentinel"
		}
		if c.Host != host {
			return nil, nil, "denied_credential_host_mismatch"
		}
		if !slices.Contains(c.Methods, method) {
			return nil, nil, "denied_credential_method"
		}
		if !pathAllowed(c.Paths, requestPath) {
			return nil, nil, "denied_credential_path"
		}
		if !slices.ContainsFunc(selected, func(selected runtime.Credential) bool { return selected.Sentinel == c.Sentinel }) {
			selected = append(selected, *c)
		}
		if !slices.Contains(refs, c.Ref) {
			refs = append(refs, c.Ref)
		}
	}
	hasHost, anyMethod, anyMethodAndPath := false, false, false
	for _, c := range creds {
		if c.Host == host {
			hasHost = true
			if slices.Contains(c.Methods, method) {
				anyMethod = true
				if pathAllowed(c.Paths, requestPath) {
					anyMethodAndPath = true
				}
			}
		}
	}
	if len(selected) == 0 && len(found) == 0 && hasHost && !anyMethodAndPath {
		if !anyMethod {
			return nil, nil, "denied_credential_method"
		}
		return nil, nil, "denied_credential_path"
	}
	return selected, refs, ""
}

func pathAllowed(paths []string, p string) bool {
	for _, prefix := range paths {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}
func canonicalPath(raw, decoded string) bool {
	return strings.HasPrefix(raw, "/") && !strings.ContainsAny(raw, "\\%") && !strings.Contains(raw, "..") && !strings.Contains(raw, "//") && path.Clean(decoded) == decoded
}
func sameTarget(target, parsed *url.URL, host string) bool {
	if parsed == nil || host == "" || target.Path != parsed.Path || target.RawPath != parsed.RawPath || target.RawQuery != parsed.RawQuery {
		return false
	}
	targetHost, err := runtime.AdmitEgress(target.Host, nil)
	if err != nil {
		return false
	}
	requestHost, err := runtime.AdmitEgress(host, nil)
	if err != nil {
		return false
	}
	if targetHost.Port == "" {
		targetHost.Port = defaultPort
	}
	if requestHost.Port == "" {
		requestHost.Port = defaultPort
	}
	if targetHost.Port != requestHost.Port {
		return false
	}
	if targetHost.Host == requestHost.Host {
		return true
	}
	targetIP, targetErr := netip.ParseAddr(targetHost.Host)
	requestIP, requestErr := netip.ParseAddr(requestHost.Host)
	return targetErr == nil && requestErr == nil && targetIP.Unmap() == requestIP.Unmap()
}
func stripHeaders(in http.Header) http.Header {
	out := in.Clone()
	for _, line := range out.Values("Connection") {
		for _, token := range strings.Split(line, ",") {
			out.Del(strings.TrimSpace(token))
		}
	}
	for k := range out {
		lower := strings.ToLower(k)
		if strings.HasPrefix(lower, "proxy-") || lower == "connection" || lower == "keep-alive" || lower == "te" || lower == "trailer" || lower == "transfer-encoding" || lower == "upgrade" || lower == "host" || lower == "forwarded" || strings.HasPrefix(lower, "x-forwarded-") || lower == "x-upmo-sandbox" {
			out.Del(k)
		}
	}
	return out
}
func remoteIP(remote string) (netip.Addr, bool) {
	if ap, err := netip.ParseAddrPort(remote); err == nil {
		return ap.Addr().Unmap(), true
	}
	ip, err := netip.ParseAddr(remote)
	return ip.Unmap(), err == nil
}
func proxyAuth(r *http.Request) (string, string, bool) {
	values := r.Header.Values("Proxy-Authorization")
	if len(values) != 1 {
		return "", "", false
	}
	q := &http.Request{Header: http.Header{"Authorization": {values[0]}}}
	return q.BasicAuth()
}
func (p *Proxy) dial() DialFunc {
	if p.Dial != nil {
		return p.Dial
	}
	g := &netguard.Guard{}
	return g.Dial
}
func (p *Proxy) reject(w http.ResponseWriter, r *http.Request, principal Principal, host, port, reason string, status int, refs ...[]string) {
	var rs []string
	if len(refs) > 0 {
		rs = refs[0]
	}
	p.log(r, principal.ID, host, port, "refuse", reason, rs)
	http.Error(w, reason, status)
}
func (p *Proxy) log(r *http.Request, id, host, port, verdict, reason string, refs []string) {
	if p.Log == nil {
		return
	}
	requestPath := r.URL.Path
	if sentinelPattern.MatchString(requestPath) {
		requestPath = ""
	}
	p.Log.Info("egress", "sandbox", id, "host", host, "port", port, "method", r.Method, "path", requestPath, "verdict", verdict, "reason", reason, "credential_refs", refs)
}

// Relay copies both directions until either side closes or ctx ends.
func Relay(ctx context.Context, a, b net.Conn) (int64, int64) {
	var once sync.Once
	closeBoth := func() { once.Do(func() { _ = a.Close(); _ = b.Close() }) }
	stop := context.AfterFunc(ctx, closeBoth)
	defer stop()
	defer closeBoth()
	var ab, ba int64
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); ab = pipe(b, a) }()
	go func() { defer wg.Done(); ba = pipe(a, b) }()
	wg.Wait()
	return ab, ba
}
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

// AuthenticateSource is kept for tests that pin the SHA-256 comparison contract.
func AuthenticateSource(secret []byte, expected [32]byte) bool {
	actual := sha256.Sum256(secret)
	return subtle.ConstantTimeCompare(actual[:], expected[:]) == 1
}
