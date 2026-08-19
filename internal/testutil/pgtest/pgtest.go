// Package pgtest creates a fresh Postgres database for each test and drops
// it afterwards.
package pgtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const dbTimeout = 30 * time.Second

// Template is the database CREATE DATABASE clones. Empty until migrations exist.
var Template string

// New creates a fresh database for t and drops it with
// DROP DATABASE ... WITH (FORCE) on cleanup. It fatals, never skips, when
// UPMO_TEST_DATABASE_URL is unset.
func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	raw := os.Getenv("UPMO_TEST_DATABASE_URL")
	if raw == "" {
		t.Fatal("UPMO_TEST_DATABASE_URL is unset")
	}

	name, err := newDBName()
	if err != nil {
		t.Fatal(err)
	}
	if err := createDatabase(raw, name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := dropDatabase(raw, name); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
	})

	pool, err := openPool(raw, name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// App returns a pool on the same database as owner that runs as upmo_app.
func App(t *testing.T, owner *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	cfg := owner.Config()
	useAppRole(cfg)
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func useAppRole(cfg *pgxpool.Config) {
	setRole := func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET ROLE upmo_app")
		return err
	}
	cfg.AfterConnect = setRole
	cfg.PrepareConn = func(ctx context.Context, conn *pgx.Conn) (bool, error) {
		err := setRole(ctx, conn)
		return err == nil, err
	}
}

func newDBName() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("pgtest: random name: %w", err)
	}
	return "t_" + hex.EncodeToString(buf[:]), nil
}

func createDatabase(raw, name string) error {
	cfg, err := pgxpool.ParseConfig(raw)
	if err != nil {
		return fmt.Errorf("pgtest: parse database url: %w", err)
	}
	pool, err := dial(cfg, 1)
	if err != nil {
		return err
	}
	defer pool.Close()

	stmt, err := createStmt(name, Template)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	if _, err := pool.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("pgtest: create database: %w", err)
	}
	return nil
}

func dropDatabase(raw, name string) error {
	cfg, err := pgxpool.ParseConfig(raw)
	if err != nil {
		return fmt.Errorf("pgtest: parse database url: %w", err)
	}
	pool, err := dial(cfg, 1)
	if err != nil {
		return err
	}
	defer pool.Close()

	stmt, err := dropStmt(name)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	if _, err := pool.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("pgtest: drop database: %w", err)
	}
	return nil
}

func openPool(raw, name string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(raw)
	if err != nil {
		return nil, fmt.Errorf("pgtest: parse database url: %w", err)
	}
	cfg.ConnConfig.Database = name
	return dial(cfg, 4)
}

func dial(cfg *pgxpool.Config, maxConns int32) (*pgxpool.Pool, error) {
	cfg.MaxConns = maxConns
	cfg.ConnConfig.ConnectTimeout = dbTimeout
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		return nil, fmt.Errorf("pgtest: connect: %w", err)
	}
	return pool, nil
}

func createStmt(name, template string) (string, error) {
	ident, err := quoteIdent(name)
	if err != nil {
		return "", err
	}
	if template == "" {
		return "CREATE DATABASE " + ident, nil
	}
	tpl, err := quoteIdent(template)
	if err != nil {
		return "", err
	}
	return "CREATE DATABASE " + ident + " TEMPLATE " + tpl, nil
}

func dropStmt(name string) (string, error) {
	ident, err := quoteIdent(name)
	if err != nil {
		return "", err
	}
	return "DROP DATABASE " + ident + " WITH (FORCE)", nil
}

func quoteIdent(name string) (string, error) {
	if err := validDBName(name); err != nil {
		return "", err
	}
	return `"` + name + `"`, nil
}

func validDBName(name string) error {
	if name == "" || len(name) > 63 {
		return fmt.Errorf("pgtest: invalid database name %q", name)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return fmt.Errorf("pgtest: invalid database name %q", name)
		}
	}
	return nil
}
