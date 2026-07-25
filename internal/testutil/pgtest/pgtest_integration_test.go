//go:build integration

package pgtest

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestParallelTestsGetSeparateDatabases(t *testing.T) {
	t.Parallel()
	var ready sync.WaitGroup
	ready.Add(2)
	for _, marker := range []string{"alpha", "beta"} {
		t.Run(marker, func(t *testing.T) {
			t.Parallel()
			var once sync.Once
			defer once.Do(ready.Done)
			other := "beta"
			if marker == "beta" {
				other = "alpha"
			}
			pool := New(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := pool.Exec(ctx, "CREATE TABLE probe (marker text PRIMARY KEY)"); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, "INSERT INTO probe (marker) VALUES ($1)", marker); err != nil {
				t.Fatal(err)
			}
			once.Do(ready.Done)
			ready.Wait()
			var got string
			if err := pool.QueryRow(ctx, "SELECT marker FROM probe WHERE marker = $1", marker).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != marker {
				t.Fatalf("marker = %q, want %q", got, marker)
			}
			err := pool.QueryRow(ctx, "SELECT marker FROM probe WHERE marker = $1", other).Scan(&got)
			if !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("database for %q contains %q (err %v)", marker, other, err)
			}
		})
	}
}

func TestDatabaseIsGoneAfterCleanup(t *testing.T) {
	t.Parallel()
	raw := os.Getenv("UPMO_TEST_DATABASE_URL")
	if raw == "" {
		t.Fatal("UPMO_TEST_DATABASE_URL is unset")
	}
	var name string
	t.Run("provision", func(t *testing.T) {
		pool := New(t)
		if err := pool.QueryRow(context.Background(), "SELECT current_database()").Scan(&name); err != nil {
			t.Fatal(err)
		}
	})
	if name == "" {
		t.Fatal("database name was not recorded")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var exists bool
	if err := pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatalf("database %s still exists", name)
	}
}

func TestNewClonesTemplate(t *testing.T) {
	raw := os.Getenv("UPMO_TEST_DATABASE_URL")
	if raw == "" {
		t.Fatal("UPMO_TEST_DATABASE_URL is unset")
	}
	tpl, err := newDBName()
	if err != nil {
		t.Fatal(err)
	}
	if err := createDatabase(raw, tpl); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := dropDatabase(raw, tpl); err != nil {
			t.Errorf("drop template: %v", err)
		}
	})
	if err := seedTemplate(raw, tpl); err != nil {
		t.Fatal(err)
	}

	prev := Template
	Template = tpl
	t.Cleanup(func() { Template = prev })

	pool := New(t)
	var got string
	if err := pool.QueryRow(context.Background(), "SELECT val FROM seed").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "synthetic-template" {
		t.Fatalf("val = %q, want synthetic-template", got)
	}
}

func seedTemplate(raw, name string) error {
	cfg, err := pgxpool.ParseConfig(raw)
	if err != nil {
		return err
	}
	cfg.ConnConfig.Database = name
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, "CREATE TABLE seed (val text)"); err != nil {
		return err
	}
	_, err = pool.Exec(ctx, "INSERT INTO seed (val) VALUES ($1)", "synthetic-template")
	return err
}
