//go:build integration

package audit_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/platform/audit"
)

// partitionsOf lists every partition of audit.events by name, sorted.
func partitionsOf(t *testing.T, owner *pgxpool.Pool) []string {
	t.Helper()
	rows, err := owner.Query(context.Background(), `
		SELECT c.relname
		FROM pg_inherits AS i
		JOIN pg_class AS c ON c.oid = i.inhrelid
		WHERE i.inhparent = 'audit.events'::regclass
		ORDER BY c.relname`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// tableGone reports whether the named table of the audit schema was dropped.
func tableGone(t *testing.T, owner *pgxpool.Pool, name string) bool {
	t.Helper()
	var gone bool
	if err := owner.QueryRow(context.Background(),
		`SELECT to_regclass($1) IS NULL`, "audit."+name,
	).Scan(&gone); err != nil {
		t.Fatal(err)
	}
	return gone
}

// directChain inserts sealed events straight into audit.events as the owner,
// with the gapless positions and chain hashes the sealer would give them, so
// a test can place events in exact months without waiting for real time.
type directChain struct {
	owner    *pgxpool.Pool
	prev     []byte
	position int64
}

// add seals one event with the given occurred and sealed times and moves the
// head along. It returns the event's hash.
func (c *directChain) add(t *testing.T, occurred, sealed time.Time) []byte {
	t.Helper()
	row := audit.Row{
		ID:             uuid.NewString(),
		OccurredAt:     occurred,
		Service:        "brain",
		Action:         audit.AuthSignOut,
		Outcome:        audit.Success,
		ActorType:      "system",
		CredentialType: "none",
		TargetType:     "session",
		TargetID:       uuid.NewString(),
		Changes:        audit.Changes{Fields: []string{}, Values: map[string]audit.Change{}},
	}
	changes, err := json.Marshal(row.Changes)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(append(append([]byte{}, c.prev...), audit.Canonical(row)...))
	ctx := context.Background()
	if _, err := c.owner.Exec(ctx, `
		INSERT INTO audit.events (
			position, sealed_at, prev_hash, hash,
			id, occurred_at, service, action, outcome, reason,
			actor_type, credential_type, target_type, target_id, changes, user_agent
		) VALUES (
			$1, $2, $3, $4,
			$5::uuid, $6, $7, $8, $9, '',
			'system', 'none', $10, $11, $12::jsonb, ''
		)`,
		c.position+1, sealed, c.prev, hash[:],
		row.ID, row.OccurredAt, row.Service, row.Action, row.Outcome,
		row.TargetType, row.TargetID, changes,
	); err != nil {
		t.Fatal(err)
	}
	c.position++
	c.prev = hash[:]
	if _, err := c.owner.Exec(ctx,
		`UPDATE audit.head SET position = $1, hash = $2, sealed_at = $3 WHERE one`,
		c.position, c.prev, sealed); err != nil {
		t.Fatal(err)
	}
	return hash[:]
}

func TestRetentionCreatesTheComingPartitions(t *testing.T) {
	owner, _ := setup(t)
	ctx := context.Background()

	// A retention of ten years expires nothing, so only creation shows.
	now := time.Date(2027, 3, 15, 12, 0, 0, 0, time.UTC)
	if err := audit.Retain(ctx, owner, now, 3650); err != nil {
		t.Fatal(err)
	}
	first := partitionsOf(t, owner)
	for _, name := range []string{"events_y2027m03", "events_y2027m04", "events_y2027m05"} {
		if !slices.Contains(first, name) {
			t.Fatalf("partitions = %v, want the current month and the next two to hold %s", first, name)
		}
	}
	if slices.Contains(first, "events_y2027m02") {
		t.Fatalf("partitions = %v, want nothing created before the current month", first)
	}

	if err := audit.Retain(ctx, owner, now, 3650); err != nil {
		t.Fatal(err)
	}
	if again := partitionsOf(t, owner); !slices.Equal(again, first) {
		t.Fatalf("a repeated run moved the partitions: %v then %v", first, again)
	}
}

func TestRetentionPurgesOnlyWholeExpiredPartitions(t *testing.T) {
	owner, _ := setup(t)
	ctx := context.Background()

	chain := &directChain{owner: owner, prev: make([]byte, 32)}
	chain.add(t,
		time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC))
	novemberEnd := chain.add(t,
		time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC))
	december := time.Date(2026, 12, 5, 9, 0, 0, 0, time.UTC)
	chain.add(t, december, december)
	chain.add(t, december.Add(time.Hour), december.Add(time.Hour))

	// Thirty days of retention from mid-January 2027 reach back to
	// mid-December 2026: November's whole month has ended by then,
	// December's has not.
	if err := audit.Retain(ctx, owner, time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC), 30); err != nil {
		t.Fatal(err)
	}

	if !tableGone(t, owner, "events_y2026m11") {
		t.Fatal("the expired November partition survived")
	}
	var kept int64
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM audit.events_y2026m12`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != 2 {
		t.Fatalf("%d events left in the December partition, want its 2", kept)
	}

	var position int64
	var hash []byte
	var purgedBefore time.Time
	if err := owner.QueryRow(ctx,
		`SELECT position, hash, purged_before FROM audit.anchors`,
	).Scan(&position, &hash, &purgedBefore); err != nil {
		t.Fatal(err)
	}
	if position != 2 {
		t.Fatalf("anchor at position %d, want the last November event", position)
	}
	if !bytes.Equal(hash, novemberEnd) {
		t.Fatal("the anchor holds a hash other than the last November event's")
	}
	if want := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC); !purgedBefore.UTC().Equal(want) {
		t.Fatalf("purged_before = %v, want the partition's upper bound %v", purgedBefore, want)
	}
}

func TestRetentionDropsEmptyPartitionsWithoutAnAnchor(t *testing.T) {
	owner, _ := setup(t)
	ctx := context.Background()

	// From February 2027 with 30 days of retention, the migrated empty
	// November and December partitions have both expired; January's month has
	// not ended by the cutoff, so it stays.
	if err := audit.Retain(ctx, owner, time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC), 30); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"events_y2026m11", "events_y2026m12"} {
		if !tableGone(t, owner, name) {
			t.Fatalf("%s survived its empty expired month", name)
		}
	}
	if tableGone(t, owner, "events_y2027m01") {
		t.Fatal("the unexpired January partition was dropped")
	}
	var anchors int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM audit.anchors`).Scan(&anchors); err != nil {
		t.Fatal(err)
	}
	if anchors != 0 {
		t.Fatalf("%d anchors written for empty partitions", anchors)
	}
}

