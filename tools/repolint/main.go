// Command repolint checks repository conventions that no general linter covers:
// Conventional Commit subjects.
//
// Usage:
//
//	repolint commits [-range base..head]
//
// The command exits non-zero when a check fails.
package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// printf writes a diagnostic. A failed write to stdout or stderr leaves
// nowhere to report it, so the error is dropped.
func printf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

// run executes one repolint command and returns the process exit code:
// 0 for success, 1 for a failed check, 2 for a usage or runtime error.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printf(stderr, "usage: repolint commits [-range base..head]\n")
		return 2
	}
	switch args[0] {
	case "commits":
		return runCommits(args[1:], stdout, stderr)
	default:
		printf(stderr, "repolint: unknown command %q\n", args[0])
		return 2
	}
}
