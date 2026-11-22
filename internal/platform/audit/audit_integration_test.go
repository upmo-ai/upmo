//go:build integration

package audit_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	httpapi "github.com/upmo-ai/upmo/internal/identity/adapters/http"
	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/httpserver"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
	"github.com/upmo-ai/upmo/internal/testutil/sealer"
)

const cookieName = "__Host-upmo_session"

// setup migrates audit and identity on a fresh database and returns the owner
// pool and the request pool that runs as upmo_app.
func setup(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := audit.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	return owner, pgtest.App(t, owner)
}

// mount wires the audit middleware around a mux the way brain does: the
// middleware outside the cross-origin guard, the guard around the mux.
func mount(t *testing.T, mux *http.ServeMux, app *pgxpool.Pool) http.Handler {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return audit.Middleware(audit.Routes(), mux, app, "brain", nil, log)(httpserver.Protect(mux))
}

func seedPerson(t *testing.T, pool *pgxpool.Pool, email string) (string, string) {
	t.Helper()
	stored, hash, keyID, err := sealer.NewEmail(t).Seal(email)
	if err != nil {
		t.Fatal(err)
	}
	var userID, roleID string
	err = pool.QueryRow(context.Background(), `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
		VALUES ($1, $2, $3, 'Ada', (SELECT id FROM identity.roles WHERE key = 'owner'), now())
		RETURNING id::text, role_id::text`, stored, hash, keyID).Scan(&userID, &roleID)
	if err != nil {
		t.Fatal(err)
	}
	return userID, roleID
}

func lookupPerson(t *testing.T, owner *pgxpool.Pool, email string) (string, string) {
	t.Helper()
	var userID, roleID string
	if err := owner.QueryRow(context.Background(), `
		SELECT id::text, role_id::text FROM identity.users WHERE email_hash = ANY($1)`, sealer.NewEmail(t).IndexHashes(email),
	).Scan(&userID, &roleID); err != nil {
		t.Fatal(err)
	}
	return userID, roleID
}

// signOutServer mounts the real sign-out route behind the middleware for a
// freshly seeded person, and returns the handler, the cookie text and the
// session id.
func signOutServer(t *testing.T, owner, app *pgxpool.Pool) (http.Handler, string, string) {
	t.Helper()
	ctx := context.Background()
	sessions := identity.Sessions(app, identity.IdleTimeout, identity.MaxAge)
	userID, _ := seedPerson(t, owner, "ada@example.com")
	uid, err := identity.ParseUserID(userID)
	if err != nil {
		t.Fatal(err)
	}
	text, session, err := sessions.Start(ctx, uid, "passkey", "")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	identity.MountSessions(mux, sessions, log, identity.Passwords(app, nil, sessions).HasPassword)
	return mount(t, mux, app), text, session.ID
}

func readEvent(t *testing.T, owner *pgxpool.Pool, position int64) (audit.Row, []byte, []byte) {
	t.Helper()
	var row audit.Row
	var changes []byte
	var prev, hash []byte
	err := owner.QueryRow(context.Background(), `
		SELECT id::text, occurred_at, service, action, outcome, reason,
		       actor_type, COALESCE(actor_id::text, ''), COALESCE(actor_role_id::text, ''),
		       credential_type, COALESCE(credential_id::text, ''),
		       target_type, target_id, changes,
		       COALESCE(request_id::text, ''), COALESCE(host(source_ip), ''), user_agent,
		       prev_hash, hash
		FROM audit.events WHERE position = $1`, position,
	).Scan(&row.ID, &row.OccurredAt, &row.Service, &row.Action, &row.Outcome, &row.Reason,
		&row.ActorType, &row.ActorID, &row.ActorRoleID, &row.CredentialType, &row.CredentialID,
		&row.TargetType, &row.TargetID, &changes,
		&row.RequestID, &row.SourceIP, &row.UserAgent, &prev, &hash)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(changes, &row.Changes); err != nil {
		t.Fatal(err)
	}
	return row, prev, hash
}

// checkChain re-computes the chain from the seed hash through last.
func checkChain(t *testing.T, owner *pgxpool.Pool, last int64) {
	t.Helper()
	prior := make([]byte, 32)
	for position := int64(1); position <= last; position++ {
		row, prev, hash := readEvent(t, owner, position)
		if !bytes.Equal(prev, prior) {
			t.Fatalf("position %d: prev_hash breaks the chain", position)
		}
		want := sha256.Sum256(append(append([]byte{}, prev...), audit.Canonical(row)...))
		if !bytes.Equal(hash, want[:]) {
			t.Fatalf("position %d: hash does not cover the canonical bytes", position)
		}
		prior = hash
	}
}

