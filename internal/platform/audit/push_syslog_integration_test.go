//go:build integration

package audit_test

// The syslog target delivers the sealed trail to one TLS collector, at least
// once, in position order, with a cursor of its own. These tests drive the
// pusher over a real database and a local TLS listener, wired the way brain
// wires it.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/platform/audit"
)

// syslogRead is one message the collector took: the octet count it read and
// the bytes that followed it.
type syslogRead struct {
	count   string
	message []byte
}

// syslogCollector is one local TLS collector: it holds a self-signed
// certificate for its loopback address and takes whole octet-counted frames,
// one connection per batch. dropAfter frames into a connection, when it is
// not -1, and the line is then cut without the TLS shutdown, the way a
// collector that dies mid-batch does. hold leaves the line open after the
// last frame instead: no close, no answer to the shutdown, the way an RFC
// 6587 collector may.
type syslogCollector struct {
	addr      string
	cert      *x509.Certificate
	dropAfter int
	hold      bool

	mu     sync.Mutex
	frames []syslogRead
}

// newSyslogCollector starts one collector on a loopback port of its own.
func newSyslogCollector(t *testing.T) *syslogCollector {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "upmo-syslog-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	config := &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS12,
	}
	collector := &syslogCollector{addr: listener.Addr().String(), cert: cert, dropAfter: -1}
	go collector.serve(listener, config)
	t.Cleanup(func() { _ = listener.Close() })
	return collector
}

// serve takes the pusher's connections until the listener closes.
func (c *syslogCollector) serve(l net.Listener, config *tls.Config) {
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		go c.read(tls.Server(conn, config))
	}
}

// read drives one connection: the handshake, then whole frames to the end of
// the batch, then the TLS shutdown — unless the line is cut first. The plain
// close answers the pusher's shutdown; a cut bypasses it, so the pusher sees
// a connection that ended without one. hold closes nothing at all: the line
// stays open, the shutdown unanswered.
func (c *syslogCollector) read(conn *tls.Conn) {
	if !c.hold {
		defer func() { _ = conn.Close() }()
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	reader := bufio.NewReader(conn)
	taken := 0
	for c.cutAt() < 0 || taken < c.cutAt() {
		head, err := reader.ReadString(' ')
		if err != nil {
			return
		}
		size, err := strconv.Atoi(strings.TrimSpace(head))
		if err != nil {
			return
		}
		message := make([]byte, size)
		if _, err := io.ReadFull(reader, message); err != nil {
			return
		}
		c.take(syslogRead{count: strings.TrimSpace(head), message: message})
		taken++
	}
	if c.hold {
		// Park with the line open: no answer to the shutdown, no close. The
		// wait ends with the test.
		<-time.After(30 * time.Second)
		return
	}
	_ = conn.NetConn().Close()
}

// dial reaches the listener directly: the loopback address it holds is one
// netguard refuses, and the pusher's own dial stays the guard's.
func (c *syslogCollector) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

// take records one message, and taken returns them oldest first.
func (c *syslogCollector) take(frame syslogRead) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.frames = append(c.frames, frame)
}

func (c *syslogCollector) taken() []syslogRead {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]syslogRead(nil), c.frames...)
}

// cutAt is how many whole frames one connection takes before the line is
// cut; -1 reads to the end.
func (c *syslogCollector) cutAt() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dropAfter
}

// withSyslog points the rig's pusher at one collector beside the targets it
// already has, rewiring it the way brain would. trusted=false keeps the
// collector's certificate out of the pusher's pool, so the handshake fails.
func (rig *pushRig) withSyslog(c *syslogCollector, trusted bool) {
	pool := x509.NewCertPool()
	if trusted {
		pool.AddCert(c.cert)
	}
	rig.cfg.SyslogAddr = c.addr
	rig.cfg.SyslogDial = c.dial
	rig.cfg.SyslogTLS = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	rig.pusher = audit.NewPusher(rig.owner, rig.cfg)
}

// newSyslogRig is the rig's pusher on one local TLS collector it trusts, with
// the webhook target off: the syslog target alone.
func newSyslogRig(t *testing.T, trusted bool) (*pushRig, *syslogCollector) {
	t.Helper()
	rig := newPushRig(t)
	rig.cfg.WebhookURL = ""
	collector := newSyslogCollector(t)
	rig.withSyslog(collector, trusted)
	return rig, collector
}

// ocsfOf returns the OCSF bytes of one sealed position, the line the pusher
// reads.
func ocsfOf(t *testing.T, owner *pgxpool.Pool, position int64) []byte {
	t.Helper()
	row, prev, hash := readEvent(t, owner, position)
	want, err := audit.OCSF(audit.Sealed{Row: row, Position: position, PrevHash: prev, Hash: hash})
	if err != nil {
		t.Fatal(err)
	}
	return want
}

