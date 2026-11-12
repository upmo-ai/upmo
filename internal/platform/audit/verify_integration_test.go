//go:build integration

package audit_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/platform/audit"
)

// tamper runs one statement on audit.events with the append-only trigger
// aside, the way a test breaks the chain the way the trigger would refuse.
func tamper(t *testing.T, owner *pgxpool.Pool, stmt string, arg any) {
	t.Helper()
	ctx := context.Background()
	if _, err := owner.Exec(ctx,
		`ALTER TABLE audit.events DISABLE TRIGGER events_no_change`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, stmt, arg); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx,
		`ALTER TABLE audit.events ENABLE TRIGGER events_no_change`,
	); err != nil {
		t.Fatal(err)
	}
}

// pendingVerify is one recorded verification event waiting in pending.
type pendingVerify struct {
	outcome    string
	targetType string
	changes    audit.Changes
}

// pendingVerifies reads the recorded verification events oldest first.
func pendingVerifies(t *testing.T, owner *pgxpool.Pool) []pendingVerify {
	t.Helper()
	rows, err := owner.Query(context.Background(), `
		SELECT outcome, target_type, changes
		FROM audit.pending WHERE action = 'audit.verify' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []pendingVerify
	for rows.Next() {
		var record pendingVerify
		var raw []byte
		if err := rows.Scan(&record.outcome, &record.targetType, &raw); err != nil {
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

// wantValues asserts the record's safe change values match want exactly.
func wantValues(t *testing.T, record pendingVerify, want map[string]float64) {
	t.Helper()
	if len(record.changes.Values) != len(want) {
		t.Fatalf("changes = %v, want exactly %v", record.changes.Values, want)
	}
	for field, value := range want {
		change, ok := record.changes.Values[field]
		if !ok {
			t.Fatalf("changes = %v, want the %s value", record.changes.Values, field)
		}
		if change.To != value {
			t.Fatalf("%s = %v, want %v", field, change.To, value)
		}
	}
}

func TestVerifyAcceptsASealedChainAndAPurgedPrefix(t *testing.T) {
	owner, _ := setup(t)
	ctx := context.Background()

	chain := &directChain{owner: owner, prev: make([]byte, 32)}
	november := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	chain.add(t, november, november)
	chain.add(t, november.Add(time.Hour), november.Add(time.Hour))
	december := time.Date(2026, 12, 5, 9, 0, 0, 0, time.UTC)
	chain.add(t, december, december)
	chain.add(t, december.Add(time.Hour), december.Add(time.Hour))

	result, err := audit.Verify(ctx, owner, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatalf("verify = %+v, want the sealed chain to hold", result)
	}
	// A window opening mid-chain links from the stored hash of the event
	// before it.
	result, err = audit.Verify(ctx, owner, 3, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatalf("verify 3..4 = %+v, want the opened window to hold", result)
	}

	// Thirty days of retention from mid-January purge November's whole month
	// and leave an anchor at its last event; the chain of what remains starts
	// there and still verifies.
	if err := audit.Retain(ctx, owner, time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC), 30); err != nil {
		t.Fatal(err)
	}
	if !tableGone(t, owner, "events_y2026m11") {
		t.Fatal("the expired November partition survived, so nothing was purged")
	}
	result, err = audit.Verify(ctx, owner, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatalf("verify after the purge = %+v, want the chain from the anchor to hold", result)
	}
}

func TestVerifyReportsAChangedEventAsABreak(t *testing.T) {
	owner, _ := setup(t)
	ctx := context.Background()

	chain := &directChain{owner: owner, prev: make([]byte, 32)}
	november := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	for hour := 0; hour < 3; hour++ {
		chain.add(t, november.Add(time.Duration(hour)*time.Hour), november.Add(time.Duration(hour)*time.Hour))
	}
	tamper(t, owner, `UPDATE audit.events SET reason = 'x' WHERE position = $1`, int64(2))

	result, err := audit.Verify(ctx, owner, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.OK || result.Position != 2 || result.Reason != "hash" {
		t.Fatalf("verify = %+v, want the changed event at 2 as a hash break", result)
	}
}

func TestVerifyReportsADeletedEventAsAGap(t *testing.T) {
	owner, _ := setup(t)
	ctx := context.Background()

	chain := &directChain{owner: owner, prev: make([]byte, 32)}
	november := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	for hour := 0; hour < 3; hour++ {
		chain.add(t, november.Add(time.Duration(hour)*time.Hour), november.Add(time.Duration(hour)*time.Hour))
	}
	tamper(t, owner, `DELETE FROM audit.events WHERE position = $1`, int64(2))

	result, err := audit.Verify(ctx, owner, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.OK || result.Position != 2 || result.Reason != "gap" {
		t.Fatalf("verify = %+v, want the missing event at 2 as a gap", result)
	}
}

func TestVerifyWorkerRecordsVerifiesAndFailures(t *testing.T) {
	owner, _ := setup(t)
	ctx := context.Background()

	chain := &directChain{owner: owner, prev: make([]byte, 32)}
	november := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	chain.add(t, november, november)
	chain.add(t, november.Add(time.Hour), november.Add(time.Hour))

	if err := audit.VerifyOnce(ctx, owner); err != nil {
		t.Fatal(err)
	}
	records := pendingVerifies(t, owner)
	if len(records) != 1 {
		t.Fatalf("%d verification events, want the one success", len(records))
	}
	if records[0].outcome != string(audit.Success) {
		t.Fatalf("outcome = %s, want success", records[0].outcome)
	}
	if records[0].targetType != "chain" {
		t.Fatalf("target = %s, want the chain", records[0].targetType)
	}
	wantValues(t, records[0], map[string]float64{"from_position": 1, "through_position": 2})

	// The recorded success reaches the head, so a repeated pass over the same
	// quiet state records nothing.
	if err := audit.VerifyOnce(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if records = pendingVerifies(t, owner); len(records) != 1 {
		t.Fatalf("%d verification events after the repeated pass, want the one", len(records))
	}

	// An event sealed and changed past the verified range fails the next pass
	// at exactly that position.
	chain.add(t, november.Add(2*time.Hour), november.Add(2*time.Hour))
	tamper(t, owner, `UPDATE audit.events SET reason = 'x' WHERE position = $1`, int64(3))
	if err := audit.VerifyOnce(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if records = pendingVerifies(t, owner); len(records) != 2 {
		t.Fatalf("%d verification events, want the success and the failure", len(records))
	}
	if records[1].outcome != string(audit.Failure) {
		t.Fatalf("outcome = %s, want failure", records[1].outcome)
	}
	wantValues(t, records[1], map[string]float64{
		"from_position":    3,
		"through_position": 3,
		"failed_position":  3,
	})
}