func TestSignOutSealsOneChainedEvent(t *testing.T) {
	owner, app := setup(t)
	ctx := context.Background()
	served, token, sessionID := signOutServer(t, owner, app)

	req := httptest.NewRequest(http.MethodDelete, "/api/session", nil)
	req.Header.Set("User-Agent", "upmo-audit-test/1")
	req.AddCookie(&http.Cookie{Name: cookieName, Value: token, Path: "/", Secure: true, HttpOnly: true})
	rec := httptest.NewRecorder()
	served.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("sign out = %d %s", rec.Code, rec.Body.String())
	}

	var count int
	if err := app.QueryRow(ctx, `SELECT count(*) FROM identity.sessions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("%d sessions survived the sign-out", count)
	}

	out := &bytes.Buffer{}
	sealed, err := audit.SealOnce(ctx, owner, out)
	if err != nil {
		t.Fatal(err)
	}
	if sealed != 2 {
		t.Fatalf("sealed %d events, want the seeded sign-in and the sign-out", sealed)
	}
	row, _, _ := readEvent(t, owner, 2)
	userID, roleID := lookupPerson(t, owner, "ada@example.com")

	if row.Action != audit.AuthSignOut || row.Outcome != audit.Success {
		t.Fatalf("event = %s %s", row.Action, row.Outcome)
	}
	if row.Service != "brain" {
		t.Fatalf("service = %s", row.Service)
	}
	if row.ActorType != "user" || row.ActorID != userID || row.ActorRoleID != roleID {
		t.Fatalf("actor = %s %s %s", row.ActorType, row.ActorID, row.ActorRoleID)
	}
	if row.CredentialType != "session" {
		t.Fatalf("credential = %s", row.CredentialType)
	}
	if row.CredentialID != sessionID {
		t.Fatalf("credential id = %s, want the ended session %s", row.CredentialID, sessionID)
	}
	if row.RequestID == "" || row.RequestID != rec.Header().Get("X-Request-Id") {
		t.Fatalf("request id = %s, header %s", row.RequestID, rec.Header().Get("X-Request-Id"))
	}
	if row.SourceIP != "192.0.2.1" {
		t.Fatalf("source address = %s", row.SourceIP)
	}
	if row.UserAgent != "upmo-audit-test/1" {
		t.Fatalf("user agent = %s", row.UserAgent)
	}
	if row.TargetType != "session" || row.TargetID != sessionID {
		t.Fatalf("target = %s %s, want session %s", row.TargetType, row.TargetID, sessionID)
	}

	// The chain runs zero seed, sign-in, sign-out; the sign-out hash covers it.
	first, firstPrev, _ := readEvent(t, owner, 1)
	if first.Action != audit.AuthSignIn {
		t.Fatalf("first event = %s, want the seeded sign-in", first.Action)
	}
	seed := make([]byte, 32)
	if !bytes.Equal(firstPrev, seed) {
		t.Fatalf("first prev_hash = %x, want the zero seed", firstPrev)
	}
	checkChain(t, owner, 2)

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d OCSF lines, want 2", len(lines))
	}
	var line map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &line); err != nil {
		t.Fatal(err)
	}
	if line["stream"] != "audit" {
		t.Fatalf("stream = %v", line["stream"])
	}
	if line["class_uid"] != float64(3002) || line["activity_id"] != float64(2) {
		t.Fatalf("class %v activity %v", line["class_uid"], line["activity_id"])
	}
	metadata, ok := line["metadata"].(map[string]any)
	if !ok || metadata["uid"] != row.ID || metadata["sequence"] != float64(2) {
		t.Fatalf("metadata = %v", line["metadata"])
	}
	api, ok := line["api"].(map[string]any)
	if !ok || api["operation"] != string(audit.AuthSignOut) {
		t.Fatalf("api = %v", line["api"])
	}
	if line["time"] != float64(row.OccurredAt.UTC().UnixMilli()) {
		t.Fatalf("time = %v, want %d", line["time"], row.OccurredAt.UTC().UnixMilli())
	}
}

func TestUnauthorizedSignOutRecordsDenied(t *testing.T) {
	owner, app := setup(t)
	served, _, _ := signOutServer(t, owner, app)

	rec := httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/session", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no cookie = %d, want 401", rec.Code)
	}

	out := &bytes.Buffer{}
	if _, err := audit.SealOnce(context.Background(), owner, out); err != nil {
		t.Fatal(err)
	}
	// Position 1 is the seeded sign-in; the refusal follows it.
	row, _, _ := readEvent(t, owner, 2)
	if row.Action != audit.AuthSignOut || row.Outcome != audit.Denied {
		t.Fatalf("event = %s %s, want a denied sign-out", row.Action, row.Outcome)
	}
	if row.ActorType != "anonymous" {
		t.Fatalf("actor = %s, want anonymous", row.ActorType)
	}
	if row.RequestID == "" || row.RequestID != rec.Header().Get("X-Request-Id") {
		t.Fatalf("request id = %s, header %s", row.RequestID, rec.Header().Get("X-Request-Id"))
	}
}

func TestCrossOriginRefusalRecordsDenied(t *testing.T) {
	owner, app := setup(t)
	served, token, _ := signOutServer(t, owner, app)

	// The cross-origin guard refuses the request before the mux runs, so the
	// middleware must resolve the pattern itself.
	req := httptest.NewRequest(http.MethodDelete, "/api/session", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Origin", "https://evil.example")
	req.AddCookie(&http.Cookie{Name: cookieName, Value: token, Path: "/", Secure: true, HttpOnly: true})
	rec := httptest.NewRecorder()
	served.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site = %d, want 403", rec.Code)
	}

	out := &bytes.Buffer{}
	if _, err := audit.SealOnce(context.Background(), owner, out); err != nil {
		t.Fatal(err)
	}
	row, _, _ := readEvent(t, owner, 2)
	if row.Action != audit.AuthSignOut || row.Outcome != audit.Denied {
		t.Fatalf("event = %s %s, want a denied sign-out", row.Action, row.Outcome)
	}
	if row.ActorType != "anonymous" {
		t.Fatalf("actor = %s, want anonymous: the guard refused before any handler knew the caller", row.ActorType)
	}
	if row.RequestID != rec.Header().Get("X-Request-Id") {
		t.Fatalf("request id = %s, header %s", row.RequestID, rec.Header().Get("X-Request-Id"))
	}
	if row.SourceIP != "192.0.2.1" {
		t.Fatalf("source address = %s", row.SourceIP)
	}
}

func TestFailedRequestSealsItsOwnFailureEvent(t *testing.T) {
	owner, app := setup(t)
	ctx := context.Background()

	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /api/session", func(w http.ResponseWriter, r *http.Request) {
		tx, err := app.Begin(r.Context())
		if err != nil {
			http.Error(w, "tx", http.StatusInternalServerError)
			return
		}
		if err := audit.Record(r.Context(), tx, audit.Event{Action: audit.AuthSignOut, Outcome: audit.Success}); err != nil {
			_ = tx.Rollback(r.Context())
			http.Error(w, "record", http.StatusInternalServerError)
			return
		}
		// The business transaction rolls back with the failed response and
		// takes its pending row along; the middleware then writes the
		// failure event in a transaction of its own.
		_ = tx.Rollback(r.Context())
		http.Error(w, "broken", http.StatusInternalServerError)
	})
	served := mount(t, mux, app)

	rec := httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/session", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("broken handler = %d, want 500", rec.Code)
	}

	// The middleware's failure event waits in pending for the sealer; the
	// handler's own success event rolled back with its transaction.
	var pending int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM audit.pending`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 1 {
		t.Fatalf("%d pending rows, want only the middleware's failure event", pending)
	}
	var action, outcome string
	if err := owner.QueryRow(ctx, `SELECT action, outcome FROM audit.pending`).Scan(&action, &outcome); err != nil {
		t.Fatal(err)
	}
	if outcome != string(audit.Failure) {
		t.Fatalf("pending holds %s %s, want the middleware's failure: the handler's own success event must have rolled back", action, outcome)
	}

	out := &bytes.Buffer{}
	if _, err := audit.SealOnce(ctx, owner, out); err != nil {
		t.Fatal(err)
	}
	row, _, _ := readEvent(t, owner, 1)
	if row.Outcome != audit.Failure {
		t.Fatalf("outcome = %s, want failure", row.Outcome)
	}
	var events int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM audit.events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("%d events sealed, want the one failure", events)
	}
}

