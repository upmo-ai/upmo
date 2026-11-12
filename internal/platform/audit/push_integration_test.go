//go:build integration

package audit_test

// The pusher delivers the sealed trail to one webhook, at least once, in
// position order, with a stored cursor. These tests drive the pusher over a
// real database and an httptest webhook, wired the way brain wires it.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/webhooksig"
)

// secretChain is a fixed secrets chain: the refs it holds and nothing else.
type secretChain map[string]string

func (c secretChain) Get(ref string) (string, error) {
	if secret, ok := c[ref]; ok {
		return secret, nil
	}
	return "", errors.New("missing secret")
}

// whsec makes one whsec_ signing secret.
func whsec(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return "whsec_" + base64.StdEncoding.EncodeToString(raw)
}

// delivered is one request the webhook saw.
type delivered struct {
	body   []byte
	header http.Header
}

// webhookTarget answers every push and remembers each request. status is the
// answer it gives, 200 when zero.
type webhookTarget struct {
	mu       sync.Mutex
	status   int
	requests []delivered
}

func (t *webhookTarget) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	t.mu.Lock()
	t.requests = append(t.requests, delivered{body: body, header: r.Header.Clone()})
	status := t.status
	t.mu.Unlock()
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
}

// received returns the requests the target has seen.
func (t *webhookTarget) received() []delivered {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]delivered(nil), t.requests...)
}

// pushRig is the pusher on one httptest webhook: the secrets it reads, the
// clock it follows and the answers it gets.
type pushRig struct {
	owner   *pgxpool.Pool
	app     *pgxpool.Pool
	target  *webhookTarget
	server  *httptest.Server
	chain   *directChain
	secrets map[string]string
	now     time.Time
	cfg     audit.PushConfig
	pusher  *audit.Pusher
}

func newPushRig(t *testing.T) *pushRig {
	t.Helper()
	owner, app := setup(t)
	target := &webhookTarget{}
	server := httptest.NewServer(target)
	t.Cleanup(server.Close)
	rig := &pushRig{
		owner:   owner,
		app:     app,
		target:  target,
		server:  server,
		chain:   &directChain{owner: owner, prev: make([]byte, 32)},
		secrets: map[string]string{},
		now:     time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC),
	}
	rig.cfg = audit.PushConfig{
		WebhookURL: server.URL,
		Secrets:    secretChain(rig.secrets),
		Client:     server.Client(),
		Now:        func() time.Time { return rig.now },
	}
	rig.pusher = audit.NewPusher(owner, rig.cfg)
	return rig
}

// seal adds n sealed events to the trail, with the positions and chain hashes
// the sealer would give them, and returns the last position.
func (rig *pushRig) seal(t *testing.T, n int) int64 {
	t.Helper()
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC).Add(time.Duration(rig.chain.position) * time.Minute)
	for i := 0; i < n; i++ {
		at = at.Add(time.Minute)
		rig.chain.add(t, at, at)
	}
	return rig.chain.position
}

// sealHeavy adds n sealed events whose changes each carry a note of size
// bytes, the payloads near the sealer's cap, and returns the last position.
func (rig *pushRig) sealHeavy(t *testing.T, n, size int) int64 {
	t.Helper()
	at := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC).Add(time.Duration(rig.chain.position) * time.Minute)
	for i := 0; i < n; i++ {
		at = at.Add(time.Minute)
		rig.chain.addHeavy(t, at, at, size)
	}
	return rig.chain.position
}

// cursorOf reads the stored cursor of one target; false when no row exists.
func (rig *pushRig) cursorOf(t *testing.T, target string) (int64, bool) {
	t.Helper()
	var position int64
	err := rig.owner.QueryRow(context.Background(),
		`SELECT position FROM audit.push_cursor WHERE target = $1`, target,
	).Scan(&position)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return position, true
}

// pushAnswer mirrors the body the webhook receives.
type pushAnswer struct {
	Events []json.RawMessage `json:"events"`
}

func bodyOf(t *testing.T, raw []byte) pushAnswer {
	t.Helper()
	var answer pushAnswer
	if err := json.Unmarshal(raw, &answer); err != nil {
		t.Fatalf("body = %s: %v", raw, err)
	}
	return answer
}

