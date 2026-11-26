package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// hooksRepo makes a scratch repository with one migration on its first
// commit, then turns on the versioned hooks the way `make hooks` does. The
// docker on PATH is a stub that logs its arguments and exits with
// DOCKER_EXIT, so the test runs without Docker. The real gitleaks scan in
// the hook is not run here; CI's Secret Scan job runs the same image and
// config on every pull request.
func hooksRepo(t *testing.T) (logPath string) {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not installed")
	}
	root := moduleRoot(t)
	gitRepo(t)
	write := func(path, body string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), mode); err != nil { //nolint:gosec // fixed test paths under temp dirs
			t.Fatal(err)
		}
	}
	write("internal/tasks/migrations/0001_init.sql", "CREATE TABLE tasks (id bigint);\n", 0o600)
	mustGit(t, "add", "-A")
	mustGit(t, "commit", "-q", "-m", "chore: start")
	for _, hook := range []string{"pre-commit", "commit-msg"} {
		body, err := os.ReadFile(filepath.Join(root, ".githooks", hook)) //nolint:gosec // fixed path in this module
		if err != nil {
			t.Fatal(err)
		}
		write(filepath.Join(".githooks", hook), string(body), 0o755) //nolint:gosec // a hook must be executable
	}
	mustGit(t, "config", "core.hooksPath", ".githooks")

	stub := t.TempDir()
	logPath = filepath.Join(stub, "docker.log")
	write(filepath.Join(stub, "docker"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$DOCKER_LOG\"\nexit \"$DOCKER_EXIT\"\n", 0o755) //nolint:gosec // the stub must be executable
	t.Setenv("PATH", stub+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DOCKER_LOG", logPath)
	t.Setenv("DOCKER_EXIT", "0")
	return logPath
}

func mustGit(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil { //nolint:gosec // fixed test arguments
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// tryCommit stages one file and commits it with message through the hooks.
func tryCommit(t *testing.T, path, body, message string) (string, bool) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	mustGit(t, "add", "-A")
	out, err := exec.Command("git", "commit", "-q", "-m", message).CombinedOutput() //nolint:gosec // fixed test arguments
	return string(out), err == nil
}

func TestHooks(t *testing.T) {
	const scan = "compose -p hook"
	const scanArgs = "--profile tools run --rm -T gitleaks git --pre-commit --staged --no-banner --redact --config /src/.gitleaks.toml /src"
	tests := []struct {
		name       string
		path       string
		message    string
		dockerExit string
		ok         bool
		output     string
		scanned    bool
	}{
		{"clean commit", "README.md", "feat: add readme", "0", true, "", true},
		{"modified migration", "internal/tasks/migrations/0001_init.sql", "fix: edit migration in place", "0", true, "", true},
		{"sql outside migrations", "internal/tasks/query.sql", "feat: add query", "0", true, "", true},
		{"human co-author", "README.md", "feat: add readme\n\nCo-authored-by: Maggy Smith <maggy@example.invalid>", "0", true, "", true},
		{"new migration", "internal/tasks/migrations/0002_more.sql", "feat: add migration", "0", false,
			"pre-commit: new migration file(s) refused before beta; edit the existing migration in place:\n" +
				"internal/tasks/migrations/0002_more.sql\n", false},
		{"scanner cannot run", "README.md", "feat: add readme", "1", false,
			"pre-commit: gitleaks found a secret in the staged changes, or could not run (is Docker up?). Fix it; never use --no-verify.\n", true},
		{"bad subject", "README.md", "Updated stuff", "0", false,
			"commit-msg: subject must be a Conventional Commit, lowercase after the colon:\n  Updated stuff\n", true},
		{"trailing period", "README.md", "feat: add readme.", "0", false,
			"commit-msg: subject must not end with a period\n", true},
		{"73 characters", "README.md", "feat: " + strings.Repeat("a", 67), "0", false,
			"commit-msg: subject is 73 characters, the limit is 72\n", true},
		{"AI trailer", "README.md", "feat: add readme\n\nCo-authored-by: Claude <noreply@anthropic.com>", "0", false,
			"commit-msg: AI or tool attribution is not allowed\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logPath := hooksRepo(t)
			t.Setenv("DOCKER_EXIT", tt.dockerExit)
			out, ok := tryCommit(t, tt.path, "changed\n", tt.message)
			if ok != tt.ok {
				t.Fatalf("commit ok = %v, want %v\n%s", ok, tt.ok, out)
			}
			if out != tt.output {
				t.Errorf("output = %q, want %q", out, tt.output)
			}
			log, _ := os.ReadFile(logPath) //nolint:gosec // test temp file
			if got := strings.Contains(string(log), scan) && strings.Contains(string(log), scanArgs); got != tt.scanned {
				t.Errorf("gitleaks scan ran = %v, want %v; docker log: %q", got, tt.scanned, log)
			}
		})
	}
}

// The commit-msg hook and repolint must agree on every message, so a
// commit the hook lets through never fails CI.
func TestCommitMsgHookMatchesRepolint(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not installed")
	}
	hook := filepath.Join(moduleRoot(t), ".githooks", "commit-msg")
	messages := []string{
		"feat: add login", "fix(auth): handle empty token", "feat(api/v2): add route", "feat!: drop old endpoint",
		"fix: 3 retries are enough", "feat: " + strings.Repeat("a", 66), "Merge branch 'ci/pipeline'",
		"Merge branch 'main' into ci/pipeline", "", "add login", "Feat: add login", "feature: add login",
		"feat add login", "feat:add login", "feat: Add login", "feat: add Login", "feat(): add login",
		"feat(Auth): add login", "feat(-x): add login", "feat: add login.", "feat: ", "feat: " + strings.Repeat("a", 67),
		"Merge pull request #1 from x/y", "Merge branch", "WIP",
		"fix(identity): require a passkey step-up for sensitive changes (UPM-418)",
		"fix(platform): request pools that cannot bypass row security (UPM-418)",
		"chore(deps): bump tools images, yaml/v3 and react-query (#55)",
		"feat: " + strings.Repeat("a", 56) + " (UPM-418)", "feat: " + strings.Repeat("a", 57) + " (UPM-418)",
		"feat: add login (#1)", "feat: add login (upm-418)", "feat: add login (UPM-)", "feat: add login (#)",
		"feat: add login (UPM-418) (#55)", "feat: add login (#1) (#2)", "feat: add login (UPM-418) extra",
		"feat: add login (UPM-418).", "feat: add Login (UPM-418)", "feat: add login. (UPM-418)",
		"feat:  (UPM-418)", "feat: (UPM-418)", "add login (UPM-418)",
		"feat: x\n\nCo-authored-by: Maggy Smith <maggy@example.invalid>",
		"feat: x\n\nCo-authored-by: Kirov Ivanov <k@example.invalid>",
		"feat: x\n\nGenerated with sqlc",
		"feat: x\n\nCo-Authored-By: Claude Opus <noreply@anthropic.com>",
		"feat: x\n\nCo-authored-by: someone <cursoragent@cursor.com>",
		"feat: x\n\nGenerated with Claude Code", "feat: x\n\nGenerated by AI", "feat: x\n\n\U0001F916 Generated",
	}
	for _, tool := range strings.Split(attributionTools, "|") {
		messages = append(messages, "feat: x\n\nCo-authored-by: "+tool+" <bot@example.invalid>")
	}
	dir := t.TempDir()
	for i, msg := range messages {
		file := filepath.Join(dir, "msg")
		if err := os.WriteFile(file, []byte(msg+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		subject, _, _ := strings.Cut(msg, "\n")
		want := checkSubject(subject) == nil && checkAttribution(msg) == nil
		err := exec.Command("sh", hook, file).Run() //nolint:gosec // fixed hook path and temp file
		if got := err == nil; got != want {
			t.Errorf("message %d %q: hook accepts = %v, repolint accepts = %v", i, msg, got, want)
		}
	}
}