// TestUnrecordedSuccessFallsBack seals the fallback event the middleware
// writes when a changing route commits no event of its own. The unrecorded
// hook that makes a test build fail stays inside the package: this file
// asserts the sealed event, which carries the same information.
func TestUnrecordedSuccessFallsBack(t *testing.T) {
	owner, app := setup(t)
	ctx := context.Background()

	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /api/session", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	served := mount(t, mux, app)

	rec := httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/session", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("handler = %d, want 204", rec.Code)
	}

	out := &bytes.Buffer{}
	if _, err := audit.SealOnce(ctx, owner, out); err != nil {
		t.Fatal(err)
	}
	row, _, _ := readEvent(t, owner, 1)
	if row.Outcome != audit.Success || row.Reason != "unrecorded" {
		t.Fatalf("event = %s %q, want a success marked unrecorded", row.Outcome, row.Reason)
	}
}

func TestPendingIsWriteOnlyForTheAppRole(t *testing.T) {
	owner, app := setup(t)
	ctx := context.Background()
	if err := audit.Record(ctx, app, audit.Event{Action: audit.AuthSignOut, Outcome: audit.Success}); err != nil {
		t.Fatalf("the app role could not insert a pending event: %v", err)
	}
	var count int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM audit.pending`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
	if _, err := app.Exec(ctx, `SELECT count(*) FROM audit.pending`); err == nil {
		t.Fatal("the app role read audit.pending")
	}
	if _, err := app.Exec(ctx, `UPDATE audit.pending SET reason = 'x'`); err == nil {
		t.Fatal("the app role updated audit.pending")
	}
	if _, err := app.Exec(ctx, `DELETE FROM audit.pending`); err == nil {
		t.Fatal("the app role deleted audit.pending")
	}
}

func TestEventsAreAppendOnly(t *testing.T) {
	owner, app := setup(t)
	ctx := context.Background()
	if err := audit.Record(ctx, app, audit.Event{Action: audit.AuthSignOut, Outcome: audit.Success}); err != nil {
		t.Fatal(err)
	}
	if _, err := audit.SealOnce(ctx, owner, io.Discard); err != nil {
		t.Fatal(err)
	}

	const refusal = "audit events cannot be changed"
	if _, err := owner.Exec(ctx, `UPDATE audit.events SET reason = 'x'`); err == nil || !strings.Contains(err.Error(), refusal) {
		t.Fatalf("owner update = %v, want %q", err, refusal)
	}
	if _, err := owner.Exec(ctx, `DELETE FROM audit.events`); err == nil || !strings.Contains(err.Error(), refusal) {
		t.Fatalf("owner delete = %v, want %q", err, refusal)
	}
	if _, err := owner.Exec(ctx, `TRUNCATE audit.events`); err == nil || !strings.Contains(err.Error(), refusal) {
		t.Fatalf("owner truncate = %v, want %q", err, refusal)
	}
	if _, err := app.Exec(ctx, `INSERT INTO audit.events (position, sealed_at, prev_hash, hash, id, occurred_at, service, action, outcome, reason, actor_type, credential_type, target_type, target_id, changes, user_agent) VALUES (9, now(), '', '', uuidv7(), now(), 'brain', 'auth.sign_out', 'success', '', 'system', 'none', '', '', '{}', '')`); err == nil {
		t.Fatal("the app role inserted a sealed event")
	}
	if _, err := app.Exec(ctx, `UPDATE audit.events SET reason = 'x'`); err == nil {
		t.Fatal("the app role updated a sealed event")
	}
	if _, err := app.Exec(ctx, `DELETE FROM audit.events`); err == nil {
		t.Fatal("the app role deleted a sealed event")
	}
	if _, err := app.Exec(ctx, `TRUNCATE audit.events`); err == nil {
		t.Fatal("the app role truncated a sealed event")
	}
}

func TestAppReadsEventsOnlyWithAuditView(t *testing.T) {
	owner, app := setup(t)
	ctx := context.Background()
	if err := audit.Record(ctx, app, audit.Event{Action: audit.AuthSignOut, Outcome: audit.Success}); err != nil {
		t.Fatal(err)
	}
	if _, err := audit.SealOnce(ctx, owner, io.Discard); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := app.QueryRow(ctx, `SELECT count(*) FROM audit.events`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("without audit:view the app role saw %d events", count)
	}
	tx, err := app.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('upmo.allow', 'audit:view', true)`); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM audit.events`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("with audit:view the app role saw %d events, want 1", count)
	}
}

func TestRolledBackChangeLeavesNoPending(t *testing.T) {
	owner, app := setup(t)
	ctx := context.Background()
	tx, err := app.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := audit.Record(ctx, tx, audit.Event{Action: audit.AuthSignOut, Outcome: audit.Success}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM audit.pending`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("%d pending rows survived the rollback", count)
	}
}

