//go:build integration

package audit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/platform/audit"
)

// newSealer returns a function that seals every pending event and returns the
// rows sealed since its last call, oldest first. Every work-context test
// drains the chain this way, so an assertion sees exactly the events its own
// writes left.
func newSealer(t *testing.T, owner *pgxpool.Pool) func() []audit.Row {
	t.Helper()
	var read int64
	return func() []audit.Row {
		if _, err := audit.SealOnce(context.Background(), owner, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		rows, err := owner.Query(context.Background(), `
			SELECT id::text, occurred_at, service, action, outcome, reason,
			       actor_type, COALESCE(actor_id::text, ''), COALESCE(actor_role_id::text, ''),
			       credential_type, COALESCE(credential_id::text, ''),
			       target_type, target_id, changes, position
			FROM audit.events WHERE position > $1 ORDER BY position`, read)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []audit.Row
		for rows.Next() {
			var row audit.Row
			var changes []byte
			var position int64
			if err := rows.Scan(&row.ID, &row.OccurredAt, &row.Service, &row.Action, &row.Outcome, &row.Reason,
				&row.ActorType, &row.ActorID, &row.ActorRoleID, &row.CredentialType, &row.CredentialID,
				&row.TargetType, &row.TargetID, &changes, &position); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(changes, &row.Changes); err != nil {
				t.Fatal(err)
			}
			out = append(out, row)
			read = position
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
}

// actions lists the actions of sealed rows, in order.
func actions(rows []audit.Row) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, string(row.Action))
	}
	return out
}

// changesJSON renders one row's changes, for the failures to show what leaked.
func changesJSON(t *testing.T, row audit.Row) string {
	t.Helper()
	out, err := json.Marshal(row.Changes)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// hasAnyValue reports a row whose changes hold a value at all: an event with
// values must say so on purpose, and one for a content change must not.
func hasAnyValue(row audit.Row) bool {
	return len(row.Changes.Values) != 0
}

// changeOf reads one safe value of a sealed row.
func changeOf(t *testing.T, row audit.Row, field string) audit.Change {
	t.Helper()
	change, ok := row.Changes.Values[field]
	if !ok {
		t.Fatalf("%s: changes = %s, want the %s value", row.Action, changesJSON(t, row), field)
	}
	return change
}

// statusEdge reads the status a task event moved.
func statusEdge(t *testing.T, row audit.Row) (string, string) {
	t.Helper()
	change := changeOf(t, row, "status")
	from, _ := change.From.(string)
	to, _ := change.To.(string)
	return from, to
}

// containsSecret reports whether content the audit must never hold appears in
// a sealed row: not in the changes, not in the reason, not anywhere.
func containsSecret(row audit.Row, secret string) bool {
	if secret == "" {
		return false
	}
	row.Changes.Fields = nil
	row.Changes.Values = nil
	row.Reason = ""
	encoded, err := json.Marshal(row)
	if err != nil {
		return strings.Contains(row.TargetID, secret)
	}
	return strings.Contains(string(encoded), secret)
}
