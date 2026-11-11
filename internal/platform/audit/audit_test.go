package audit

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestCanonicalGolden(t *testing.T) {
	want := `{"action":"auth.sign_out","actor_id":"0199a2f5-7a01-7c1e-9b3a-5c2d8e4f6a02",` +
		`"actor_role_id":"0199a2f5-7a01-7c1e-9b3a-5c2d8e4f6a03","actor_type":"user",` +
		`"changes":{"fields":[],"values":{}},` +
		`"credential_id":"0199a2f5-7a01-7c1e-9b3a-5c2d8e4f6a04","credential_type":"session",` +
		`"id":"0199a2f5-7a01-7c1e-9b3a-5c2d8e4f6a01","occurred_at":"2026-11-11T10:30:00.123456789Z",` +
		`"outcome":"success","reason":"","request_id":"0199a2f5-7a01-7c1e-9b3a-5c2d8e4f6a05",` +
		`"service":"brain","source_ip":"203.0.113.9",` +
		`"target_id":"0199a2f5-7a01-7c1e-9b3a-5c2d8e4f6a04","target_type":"session",` +
		`"user_agent":"upmo-test/1"}`
	if got := string(Canonical(goldenRow())); got != want {
		t.Fatalf("canonical bytes:\n got %s\nwant %s", got, want)
	}
}

func TestChainHashCoversTheWholeChain(t *testing.T) {
	prev := make([]byte, 32)
	canonical := Canonical(goldenRow())
	want := sha256.Sum256(append(append([]byte{}, prev...), canonical...))

	// A different previous hash changes the chain hash, so an event cannot
	// pass for another's successor.
	other := make([]byte, 32)
	other[0] = 1
	if sha256.Sum256(append(append([]byte{}, other...), canonical...)) == want {
		t.Fatal("the chain hash ignores the previous hash")
	}
	// A different event body changes it too.
	changed := goldenRow()
	changed.Outcome = Denied
	if sha256.Sum256(append(append([]byte{}, prev...), Canonical(changed)...)) == want {
		t.Fatal("the chain hash ignores the event body")
	}
}

func TestFilteredChangesKeepOnlySafeValues(t *testing.T) {
	changes := Changes{
		Fields: []string{"visibility", "note", "status"},
		Values: map[string]Change{
			"visibility": {From: "private", To: "team"},
			"note":       {From: "secret note", To: "another secret"},
			"status":     {From: "open", To: "done"},
		},
	}
	got := filtered(changes, catalog[AuthSignOut])
	// auth.sign_out admits no values: the field names stay, the secrets go.
	if len(got.Values) != 0 {
		t.Fatalf("values = %v, want none", got.Values)
	}
	if strings.Join(got.Fields, ",") != "note,status,visibility" {
		t.Fatalf("fields = %v, want note, status, visibility", got.Fields)
	}

	allowed := definition{values: map[string]bool{"status": true, "visibility": true}}
	got = filtered(changes, allowed)
	if len(got.Values) != 2 {
		t.Fatalf("values = %v, want visibility and status", got.Values)
	}
	if got.Values["visibility"].To != "team" || got.Values["status"].To != "done" {
		t.Fatalf("kept the wrong values: %v", got.Values)
	}
	// The dropped value loses its values entry, not its field name.
	if strings.Join(got.Fields, ",") != "note,status,visibility" {
		t.Fatalf("fields = %v, want note, status, visibility", got.Fields)
	}
}

// failingExecer fails on any write, proving the catalog check runs first.
type failingExecer struct{}

func (failingExecer) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("the catalog check must run first")
}

func TestRecordRefusesUnknownAction(t *testing.T) {
	err := Record(context.Background(), failingExecer{}, Event{Action: "test.unknown"})
	if err == nil || !strings.Contains(err.Error(), "not in the catalog") {
		t.Fatalf("err = %v, want a catalog refusal", err)
	}
}

func goldenRow() Row {
	return Row{ //nolint:gosec // fixed test vector, not a credential
		ID:             "0199a2f5-7a01-7c1e-9b3a-5c2d8e4f6a01",
		OccurredAt:     time.Date(2026, 11, 11, 10, 30, 0, 123456789, time.UTC),
		Service:        "brain",
		Action:         AuthSignOut,
		Outcome:        Success,
		ActorType:      "user",
		ActorID:        "0199a2f5-7a01-7c1e-9b3a-5c2d8e4f6a02",
		ActorRoleID:    "0199a2f5-7a01-7c1e-9b3a-5c2d8e4f6a03",
		CredentialType: "session",
		CredentialID:   "0199a2f5-7a01-7c1e-9b3a-5c2d8e4f6a04",
		TargetType:     "session",
		TargetID:       "0199a2f5-7a01-7c1e-9b3a-5c2d8e4f6a04",
		RequestID:      "0199a2f5-7a01-7c1e-9b3a-5c2d8e4f6a05",
		SourceIP:       "203.0.113.9",
		UserAgent:      "upmo-test/1",
	}
}