func TestFailedAuditInsertRollsTheChangeBack(t *testing.T) {
	owner, app := setup(t)
	ctx := context.Background()
	tx, err := app.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `INSERT INTO identity.teams (name) VALUES ('Sam')`); err != nil {
		t.Fatal(err)
	}
	if err := audit.Record(ctx, tx, audit.Event{Action: "test.unknown"}); err == nil {
		t.Fatal("an action outside the catalog was recorded")
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := owner.QueryRow(ctx,
		`SELECT count(*) FROM identity.teams WHERE name = 'Sam'`,
	).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("the change survived its failed audit insert")
	}
}

func TestSealerChainsAndIsIdempotent(t *testing.T) {
	owner, app := setup(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := audit.Record(ctx, app, audit.Event{Action: audit.AuthSignOut, Outcome: audit.Success}); err != nil {
			t.Fatal(err)
		}
	}
	out := &bytes.Buffer{}
	sealed, err := audit.SealOnce(ctx, owner, out)
	if err != nil {
		t.Fatal(err)
	}
	if sealed != 5 {
		t.Fatalf("sealed %d events, want 5", sealed)
	}
	if lines := strings.Count(out.String(), "\n"); lines != 5 {
		t.Fatalf("%d OCSF lines, want 5", lines)
	}
	checkChain(t, owner, 5)
	var head int64
	if err := owner.QueryRow(ctx, `SELECT position FROM audit.head WHERE one`).Scan(&head); err != nil {
		t.Fatal(err)
	}
	if head != 5 {
		t.Fatalf("head at %d, want 5", head)
	}

	again := &bytes.Buffer{}
	sealed, err = audit.SealOnce(ctx, owner, again)
	if err != nil {
		t.Fatal(err)
	}
	if sealed != 0 || again.Len() != 0 {
		t.Fatalf("a repeated run sealed %d events with %d bytes, want silence", sealed, again.Len())
	}
	var events int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM audit.events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 5 {
		t.Fatalf("%d events after the repeated run, want 5", events)
	}
	var unchanged int64
	if err := owner.QueryRow(ctx, `SELECT position FROM audit.head WHERE one`).Scan(&unchanged); err != nil {
		t.Fatal(err)
	}
	if unchanged != 5 {
		t.Fatalf("head moved to %d on an empty run", unchanged)
	}
}

