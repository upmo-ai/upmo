package secretchain

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/platform/netguard"
)

// systemCA is the CA the test process treats as a system root, so a
// certificate the system roots would verify is still refused when
// UPMO_TLS_CA_FILE names another CA. TestMain points SSL_CERT_FILE at it
// before any test runs.
var systemCA = mustCA()

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "secretchain-roots")
	if err != nil {
		panic(err)
	}
	path := filepath.Join(dir, "system-roots.pem")
	if err := os.WriteFile(path, systemCA.pem, 0o600); err != nil {
		panic(err)
	}
	if err := os.Setenv("SSL_CERT_FILE", path); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func TestVaultHTTPReadsValue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != "tok" {
			t.Errorf("token = %q", r.Header.Get("X-Vault-Token"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"data": map[string]any{"value": "secret"}},
		})
	}))
	defer srv.Close()
	got, err := (VaultHTTP{Development: true}).Fetch(srv.URL, "tok", "secret/data", "house")
	if err != nil || got != "secret" {
		t.Fatalf("got %q %v", got, err)
	}
}

// The guard refuses the metadata address before any byte is sent: directly,
// and through a name the test resolves to it.
func TestVaultHTTPDialsThroughTheGuard(t *testing.T) {
	_, err := (VaultHTTP{}).Fetch("http://169.254.169.254", "hvs.test", "secret/data", "house")
	if err == nil || err.Error() != "secretchain: UPMO_VAULT_ADDR must be https when UPMO_ENV=production" {
		t.Fatalf("production err = %v", err)
	}
	_, err = (VaultHTTP{Development: true}).Fetch("http://169.254.169.254", "hvs.test", "secret/data", "house")
	if err == nil || err.Error() != "secretchain: UPMO_VAULT_ADDR must be https unless the host is loopback or one DNS label" {
		t.Fatalf("development plaintext err = %v", err)
	}
	_, err = (VaultHTTP{Development: true}).Fetch("https://169.254.169.254", "hvs.test", "secret/data", "house")
	if !errors.Is(err, netguard.ErrBlocked) {
		t.Fatalf("development https err = %v, want netguard.ErrBlocked", err)
	}
	lookup := staticLookup(map[string]string{"vault": "169.254.169.254"})
	_, err = (VaultHTTP{Development: true, Lookup: lookup}).Fetch("http://vault", "hvs.test", "secret/data", "house")
	if !errors.Is(err, netguard.ErrBlocked) {
		t.Fatalf("resolved err = %v, want netguard.ErrBlocked", err)
	}
}

// A 302 is a response, not a hop: the token never leaves the named address,
// not for a local listener and not for the metadata address.
func TestVaultHTTPDoesNotFollowRedirects(t *testing.T) {
	records := newRequestRecorder()
	meta := serveHTTP(t, tls.Certificate{}, records.handler())
	vault := serveHTTP(t, tls.Certificate{}, redirectHandler(fmt.Sprintf("http://meta:%d", meta.port())))
	_, err := (VaultHTTP{Development: true, Lookup: localLookup}).Fetch(fmt.Sprintf("http://vault:%d", vault.port()), "hvs.test", "secret/data", "house")
	if !errors.Is(err, ErrMissing) {
		t.Fatalf("meta redirect err = %v, want ErrMissing", err)
	}
	if n := meta.accepted(); n != 0 {
		t.Fatalf("meta accepted %d connections", n)
	}
	if records.token("hvs.test") {
		t.Fatal("the vault token reached the meta listener")
	}

	vault = serveHTTP(t, tls.Certificate{}, redirectHandler("http://169.254.169.254"))
	_, err = (VaultHTTP{Development: true, Lookup: localLookup}).Fetch(fmt.Sprintf("http://vault:%d", vault.port()), "hvs.test", "secret/data", "house")
	if !errors.Is(err, ErrMissing) {
		t.Fatalf("metadata redirect err = %v, want ErrMissing", err)
	}
}

// Production refuses plaintext on any host, one DNS label included, before a
// byte is sent; the listener count proves it. FromEnv enforces the same rule
// with UPMO_ENV unset (production by default).
func TestVaultHTTPRefusesPlaintextInProduction(t *testing.T) {
	vault := serveHTTP(t, tls.Certificate{}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the production plaintext request was sent")
	}))
	_, err := (VaultHTTP{Lookup: localLookup}).Fetch(fmt.Sprintf("http://vault:%d", vault.port()), "hvs.test", "secret/data", "house")
	if err == nil || err.Error() != "secretchain: UPMO_VAULT_ADDR must be https when UPMO_ENV=production" {
		t.Fatalf("err = %v", err)
	}
	if n := vault.accepted(); n != 0 {
		t.Fatalf("vault accepted %d connections", n)
	}
}

// Production trusts the CA file: a certificate it signed returns the secret,
// a certificate another CA signed is refused with the CA file named.
func TestVaultHTTPTrustsOnlyTheCAFile(t *testing.T) {
	caA := mustCA()
	caFile := writeCA(t, caA)
	body := KVBody("ok")

	vault := serveHTTP(t, mustCert(t, caA, "vault"), body)
	got, err := (VaultHTTP{CAFile: caFile, Lookup: localLookup}).Fetch(fmt.Sprintf("https://vault:%d", vault.port()), "hvs.test", "secret/data", "house")
	if err != nil || got != "ok" {
		t.Fatalf("got %q %v", got, err)
	}

	other := serveHTTP(t, mustCert(t, systemCA, "vault"), body)
	_, err = (VaultHTTP{CAFile: caFile, Lookup: localLookup}).Fetch(fmt.Sprintf("https://vault:%d", other.port()), "hvs.test", "secret/data", "house")
	if err == nil || !strings.Contains(err.Error(), "secretchain: Vault certificate is not trusted by UPMO_TLS_CA_FILE") {
		t.Fatalf("untrusted err = %v", err)
	}
}

