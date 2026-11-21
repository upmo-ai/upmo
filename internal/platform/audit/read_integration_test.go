//go:build integration

package audit_test

// The events endpoint serves the sealed trail to the people and tokens that
// hold audit:view, and reading is audited. These tests drive the endpoint
// behind the audit middleware with the real identity session and token
// services, wired the way brain wires them.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/testutil/sealer"
)

// readRig is the events endpoint on one mux behind the audit middleware, with
// the identity services its guard composes. now is the handler's clock, so a
// test can move the hourly export window.
type readRig struct {
	owner    *pgxpool.Pool
	app      *pgxpool.Pool
	served   http.Handler
	sessions app.Service
	now      time.Time
}

func newReadRig(t *testing.T) *readRig {
	t.Helper()
	owner, appPool := setup(t)
	// The first joined person must be an owner. Seed one without a session,
	// so the trails hold only the sessions the test opens.
	if _, err := owner.Exec(context.Background(), `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
		VALUES ('root@example.com', sha256('root@example.com'::bytea), 'k1', 'Root', (SELECT id FROM identity.roles WHERE key = 'owner'), now())`); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions := identity.Sessions(appPool, identity.IdleTimeout, identity.MaxAge)
	tokens := identity.APITokens(appPool, log)
	members := identity.Members(appPool, nil, "http://localhost:5173", nil, log)
	rig := &readRig{owner: owner, app: appPool, sessions: sessions, now: time.Now()}
	events := &audit.ReadHandler{Pool: appPool, Now: func() time.Time { return rig.now }}

	mux := http.NewServeMux()
	// The real session and token routes, so a test creates its tokens the way
	// an operator does.
	identity.MountAPITokens(mux, sessions, tokens, log, identity.Passwords(appPool, nil, sessions).HasPassword, appPool, nil)
	events.Mount(mux, audit.Guard(auditCallers{sessions: sessions, tokens: tokens, members: members}))
	rig.served = mount(t, mux, appPool)
	return rig
}

// auditCallers mirrors the adapter brain supplies to audit.Guard: the three
// identity calls alone, with no permission logic of its own.
type auditCallers struct {
	sessions app.Service
	tokens   app.APITokens
	members  identity.MemberService
}

// Token resolves one bearer secret to its token's reader.
func (c auditCallers) Token(ctx context.Context, secret string) (audit.Reader, error) {
	bearer, err := c.tokens.Authenticate(ctx, secret)
	if err != nil {
		return audit.Reader{}, err
	}
	return audit.Reader{
		UserID:         bearer.UserID.String(),
		RoleID:         bearer.RoleID,
		CredentialType: "token",
		CredentialID:   bearer.ID,
		Allow:          strings.Join(bearer.Scope, ","),
	}, nil
}

// Session resolves the request's session to the signed-in person's reader.
func (c auditCallers) Session(ctx context.Context, r *http.Request) (audit.Reader, bool, error) {
	person, ok, err := identity.Recheck(ctx, c.sessions, r)
	if err != nil || !ok {
		return audit.Reader{}, false, err
	}
	allow, err := c.members.EffectiveAllow(ctx, person.UserID)
	if err != nil {
		return audit.Reader{}, false, err
	}
	return audit.Reader{
		UserID:         person.UserID.String(),
		RoleID:         person.RoleID,
		CredentialType: "session",
		CredentialID:   person.SessionID,
		Allow:          allow,
	}, true, nil
}

