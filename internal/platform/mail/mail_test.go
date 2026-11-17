package mail

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"math/big"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSMTPSendsTheInviteLink(t *testing.T) {
	relay := startRelay(t, "127.0.0.1")
	sender := SMTP{Host: "127.0.0.1", Port: relay.port, From: "Upmo <noreply@upmo.test>"}
	link := "https://upmo.test/login/link#upmo_link_abc"
	if err := sender.SendInvite(context.Background(), "Ada <ada@example.com>", link); err != nil {
		t.Fatal(err)
	}
	got := <-relay.got
	if got.from != "<noreply@upmo.test>" || got.to != "<ada@example.com>" {
		t.Fatalf("envelope = %q %q", got.from, got.to)
	}
	for _, want := range []string{
		"From: \"Upmo\" <noreply@upmo.test>\r\n",
		"To: \"Ada\" <ada@example.com>\r\n",
		"Subject: You are invited to Upmo\r\n",
		"Content-Type: text/plain; charset=UTF-8\r\n",
		"\r\n" + link + "\r\n",
	} {
		if !strings.Contains(got.data, want) {
			t.Fatalf("message lacks %q:\n%s", want, got.data)
		}
	}
}

func TestSMTPSendsTheRecoverLink(t *testing.T) {
	relay := startRelay(t, "127.0.0.1")
	sender := SMTP{Host: "127.0.0.1", Port: relay.port, From: "noreply@upmo.test"}
	link := "https://upmo.test/login/link#upmo_link_def"
	if err := sender.SendRecover(context.Background(), "bob@example.com", link); err != nil {
		t.Fatal(err)
	}
	got := <-relay.got
	if got.to != "<bob@example.com>" || !strings.Contains(got.data, "Subject: Log in to Upmo\r\n") || !strings.Contains(got.data, link) {
		t.Fatalf("got %+v", got)
	}
}

func TestSMTPRefusesAnAddressThatBreaksHeaders(t *testing.T) {
	sender := SMTP{Host: "127.0.0.1", Port: 1, From: "noreply@upmo.test"}
	for _, to := range []string{"", "not an address", "a@example.com\r\nBcc: b@example.com", "a@example.com, b@example.com"} {
		if err := sender.SendInvite(context.Background(), to, "https://upmo.test/login/link#x"); !errors.Is(err, ErrAddress) {
			t.Errorf("to %q: err = %v", to, err)
		}
	}
}

func TestSMTPErrorDoesNotHoldTheLink(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	link := "https://upmo.test/login/link#upmo_link_secret"
	err = SMTP{Host: "127.0.0.1", Port: port, From: "noreply@upmo.test"}.SendRecover(context.Background(), "bob@example.com", link)
	if err == nil || strings.Contains(err.Error(), "upmo_link_secret") {
		t.Fatalf("err = %v", err)
	}
}

func TestNoopSendsNothing(t *testing.T) {
	var sender Sender = Noop{}
	if sender.SendInvite(context.Background(), "a@example.com", "x") != nil || sender.SendRecover(context.Background(), "a@example.com", "x") != nil {
		t.Fatal("noop failed")
	}
}

// The relay offers no STARTTLS. Dialed on 127.0.0.1 the send goes through
// (Mailpit and the tests above); dialed as the dotted host 127.0.0.2, which
// stands in for smtp.example.com because a real name cannot be pointed at a
// local listener, the send is refused and the error says STARTTLS.
func TestSMTPRequiresStartTLSOffLocalhost(t *testing.T) {
	relay := startRelay(t, "127.0.0.2")
	sender := SMTP{Host: "127.0.0.2", Port: relay.port, From: "noreply@upmo.test"}
	err := sender.SendInvite(context.Background(), "ada@example.com", "https://upmo.test/login/link#x")
	if err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("err = %v, want it to say STARTTLS", err)
	}
}

// The relay offers STARTTLS but presents a self-signed certificate. The
// send must fail: certificate verification stays on for a non-exempt host,
// and nothing sets InsecureSkipVerify. Dotted 127.0.0.2 stands in for
// smtp.example.com because a real name cannot point at a local listener.
func TestSMTPRefusesSTARTTLSWithAnUntrustedCertificate(t *testing.T) {
	port := startStartTLSRelay(t, "127.0.0.2")
	sender := SMTP{Host: "127.0.0.2", Port: port, From: "noreply@upmo.test"}
	err := sender.SendInvite(context.Background(), "ada@example.com", "https://upmo.test/login/link#x")
	if err == nil || !strings.Contains(err.Error(), "starttls") {
		t.Fatalf("err = %v, want it to say starttls", err)
	}
}

type received struct {
	from, to, data string
}

type relay struct {
	port int
	got  chan received
}

// startRelay serves one plain SMTP session without STARTTLS or AUTH.
func startRelay(t *testing.T, host string) relay {
	t.Helper()
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	r := relay{port: listener.Addr().(*net.TCPAddr).Port, got: make(chan received, 1)}
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		text := textproto.NewConn(conn)
		var got received
		reply := func(code int, msg string) { _ = text.PrintfLine("%s %s", strconv.Itoa(code), msg) }
		reply(220, "relay.test ready")
		for {
			line, err := text.ReadLine()
			if err != nil {
				return
			}
			verb := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
			switch {
			case verb == "EHLO" || verb == "HELO":
				reply(250, "relay.test")
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
				r.got <- got
				return
			default:
				reply(502, "not implemented")
			}
		}
	}()
	return r
}

// startStartTLSRelay serves one plain SMTP session that advertises STARTTLS
// and then presents a self-signed certificate for host.
func startStartTLSRelay(t *testing.T, host string) int {
	t.Helper()
	listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		IPAddresses:  []net.IP{net.ParseIP(host)},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		text := textproto.NewConn(conn)
		reply := func(code int, msg string) { _ = text.PrintfLine("%s %s", strconv.Itoa(code), msg) }
		reply(220, "relay.test ready")
		for {
			line, err := text.ReadLine()
			if err != nil {
				return
			}
			switch strings.ToUpper(strings.SplitN(line, " ", 2)[0]) {
			case "EHLO", "HELO":
				_ = text.PrintfLine("250-relay.test")
				reply(250, "STARTTLS")
			case "STARTTLS":
				reply(220, "ready to start TLS")
				tlsConn := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}})
				_ = tlsConn.Handshake()
				return
			default:
				reply(502, "not implemented")
			}
		}
	}()
	return listener.Addr().(*net.TCPAddr).Port
}