func TestRetentionKeepsALateSealedEventInItsOwnPartition(t *testing.T) {
	owner, _ := setup(t)
	ctx := context.Background()

	chain := &directChain{owner: owner, prev: make([]byte, 32)}
	// Both events occur inside November; the second is sealed one second into
	// December, and the sealed time is what the partitions follow.
	lastNovember := time.Date(2026, 11, 30, 23, 59, 59, 0, time.UTC)
	novemberEnd := chain.add(t, lastNovember, lastNovember)
	chain.add(t, lastNovember, time.Date(2026, 12, 1, 0, 0, 1, 0, time.UTC))
	if got := partitionOf(t, owner, 2); got != "audit.events_y2026m12" {
		t.Fatalf("the late-sealed event sits in %s, want the December partition", got)
	}

	if err := audit.Retain(ctx, owner, time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC), 30); err != nil {
		t.Fatal(err)
	}

	var position int64
	var hash []byte
	if err := owner.QueryRow(ctx, `SELECT position, hash FROM audit.anchors`).Scan(&position, &hash); err != nil {
		t.Fatal(err)
	}
	if position != 1 || !bytes.Equal(hash, novemberEnd) {
		t.Fatalf("anchor = %d %x, want the last November event at 1 %x", position, hash, novemberEnd)
	}
	var lowest int64
	if err := owner.QueryRow(ctx, `SELECT min(position) FROM audit.events`).Scan(&lowest); err != nil {
		t.Fatal(err)
	}
	if lowest != position+1 {
		t.Fatalf("the chain of what remains starts at %d, want the position after the anchor %d", lowest, position)
	}
	if got := partitionOf(t, owner, lowest); got != "audit.events_y2026m12" {
		t.Fatalf("the December event moved to %s", got)
	}
}

func TestRetentionRecordsThePurgeEvent(t *testing.T) {
	owner, _ := setup(t)
	ctx := context.Background()

	chain := &directChain{owner: owner, prev: make([]byte, 32)}
	chain.add(t,
		time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC))

	if err := audit.Retain(ctx, owner, time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC), 30); err != nil {
		t.Fatal(err)
	}

	rows, err := owner.Query(ctx, `
		SELECT action, outcome, target_type, target_id, changes
		FROM audit.pending WHERE action = 'audit.purge' ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var purges int
	for rows.Next() {
		purges++
		var action, outcome, targetType, targetID string
		var raw []byte
		if err := rows.Scan(&action, &outcome, &targetType, &targetID, &raw); err != nil {
			t.Fatal(err)
		}
		if action != string(audit.AuditPurge) || outcome != string(audit.Success) {
			t.Fatalf("purge event = %s %s", action, outcome)
		}
		if targetType != "partition" || targetID != "events_y2026m11" {
			t.Fatalf("target = %s %s, want the dropped partition", targetType, targetID)
		}
		var changes audit.Changes
		if err := json.Unmarshal(raw, &changes); err != nil {
			t.Fatal(err)
		}
		if len(changes.Values) != 3 {
			t.Fatalf("changes = %s, want exactly the three safe values", raw)
		}
		for field, want := range map[string]any{
			"partition":        "events_y2026m11",
			"events":           float64(1),
			"through_position": float64(1),
		} {
			change, ok := changes.Values[field]
			if !ok {
				t.Fatalf("changes = %s, want the %s value", raw, field)
			}
			if change.To != want {
				t.Fatalf("%s = %v, want %v", field, change.To, want)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if purges != 1 {
		t.Fatalf("%d purge events recorded, want the one for November", purges)
	}
}