// openSession seeds one person of this role and opens their session.
func (rig *readRig) openSession(t *testing.T, email, role string) string {
	t.Helper()
	ctx := context.Background()
	stored, hash, keyID, err := sealer.NewEmail(t).Seal(email)
	if err != nil {
		t.Fatal(err)
	}
	var userID string
	err = rig.owner.QueryRow(ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
		VALUES ($1, $2, $3, 'Ada', (SELECT id FROM identity.roles WHERE key = $4), now())
		RETURNING id::text`, stored, hash, keyID, role).Scan(&userID)
	if err != nil {
		t.Fatal(err)
	}
	uid, err := identity.ParseUserID(userID)
	if err != nil {
		t.Fatal(err)
	}
	text, _, err := rig.sessions.Start(ctx, uid, "passkey", "")
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func (rig *readRig) get(t *testing.T, path, cookie, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: cookieName, Value: cookie, Path: "/", Secure: true, HttpOnly: true})
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	rig.served.ServeHTTP(rec, req)
	return rec
}

// makeToken creates one personal token through the real route.
func (rig *readRig) makeToken(t *testing.T, cookie string, scope ...string) (id, secret string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"name": "trail", "scope": scope, "days": 7})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/tokens", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: cookieName, Value: cookie, Path: "/", Secure: true, HttpOnly: true})
	rec := httptest.NewRecorder()
	rig.served.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("token create = %d %s", rec.Code, rec.Body.String())
	}
	var made struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &made); err != nil {
		t.Fatal(err)
	}
	return made.ID, made.Secret
}

// eventsAnswer mirrors the endpoint's answer shape.
type eventsAnswer struct {
	Events []json.RawMessage `json:"events"`
	Next   int64             `json:"next"`
	More   bool              `json:"more"`
}

func answerOf(t *testing.T, rec *httptest.ResponseRecorder) eventsAnswer {
	t.Helper()
	var answer eventsAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatalf("answer = %s: %v", rec.Body.String(), err)
	}
	return answer
}

// eventLine is the part of one answer line the tests read.
type eventLine struct {
	Metadata struct {
		UID      string `json:"uid"`
		Sequence int64  `json:"sequence"`
	} `json:"metadata"`
	API struct {
		Operation string `json:"operation"`
	} `json:"api"`
	Actor struct {
		User struct {
			UID string `json:"uid"`
		} `json:"user"`
	} `json:"actor"`
	Resources []struct {
		Type string `json:"type"`
		UID  string `json:"uid"`
	} `json:"resources"`
}

func parseLine(t *testing.T, raw json.RawMessage) eventLine {
	t.Helper()
	var line eventLine
	if err := json.Unmarshal(raw, &line); err != nil {
		t.Fatalf("line = %s: %v", raw, err)
	}
	return line
}

// plant stores one pending event with chosen fields, the sealer's input.
func plant(t *testing.T, owner *pgxpool.Pool, action, actor, targetType, targetID, outcome string, at time.Time) {
	t.Helper()
	if _, err := owner.Exec(context.Background(), `
		INSERT INTO audit.pending (occurred_at, service, action, outcome, actor_type, actor_id, credential_type, target_type, target_id)
		VALUES ($1, 'brain', $2, $3, 'user', $4::uuid, 'session', $5, $6)`,
		at, action, outcome, actor, targetType, targetID); err != nil {
		t.Fatal(err)
	}
}

func TestAuditEventsRefusedForTheMemberAndAnonymous(t *testing.T) {
	rig := newReadRig(t)
	member := rig.openSession(t, "member@example.com", "member")
	sealed := newSealer(t, rig.owner)

	rec := rig.get(t, "/api/audit/events", member, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member read = %d %s, want 403", rec.Code, rec.Body.String())
	}
	if rec = rig.get(t, "/api/audit/events", "", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous read = %d %s, want 401", rec.Code, rec.Body.String())
	}
	if rec = rig.get(t, "/api/audit/events", "", "upmo_pat_not-a-token"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad bearer = %d %s, want 401", rec.Code, rec.Body.String())
	}

	rows := sealed()
	memberID, _ := lookupPerson(t, rig.owner, "member@example.com")
	var denials []audit.Row
	for _, row := range rows {
		if row.Action == audit.AuditRead && row.Outcome == audit.Denied {
			denials = append(denials, row)
		}
	}
	if len(denials) != 3 {
		t.Fatalf("%d denied audit.read events among %v, want the member and the two anonymous", len(denials), actions(rows))
	}
	if denials[0].ActorID != memberID || denials[0].CredentialType != "session" {
		t.Fatalf("member denial = %s %s %s, want the named member", denials[0].ActorType, denials[0].ActorID, denials[0].CredentialType)
	}
	for _, row := range denials[1:] {
		if row.ActorType != "anonymous" {
			t.Fatalf("anonymous denial = %s %s", row.ActorType, row.ActorID)
		}
	}
}

func TestAuditEventsAnswerTheSealedRows(t *testing.T) {
	rig := newReadRig(t)
	admin := rig.openSession(t, "admin@example.com", "admin")
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := audit.Record(ctx, rig.app, audit.Event{Action: audit.AuthSignOut, Outcome: audit.Success}); err != nil {
			t.Fatal(err)
		}
	}
	if err := audit.Migrated(ctx, rig.owner); err != nil {
		t.Fatal(err)
	}
	if _, err := audit.SealOnce(ctx, rig.owner, io.Discard); err != nil {
		t.Fatal(err)
	}

	rec := rig.get(t, "/api/audit/events", admin, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("read = %d %s", rec.Code, rec.Body.String())
	}
	answer := answerOf(t, rec)
	var head int64
	if err := rig.owner.QueryRow(ctx, `SELECT position FROM audit.head WHERE one`).Scan(&head); err != nil {
		t.Fatal(err)
	}
	if int64(len(answer.Events)) != head || answer.Next != head || answer.More {
		t.Fatalf("answer = %d events, next %d, more %v; head %d", len(answer.Events), answer.Next, answer.More, head)
	}
	for position := int64(1); position <= head; position++ {
		row, prev, hash := readEvent(t, rig.owner, position)
		want, err := audit.OCSF(audit.Sealed{Row: row, Position: position, PrevHash: prev, Hash: hash})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(answer.Events[position-1], want) {
			t.Fatalf("position %d:\n got %s\nwant %s", position, answer.Events[position-1], want)
		}
	}
}

func TestAuditEventsPageUnderAConcurrentWriter(t *testing.T) {
	rig := newReadRig(t)
	admin := rig.openSession(t, "admin@example.com", "admin")
	ctx := context.Background()

	const written = 25
	stop := make(chan struct{})
	sealDone := make(chan struct{})
	sealFailed := make(chan error, 1)
	go func() {
		defer close(sealDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := audit.SealOnce(ctx, rig.owner, io.Discard); err != nil {
				sealFailed <- err
				return
			}
		}
	}()
	var writers sync.WaitGroup
	writers.Add(1)
	go func() {
		defer writers.Done()
		for i := 0; i < written; i++ {
			if err := audit.Record(ctx, rig.app, audit.Event{
				Action: audit.AuthSignOut, Outcome: audit.Success,
				TargetID: fmt.Sprintf("writer-%d", i),
			}); err != nil {
				t.Errorf("record: %v", err)
				return
			}
		}
	}()

	// Page the trail while the writer fills it: every position exactly once,
	// none skipped.
	seen := map[int64]bool{}
	writerEvents := map[string]bool{}
	after := int64(0)
	for {
		rec := rig.get(t, fmt.Sprintf("/api/audit/events?after=%d&limit=3", after), admin, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("page after %d = %d %s", after, rec.Code, rec.Body.String())
		}
		answer := answerOf(t, rec)
		for _, raw := range answer.Events {
			line := parseLine(t, raw)
			if seen[line.Metadata.Sequence] {
				t.Fatalf("position %d returned twice", line.Metadata.Sequence)
			}
			seen[line.Metadata.Sequence] = true
			if id := line.Resources[0].UID; strings.HasPrefix(id, "writer-") {
				if writerEvents[id] {
					t.Fatalf("writer event %s returned twice", id)
				}
				writerEvents[id] = true
			}
		}
		after = answer.Next
		if len(writerEvents) == written && !answer.More {
			break
		}
	}
	writers.Wait()
	close(stop)
	<-sealDone
	select {
	case err := <-sealFailed:
		t.Fatal(err)
	default:
	}

	// The pages together hold a contiguous run from the first position, and
	// the chain they paged is still whole.
	count := int64(len(seen))
	for position := int64(1); position <= count; position++ {
		if !seen[position] {
			t.Fatalf("position %d was never returned", position)
		}
	}
	for {
		sealed, err := audit.SealOnce(ctx, rig.owner, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if sealed < audit.SealBatch {
			break
		}
	}
	var head int64
	if err := rig.owner.QueryRow(ctx, `SELECT position FROM audit.head WHERE one`).Scan(&head); err != nil {
		t.Fatal(err)
	}
	checkChain(t, rig.owner, head)
}

func TestAuditEventsNarrowToEachFilter(t *testing.T) {
	rig := newReadRig(t)
	admin := rig.openSession(t, "admin@example.com", "admin")
	ctx := context.Background()
	// Seal the sign-in first, so the planted rows are the only ones the time
	// filters can catch.
	if _, err := audit.SealOnce(ctx, rig.owner, io.Discard); err != nil {
		t.Fatal(err)
	}

	const (
		userA = "018f3b2c-7a10-7c4a-8e11-111111111111"
		userB = "018f3b2c-7a10-7c4a-8e11-222222222222"
		userC = "018f3b2c-7a10-7c4a-8e11-333333333333"
	)
	base := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	plant(t, rig.owner, "auth.sign_out", userA, "session", "aaa00000-0000-7000-8000-000000000001", "success", base)
	plant(t, rig.owner, "member.invite", userB, "member", "aaa00000-0000-7000-8000-000000000002", "denied", base.Add(time.Minute))
	plant(t, rig.owner, "token.create", userA, "token", "aaa00000-0000-7000-8000-000000000003", "success", base.Add(2*time.Minute))
	plant(t, rig.owner, "passkey.remove", userC, "passkey", "aaa00000-0000-7000-8000-000000000004", "failure", base.Add(3*time.Minute))
	if _, err := audit.SealOnce(ctx, rig.owner, io.Discard); err != nil {
		t.Fatal(err)
	}

	filtered := func(query string) []eventLine {
		t.Helper()
		rec := rig.get(t, "/api/audit/events?"+query, admin, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", query, rec.Code, rec.Body.String())
		}
		var lines []eventLine
		for _, raw := range answerOf(t, rec).Events {
			lines = append(lines, parseLine(t, raw))
		}
		return lines
	}
	only := func(query string, want int) []eventLine {
		t.Helper()
		lines := filtered(query)
		if len(lines) != want {
			t.Fatalf("%s returned %d events, want %d", query, len(lines), want)
		}
		return lines
	}

	if lines := only("action=member.invite", 1); lines[0].API.Operation != "member.invite" {
		t.Fatalf("action filter = %s", lines[0].API.Operation)
	}
	if lines := only("actor_id="+userB, 1); lines[0].Actor.User.UID != userB {
		t.Fatalf("actor filter = %s", lines[0].Actor.User.UID)
	}
	if lines := only("target_type=token", 1); lines[0].Resources[0].Type != "token" {
		t.Fatalf("target type filter = %s", lines[0].Resources[0].Type)
	}
	if lines := only("target_id=aaa00000-0000-7000-8000-000000000004", 1); lines[0].API.Operation != "passkey.remove" {
		t.Fatalf("target id filter = %s", lines[0].API.Operation)
	}
	if lines := only("outcome=failure", 1); lines[0].API.Operation != "passkey.remove" {
		t.Fatalf("outcome filter = %s", lines[0].API.Operation)
	}
	// The sign-in seals below base, so the time filters hold only the planted
	// rows.
	only("from="+url.QueryEscape(base.Add(90*time.Second).Format(time.RFC3339)), 2)
	only("to="+url.QueryEscape(base.Add(90*time.Second).Format(time.RFC3339)), 3)
}

func TestAuditEventsTokensReadOrRefused(t *testing.T) {
	rig := newReadRig(t)
	owner := rig.openSession(t, "owner@example.com", "owner")
	sealed := newSealer(t, rig.owner)
	sealed() // the sign-in

	readerID, reader := rig.makeToken(t, owner, "audit:view")
	_, plain := rig.makeToken(t, owner, "task:view")
	if rows := sealed(); len(rows) != 2 {
		t.Fatalf("events = %v, want the two token creates", actions(rows))
	}

	rec := rig.get(t, "/api/audit/events", "", reader)
	if rec.Code != http.StatusOK {
		t.Fatalf("token read = %d %s", rec.Code, rec.Body.String())
	}
	answer := answerOf(t, rec)
	if len(answer.Events) == 0 || answer.More {
		t.Fatalf("token read = %d events, more %v", len(answer.Events), answer.More)
	}
	// The read recorded its export, naming the token.
	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.AuditExport || rows[0].CredentialID != readerID {
		t.Fatalf("events = %v, want one export of the reading token", actions(rows))
	}

	// A token without the scope is refused, and the refusal is sealed.
	rec = rig.get(t, "/api/audit/events", "", plain)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("plain token read = %d %s, want 403", rec.Code, rec.Body.String())
	}
	rows = sealed()
	if len(rows) != 1 || rows[0].Action != audit.AuditRead || rows[0].Outcome != audit.Denied {
		t.Fatalf("events = %v, want one denied audit.read", actions(rows))
	}
	if rows[0].CredentialType != "token" {
		t.Fatalf("credential = %s, want the token", rows[0].CredentialType)
	}
}

func TestAuditReadRecordsFilterNamesAndCount(t *testing.T) {
	rig := newReadRig(t)
	admin := rig.openSession(t, "admin@example.com", "admin")
	ctx := context.Background()
	sealed := newSealer(t, rig.owner)
	sealed() // the sign-in

	plant(t, rig.owner, "member.invite", "018f3b2c-7a10-7c4a-8e11-222222222222", "member", "m-1", "denied", time.Now().UTC().Add(time.Hour))
	if rows := sealed(); len(rows) != 1 || rows[0].Action != audit.MemberInvite {
		t.Fatalf("events = %v, want the planted denial", actions(rows))
	}

	rec := rig.get(t, "/api/audit/events?action=member.invite&outcome=denied&limit=5", admin, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("read = %d %s", rec.Code, rec.Body.String())
	}
	if answer := answerOf(t, rec); len(answer.Events) != 1 {
		t.Fatalf("events = %d, want the planted denial", len(answer.Events))
	}

	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.AuditRead {
		t.Fatalf("events = %v, want one audit.read", actions(rows))
	}
	row := rows[0]
	if row.Outcome != audit.Success || row.ActorType != "user" || row.CredentialType != "session" {
		t.Fatalf("event = %s %s %s %s", row.Action, row.Outcome, row.ActorType, row.CredentialType)
	}
	if got := changeOf(t, row, "filters"); !reflect.DeepEqual(got.To, []any{"action", "outcome"}) {
		t.Fatalf("filters = %v, want the filter names alone", got.To)
	}
	if got := changeOf(t, row, "count"); got.To != float64(1) {
		t.Fatalf("count = %v, want 1", got.To)
	}
	// The stored row carries the filter names, never their values.
	var position int64
	if err := rig.owner.QueryRow(ctx,
		`SELECT position FROM audit.events WHERE action = 'audit.read'`).Scan(&position); err != nil {
		t.Fatal(err)
	}
	freeOf(t, rowDump(t, rig.owner, position), "member.invite", "denied")
}

func TestAuditExportRecordsOnceAnHourPerToken(t *testing.T) {
	rig := newReadRig(t)
	owner := rig.openSession(t, "owner@example.com", "owner")
	sealed := newSealer(t, rig.owner)
	sealed() // the sign-in

	tokenID, secret := rig.makeToken(t, owner, "audit:view")
	if rows := sealed(); len(rows) != 1 || rows[0].Action != audit.TokenCreate {
		t.Fatalf("events = %v, want the token create", actions(rows))
	}

	first := rig.get(t, "/api/audit/events?limit=2", "", secret)
	if first.Code != http.StatusOK {
		t.Fatalf("first read = %d %s", first.Code, first.Body.String())
	}
	count := len(answerOf(t, first).Events)
	if count != 2 {
		t.Fatalf("first read = %d events, want the sign-in and the create", count)
	}
	// A second poll inside the window records nothing at all.
	if second := rig.get(t, "/api/audit/events?limit=2", "", secret); second.Code != http.StatusOK {
		t.Fatalf("second read = %d %s", second.Code, second.Body.String())
	}

	rows := sealed()
	var exports, reads int
	for _, row := range rows {
		switch row.Action {
		case audit.AuditExport:
			exports++
			if row.CredentialType != "token" || row.CredentialID != tokenID {
				t.Fatalf("export credential = %s %s, want the token", row.CredentialType, row.CredentialID)
			}
			if got := changeOf(t, row, "count"); got.To != float64(count) {
				t.Fatalf("export count = %v, want %d", got.To, count)
			}
		case audit.AuditRead:
			reads++
		}
	}
	if exports != 1 || reads != 0 {
		t.Fatalf("%d exports and %d reads in one window, want one export and no read", exports, reads)
	}

	// An hour later the window opens again.
	rig.now = rig.now.Add(61 * time.Minute)
	if third := rig.get(t, "/api/audit/events?limit=2", "", secret); third.Code != http.StatusOK {
		t.Fatalf("third read = %d %s", third.Code, third.Body.String())
	}
	exports = 0
	for _, row := range sealed() {
		if row.Action == audit.AuditExport {
			exports++
		}
	}
	if exports != 1 {
		t.Fatalf("%d exports after the window, want the second alone", exports)
	}
}

// A read that fails after its export was due consumes no hour: the slot is
// taken only once the export has committed.
func TestAuditExportSlotStaysOpenWhenAReadFails(t *testing.T) {
	rig := newReadRig(t)
	owner := rig.openSession(t, "owner@example.com", "owner")
	sealed := newSealer(t, rig.owner)
	sealed() // the sign-in

	_, secret := rig.makeToken(t, owner, "audit:view")
	sealed() // the token create

	// The trail's insert is refused, so the read fails once its export is
	// already due.
	if _, err := rig.owner.Exec(context.Background(), `REVOKE INSERT ON audit.pending FROM upmo_app`); err != nil {
		t.Fatal(err)
	}
	rec := rig.get(t, "/api/audit/events", "", secret)
	if _, err := rig.owner.Exec(context.Background(), `GRANT INSERT ON audit.pending TO upmo_app`); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("refused insert read = %d %s, want 500", rec.Code, rec.Body.String())
	}

	// The retry in the same window still records its export.
	if rec = rig.get(t, "/api/audit/events", "", secret); rec.Code != http.StatusOK {
		t.Fatalf("retry = %d %s", rec.Code, rec.Body.String())
	}
	exports := 0
	for _, row := range sealed() {
		if row.Action == audit.AuditExport {
			exports++
		}
	}
	if exports != 1 {
		t.Fatalf("%d exports after the failed read, want the retry's one", exports)
	}
}
