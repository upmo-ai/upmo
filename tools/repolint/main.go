// Command repolint checks repository conventions that no general linter covers:
// Conventional Commit messages and pull request titles without AI attribution,
// new migration files, retired glossary words and going-public checks.
//
// Usage:
//
//	repolint commits [-range base..head]
//	repolint migrations [-range base...head]
//	repolint title
//	repolint terms
//	repolint public [-dir path]
//	repolint summary -title name -status status [-file path]...
//
// Commands exit non-zero when a check fails. terms reads file paths on stdin,
// one per line. title reads the pull request title from PR_TITLE. summary
// writes a Markdown job summary and exits 0.
package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// printf writes a diagnostic. A failed write to stdout or stderr leaves
// nowhere to report it, so the error is dropped.
func printf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

// run executes one repolint command and returns the process exit code:
// 0 for success, 1 for a failed check, 2 for a usage or runtime error.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printf(stderr, "usage: repolint commits [-range base..head] | repolint migrations [-range base...head] | repolint title | repolint terms | repolint public [-dir path] | repolint summary -title name -status status [-file path]...\n")
		return 2
	}
	switch args[0] {
	case "commits":
		return runCommits(args[1:], stdout, stderr)
	case "migrations":
		return runMigrations(args[1:], stdout, stderr)
	case "title":
		return runTitle(args[1:], stdout, stderr)
	case "terms":
		return runTerms(args[1:], stdin, stdout, stderr)
	case "public":
		return runPublic(args[1:], stdout, stderr)
	case "summary":
		return runSummary(args[1:], stdout, stderr)
	default:
		printf(stderr, "repolint: unknown command %q\n", args[0])
		return 2
	}
}