// pendingPush is one recorded push event waiting in pending.
type pendingPush struct {
	outcome    string
	credential string
	changes    audit.Changes
}

// pendingPushes reads the recorded push events oldest first.
func pendingPushes(t *testing.T, owner *pgxpool.Pool) []pendingPush {
	t.Helper()
	rows, err := owner.Query(context.Background(), `
		SELECT outcome, credential_type, changes
		FROM audit.pending WHERE action = 'audit.push' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []pendingPush
	for rows.Next() {
		var record pendingPush
		var raw []byte
		if err := rows.Scan(&record.outcome, &record.credential, &raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &record.changes); err != nil {
			t.Fatal(err)
		}
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPushDeliversTheSealedTrail(t *testing.T) {
	rig := newPushRig(t)
	rig.secrets["AUDIT_WEBHOOK_SECRET"] = whsec(t)
	last := rig.seal(t, 3)

	if err := rig.pusher.PushOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	requests := rig.target.received()
	if len(requests) != 1 {
		t.Fatalf("%d requests, want the one batch", len(requests))
	}
	request := requests[0]
	if got := request.header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %s", got)
	}
	// Each event is the OCSF line the sealer wrote, in position order.
	answer := bodyOf(t, request.body)
	if int64(len(answer.Events)) != last {
		t.Fatalf("%d events, want the three sealed", len(answer.Events))
	}
	for position := int64(1); position <= last; position++ {
		row, prev, hash := readEvent(t, rig.owner, position)
		want, err := audit.OCSF(audit.Sealed{Row: row, Position: position, PrevHash: prev, Hash: hash})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(answer.Events[position-1], want) {
			t.Fatalf("position %d:\n got %s\nwant %s", position, answer.Events[position-1], want)
		}
	}

	// The Standard Webhooks headers carry the signature of exactly this body,
	// and the id names the batch's position range.
	if got := request.header.Get("webhook-id"); got != "audit-1-3" {
		t.Fatalf("webhook id = %s, want audit-1-3", got)
	}
	timestamp := request.header.Get("webhook-timestamp")
	if timestamp != strconv.FormatInt(rig.now.Unix(), 10) {
		t.Fatalf("webhook timestamp = %s", timestamp)
	}
	if _, err := webhooksig.Verify(
		[]string{rig.secrets["AUDIT_WEBHOOK_SECRET"]}, "audit-1-3", timestamp,
		request.header.Get("webhook-signature"), request.body, rig.now); err != nil {
		t.Fatalf("verify: %v", err)
	}

	if cursor, ok := rig.cursorOf(t, "webhook"); !ok || cursor != last {
		t.Fatalf("cursor = %d %v, want the last position %d", cursor, ok, last)
	}

	// A quiet state delivers nothing and leaves the cursor alone.
	if err := rig.pusher.PushOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(rig.target.received()) != 1 {
		t.Fatal("a pass with nothing new delivered a batch")
	}
}

// A refused batch is resent whole, under the same webhook id, once the target
// accepts again.
func TestPushResendsARefusedBatch(t *testing.T) {
	rig := newPushRig(t)
	rig.secrets["AUDIT_WEBHOOK_SECRET"] = whsec(t)
	rig.seal(t, 3)
	rig.target.status = http.StatusInternalServerError

	if err := rig.pusher.PushOnce(context.Background()); err == nil {
		t.Fatal("a refused batch counted as delivered")
	}
	if cursor, ok := rig.cursorOf(t, "webhook"); ok {
		t.Fatalf("cursor = %d after the refusal, want none", cursor)
	}

	rig.target.status = 0
	if err := rig.pusher.PushOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	requests := rig.target.received()
	if len(requests) != 2 {
		t.Fatalf("%d requests, want the refused attempt and the resend", len(requests))
	}
	first, second := requests[0], requests[1]
	if !bytes.Equal(first.body, second.body) {
		t.Fatalf("the resend changed:\n got %s\nwant %s", second.body, first.body)
	}
	if got := second.header.Get("webhook-id"); got != "audit-1-3" || first.header.Get("webhook-id") != got {
		t.Fatalf("webhook ids = %s and %s, want the same audit-1-3",
			first.header.Get("webhook-id"), got)
	}
	if cursor, _ := rig.cursorOf(t, "webhook"); cursor != 3 {
		t.Fatalf("cursor = %d, want 3", cursor)
	}
}

// Events sealed after a delivered batch are the only ones the next batch
// carries.
func TestPushResumesAtTheCursor(t *testing.T) {
	rig := newPushRig(t)
	rig.secrets["AUDIT_WEBHOOK_SECRET"] = whsec(t)
	rig.seal(t, 2)
	if err := rig.pusher.PushOnce(context.Background()); err != nil {
		t.Fatal(err)
	}

	last := rig.seal(t, 2)
	if err := rig.pusher.PushOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	requests := rig.target.received()
	if len(requests) != 2 {
		t.Fatalf("%d requests, want one per batch", len(requests))
	}
	answer := bodyOf(t, requests[1].body)
	if int64(len(answer.Events)) != last-2 {
		t.Fatalf("%d events, want only the two new", len(answer.Events))
	}
	if got := requests[1].header.Get("webhook-id"); got != "audit-3-4" {
		t.Fatalf("webhook id = %s, want the second batch's range", got)
	}
	row, prev, hash := readEvent(t, rig.owner, 3)
	want, err := audit.OCSF(audit.Sealed{Row: row, Position: 3, PrevHash: prev, Hash: hash})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(answer.Events[0], want) {
		t.Fatalf("first event:\n got %s\nwant %s", answer.Events[0], want)
	}
	if cursor, _ := rig.cursorOf(t, "webhook"); cursor != last {
		t.Fatalf("cursor = %d, want %d", cursor, last)
	}
}

// Events with changes near the sealer's cap still all deliver: the page
// splits so every marshalled body stays under the signer's limit, each post
// verifies, and the order holds with no gaps or repeats.
func TestPushSplitsAPageTheSignerWouldRefuse(t *testing.T) {
	rig := newPushRig(t)
	rig.secrets["AUDIT_WEBHOOK_SECRET"] = whsec(t)
	const events, size = 250, 8000
	last := rig.sealHeavy(t, events, size)

	if err := rig.pusher.PushOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	requests := rig.target.received()
	if len(requests) < 2 {
		t.Fatalf("%d posts for %d heavy events, want the page split", len(requests), events)
	}
	var delivered []json.RawMessage
	for i, request := range requests {
		if len(request.body) >= webhooksig.MaxBody {
			t.Fatalf("post %d body = %d bytes, want under %d", i, len(request.body), webhooksig.MaxBody)
		}
		timestamp := request.header.Get("webhook-timestamp")
		if _, err := webhooksig.Verify(
			[]string{rig.secrets["AUDIT_WEBHOOK_SECRET"]}, request.header.Get("webhook-id"), timestamp,
			request.header.Get("webhook-signature"), request.body, rig.now); err != nil {
			t.Fatalf("post %d verify: %v", i, err)
		}
		delivered = append(delivered, bodyOf(t, request.body).Events...)
	}
	if int64(len(delivered)) != last {
		t.Fatalf("%d events delivered across the posts, want the %d sealed", len(delivered), last)
	}
	for position := int64(1); position <= last; position++ {
		if got, want := delivered[position-1], ocsfOf(t, rig.owner, position); !bytes.Equal(got, want) {
			t.Fatalf("position %d:\n got %s\nwant %s", position, got, want)
		}
	}
	if cursor, _ := rig.cursorOf(t, "webhook"); cursor != last {
		t.Fatalf("cursor = %d, want the last position %d", cursor, last)
	}
}

// A failed delivery names the scheme and host alone: the URL's query may hold
// a token, which belongs in no error and no log.
func TestPushKeepsTheURLQueryOutOfItsErrors(t *testing.T) {
	rig := newPushRig(t)
	rig.secrets["AUDIT_WEBHOOK_SECRET"] = whsec(t)
	rig.seal(t, 1)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	rig.cfg.WebhookURL = "http://" + closed + "/push?token=SECRET"
	rig.pusher = audit.NewPusher(rig.owner, rig.cfg)

	logs := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	err = rig.pusher.PushOnce(context.Background())
	if err == nil {
		t.Fatal("a webhook on a closed port counted as delivered")
	}
	if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "token=") {
		t.Fatalf("error = %s, want the query out of it", err)
	}
	if out := logs.String(); strings.Contains(out, "SECRET") || strings.Contains(out, "token=") {
		t.Fatalf("log = %s, want the query out of it", out)
	}
	if _, ok := rig.cursorOf(t, "webhook"); ok {
		t.Fatal("a cursor row appeared for an undelivered batch")
	}
}

// A missing signing secret sends nothing: the batch fails rather than go out
// unsigned.
func TestPushWithoutASecretSendsNothing(t *testing.T) {
	rig := newPushRig(t)
	rig.seal(t, 2)

	if err := rig.pusher.PushOnce(context.Background()); err == nil {
		t.Fatal("a batch without a secret was delivered")
	}
	if requests := rig.target.received(); len(requests) != 0 {
		t.Fatalf("%d requests reached the webhook, want none", len(requests))
	}
	if _, ok := rig.cursorOf(t, "webhook"); ok {
		t.Fatal("a cursor row appeared for an undelivered batch")
	}
}

// With both secrets in the chain the header carries both signatures, and
// either one alone verifies.
func TestPushSignsWithBothSecrets(t *testing.T) {
	rig := newPushRig(t)
	current, previous := whsec(t), whsec(t)
	rig.secrets["AUDIT_WEBHOOK_SECRET"] = current
	rig.secrets["AUDIT_WEBHOOK_SECRET_PREVIOUS"] = previous
	rig.seal(t, 1)

	if err := rig.pusher.PushOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	requests := rig.target.received()
	if len(requests) != 1 {
		t.Fatalf("%d requests, want the one batch", len(requests))
	}
	request := requests[0]
	signature := request.header.Get("webhook-signature")
	if got := len(strings.Fields(signature)); got != 2 {
		t.Fatalf("signature = %q, want the current and the previous", signature)
	}
	timestamp := request.header.Get("webhook-timestamp")
	for _, secret := range []string{current, previous} {
		if _, err := webhooksig.Verify([]string{secret}, "audit-1-1", timestamp, signature, request.body, rig.now); err != nil {
			t.Fatalf("verify with one secret alone: %v", err)
		}
	}
	if _, err := webhooksig.Verify([]string{whsec(t)}, "audit-1-1", timestamp, signature, request.body, rig.now); err == nil {
		t.Fatal("a secret that signed nothing verified")
	}
}

// Failures report at most once an hour per target.
func TestPushReportsFailuresOnceAnHour(t *testing.T) {
	rig := newPushRig(t)
	rig.secrets["AUDIT_WEBHOOK_SECRET"] = whsec(t)
	rig.seal(t, 2)
	rig.target.status = http.StatusInternalServerError

	for pass := 0; pass < 2; pass++ {
		if err := rig.pusher.PushOnce(context.Background()); err == nil {
			t.Fatal("a refused batch counted as delivered")
		}
	}
	records := pendingPushes(t, rig.owner)
	if len(records) != 1 {
		t.Fatalf("%d push events in one hour, want the one", len(records))
	}
	if records[0].outcome != string(audit.Failure) {
		t.Fatalf("outcome = %s, want failure", records[0].outcome)
	}
	if records[0].credential != audit.CredentialService {
		t.Fatalf("credential = %s, want the service runner", records[0].credential)
	}
	if got := records[0].changes.Values["target"].To; got != "webhook" {
		t.Fatalf("target = %v, want the webhook", got)
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
		t.Fatal("a refused batch counted as delivered")
	}
	if records = pendingPushes(t, rig.owner); len(records) != 2 {
		t.Fatalf("%d push events after the hour, want two", len(records))
	}
}

// The cursor is the owner role's alone.
func TestPushCursorIsOwnerOnly(t *testing.T) {
	rig := newPushRig(t)
	ctx := context.Background()
	if _, err := rig.app.Exec(ctx, `SELECT position FROM audit.push_cursor`); err == nil {
		t.Fatal("the app role read the push cursor")
	}
	if _, err := rig.app.Exec(ctx, `INSERT INTO audit.push_cursor (target) VALUES ('webhook')`); err == nil {
		t.Fatal("the app role wrote the push cursor")
	}
}
