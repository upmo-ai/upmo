package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
)

// migrationPattern matches a SQL file directly under any migrations directory.
var migrationPattern = regexp.MustCompile(`(^|/)migrations/[^/]+\.sql$`)

// gitNewMigrations lists the migration files that a base...head range adds or
// renames. Before beta the existing migrations are edited in place instead.
func gitNewMigrations(rev string) ([]string, error) {
	// Three dots diff head against the merge base, so changes on the base
	// branch never show up as this branch's additions.
	if !rangePattern.MatchString(rev) || !strings.Contains(rev, "...") {
		return nil, fmt.Errorf("invalid revision range %q, want base...head", rev)
	}
	// The range is validated above and passed as one argument, never to a shell.
	cmd := exec.Command("git", "diff", "--name-status", "-z", "-M", "--diff-filter=AR", rev) //nolint:gosec
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git diff %s: %w: %s", rev, err, strings.TrimSpace(errOut.String()))
	}
	return parseNewMigrations(out.String()), nil
}

// parseNewMigrations reads `git diff --name-status -z` output and returns the
// resulting path of every entry that is a migration file. Renames and copies
// carry two paths; the second is the new one.
func parseNewMigrations(out string) []string {
	fields := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	var paths []string
	for i := 0; i < len(fields); i++ {
		status := fields[i]
		if status == "" {
			continue
		}
		if status[0] == 'R' || status[0] == 'C' {
			i++
		}
		i++
		if i >= len(fields) {
			break
		}
		if migrationPattern.MatchString(fields[i]) {
			paths = append(paths, fields[i])
		}
	}
	return paths
}

func runMigrations(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("migrations", flag.ContinueOnError)
	fs.SetOutput(stderr)
	rev := fs.String("range", "origin/main...HEAD", "base...head range of changes to check")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	paths, err := gitNewMigrations(*rev)
	if err != nil {
		printf(stderr, "repolint: %v\n", err)
		return 2
	}
	for _, p := range paths {
		printf(stderr, "%s: new migration file; edit the existing migration in place before beta\n", p)
	}
	if len(paths) > 0 {
		printf(stderr, "repolint: %d new migration files in %s\n", len(paths), *rev)
		return 1
	}
	printf(stdout, "repolint: no new migration files in %s\n", *rev)
	return 0
}
