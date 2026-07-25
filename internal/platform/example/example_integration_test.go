//go:build integration

package example

import (
	"context"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func TestBracketRoundTrip(t *testing.T) {
	t.Parallel()
	pool := pgtest.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, "CREATE TABLE example_row (label text)"); err != nil {
		t.Fatal(err)
	}
	want := Bracket("row")
	if _, err := pool.Exec(ctx, "INSERT INTO example_row (label) VALUES ($1)", want); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := pool.QueryRow(ctx, "SELECT label FROM example_row").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("label = %q, want %q", got, want)
	}
}
