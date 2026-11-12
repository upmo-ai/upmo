package audit

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/upmo-ai/upmo/internal/platform/netguard"
	"github.com/upmo-ai/upmo/internal/platform/webhooksig"
)

// PushBatch is how many sealed events one push page holds at most. A full page
// is followed by another in the same pass.
const PushBatch = 500

// pageBodyBytes caps the marshalled body of one page, far enough under
// webhooksig.MaxBody that the JSON around the events cannot reach the
// signer's limit: a run of large events turns into more, smaller pages.
const pageBodyBytes = 900 << 10

// The secrets chain refs of the webhook signing secrets, whsec_ values the
// webhook verifies with.
const (
	webhookSecretRef         = "AUDIT_WEBHOOK_SECRET"          // #nosec G101 -- the ref a secret is read under, not a secret
	webhookSecretPreviousRef = "AUDIT_WEBHOOK_SECRET_PREVIOUS" // #nosec G101 -- the ref a secret is read under, not a secret
)

// PushArgs is the River payload that runs one push pass.
type PushArgs struct{}

func (PushArgs) Kind() string { return "audit_push" }

type pushWorker struct {
	river.WorkerDefaults[PushArgs]
	pusher *Pusher
}

// AddPusher registers the worker that pushes sealed events to the configured
// target. It runs on owner, the pool of the role that owns the audit tables.
func AddPusher(workers *river.Workers, owner *pgxpool.Pool, cfg PushConfig) {
	river.AddWorker(workers, &pushWorker{pusher: NewPusher(owner, cfg)})
}

func (w *pushWorker) Work(ctx context.Context, _ *river.Job[PushArgs]) error {
	return w.pusher.PushOnce(ctx)
}

// PushPeriodic runs the push at startup and then every ten seconds.
func PushPeriodic() *river.PeriodicJob {
	return river.NewPeriodicJob(
		river.PeriodicInterval(10*time.Second),
		func() (river.JobArgs, *river.InsertOpts) { return PushArgs{}, nil },
		&river.PeriodicJobOpts{RunOnStart: true},
	)
}

// PushConfig is what one push needs beside the database.
type PushConfig struct {
	// WebhookURL is the signed target of every batch.
	WebhookURL string
	// SyslogAddr is the host:port of the syslog collector every batch is
	// framed for, over TLS.
	SyslogAddr string
	// Secrets resolves the signing secrets by ref, read once per batch so a
	// rotation in the chain applies without a restart.
	Secrets interface {
		Get(ref string) (string, error)
	}
	// Client delivers the requests. Nil means a client on netguard's
	// transport, which refuses private addresses; tests inject their own.
	Client *http.Client
	// SyslogDial dials the collector. Nil means netguard's guard, which
	// refuses private addresses; tests inject their own.
	SyslogDial func(ctx context.Context, network, addr string) (net.Conn, error)
	// SyslogTLS is the collector's TLS settings. Nil means the system roots
	// and TLS 1.2; the server name is the dial host.
	SyslogTLS *tls.Config
	// Now is the clock of the webhook timestamp and the hourly failure
	// window. Nil follows time.Now.
	Now func() time.Time
}

// Pusher pushes sealed audit events out of the install, one cursor per target.
type Pusher struct {
	pool *pgxpool.Pool
	cfg  PushConfig

	mu       sync.Mutex
	reported map[string]time.Time
}