// Production without a usable CA file fails closed before the dial: no file
// and a missing file are the same refusal, and no byte is sent.
func TestVaultHTTPFailsClosedWithoutACAFile(t *testing.T) {
	vault := serveHTTP(t, mustCert(t, mustCA(), "vault"), KVBody("ok"))
	for _, caFile := range []string{"", filepath.Join(t.TempDir(), "missing.pem")} {
		_, err := (VaultHTTP{CAFile: caFile, Lookup: localLookup}).Fetch(fmt.Sprintf("https://vault:%d", vault.port()), "hvs.test", "secret/data", "house")
		if err == nil || err.Error() != "tlscheck: UPMO_TLS_CA_FILE must name a readable PEM CA file" {
			t.Fatalf("caFile %q: err = %v", caFile, err)
		}
		if n := vault.accepted(); n != 0 {
			t.Fatalf("caFile %q: vault accepted %d connections", caFile, n)
		}
	}
}

// A certificate the system roots verify (SSL_CERT_FILE holds its CA) is still
// refused when UPMO_TLS_CA_FILE names another CA.
func TestVaultHTTPDoesNotTrustSystemRoots(t *testing.T) {
	caA := mustCA()
	caFile := writeCA(t, caA)
	leaf := mustCert(t, systemCA, "vault")
	pool, err := x509.SystemCertPool()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leafCert(t, leaf).Verify(x509.VerifyOptions{Roots: pool}); err != nil {
		t.Fatalf("precondition failed: the server certificate does not verify against the system roots: %v", err)
	}
	vault := serveHTTP(t, leaf, KVBody("ok"))
	_, err = (VaultHTTP{CAFile: caFile, Lookup: localLookup}).Fetch(fmt.Sprintf("https://vault:%d", vault.port()), "hvs.test", "secret/data", "house")
	if err == nil || !strings.Contains(err.Error(), "secretchain: Vault certificate is not trusted by UPMO_TLS_CA_FILE") {
		t.Fatalf("err = %v", err)
	}
}

// Development keeps the single-label exemption: http to one label, no CA
// file, still returns the listener's value.
func TestVaultHTTPDevelopmentKeepsPlaintextExemption(t *testing.T) {
	vault := serveHTTP(t, tls.Certificate{}, KVBody("dev-ok"))
	got, err := (VaultHTTP{Development: true, Lookup: localLookup}).Fetch(fmt.Sprintf("http://vault:%d", vault.port()), "hvs.test", "secret/data", "house")
	if err != nil || got != "dev-ok" {
		t.Fatalf("got %q %v", got, err)
	}
}

// localLookup names the test hosts on the loopback listener.
var localLookup = staticLookup(map[string]string{"vault": "127.0.0.1", "meta": "127.0.0.1"})

func staticLookup(mapping map[string]string) func(context.Context, string, string) ([]netip.Addr, error) {
	return func(_ context.Context, _, host string) ([]netip.Addr, error) {
		if ip, ok := mapping[host]; ok {
			return []netip.Addr{netip.MustParseAddr(ip)}, nil
		}
		return nil, fmt.Errorf("no address for %q", host)
	}
}

// KVBody is the Vault KV v2 reply that carries value.
func KVBody(value string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"data": map[string]any{"value": value}},
		})
	})
}

func redirectHandler(location string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", location)
		w.WriteHeader(http.StatusFound)
	})
}

// testServer serves handler over TLS (plain when cert is empty) on
// 127.0.0.1:0 and counts accepted connections, so a test can prove no byte
// was sent.
type testServer struct {
	listener *countingListener
}

func (s testServer) port() int { return s.listener.Addr().(*net.TCPAddr).Port }

func (s testServer) accepted() int32 { return s.listener.accepted.Load() }

type countingListener struct {
	net.Listener
	accepted atomic.Int32
}

func (l *countingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		l.accepted.Add(1)
	}
	return conn, err
}

func serveHTTP(t *testing.T, cert tls.Certificate, handler http.Handler) testServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cl := &countingListener{Listener: listener}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	if len(cert.Certificate) > 0 {
		srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
		go func() { _ = srv.ServeTLS(cl, "", "") }()
	} else {
		go func() { _ = srv.Serve(cl) }()
	}
	t.Cleanup(func() { _ = srv.Close() })
	return testServer{listener: cl}
}

// requestRecorder records the tokens requests carried, so a test can prove a
// token never reached a listener it should not.
type requestRecorder struct {
	mu     sync.Mutex
	tokens []string
}

func newRequestRecorder() *requestRecorder { return &requestRecorder{} }

func (r *requestRecorder) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.tokens = append(r.tokens, req.Header.Get("X-Vault-Token"))
		r.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
}

func (r *requestRecorder) token(want string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, token := range r.tokens {
		if token == want {
			return true
		}
	}
	return false
}

// testCA is a self-signed CA the behavior tests issue server certificates
// from.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func mustCA() testCA {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "upmo test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		panic(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		panic(err)
	}
	return testCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// mustCert issues a server certificate for names from ca. A name that is an
// IP address becomes an IP SAN; any other is a DNS name.
func mustCert(t *testing.T, ca testCA, names ...string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, name := range names {
		if ip, err := netip.ParseAddr(name); err == nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip.AsSlice())
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, name)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// leafCert parses the certificate of a tls.Certificate.
func leafCert(t *testing.T, cert tls.Certificate) *x509.Certificate {
	t.Helper()
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}

func writeCA(t *testing.T, ca testCA) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, ca.pem, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
