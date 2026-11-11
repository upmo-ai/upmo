//go:build integration

package audit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/memory"
	memoryapp "github.com/upmo-ai/upmo/internal/memory/app"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

// memoryRig migrates audit, identity and memory on a fresh database and
// returns the owner pool and the request pool that runs as upmo_app.
func memoryRig(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := audit.Up(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, identity.Schema, identity.Migrations()); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, owner, memory.Schema, memory.Migrations()); err != nil {
		t.Fatal(err)
	}
	return owner, pgtest.App(t, owner)
}

const memoryUser = "018f3b2c-7a10-7c4a-8e11-111111111111"

func TestMemoryActionsSealTheirWrites(t *testing.T) {
	owner, appPool := memoryRig(t)
	sealed := newSealer(t, owner)
	facts := memory.NewFacts(appPool)
	ctx := context.Background()
	now := time.Now()

	if err := memoryapp.SaveProfile(ctx, facts, memoryUser, "Ada's own profile text", now); err != nil {
		t.Fatal(err)
	}
	first, err := memoryapp.AddFact(ctx, facts, memoryUser, "Ada prefers tea over coffee", now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := memoryapp.EditFact(ctx, facts, memoryUser, first, "Ada drinks only tea", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := memoryapp.Forget(ctx, facts, memoryUser, second, now); err != nil {
		t.Fatal(err)
	}

	rows := sealed()
	if len(rows) != 4 {
		t.Fatalf("%d events, want profile, create, edit and forget", len(rows))
	}
	for _, row := range rows {
		if row.ActorType != "system" || row.CredentialType != "none" {
			t.Fatalf("%s: actor = %s %s, want the system fallback of a writer with no request", row.Action, row.ActorType, row.CredentialType)
		}
	}

	if rows[0].Action != audit.ProfileUpdate {
		t.Fatalf("first event = %s, want profile.update", rows[0].Action)
	}
	if strings.Join(rows[0].Changes.Fields, ",") != "body" || hasAnyValue(rows[0]) {
		t.Fatalf("profile changes = %s, want the field name alone", changesJSON(t, rows[0]))
	}
	if containsSecret(rows[0], "Ada's own profile text") {
		t.Fatalf("profile event holds the body: %s", changesJSON(t, rows[0]))
	}

	if rows[1].Action != audit.FactCreate || rows[1].TargetID != first {
		t.Fatalf("second event = %s target %s, want the created fact %s", rows[1].Action, rows[1].TargetID, first)
	}
	if strings.Join(rows[1].Changes.Fields, ",") != "claim" || hasAnyValue(rows[1]) {
		t.Fatalf("create changes = %s, want the field name alone", changesJSON(t, rows[1]))
	}
	if containsSecret(rows[1], "Ada prefers tea over coffee") {
		t.Fatalf("create event holds the claim: %s", changesJSON(t, rows[1]))
	}

	if rows[2].Action != audit.FactUpdate || rows[2].TargetID != first {
		t.Fatalf("third event = %s target %s, want the edited fact %s", rows[2].Action, rows[2].TargetID, first)
	}
	superseded := changeOf(t, rows[2], "superseded_by")
	if superseded.To != second {
		t.Fatalf("superseded_by = %v, want the new fact %s", superseded.To, second)
	}
	if containsSecret(rows[2], "Ada drinks only tea") {
		t.Fatalf("edit event holds the claim: %s", changesJSON(t, rows[2]))
	}

	if rows[3].Action != audit.FactDelete || rows[3].TargetID != second {
		t.Fatalf("fourth event = %s target %s, want the forgotten fact %s", rows[3].Action, rows[3].TargetID, second)
	}
	if hasAnyValue(rows[3]) {
		t.Fatalf("forget changes = %s, want no values", changesJSON(t, rows[3]))
	}
}

func TestMemoryRouteSealsItsOwnEvent(t *testing.T) {
	owner, appPool := memoryRig(t)
	sealed := newSealer(t, owner)
	facts := memory.NewFacts(appPool)

	mux := http.NewServeMux()
	memory.Handler{
		Editor: facts,
		User: func(r *http.Request) (string, bool) {
			audit.WithActor(r.Context(), audit.Actor{Type: "user", ID: memoryUser, CredentialType: "session"})
			return memoryUser, true
		},
	}.Mount(mux, nil)
	served := serveWithAudit(t, mux, appPool)

	rec := httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/personalization/facts", strings.NewReader(`{"claim":"Ada prefers tea over coffee"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("add fact = %d %s", rec.Code, rec.Body.String())
	}

	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.FactCreate {
		t.Fatalf("events = %v, want one fact.create", actions(rows))
	}
	// The store sealed the event in its own transaction, so the middleware had
	// nothing to fall back on: the reason is empty, not unrecorded.
	if rows[0].Reason != "" {
		t.Fatalf("reason = %q, want the store's own event, not the unrecorded fallback", rows[0].Reason)
	}
	if rows[0].ActorType != "user" || rows[0].ActorID != memoryUser || rows[0].CredentialType != "session" {
		t.Fatalf("actor = %s %s %s", rows[0].ActorType, rows[0].ActorID, rows[0].CredentialType)
	}
	if containsSecret(rows[0], "Ada prefers tea over coffee") {
		t.Fatalf("create event holds the claim: %s", changesJSON(t, rows[0]))
	}
}

func TestMemoryRouteRefusalRecordsDenied(t *testing.T) {
	owner, appPool := memoryRig(t)
	sealed := newSealer(t, owner)
	facts := memory.NewFacts(appPool)

	mux := http.NewServeMux()
	memory.Handler{
		Editor: facts,
		// The personalisation page has no other person's access to refuse:
		// a request with no known person is the refusal this route has.
		User: func(r *http.Request) (string, bool) { return "", false },
	}.Mount(mux, nil)
	served := serveWithAudit(t, mux, appPool)

	rec := httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/personalization/profile", strings.NewReader(`{"body":"nobody wrote this"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("a person with no session = %d %s, want 401", rec.Code, rec.Body.String())
	}

	rows := sealed()
	if len(rows) != 1 || rows[0].Action != audit.ProfileUpdate {
		t.Fatalf("events = %v, want one denied profile.update", actions(rows))
	}
	if rows[0].Outcome != audit.Denied {
		t.Fatalf("outcome = %s, want denied", rows[0].Outcome)
	}
	if rows[0].ActorType != "anonymous" {
		t.Fatalf("actor = %s, want anonymous: no person was known", rows[0].ActorType)
	}
	if containsSecret(rows[0], "nobody wrote this") {
		t.Fatalf("denied event holds the refused body: %s", changesJSON(t, rows[0]))
	}
}