// NewPusher builds the pusher of cfg on the owner pool.
func NewPusher(owner *pgxpool.Pool, cfg PushConfig) *Pusher {
	if cfg.Client == nil {
		cfg.Client = newWebhookClient()
	}
	if cfg.SyslogDial == nil {
		cfg.SyslogDial = (&netguard.Guard{}).Dial
	}
	if cfg.SyslogTLS == nil {
		cfg.SyslogTLS = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return &Pusher{pool: owner, cfg: cfg, reported: map[string]time.Time{}}
}

// newWebhookClient is the default webhook client: netguard's transport, which
// refuses private addresses, a 30 second timeout, and no redirects — a 3xx
// reaches the pusher as the answer it is, a refusal, instead of being
// followed to a URL the install never named.
func newWebhookClient() *http.Client {
	return &http.Client{
		Transport: (&netguard.Guard{}).Transport(),
		Timeout:   30 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// target is one push endpoint: a name that keys its cursor row, and send
// which delivers one batch.
type target struct {
	name string
	send func(ctx context.Context, b batch) error
}

// batch is one page of sealed events headed for a target: their OCSF bytes and
// the position range the page covers.
type batch struct {
	events        []json.RawMessage
	from, through int64
}

// pushBody is one delivered batch: the OCSF bytes of each sealed event, the
// same lines GET /api/audit/events serves.
type pushBody struct {
	Events []json.RawMessage `json:"events"`
}

// PushOnce runs one pass over every target. One target's failure does not
// stop the others: each runs, and the joined errors come back together.
func (p *Pusher) PushOnce(ctx context.Context) error {
	var errs []error
	for _, t := range p.targets() {
		if err := p.push(ctx, t); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// targets lists the push targets the install configured: the webhook and the
// syslog collector, each with a cursor row of its own.
func (p *Pusher) targets() []target {
	var targets []target
	if p.cfg.WebhookURL != "" {
		targets = append(targets, target{name: "webhook", send: p.sendWebhook})
	}
	if p.cfg.SyslogAddr != "" {
		targets = append(targets, target{name: "syslog", send: p.sendSyslog})
	}
	return targets
}

// push walks one target's cursor: each page of sealed events above it is
// delivered, and only once the target accepted does the cursor move, so a
// refused page is resent whole, under the same webhook id, by the next pass. A
// short page ends the walk.
func (p *Pusher) push(ctx context.Context, t target) error {
	cursor, err := readCursor(ctx, p.pool, t.name)
	if err != nil {
		return err
	}
	for {
		events, next, more, err := readPage(ctx, p.pool, cursor)
		if err != nil {
			return err
		}
		if len(events) == 0 {
			return nil
		}
		// Positions are gapless, so the page starts one past the cursor.
		b := batch{events: events, from: cursor + 1, through: next}
		if err := t.send(ctx, b); err != nil {
			p.reportFailure(ctx, t, b, err)
			return fmt.Errorf("audit: push %s %d-%d: %w", t.name, b.from, b.through, err)
		}
		if err := writeCursor(ctx, p.pool, t.name, b.through); err != nil {
			return err
		}
		cursor = b.through
		if !more {
			return nil
		}
	}
}

// readPage reads one page of sealed events after position as the OCSF bytes
// the events endpoint serves, and whether events were left for the next page.
// The page holds at most PushBatch events and its marshalled body stays under
// pageBodyBytes — the first event stays whatever it weighs, and the rest wait
// for the next page — so no page grows past what the signer accepts.
func readPage(ctx context.Context, owner *pgxpool.Pool, after int64) ([]json.RawMessage, int64, bool, error) {
	tx, err := owner.Begin(ctx)
	if err != nil {
		return nil, 0, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	events, _, more, err := queryPage(ctx, tx, readQuery{after: after, limit: PushBatch})
	if err != nil {
		return nil, 0, false, fmt.Errorf("audit: push page: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, 0, false, err
	}
	// What one event weighs in the body is what json.Marshal writes for it,
	// and the body adds its own JSON and a comma per event.
	size, keep := len(`{"events":[]`), 0
	for i, event := range events {
		marshalled, err := json.Marshal(event)
		if err != nil {
			return nil, 0, false, fmt.Errorf("audit: push page: %w", err)
		}
		if i > 0 && size+len(marshalled)+1 > pageBodyBytes {
			break
		}
		size += len(marshalled) + 1
		keep++
	}
	// Positions are gapless, so the kept events end after plus their count.
	return events[:keep], after + int64(keep), more || keep < len(events), nil
}

// readCursor returns the highest position the target accepted, zero when its
// row is missing.
func readCursor(ctx context.Context, owner *pgxpool.Pool, name string) (int64, error) {
	var position int64
	err := owner.QueryRow(ctx,
		`SELECT position FROM audit.push_cursor WHERE target = $1`, name,
	).Scan(&position)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("audit: cursor %s: %w", name, err)
	}
	return position, nil
}

// writeCursor stores the position the target accepted, never lower than the
// one already stored: a stale write cannot move the trail backwards.
func writeCursor(ctx context.Context, owner *pgxpool.Pool, name string, position int64) error {
	if _, err := owner.Exec(ctx, `
		INSERT INTO audit.push_cursor (target, position) VALUES ($1, $2)
		ON CONFLICT (target) DO UPDATE
		SET position = GREATEST(audit.push_cursor.position, excluded.position), updated_at = now()`, name, position,
	); err != nil {
		return fmt.Errorf("audit: cursor %s: %w", name, err)
	}
	return nil
}

// sendWebhook delivers one batch to the configured webhook: the OCSF events as
// the body, signed the Standard Webhooks way. The secrets are read per batch,
// so a rotation applies without a restart, and a missing current secret sends
// nothing: the batch counts as a failure rather than go out unsigned. The
// previous secret, when the chain holds one, adds a second signature.
func (p *Pusher) sendWebhook(ctx context.Context, b batch) error {
	secret, err := p.cfg.Secrets.Get(webhookSecretRef)
	if err != nil {
		return fmt.Errorf("audit: webhook secret: %w", err)
	}
	body, err := json.Marshal(pushBody{Events: b.events})
	if err != nil {
		return fmt.Errorf("audit: webhook body: %w", err)
	}
	// The id names the batch's position range, so a resend of the same batch
	// carries the id the target already saw.
	id := fmt.Sprintf("audit-%d-%d", b.from, b.through)
	at := p.now()
	signature, err := webhooksig.Sign(secret, id, at, body)
	if err != nil {
		return fmt.Errorf("audit: webhook signature: %w", err)
	}
	// The previous secret is optional: none means no rotation is running.
	if previous, _ := p.cfg.Secrets.Get(webhookSecretPreviousRef); previous != "" {
		older, err := webhooksig.Sign(previous, id, at, body)
		if err != nil {
			return fmt.Errorf("audit: webhook signature: %w", err)
		}
		signature += " " + older
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("webhook-id", id)
	req.Header.Set("webhook-timestamp", strconv.FormatInt(at.Unix(), 10))
	req.Header.Set("webhook-signature", signature)
	resp, err := p.cfg.Client.Do(req)
	if err != nil {
		return webhookPostError(p.cfg.WebhookURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	// Only a 2xx answer counts as delivered; anything else leaves the cursor,
	// so the next attempt resends the batch.
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("audit: webhook answered %s", resp.Status)
	}
	return nil
}

// webhookPostError wraps one failed delivery. *url.Error's message carries
// the whole URL, query and all, and the query may hold a token, so the wrap
// keeps the operation, the scheme and host alone, and the underlying error.
func webhookPostError(rawURL string, err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	origin := ""
	if parsed, parseErr := url.Parse(rawURL); parseErr == nil {
		origin = parsed.Scheme + "://" + parsed.Host
	}
	return fmt.Errorf("audit: webhook post %s: %w", origin, err)
}

// syslogPriority is the RFC 5424 priority of one audit event: facility 13,
// log audit, severity 6, informational.
const syslogPriority = 110

// syslogReadWait is how long the pusher listens after sending its shutdown:
// an RFC 6587 collector may keep the connection open, so silence within the
// window is acceptance.
const syslogReadWait = 3 * time.Second

// sendSyslog delivers one batch to the configured collector: one TLS
// connection per batch, every event its own octet-counted frame. The batch
// counts as accepted once every frame is written, the shutdown sent, and a
// short read after it ends clean — a timeout or a close, since an RFC 6587
// collector may hold the connection open. A read that fails any other way —
// the line dropped mid-batch, a reset, an alert — refuses the batch the way
// an answer short of 2xx does for the webhook.
func (p *Pusher) sendSyslog(ctx context.Context, b batch) error {
	host, _, err := net.SplitHostPort(p.cfg.SyslogAddr)
	if err != nil {
		return fmt.Errorf("audit: syslog address: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := p.cfg.SyslogDial(ctx, "tcp", p.cfg.SyslogAddr)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	tlsConfig := p.cfg.SyslogTLS.Clone()
	if tlsConfig.ServerName == "" {
		tlsConfig.ServerName = host
	}
	tlsConn := tls.Client(conn, tlsConfig)
	// The deadline bounds the whole exchange: the handshake and the frames.
	// The read after the shutdown waits its own short while.
	deadline, _ := ctx.Deadline()
	if err := tlsConn.SetDeadline(deadline); err != nil {
		return err
	}
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		return err
	}
	hostname, err := os.Hostname()
	if err != nil {
		hostname = ""
	}
	for _, event := range b.events {
		if _, err := tlsConn.Write(syslogFrame(hostname, event)); err != nil {
			return err
		}
	}
	if err := tlsConn.CloseWrite(); err != nil {
		return err
	}
	// The collector need not close: read for a short while, where a timeout
	// or a clean end counts as taken.
	if err := tlsConn.SetReadDeadline(time.Now().Add(syslogReadWait)); err != nil {
		return err
	}
	if _, err := io.Copy(io.Discard, tlsConn); err != nil {
		var netErr net.Error
		if !errors.As(err, &netErr) || !netErr.Timeout() {
			return err
		}
	}
	return tlsConn.Close()
}

// syslogFrame builds one event as its own syslog message in an RFC 6587
// octet-counted frame: the message's byte count, a space, the message. The
// message is RFC 5424, the timestamp and the message id read out of the
// event's own OCSF bytes, the OCSF JSON as the message, as is. The header
// stays legal RFC 5424: the message id is the uid without its hyphens, a
// UUID then exactly the 32 printable ASCII allowed, and the hostname the nil
// value whenever the host's own name is not.
func syslogFrame(hostname string, event json.RawMessage) []byte {
	// The header names the event, not the push, so a resent batch carries
	// the message the collector may already have seen.
	var ocsf struct {
		Time     *int64 `json:"time"`
		Metadata struct {
			UID string `json:"uid"`
		} `json:"metadata"`
	}
	at, id := "-", "-"
	if err := json.Unmarshal(event, &ocsf); err == nil {
		if ocsf.Time != nil {
			at = time.UnixMilli(*ocsf.Time).UTC().Format(time.RFC3339Nano)
		}
		if ocsf.Metadata.UID != "" {
			id = strings.ReplaceAll(ocsf.Metadata.UID, "-", "")
		}
	}
	message := fmt.Sprintf("<%d>1 %s %s upmo - %s - ", syslogPriority, at, syslogHost(hostname), id)
	// A newline would end the frame early; the export emits none.
	body := bytes.ReplaceAll(event, []byte("\n"), []byte(" "))
	frame := make([]byte, 0, len(message)+len(body)+8)
	frame = strconv.AppendInt(frame, int64(len(message)+len(body)), 10)
	frame = append(frame, ' ')
	frame = append(frame, message...)
	frame = append(frame, body...)
	return frame
}

// syslogHost keeps HOSTNAME legal RFC 5424 — 1 to 255 printable ASCII
// characters, no spaces — and gives the nil value for anything else.
func syslogHost(name string) string {
	if len(name) == 0 || len(name) > 255 {
		return "-"
	}
	for i := 0; i < len(name); i++ {
		if name[i] < 33 || name[i] > 126 {
			return "-"
		}
	}
	return name
}

// reportFailure logs every failed attempt and records one audit.push failure
// event per target per hour. The hour is remembered in memory alone, so a
// restart may report one extra.
func (p *Pusher) reportFailure(ctx context.Context, t target, b batch, cause error) {
	slog.ErrorContext(ctx, "audit push failed",
		"target", t.name, "from_position", b.from, "through_position", b.through, "err", cause)
	at := p.now()
	// The hour is taken only once the event has committed, so a failed insert
	// leaves it open and the next failure reports.
	if !p.failureDue(t.name, at) {
		return
	}
	if err := Record(WithRunner(ctx, CredentialService), p.pool, Event{
		Action:  AuditPush,
		Outcome: Failure,
		Changes: Changes{
			Fields: []string{"target", "from_position", "through_position"},
			Values: map[string]Change{
				"target":           {To: t.name},
				"from_position":    {To: b.from},
				"through_position": {To: b.through},
			},
		},
	}); err != nil {
		slog.ErrorContext(ctx, "audit push failure event", "err", err)
		return
	}
	p.failureDone(t.name, at)
}

// failureDue reports whether the target's hourly failure slot is open, and
// failureDone takes it.
func (p *Pusher) failureDue(name string, at time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if last, ok := p.reported[name]; ok && at.Sub(last) < time.Hour {
		return false
	}
	return true
}

func (p *Pusher) failureDone(name string, at time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reported[name] = at
}

// now is the pusher's clock, the webhook timestamp and the hourly failure
// window read it.
func (p *Pusher) now() time.Time {
	if p.cfg.Now != nil {
		return p.cfg.Now()
	}
	return time.Now()
}
