package main

import (
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

// refPattern matches one ticket or pull request reference at the end of a
// subject, as in our squash subjects: ` (UPM-418)` or ` (#55)`.
var refPattern = regexp.MustCompile(`^(.+) \((?:UPM-[0-9]+|#[0-9]+)\)$`)

// badRefPattern matches a reference-like ending left after the one allowed
// reference is removed: a second reference, `(upm-418)` or `(UPM-)`.
var badRefPattern = regexp.MustCompile(`\((?:#|[Uu][Pp][Mm]-)[0-9]*\)$`)

// mergePattern matches the subject git writes for a local branch merge.
var mergePattern = regexp.MustCompile(`^Merge branch '.+'`)

// rangePattern limits a revision range to characters git accepts in a ref or
// range, and keeps it from starting with a dash so it cannot become an option.
var rangePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./@^~{}-]*(\.\.\.?[A-Za-z0-9_][A-Za-z0-9_./@^~{}-]*)?$`)

// checkSubject returns nil when subject is a valid Conventional Commit subject,
// optionally ending with one ` (UPM-<n>)` or ` (#<n>)` reference, or a
// `Merge branch '...'` subject, and an error naming the problem otherwise.
func checkSubject(subject string) error {
	if mergePattern.MatchString(subject) {
		return nil
	}
	if len(subject) > maxSubjectLength {
		return fmt.Errorf("subject is %d characters, the limit is %d", len(subject), maxSubjectLength)
	}
	// The length counts the reference; the other rules apply to the subject without it.
	if r := refPattern.FindStringSubmatch(subject); r != nil {
		subject = r[1]
	}
	m := subjectPattern.FindStringSubmatch(subject)
	if m == nil || badRefPattern.MatchString(subject) {
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

// commit is one commit in a checked range: its hash and full message.
type commit struct {
	sha, message string
}

// subject returns the first line of the commit message.
func (c commit) subject() string {
	s, _, _ := strings.Cut(c.message, "\n")
	return s
}

// gitCommits lists the commits in a revision range with their full
// messages (subject and body), oldest first.
func gitCommits(rev string) ([]commit, error) {
	if !rangePattern.MatchString(rev) {
		return nil, fmt.Errorf("invalid revision range %q", rev)
	}
	// The range is validated above and passed as one argument, never to a shell.
	// -z ends each commit with a NUL, so a body can hold any newline.
	cmd := exec.Command("git", "log", "-z", "--reverse", "--format=%H%x00%B", rev) //nolint:gosec
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git log %s: %w: %s", rev, err, strings.TrimSpace(errOut.String()))
	}
	fields := strings.Split(strings.TrimSuffix(out.String(), "\x00"), "\x00")
	var commits []commit
	for i := 0; i+1 < len(fields); i += 2 {
		commits = append(commits, commit{sha: fields[i], message: strings.TrimSpace(fields[i+1])})
	}
	return commits, nil
}

func runCommits(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("commits", flag.ContinueOnError)
	fs.SetOutput(stderr)
	rev := fs.String("range", "origin/main..HEAD", "revision range of commits to check")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	commits, err := gitCommits(*rev)
	if err != nil {
		printf(stderr, "repolint: %v\n", err)
		return 2
	}
	failed := 0
	for _, c := range commits {
		bad := false
		for _, err := range []error{checkSubject(c.subject()), checkAttribution(c.message)} {
			if err != nil {
				bad = true
				printf(stderr, "%s: %q: %v\n", shortSHA(c.sha), c.subject(), err)
			}
		}
		if bad {
			failed++
		}
	}
	if failed > 0 {
		printf(stderr, "repolint: %d of %d commit messages failed\n", failed, len(commits))
		return 1
	}
	printf(stdout, "repolint: %d commit messages ok in %s\n", len(commits), *rev)
	return 0
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
