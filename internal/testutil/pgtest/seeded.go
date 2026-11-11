package pgtest

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RunSeeded is for a package's TestMain. It builds one database that seed has
// filled, makes it the Template, so that every New in the test binary starts
// from a copy of it, runs the tests, and drops it. A package whose tables
// point at another context's tables uses it to bring that context's schema up
// once, instead of in every test.
//
// With UPMO_TEST_DATABASE_URL unset it runs the tests as they are; New then
// fails each test that wants a database, as it always did.
func RunSeeded(m *testing.M, seed func(ctx context.Context, pool *pgxpool.Pool) error) int {
	raw := os.Getenv("UPMO_TEST_DATABASE_URL")
	if raw == "" {
		return m.Run()
	}
	name, err := newDBName()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := createDatabase(raw, name); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() {
		if err := dropDatabase(raw, name); err != nil {
			fmt.Fprintf(os.Stderr, "pgtest: drop seeded database %s: %v\n", name, err)
		}
	}()
	pool, err := openPool(raw, name)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	err = seed(ctx, pool)
	cancel()
	// A database cannot be copied while anyone is connected to it.
	pool.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pgtest: seed: %v\n", err)
		return 1
	}
	Template = name
	defer func() { Template = "" }()
	return m.Run()
}
