package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
)

const maxSubjectLength = 72

// subjectPattern is type(optional scope): description. The description starts
// with a lowercase letter or a digit; the length, case and trailing period
// rules are checked separately so each failure gets its own message.
var subjectPattern = regexp.MustCompile(
	`^(feat|fix|docs|style|refactor|perf|test|chore|build|ci|revert)(\([a-z0-9][a-z0-9._/-]*\))?!?: ([a-z0-9].*)$`,
)

// mergePattern matches the subject git writes for a local branch merge.
var mergePattern = regexp.MustCompile(`^Merge branch '.+'`)

// rangePattern limits a revision range to characters git accepts in a ref or
// range, and keeps it from starting with a dash so it cannot become an option.
var rangePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./@^~{}-]*(\.\.\.?[A-Za-z0-9_][A-Za-z0-9_./@^~{}-]*)?$`)

// checkSubject returns nil when subject is a valid Conventional Commit subject
// or a `Merge branch '...'` subject, and an error naming the problem otherwise.
func checkSubject(subject string) error {
	if mergePattern.MatchString(subject) {
		return nil
	}
	if len(subject) > maxSubjectLength {
		return fmt.Errorf("subject is %d characters, the limit is %d", len(subject), maxSubjectLength)
	}
	m := subjectPattern.FindStringSubmatch(subject)
	if m == nil {
		return errors.New("subject must look like type(optional scope): lowercase description, " +
			"with type one of feat, fix, docs, style, refactor, perf, test, chore, build, ci, revert")
	}
	if strings.ToLower(m[3]) != m[3] {
		return errors.New("description must be lowercase")
	}
	if strings.HasSuffix(subject, ".") {
		return errors.New("subject must not end with a period")
	}
	return nil
}

// gitSubjects lists the commit subjects in a revision range, oldest first.
func gitSubjects(rev string) ([]string, error) {
	if !rangePattern.MatchString(rev) {
		return nil, fmt.Errorf("invalid revision range %q", rev)
	}
	// The range is validated above and passed as one argument, never to a shell.
	cmd := exec.Command("git", "log", "--reverse", "--format=%H%x00%s", rev) //nolint:gosec
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git log %s: %w: %s", rev, err, strings.TrimSpace(errOut.String()))
	}
	var lines []string
	sc := bufio.NewScanner(&out)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines, sc.Err()
}

func runCommits(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("commits", flag.ContinueOnError)
	fs.SetOutput(stderr)
	rev := fs.String("range", "origin/main..HEAD", "revision range of commits to check")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	lines, err := gitSubjects(*rev)
	if err != nil {
		printf(stderr, "repolint: %v\n", err)
		return 2
	}
	failed := 0
	for _, line := range lines {
		sha, subject, _ := strings.Cut(line, "\x00")
		if err := checkSubject(subject); err != nil {
			failed++
			printf(stderr, "%s: %q: %v\n", shortSHA(sha), subject, err)
		}
	}
	if failed > 0 {
		printf(stderr, "repolint: %d of %d commit subjects failed\n", failed, len(lines))
		return 1
	}
	printf(stdout, "repolint: %d commit subjects ok in %s\n", len(lines), *rev)
	return 0
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