func TestSealerUnderConcurrentWriters(t *testing.T) {
	owner, app := setup(t)
	ctx := context.Background()

	const writers, per = 3, 20
	stop := make(chan struct{})
	done := make(chan struct{})
	failed := make(chan error, 1)
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := audit.SealOnce(ctx, owner, io.Discard); err != nil {
				failed <- err
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				if err := audit.Record(ctx, app, audit.Event{Action: audit.AuthSignOut, Outcome: audit.Success}); err != nil {
					t.Errorf("record: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(stop)
	<-done
	select {
	case err := <-failed:
		t.Fatal(err)
	default:
	}

	for {
		sealed, err := audit.SealOnce(ctx, owner, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if sealed < audit.SealBatch {
			break
		}
	}

	var last int64
	if err := owner.QueryRow(ctx, `SELECT position FROM audit.head WHERE one`).Scan(&last); err != nil {
		t.Fatal(err)
	}
	if last != writers*per {
		t.Fatalf("head at %d, want %d", last, writers*per)
	}
	checkChain(t, owner, last)
}

func TestSealedAtMovesForwardAcrossPartitions(t *testing.T) {
	owner, app := setup(t)
	ctx := context.Background()
	if err := audit.Record(ctx, app, audit.Event{Action: audit.AuthSignOut, Outcome: audit.Success}); err != nil {
		t.Fatal(err)
	}
	if _, err := audit.SealOnce(ctx, owner, io.Discard); err != nil {
		t.Fatal(err)
	}
	// Push the chain's sealed time past a month boundary: the next event
	// occurs before the boundary but is sealed after it, and still may not
	// send position or sealed time backwards.
	if _, err := owner.Exec(ctx, `UPDATE audit.head SET sealed_at = '2027-01-15T00:00:00Z' WHERE one`); err != nil {
		t.Fatal(err)
	}
	if err := audit.Record(ctx, app, audit.Event{Action: audit.AuthSignOut, Outcome: audit.Success}); err != nil {
		t.Fatal(err)
	}
	if _, err := audit.SealOnce(ctx, owner, io.Discard); err != nil {
		t.Fatal(err)
	}
	checkChain(t, owner, 2)

	row, _, _ := readEvent(t, owner, 2)
	if row.OccurredAt.After(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("the test lost its point: occurred_at %v is after the boundary", row.OccurredAt)
	}
	first := partitionOf(t, owner, 1)
	second := partitionOf(t, owner, 2)
	if first == second {
		t.Fatalf("both events landed in %s", first)
	}
	if first == "audit.events_default" {
		t.Fatal("the first event landed in the default partition")
	}
	if second != "audit.events_y2027m01" {
		t.Fatalf("the later-sealed event landed in %s", second)
	}
}

func partitionOf(t *testing.T, owner *pgxpool.Pool, position int64) string {
	t.Helper()
	var name string
	if err := owner.QueryRow(context.Background(),
		`SELECT tableoid::regclass::text FROM audit.events WHERE position = $1`, position,
	).Scan(&name); err != nil {
		t.Fatal(err)
	}
	return name
}

func TestMigrationUpDownUp(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := audit.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Down(ctx, owner, audit.Schema, audit.Migrations()); err != nil {
		t.Fatal(err)
	}
	var tables int
	if err := owner.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.tables WHERE table_schema = 'audit'`,
	).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Fatalf("%d audit tables survived the down migration", tables)
	}
	if err := audit.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	var position int64
	if err := owner.QueryRow(ctx, `SELECT position FROM audit.head WHERE one`).Scan(&position); err != nil {
		t.Fatal(err)
	}
	if position != 0 {
		t.Fatalf("head at %d after a fresh up, want 0", position)
	}
}

func TestStartupEventRecordsSchemaVersions(t *testing.T) {
	owner, _ := setup(t)
	ctx := context.Background()
	if err := audit.Migrated(ctx, owner); err != nil {
		t.Fatal(err)
	}
	var action string
	var changes []byte
	if err := owner.QueryRow(ctx, `SELECT action, changes FROM audit.pending`).Scan(&action, &changes); err != nil {
		t.Fatal(err)
	}
	if action != string(audit.SystemMigrated) {
		t.Fatalf("pending action = %s", action)
	}
	if _, err := audit.SealOnce(ctx, owner, io.Discard); err != nil {
		t.Fatal(err)
	}
	row, _, _ := readEvent(t, owner, 1)
	if row.ActorType != "system" || row.ActorID != "" {
		t.Fatalf("actor = %s %s, want the system actor", row.ActorType, row.ActorID)
	}
	var got audit.Changes
	if err := json.Unmarshal(changes, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Fields) != 1 || got.Fields[0] != "versions" {
		t.Fatalf("fields = %v, want versions", got.Fields)
	}
	version, ok := got.Values["versions"]
	if !ok {
		t.Fatalf("values = %v, want versions", got.Values)
	}
	versions, ok := version.To.(string)
	if !ok || versions != "audit=1 identity=1" {
		t.Fatalf("versions = %v, want %q", version.To, "audit=1 identity=1")
	}
}

// TestSealedChangesCarryThePendingValues seals the startup event, whose safe
// value is non-empty, and a sign-out, and reads both back: the stored changes
// column must hold the pending row's changes, so the row hashes again to its
// stored hash and the OCSF stream agrees with the database.
func TestSealedChangesCarryThePendingValues(t *testing.T) {
	owner, app := setup(t)
	ctx := context.Background()
	if err := audit.Migrated(ctx, owner); err != nil {
		t.Fatal(err)
	}
	served, token, _ := signOutServer(t, owner, app)

	req := httptest.NewRequest(http.MethodDelete, "/api/session", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: token, Path: "/", Secure: true, HttpOnly: true})
	rec := httptest.NewRecorder()
	served.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("sign out = %d %s", rec.Code, rec.Body.String())
	}

	pending := pendingChanges(t, owner)

	out := &bytes.Buffer{}
	sealed, err := audit.SealOnce(ctx, owner, out)
	if err != nil {
		t.Fatal(err)
	}
	if sealed != 3 {
		t.Fatalf("sealed %d events, want the migrated, the seeded sign-in and the sign-out", sealed)
	}
	checkChain(t, owner, 3)

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("%d OCSF lines, want 3", len(lines))
	}
	for position := int64(1); position <= 3; position++ {
		row, _, _ := readEvent(t, owner, position)
		want, ok := pending[string(row.Action)]
		if !ok {
			t.Fatalf("no pending changes recorded for %s", row.Action)
		}
		var wantChanges audit.Changes
		if err := json.Unmarshal(want, &wantChanges); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(row.Changes, wantChanges) {
			t.Fatalf("%s: stored changes = %+v, want the pending %+v", row.Action, row.Changes, wantChanges)
		}

		var line map[string]any
		if err := json.Unmarshal([]byte(lines[position-1]), &line); err != nil {
			t.Fatal(err)
		}
		metadata, ok := line["metadata"].(map[string]any)
		if !ok || metadata["uid"] != row.ID {
			t.Fatalf("line %d = %v, want the sealed %s", position, line["metadata"], row.Action)
		}
		unmapped, ok := line["unmapped"].(map[string]any)
		if !ok {
			t.Fatalf("%s: OCSF line has no unmapped object", row.Action)
		}
		got, err := json.Marshal(unmapped["changes"])
		if err != nil {
			t.Fatal(err)
		}
		var lineChanges audit.Changes
		if err := json.Unmarshal(got, &lineChanges); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(lineChanges, wantChanges) {
			t.Fatalf("%s: OCSF unmapped.changes = %+v, want the pending %+v", row.Action, lineChanges, wantChanges)
		}
	}
}

// pendingChanges reads each pending row's changes before the sealer moves it,
// keyed by action.
func pendingChanges(t *testing.T, owner *pgxpool.Pool) map[string][]byte {
	t.Helper()
	rows, err := owner.Query(context.Background(), `SELECT action, changes FROM audit.pending ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string][]byte{}
	for rows.Next() {
		var action string
		var changes []byte
		if err := rows.Scan(&action, &changes); err != nil {
			t.Fatal(err)
		}
		out[action] = changes
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestBearerTokenRecordsTheHeldRole presents a personal token: the event must
// carry the role the person holds at that moment, like the session path.
func TestBearerTokenRecordsTheHeldRole(t *testing.T) {
	owner, app := setup(t)
	ctx := context.Background()
	sessions := identity.Sessions(app, identity.IdleTimeout, identity.MaxAge)
	userID, _ := seedPerson(t, owner, "ada@example.com")
	secret, hash, err := domain.NewAPITokenSecret()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO identity.api_tokens (user_id, name, token_hash, scope, created_at, expires_at)
		VALUES ($1, 'ci', $2, ARRAY['task:view'], now(), now() + interval '30 days')`,
		userID, hash); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	httpapi.Handler{Sessions: sessions, Log: log, Tokens: identity.APITokens(app, log)}.Mount(mux)
	// The bearer path serves no audited route yet; mapping the session route
	// here makes the middleware write the fallback event, which carries the
	// actor the token path put on the scope.
	table := audit.Table{"GET /api/session": {Action: audit.AuthSignOut}}
	served := audit.Middleware(table, mux, app, "brain", nil, log)(httpserver.Protect(mux))

	req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	rec := httptest.NewRecorder()
	served.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("bearer session = %d %s", rec.Code, rec.Body.String())
	}

	if _, err := audit.SealOnce(ctx, owner, io.Discard); err != nil {
		t.Fatal(err)
	}
	row, _, _ := readEvent(t, owner, 1)
	_, roleID := lookupPerson(t, owner, "ada@example.com")
	if row.ActorType != "user" || row.ActorID != userID || row.ActorRoleID != roleID {
		t.Fatalf("actor = %s %s %s, want the user with the held role %s", row.ActorType, row.ActorID, row.ActorRoleID, roleID)
	}
	if row.CredentialType != "token" {
		t.Fatalf("credential = %s, want token", row.CredentialType)
	}
}
