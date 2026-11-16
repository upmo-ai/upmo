// Command repolint checks repository conventions that no general linter covers:
// Conventional Commit subjects, retired glossary words and going-public checks.
//
// Usage:
//
//	repolint commits [-range base..head]
//	repolint terms
//	repolint public [-dir path]
//
// Commands exit non-zero when a check fails. terms reads file paths on stdin,
// one per line.
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
		printf(stderr, "usage: repolint commits [-range base..head] | repolint terms | repolint public [-dir path]\n")
		return 2
	}
	switch args[0] {
	case "commits":
		return runCommits(args[1:], stdout, stderr)
	case "terms":
		return runTerms(args[1:], stdin, stdout, stderr)
	case "public":
		return runPublic(args[1:], stdout, stderr)
	default:
		printf(stderr, "repolint: unknown command %q\n", args[0])
		return 2
	}
}