// eventJSON returns the message of one frame the collector took: the event's
// OCSF bytes.
func eventJSON(t *testing.T, frame syslogRead) []byte {
	t.Helper()
	parts := strings.SplitN(string(frame.message), " ", 8)
	if len(parts) != 8 {
		t.Fatalf("frame = %s: want the RFC 5424 header and the message", frame.message)
	}
	return []byte(parts[7])
}

// Three sealed events arrive in order, one frame each, and only the syslog
// cursor moves: the install has no webhook configured.
func TestSyslogPushDeliversTheSealedTrail(t *testing.T) {
	rig, collector := newSyslogRig(t, true)
	last := rig.seal(t, 3)

	if err := rig.pusher.PushOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	frames := collector.taken()
	if len(frames) != 3 {
		t.Fatalf("%d frames, want the three sealed events", len(frames))
	}
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "-"
	}
	for i, frame := range frames {
		if frame.count != strconv.Itoa(len(frame.message)) {
			t.Fatalf("frame %d counts %s over %d bytes", i, frame.count, len(frame.message))
		}
		want := ocsfOf(t, rig.owner, int64(i+1))
		var head struct {
			Time     int64 `json:"time"`
			Metadata struct {
				UID string `json:"uid"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(want, &head); err != nil {
			t.Fatal(err)
		}
		at := time.UnixMilli(head.Time).UTC().Format(time.RFC3339Nano)
		id := strings.ReplaceAll(head.Metadata.UID, "-", "")
		message := "<110>1 " + at + " " + hostname + " upmo - " + id + " - " + string(want)
		if string(frame.message) != message {
			t.Fatalf("frame %d:\n got %s\nwant %s", i, frame.message, message)
		}
	}
	if cursor, ok := rig.cursorOf(t, "syslog"); !ok || cursor != last {
		t.Fatalf("cursor = %d %v, want the last position %d", cursor, ok, last)
	}
	if _, ok := rig.cursorOf(t, "webhook"); ok {
		t.Fatal("the webhook cursor moved on a syslog-only install")
	}
	if requests := rig.target.received(); len(requests) != 0 {
		t.Fatalf("%d webhook requests on a syslog-only install", len(requests))
	}

	// A quiet state delivers nothing and leaves the cursor alone.
	if err := rig.pusher.PushOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(collector.taken()) != 3 {
		t.Fatal("a pass with nothing new delivered a batch")
	}
}

// A batch the collector drops mid-way is refused: the cursor waits, and once
// the collector behaves the next pass resends the whole batch.
func TestSyslogPushResendsADroppedBatch(t *testing.T) {
	rig, collector := newSyslogRig(t, true)
	collector.dropAfter = 1
	last := rig.seal(t, 3)

	if err := rig.pusher.PushOnce(context.Background()); err == nil {
		t.Fatal("a dropped batch counted as delivered")
	}
	if cursor, ok := rig.cursorOf(t, "syslog"); ok {
		t.Fatalf("cursor = %d after the drop, want none", cursor)
	}
	if taken := len(collector.taken()); taken > 1 {
		t.Fatalf("%d frames taken before the drop, want at most the one", taken)
	}

	collector.dropAfter = -1
	if err := rig.pusher.PushOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	frames := collector.taken()
	if len(frames) != int(last)+1 {
		t.Fatalf("%d frames, want the dropped one and the whole batch again", len(frames))
	}
	for i := int64(1); i <= last; i++ {
		if got, want := eventJSON(t, frames[i]), ocsfOf(t, rig.owner, i); !bytes.Equal(got, want) {
			t.Fatalf("frame %d:\n got %s\nwant %s", i, got, want)
		}
	}
	if cursor, _ := rig.cursorOf(t, "syslog"); cursor != last {
		t.Fatalf("cursor = %d, want %d", cursor, last)
	}
}

// A collector that reads every frame and then holds the line open — no close,
// the way an RFC 6587 one may — has still taken the batch: the pass returns
// within the short read window and the cursor moves.
func TestSyslogPushAcceptsACollectorThatHoldsTheLine(t *testing.T) {
	rig, collector := newSyslogRig(t, true)
	collector.hold = true
	last := rig.seal(t, 3)

	start := time.Now()
	if err := rig.pusher.PushOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if taken := time.Since(start); taken > 10*time.Second {
		t.Fatalf("the pass took %s, waiting for a close that never came", taken)
	}
	if frames := collector.taken(); len(frames) != int(last) {
		t.Fatalf("%d frames, want the three sealed events", len(frames))
	}
	if cursor, ok := rig.cursorOf(t, "syslog"); !ok || cursor != last {
		t.Fatalf("cursor = %d %v, want the last position %d", cursor, ok, last)
	}
}

// A collector whose certificate the pusher does not trust gets nothing: the
// handshake fails and the cursor stays.
func TestSyslogPushVerifiesTheCollector(t *testing.T) {
	rig, collector := newSyslogRig(t, false)
	rig.seal(t, 2)

	if err := rig.pusher.PushOnce(context.Background()); err == nil {
		t.Fatal("an unverified collector counted as delivered")
	}
	if frames := collector.taken(); len(frames) != 0 {
		t.Fatalf("%d frames delivered without the verification, want none", len(frames))
	}
	if _, ok := rig.cursorOf(t, "syslog"); ok {
		t.Fatal("a cursor row appeared for an undelivered batch")
	}
}

// Each target keeps its own cursor: a failing syslog collector leaves the
// webhook's alone, and the pass still reports the error.
func TestSyslogPushKeepsItsOwnCursor(t *testing.T) {
	rig := newPushRig(t)
	rig.secrets["AUDIT_WEBHOOK_SECRET"] = whsec(t)
	collector := newSyslogCollector(t)
	collector.dropAfter = 0
	rig.withSyslog(collector, true)
	last := rig.seal(t, 2)

	if err := rig.pusher.PushOnce(context.Background()); err == nil {
		t.Fatal("a dropped syslog batch counted as delivered")
	}
	if cursor, _ := rig.cursorOf(t, "webhook"); cursor != last {
		t.Fatalf("webhook cursor = %d, want %d", cursor, last)
	}
	if _, ok := rig.cursorOf(t, "syslog"); ok {
		t.Fatal("the syslog cursor moved on a dropped batch")
	}

	// Once the collector behaves the syslog batch goes out, and the quiet
	// webhook stays where it is.
	collector.dropAfter = -1
	if err := rig.pusher.PushOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cursor, _ := rig.cursorOf(t, "webhook"); cursor != last {
		t.Fatalf("webhook cursor = %d, want %d: a quiet target was pushed again", cursor, last)
	}
	if cursor, _ := rig.cursorOf(t, "syslog"); cursor != last {
		t.Fatalf("syslog cursor = %d, want %d", cursor, last)
	}
	if requests := rig.target.received(); len(requests) != 1 {
		t.Fatalf("%d webhook requests, want the one batch", len(requests))
	}
}

// One target's failure does not stop the other: a webhook without its secret
// still leaves the syslog batch delivered, and the pass reports the webhook.
func TestSyslogPushRunsWhenTheWebhookFails(t *testing.T) {
	rig := newPushRig(t)
	collector := newSyslogCollector(t)
	rig.withSyslog(collector, true)
	last := rig.seal(t, 2)

	err := rig.pusher.PushOnce(context.Background())
	if err == nil {
		t.Fatal("the missing webhook secret counted as delivered")
	}
	if !strings.Contains(err.Error(), "webhook") {
		t.Fatalf("error = %v, want the webhook named", err)
	}
	if requests := rig.target.received(); len(requests) != 0 {
		t.Fatalf("%d webhook requests, want none without a secret", len(requests))
	}
	if frames := collector.taken(); len(frames) != int(last) {
		t.Fatalf("%d frames, want the %d sealed events", len(frames), last)
	}
	if cursor, ok := rig.cursorOf(t, "syslog"); !ok || cursor != last {
		t.Fatalf("syslog cursor = %d %v, want %d", cursor, ok, last)
	}
	if _, ok := rig.cursorOf(t, "webhook"); ok {
		t.Fatal("the webhook cursor moved without a secret")
	}
}

// Syslog failures report at most once an hour, under their own target.
func TestSyslogPushReportsFailuresOnceAnHour(t *testing.T) {
	rig, collector := newSyslogRig(t, true)
	collector.dropAfter = 0
	rig.seal(t, 2)

	for pass := 0; pass < 2; pass++ {
		if err := rig.pusher.PushOnce(context.Background()); err == nil {
			t.Fatal("a dropped batch counted as delivered")
		}
	}
	records := pendingPushes(t, rig.owner)
	if len(records) != 1 {
		t.Fatalf("%d push events in one hour, want the one", len(records))
	}
	if records[0].outcome != string(audit.Failure) {
		t.Fatalf("outcome = %s, want failure", records[0].outcome)
	}
	if got := records[0].changes.Values["target"].To; got != "syslog" {
		t.Fatalf("target = %v, want the syslog collector", got)
	}
	if got := records[0].changes.Values["from_position"].To; got != float64(1) {
		t.Fatalf("from_position = %v, want 1", got)
	}
	if got := records[0].changes.Values["through_position"].To; got != float64(2) {
		t.Fatalf("through_position = %v, want 2", got)
	}

	// An hour later the window opens and the next failure reports again.
	rig.now = rig.now.Add(time.Hour)
	if err := rig.pusher.PushOnce(context.Background()); err == nil {
		t.Fatal("a dropped batch counted as delivered")
	}
	if records = pendingPushes(t, rig.owner); len(records) != 2 {
		t.Fatalf("%d push events after the hour, want two", len(records))
	}
}
