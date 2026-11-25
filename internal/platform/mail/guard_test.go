package mail

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/netip"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/platform/netguard"
)

// systemCA is the CA the test process treats as a system root, so a relay
// certificate the system roots would verify is still refused when
// UPMO_TLS_CA_FILE names another CA. TestMain points SSL_CERT_FILE at it
// before any test runs.
var systemCA = mustCA()

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mail-roots")
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

// The guard refuses the metadata address before any byte is sent: directly,
// and through a name the test resolves to it.
func TestSMTPDialsThroughTheGuard(t *testing.T) {
	caFile := writeCA(t, mustCA())
	link := "https://upmo.test/login/link#x"
	err := (SMTP{Host: "169.254.169.254", Port: 25, From: "noreply@upmo.test", Development: true}).SendInvite(context.Background(), "ada@example.com", link)
	if !errors.Is(err, netguard.ErrBlocked) {
		t.Fatalf("development err = %v, want netguard.ErrBlocked", err)
	}
	err = (SMTP{Host: "169.254.169.254", Port: 25, From: "noreply@upmo.test", CAFile: caFile}).SendInvite(context.Background(), "ada@example.com", link)
	if !errors.Is(err, netguard.ErrBlocked) {
		t.Fatalf("production err = %v, want netguard.ErrBlocked", err)
	}
	lookup := staticLookup(map[string]string{"smtp": "169.254.169.254"})
	err = (SMTP{Host: "smtp", Port: 25, From: "noreply@upmo.test", Development: true, Lookup: lookup}).SendInvite(context.Background(), "ada@example.com", link)
	if !errors.Is(err, netguard.ErrBlocked) {
		t.Fatalf("development resolved err = %v, want netguard.ErrBlocked", err)
	}
	err = (SMTP{Host: "smtp", Port: 25, From: "noreply@upmo.test", CAFile: caFile, Lookup: lookup}).SendInvite(context.Background(), "ada@example.com", link)
	if !errors.Is(err, netguard.ErrBlocked) {
		t.Fatalf("production resolved err = %v, want netguard.ErrBlocked", err)
	}
}

// Production without a usable CA file fails closed before the dial: no file
// and a missing file are the same refusal, and no byte is sent.
func TestSMTPFailsClosedWithoutACAFile(t *testing.T) {
	relay := startGuardedRelay(t, mustCert(t, mustCA(), "smtp"))
	for _, caFile := range []string{"", filepath.Join(t.TempDir(), "missing.pem")} {
		sender := SMTP{Host: "smtp", Port: relay.port(), From: "noreply@upmo.test", CAFile: caFile, Lookup: localLookup}
		err := sender.SendInvite(context.Background(), "ada@example.com", "https://upmo.test/login/link#x")
		if err == nil || err.Error() != "mail: tlscheck: UPMO_TLS_CA_FILE must name a readable PEM CA file" {
			t.Fatalf("caFile %q: err = %v", caFile, err)
		}
		if n := relay.accepted(); n != 0 {
			t.Fatalf("caFile %q: relay accepted %d connections", caFile, n)
		}
	}
}

// Production completes STARTTLS against the CA file: a certificate it signed
// delivers the message, a certificate another CA signed is refused with the
// CA file named.
func TestSMTPSTARTTLSTrustsOnlyTheCAFile(t *testing.T) {
	ca := mustCA()
	caFile := writeCA(t, ca)
	link := "https://upmo.test/login/link#upmo_link_abc"

	relay := startGuardedRelay(t, mustCert(t, ca, "smtp"))
	sender := SMTP{Host: "smtp", Port: relay.port(), From: "noreply@upmo.test", CAFile: caFile, Lookup: localLookup}
	if err := sender.SendInvite(context.Background(), "ada@example.com", link); err != nil {
		t.Fatal(err)
	}
	got := <-relay.got
	if got.to != "<ada@example.com>" || !strings.Contains(got.data, link) {
		t.Fatalf("message = %+v", got)
	}

	other := startGuardedRelay(t, mustCert(t, systemCA, "smtp"))
	sender = SMTP{Host: "smtp", Port: other.port(), From: "noreply@upmo.test", CAFile: caFile, Lookup: localLookup}
	err := sender.SendInvite(context.Background(), "ada@example.com", link)
	if err == nil || !strings.Contains(err.Error(), "mail: starttls: the relay certificate is not trusted by UPMO_TLS_CA_FILE") {
		t.Fatalf("untrusted err = %v", err)
	}
}

// A relay certificate the system roots verify (SSL_CERT_FILE holds its CA) is
// still refused when UPMO_TLS_CA_FILE names another CA.
func TestSMTPDoesNotTrustSystemRoots(t *testing.T) {
	caFile := writeCA(t, mustCA())
	leaf := mustCert(t, systemCA, "smtp")
	pool, err := x509.SystemCertPool()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leafCert(t, leaf).Verify(x509.VerifyOptions{Roots: pool}); err != nil {
		t.Fatalf("precondition failed: the relay certificate does not verify against the system roots: %v", err)
	}
	relay := startGuardedRelay(t, leaf)
	sender := SMTP{Host: "smtp", Port: relay.port(), From: "noreply@upmo.test", CAFile: caFile, Lookup: localLookup}
	err = sender.SendInvite(context.Background(), "ada@example.com", "https://upmo.test/login/link#x")
	if err == nil || !strings.Contains(err.Error(), "mail: starttls: the relay certificate is not trusted by UPMO_TLS_CA_FILE") {
		t.Fatalf("err = %v", err)
	}
}

