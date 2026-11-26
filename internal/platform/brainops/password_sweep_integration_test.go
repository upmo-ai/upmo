//go:build integration

package brainops

import (
	"bytes"
	"context"
	"testing"

	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

// The worker's sweep job deletes a password check window that began over
// 10 minutes ago and keeps a newer one.
func TestPasswordWindowSweepDeletesOnlyOldWindows(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	old, recent := bytes.Repeat([]byte{'o'}, 32), bytes.Repeat([]byte{'n'}, 32)
	if _, err := owner.Exec(ctx, `
		INSERT INTO identity.password_verify_window (subject, window_start, attempts)
		VALUES ($1, now() - interval '11 minutes', 3), ($2, now() - interval '1 minute', 2)`, old, recent); err != nil {
		t.Fatal(err)
	}
	if err := (&passwordWindowSweep{owner: owner}).Work(ctx, nil); err != nil {
		t.Fatal(err)
	}
	rows, err := owner.Query(ctx, `SELECT subject FROM identity.password_verify_window ORDER BY subject`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var left [][]byte
	for rows.Next() {
		var s []byte
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		left = append(left, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || !bytes.Equal(left[0], recent) {
		t.Fatalf("windows left = %q, want only %q", left, recent)
	}
}
