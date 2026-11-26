package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

func TestParseNewMigrations(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want []string
	}{
		{"empty", "", nil},
		{"added under migrations", "A\x00internal/tasks/migrations/0002_more.sql\x00", []string{"internal/tasks/migrations/0002_more.sql"}},
		{"added at top level migrations", "A\x00migrations/0001_init.sql\x00", []string{"migrations/0001_init.sql"}},
		{"sql outside migrations", "A\x00internal/tasks/query.sql\x00", nil},
		{"non sql under migrations", "A\x00internal/tasks/migrations/README.md\x00", nil},
		{"nested below migrations", "A\x00internal/tasks/migrations/old/0001.sql\x00", nil},
		{"similar directory name", "A\x00internal/tasks/oldmigrations/0001.sql\x00", nil},
		{"rename into migrations", "R100\x00internal/tasks/draft.sql\x00internal/tasks/migrations/0002_more.sql\x00", []string{"internal/tasks/migrations/0002_more.sql"}},
		{"rename out of migrations", "R100\x00internal/tasks/migrations/0002_more.sql\x00internal/tasks/draft.sql\x00", nil},
		{
			"mixed entries",
			"A\x00a.go\x00R087\x00x/migrations/1.sql\x00x/migrations/2.sql\x00A\x00y/migrations/3.sql\x00",
			[]string{"x/migrations/2.sql", "y/migrations/3.sql"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseNewMigrations(tt.out); !slices.Equal(got, tt.want) {
				t.Errorf("parseNewMigrations(%q) = %q, want %q", tt.out, got, tt.want)
			}
		})
	}
}

// migrationsRepo creates a scratch repository with one existing migration on
// a base branch, then applies change on top and commits it.
func migrationsRepo(t *testing.T, change func(git func(...string), write func(string, string))) {
	t.Helper()
	gitRepo(t)
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil { //nolint:gosec // fixed test arguments
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("internal/tasks/migrations/0001_init.sql", "CREATE TABLE tasks (id bigint);\n")
	write("internal/tasks/draft.sql", "SELECT 1 FROM tasks WHERE id = 42 AND name = 'draft';\n")
	git("add", "-A")
	git("commit", "-q", "-m", "chore: start")
	git("branch", "base")
	change(git, write)
	git("add", "-A")
	git("commit", "-q", "--allow-empty", "-m", "feat: change")
}

func TestRunMigrations(t *testing.T) {
	type gitFn = func(...string)
	type writeFn = func(string, string)
	tests := []struct {
		name   string
		change func(gitFn, writeFn)
		want   int
		stderr string
	}{
		{"modified migration", func(_ gitFn, w writeFn) {
			w("internal/tasks/migrations/0001_init.sql", "CREATE TABLE tasks (id bigint, name text);\n")
		}, 0, ""},
		{"deleted migration", func(g gitFn, _ writeFn) {
			g("rm", "-q", "internal/tasks/migrations/0001_init.sql")
		}, 0, ""},
		{"sql outside migrations", func(_ gitFn, w writeFn) {
			w("internal/tasks/query.sql", "SELECT 2;\n")
		}, 0, ""},
		{"added migration", func(_ gitFn, w writeFn) {
			w("internal/tasks/migrations/0002_more.sql", "ALTER TABLE tasks ADD COLUMN name text;\n")
		}, 1, "internal/tasks/migrations/0002_more.sql: new migration file; edit the existing migration in place before beta\n" +
			"repolint: 1 new migration files in base...HEAD\n"},
		{"renamed into migrations", func(g gitFn, _ writeFn) {
			g("mv", "internal/tasks/draft.sql", "internal/tasks/migrations/0002_draft.sql")
		}, 1, "internal/tasks/migrations/0002_draft.sql: new migration file; edit the existing migration in place before beta\n" +
			"repolint: 1 new migration files in base...HEAD\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			migrationsRepo(t, tt.change)
			var stdout, stderr bytes.Buffer
			got := run([]string{"migrations", "-range", "base...HEAD"}, nil, &stdout, &stderr)
			if got != tt.want {
				t.Fatalf("exit code = %d, want %d\nstdout: %s\nstderr: %s", got, tt.want, &stdout, &stderr)
			}
			if stderr.String() != tt.stderr {
				t.Errorf("stderr = %q, want %q", &stderr, tt.stderr)
			}
			if tt.want == 0 && stdout.String() != "repolint: no new migration files in base...HEAD\n" {
				t.Errorf("stdout = %q", &stdout)
			}
		})
	}
}

// A change on the base branch after the branch point is not the branch's own.
func TestRunMigrationsIgnoresBaseBranch(t *testing.T) {
	migrationsRepo(t, func(_ func(...string), w func(string, string)) {
		w("internal/tasks/migrations/0001_init.sql", "CREATE TABLE tasks (id bigint, done boolean);\n")
	})
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil { //nolint:gosec // fixed test arguments
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("checkout", "-q", "base")
	if err := os.WriteFile("internal/tasks/migrations/0002_base.sql", []byte("SELECT 3;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "feat: base change")
	git("checkout", "-q", "-")
	var stdout, stderr bytes.Buffer
	if got := run([]string{"migrations", "-range", "base...HEAD"}, nil, &stdout, &stderr); got != 0 {
		t.Errorf("exit code = %d, want 0\nstderr: %s", got, &stderr)
	}
}

func TestRunMigrationsBadRange(t *testing.T) {
	migrationsRepo(t, func(func(...string), func(string, string)) {})
	for _, rev := range []string{"base..HEAD", "HEAD", "--output=x", "no-such-ref...HEAD"} {
		var stdout, stderr bytes.Buffer
		if got := run([]string{"migrations", "-range", rev}, nil, &stdout, &stderr); got != 2 {
			t.Errorf("range %q: exit code = %d, want 2\nstderr: %s", rev, got, &stderr)
		}
	}
}
