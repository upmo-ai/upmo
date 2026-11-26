package main

import (
	"flag"
	"io"
	"os"
)

// runTitle checks the pull request title, which the squash merge uses as the
// subject on main. The title comes from the PR_TITLE environment variable so
// CI never interpolates it into a shell line.
func runTitle(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("title", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	title, ok := os.LookupEnv("PR_TITLE")
	if !ok || title == "" {
		printf(stderr, "repolint: PR_TITLE is not set\n")
		return 2
	}
	failed := false
	for _, err := range []error{checkSubject(title), checkAttribution(title)} {
		if err != nil {
			failed = true
			printf(stderr, "pull request title %q: %v\n", title, err)
		}
	}
	if failed {
		printf(stderr, "repolint: the pull request title is the squash commit subject on main; fix the title\n")
		return 1
	}
	printf(stdout, "repolint: pull request title ok\n")
	return 0
}
