package pgtest

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestNewFatalsWhenDatabaseURLUnset(t *testing.T) {
	if os.Getenv("UPMO_PGTEST_FATAL") == "1" {
		if err := os.Unsetenv("UPMO_TEST_DATABASE_URL"); err != nil {
			t.Fatal(err)
		}
		New(t)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNewFatalsWhenDatabaseURLUnset$", "-test.v") //nolint:gosec // re-exec this test binary
	cmd.Env = append(envWithout("UPMO_TEST_DATABASE_URL"), "UPMO_PGTEST_FATAL=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("exited 0, want non-zero\n%s", out)
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("run subprocess: %v\n%s", err, out)
	}
	if bytes.Contains(out, []byte("--- SKIP:")) {
		t.Fatalf("skipped, want fatal\n%s", out)
	}
	if !bytes.Contains(out, []byte("UPMO_TEST_DATABASE_URL is unset")) {
		t.Fatalf("fatal message missing\n%s", out)
	}
}

func TestQuoteIdent(t *testing.T) {
	ident, err := quoteIdent("t_abc123")
	if err != nil {
		t.Fatal(err)
	}
	if ident != `"t_abc123"` {
		t.Fatalf("ident = %q, want %q", ident, `"t_abc123"`)
	}

	long := "a" + strings.Repeat("b", 62)
	ident, err = quoteIdent(long)
	if err != nil {
		t.Fatal(err)
	}
	if ident != `"`+long+`"` {
		t.Fatalf("ident = %q", ident)
	}

	for _, name := range []string{"", "T", "a-b", "has space", strings.Repeat("a", 64), `a"b`} {
		if _, err := quoteIdent(name); err == nil {
			t.Fatalf("quoteIdent(%q) succeeded", name)
		}
	}
}

func TestCreateStmt(t *testing.T) {
	stmt, err := createStmt("t_abc", "")
	if err != nil {
		t.Fatal(err)
	}
	if stmt != `CREATE DATABASE "t_abc"` {
		t.Fatalf("stmt = %q", stmt)
	}

	stmt, err = createStmt("t_abc", "migrated")
	if err != nil {
		t.Fatal(err)
	}
	if stmt != `CREATE DATABASE "t_abc" TEMPLATE "migrated"` {
		t.Fatalf("stmt = %q", stmt)
	}

	if _, err := createStmt("t_abc", "Migrated"); err == nil {
		t.Fatal("uppercase template succeeded")
	}
}

func TestDropStmt(t *testing.T) {
	stmt, err := dropStmt("t_abc")
	if err != nil {
		t.Fatal(err)
	}
	if stmt != `DROP DATABASE "t_abc" WITH (FORCE)` {
		t.Fatalf("stmt = %q", stmt)
	}
}

func envWithout(key string) []string {
	prefix := key + "="
	in := os.Environ()
	out := make([]string, 0, len(in)+1)
	for _, kv := range in {
		if !strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	return out
}