// Development keeps plaintext for one DNS label; production requires STARTTLS
// there too. A non-exempt host must offer STARTTLS in both environments.
func TestSMTPPlaintextRules(t *testing.T) {
	const refusal = "mail: the relay must offer STARTTLS unless its host is loopback or one DNS label"
	link := "https://upmo.test/login/link#x"
	caFile := writeCA(t, mustCA())

	relay := startGuardedRelay(t, tls.Certificate{})
	dev := SMTP{Host: "smtp", Port: relay.port(), From: "noreply@upmo.test", Development: true, Lookup: localLookup}
	if err := dev.SendInvite(context.Background(), "ada@example.com", link); err != nil {
		t.Fatalf("development one label: %v", err)
	}
	if got := <-relay.got; got.to != "<ada@example.com>" {
		t.Fatalf("message = %+v", got)
	}

	relay = startGuardedRelay(t, tls.Certificate{})
	prod := SMTP{Host: "smtp", Port: relay.port(), From: "noreply@upmo.test", CAFile: caFile, Lookup: localLookup}
	err := prod.SendInvite(context.Background(), "ada@example.com", link)
	if err == nil || err.Error() != refusal {
		t.Fatalf("production one label err = %v", err)
	}

	for _, sender := range []SMTP{
		{Host: "relay.example", Port: relay.port(), From: "noreply@upmo.test", Development: true, Lookup: localLookup},
		{Host: "relay.example", Port: relay.port(), From: "noreply@upmo.test", CAFile: caFile, Lookup: localLookup},
	} {
		err := sender.SendInvite(context.Background(), "ada@example.com", link)
		if err == nil || err.Error() != refusal {
			t.Fatalf("non-exempt host %+v err = %v", sender, err)
		}
	}
}

// localLookup names the test hosts on the loopback listener.
var localLookup = staticLookup(map[string]string{"smtp": "127.0.0.1", "relay.example": "127.0.0.1"})

func staticLookup(mapping map[string]string) func(context.Context, string, string) ([]netip.Addr, error) {
	return func(_ context.Context, _, host string) ([]netip.Addr, error) {
		if ip, ok := mapping[host]; ok {
			return []netip.Addr{netip.MustParseAddr(ip)}, nil
		}
		return nil, fmt.Errorf("no address for %q", host)
	}
}

// guardedRelay is an SMTP relay on 127.0.0.1:0 that counts accepted
// connections and records each delivered message. A non-nil certificate is
// offered through STARTTLS.
type guardedRelay struct {
	listener *countingListener
	got      chan received
}

func (r guardedRelay) port() int     { return r.listener.Addr().(*net.TCPAddr).Port }
func (r guardedRelay) accepted() int { return int(r.listener.accepted.Load()) }

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

func startGuardedRelay(t *testing.T, cert tls.Certificate) guardedRelay {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	relay := guardedRelay{listener: &countingListener{Listener: listener}, got: make(chan received, 4)}
	go func() {
		for {
			conn, err := relay.listener.Accept()
			if err != nil {
				return
			}
			serveRelaySession(conn, cert, relay.got)
		}
	}()
	return relay
}

// serveRelaySession serves one SMTP session, upgrading to TLS with cert when
// the client asks for STARTTLS, and records the delivered message.
func serveRelaySession(conn net.Conn, cert tls.Certificate, out chan<- received) {
	defer func() { _ = conn.Close() }()
	text := textproto.NewConn(conn)
	reply := func(code int, msg string) { _ = text.PrintfLine("%s %s", strconv.Itoa(code), msg) }
	var got received
	reply(220, "relay.test ready")
	for {
		line, err := text.ReadLine()
		if err != nil {
			return
		}
		verb := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		switch {
		case verb == "EHLO" || verb == "HELO":
			if len(cert.Certificate) == 0 {
				reply(250, "relay.test")
				continue
			}
			_ = text.PrintfLine("250-relay.test")
			reply(250, "STARTTLS")
		case verb == "STARTTLS" && len(cert.Certificate) > 0:
			reply(220, "ready to start TLS")
			tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}})
			if err := tlsConn.Handshake(); err != nil {
				return
			}
			conn = tlsConn
			text = textproto.NewConn(conn)
		case strings.HasPrefix(strings.ToUpper(line), "MAIL FROM:"):
			got.from = strings.TrimSpace(line[len("MAIL FROM:"):])
			reply(250, "ok")
		case strings.HasPrefix(strings.ToUpper(line), "RCPT TO:"):
			got.to = strings.TrimSpace(line[len("RCPT TO:"):])
			reply(250, "ok")
		case verb == "DATA":
			reply(354, "go ahead")
			raw, err := io.ReadAll(text.DotReader())
			if err != nil {
				return
			}
			got.data = strings.ReplaceAll(string(raw), "\n", "\r\n")
			reply(250, "queued")
		case verb == "QUIT":
			reply(221, "bye")
			out <- got
			return
		default:
			reply(502, "not implemented")
		}
	}
}

// testCA is a self-signed CA the behavior tests issue relay certificates
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

// mustCert issues a server certificate for names from ca.
func mustCert(t *testing.T, ca testCA, names ...string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		DNSNames:     names,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
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
