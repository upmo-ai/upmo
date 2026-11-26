package main

import (
	"bytes"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestCheckSubject(t *testing.T) {
	long := "feat: " + strings.Repeat("a", maxSubjectLength-len("feat: ")+1)
	exact := "feat: " + strings.Repeat("a", maxSubjectLength-len("feat: "))

	tests := []struct {
		subject string
		ok      bool
	}{
		{"feat: add login", true},
		{"fix(auth): handle empty token", true},
		{"docs: update readme", true},
		{"style: format code", true},
		{"refactor(kernel): split ids", true},
		{"perf: cache lookups", true},
		{"test: cover merge subjects", true},
		{"chore: bump deps", true},
		{"build: add vulnerability checks to make ci", true},
		{"ci: add workflow", true},
		{"revert: undo earlier change", true},
		{"feat(api/v2): add route", true},
		{"feat!: drop old endpoint", true},
		{"fix: 3 retries are enough", true},
		{exact, true},
		{"Merge branch 'ci/pipeline'", true},
		{"Merge branch 'main' into ci/pipeline", true},

		{"", false},
		{"add login", false},
		{"update stuff", false},
		{"Feat: add login", false},
		{"feature: add login", false},
		{"feat add login", false},
		{"feat:add login", false},
		{"feat: Add login", false},
		{"feat: add Login", false},
		{"feat(): add login", false},
		{"feat(Auth): add login", false},
		{"feat: add login.", false},
		{"feat: ", false},
		{long, false},
		{"Merge pull request #1 from x/y", false},
		{"Merge branch", false},
		{"WIP", false},
	}
	for _, tt := range tests {
		err := checkSubject(tt.subject)
		if tt.ok && err != nil {
			t.Errorf("checkSubject(%q) = %v, want nil", tt.subject, err)
		}
		if !tt.ok && err == nil {
			t.Errorf("checkSubject(%q) = nil, want an error", tt.subject)
		}
	}
}

func TestRangePattern(t *testing.T) {
	for _, ok := range []string{"origin/main..HEAD", "main..HEAD", "HEAD~3..HEAD", "abc123..def456", "HEAD"} {
		if !rangePattern.MatchString(ok) {
			t.Errorf("range %q rejected, want accepted", ok)
		}
	}
	for _, bad := range []string{"", "--output=x", "-n1", "a b", "a;b", "$(x)", "..HEAD"} {
		if rangePattern.MatchString(bad) {
			t.Errorf("range %q accepted, want rejected", bad)
		}
	}
}

// gitRepo creates a temporary repository whose commits carry the given
// subjects, oldest first, and makes it the working directory for the test.
func gitRepo(t *testing.T, subjects ...string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_AUTHOR_NAME", "Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.invalid")
	git := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil { //nolint:gosec // fixed test arguments
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	for _, s := range subjects {
		git("commit", "-q", "--allow-empty", "-m", s)
	}
}

func TestRunCommits(t *testing.T) {
	tests := []struct {
		name     string
		subjects []string
		want     int
	}{
		{"all compliant", []string{"chore: start", "feat: add one", "fix(scope): add two"}, 0},
		{"merge branch subject", []string{"chore: start", "Merge branch 'ci/pipeline'"}, 0},
		{"bad subject", []string{"chore: start", "feat: add one", "Updated stuff"}, 1},
		{"trailing period", []string{"chore: start", "feat: add one."}, 1},
		{"uppercase description", []string{"chore: start", "feat: Add one"}, 1},
		{"too long", []string{"chore: start", "feat: " + strings.Repeat("a", 80)}, 1},
		{"human co-author in body", []string{"chore: start", "feat: add one\n\nWhy.\n\nCo-authored-by: Maggy Smith <maggy@example.invalid>"}, 0},
		{"tool co-author in body", []string{"chore: start", "feat: add one\n\nWhy.\n\nCo-authored-by: Claude <noreply@anthropic.com>"}, 1},
		{"generated line in body", []string{"chore: start", "feat: add one\n\nGenerated with Kiro"}, 1},
		{"attribution in an earlier commit", []string{"chore: start", "feat: add one\n\n\U0001F916 Generated", "fix: add two"}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gitRepo(t, tt.subjects...)
			var stdout, stderr bytes.Buffer
			got := run([]string{"commits", "-range", "HEAD~" + strconv.Itoa(len(tt.subjects)-1) + "..HEAD"}, nil, &stdout, &stderr)
			if got != tt.want {
				t.Errorf("exit code = %d, want %d\nstdout: %s\nstderr: %s", got, tt.want, &stdout, &stderr)
			}
			if tt.want != 0 && !strings.Contains(stderr.String(), "failed") {
				t.Errorf("stderr does not report the failure: %s", &stderr)
			}
		})
	}
}

func TestCheckAttribution(t *testing.T) {
	var bad []string
	for _, tool := range strings.Split(attributionTools, "|") {
		bad = append(bad, "feat: x\n\nCo-authored-by: "+tool+" <bot@example.invalid>")
	}
	bad = append(bad,
		"feat: x\n\nCo-Authored-By: Claude Opus <noreply@anthropic.com>",
		"feat: x\n\nco-authored-by: GitHub Copilot",
		"feat: x\n\nCo-authored-by: someone <cursoragent@cursor.com>",
		"feat: x\n\nGenerated with Claude Code",
		"feat: x\n\nGenerated by AI",
		"feat: x\n\ngenerated with OpenCode",
		"feat: x\n\n\U0001F916 Generated",
		"feat: add \U0001F916 support",
	)
	for _, msg := range bad {
		if err := checkAttribution(msg); err == nil || err.Error() != "AI or tool attribution is not allowed" {
			t.Errorf("checkAttribution(%q) = %v, want the attribution error", msg, err)
		}
	}
	for _, msg := range []string{
		"feat: add login",
		"feat: x\n\nCo-authored-by: Maggy Smith <maggy@example.invalid>",
		"feat: x\n\nCo-authored-by: Kirov Ivanov <k@example.invalid>",
		"feat: x\n\nGenerated with sqlc",
		"feat: x\n\nThe cursor moves to the end.",
		"feat: add claude provider",
	} {
		if err := checkAttribution(msg); err != nil {
			t.Errorf("checkAttribution(%q) = %v, want nil", msg, err)
		}
	}
}

// The failure names the commit and the rule, and a body-only problem still
// counts the commit as failed.
func TestRunCommitsBodyMessage(t *testing.T) {
	gitRepo(t, "chore: start", "feat: add one\n\nCo-authored-by: Codex <codex@openai.com>")
	var stdout, stderr bytes.Buffer
	if got := run([]string{"commits", "-range", "HEAD~1..HEAD"}, nil, &stdout, &stderr); got != 1 {
		t.Fatalf("exit code = %d, want 1\nstderr: %s", got, &stderr)
	}
	out, err := exec.Command("git", "rev-parse", "--short=7", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	want := strings.TrimSpace(string(out)) + `: "feat: add one": AI or tool attribution is not allowed` + "\n" +
		"repolint: 1 of 1 commit messages failed\n"
	if stderr.String() != want {
		t.Errorf("stderr = %q, want %q", &stderr, want)
	}
}

func TestRunCommitsBadRange(t *testing.T) {
	gitRepo(t, "chore: start")
	for _, rev := range []string{"--output=x", "no-such-ref..HEAD"} {
		var stdout, stderr bytes.Buffer
		if got := run([]string{"commits", "-range", rev}, nil, &stdout, &stderr); got != 2 {
			t.Errorf("range %q: exit code = %d, want 2", rev, got)
		}
	}
}
