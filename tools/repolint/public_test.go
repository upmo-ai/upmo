package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeValidPublicRepo(t *testing.T, dir string) {
	t.Helper()
	files := map[string]string{
		"LICENSE":         agplBanner + "\nVersion 3, 19 November 2007\n",
		"SECURITY.md":     "# Security policy\n",
		"CONTRIBUTING.md": "# Contributing\n",
		".gitignore":      strings.Join(requiredGitignoreEntries, "\n") + "\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCheckPublic(t *testing.T) {
	t.Run("valid repo", func(t *testing.T) {
		dir := t.TempDir()
		writeValidPublicRepo(t, dir)
		failures, err := checkPublic(dir)
		if err != nil {
			t.Fatalf("checkPublic() error = %v", err)
		}
		if len(failures) != 0 {
			t.Errorf("checkPublic() failures = %v, want none", failures)
		}
	})

	t.Run("missing LICENSE", func(t *testing.T) {
		dir := t.TempDir()
		writeValidPublicRepo(t, dir)
		if err := os.Remove(filepath.Join(dir, "LICENSE")); err != nil {
			t.Fatal(err)
		}
		failures, err := checkPublic(dir)
		if err != nil {
			t.Fatalf("checkPublic() error = %v", err)
		}
		if len(failures) == 0 || !strings.Contains(failures[0], "LICENSE is missing") {
			t.Errorf("failures = %v, want LICENSE is missing", failures)
		}
	})

	t.Run("wrong LICENSE", func(t *testing.T) {
		dir := t.TempDir()
		writeValidPublicRepo(t, dir)
		if err := os.WriteFile(filepath.Join(dir, "LICENSE"), []byte("MIT License\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		failures, err := checkPublic(dir)
		if err != nil {
			t.Fatalf("checkPublic() error = %v", err)
		}
		if len(failures) == 0 || !strings.Contains(failures[0], agplBanner) {
			t.Errorf("failures = %v, want AGPL banner failure", failures)
		}
	})

	for _, doc := range []string{"SECURITY.md", "CONTRIBUTING.md"} {
		t.Run("missing "+doc, func(t *testing.T) {
			dir := t.TempDir()
			writeValidPublicRepo(t, dir)
			if err := os.Remove(filepath.Join(dir, doc)); err != nil {
				t.Fatal(err)
			}
			failures, err := checkPublic(dir)
			if err != nil {
				t.Fatalf("checkPublic() error = %v", err)
			}
			if len(failures) == 0 || !strings.Contains(failures[0], doc+" is missing") {
				t.Errorf("failures = %v, want %s is missing", failures, doc)
			}
		})

		t.Run("empty "+doc, func(t *testing.T) {
			dir := t.TempDir()
			writeValidPublicRepo(t, dir)
			if err := os.WriteFile(filepath.Join(dir, doc), []byte("  \n"), 0o600); err != nil {
				t.Fatal(err)
			}
			failures, err := checkPublic(dir)
			if err != nil {
				t.Fatalf("checkPublic() error = %v", err)
			}
			if len(failures) == 0 || !strings.Contains(failures[0], doc+" is empty") {
				t.Errorf("failures = %v, want %s is empty", failures, doc)
			}
		})
	}

	t.Run("missing .gitignore", func(t *testing.T) {
		dir := t.TempDir()
		writeValidPublicRepo(t, dir)
		if err := os.Remove(filepath.Join(dir, ".gitignore")); err != nil {
			t.Fatal(err)
		}
		failures, err := checkPublic(dir)
		if err != nil {
			t.Fatalf("checkPublic() error = %v", err)
		}
		if len(failures) == 0 || !strings.Contains(failures[0], ".gitignore is missing") {
			t.Errorf("failures = %v, want .gitignore is missing", failures)
		}
	})

	for _, req := range requiredGitignoreEntries {
		t.Run("gitignore missing "+req, func(t *testing.T) {
			dir := t.TempDir()
			writeValidPublicRepo(t, dir)
			var kept []string
			for _, entry := range requiredGitignoreEntries {
				if entry != req {
					kept = append(kept, entry)
				}
			}
			if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(strings.Join(kept, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			failures, err := checkPublic(dir)
			if err != nil {
				t.Fatalf("checkPublic() error = %v", err)
			}
			if len(failures) == 0 || !strings.Contains(failures[0], req) {
				t.Errorf("failures = %v, want missing entry %q", failures, req)
			}
		})
	}
}

func TestCheckPublicTrackedFiles(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	initRepo := func(t *testing.T, extraFiles map[string]string) string {
		t.Helper()
		dir := t.TempDir()
		writeValidPublicRepo(t, dir)
		for rel, content := range extraFiles {
			full := filepath.Join(dir, rel)
			if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		git := func(args ...string) {
			t.Helper()
			cmd := exec.Command("git", args...) //nolint:gosec // fixed test arguments
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
		git("init", "-q")
		git("add", "-f", ".")
		return dir
	}

	t.Run("allows .env.example", func(t *testing.T) {
		dir := initRepo(t, map[string]string{".env.example": "PORT=8080\n"})
		failures, err := checkPublic(dir)
		if err != nil {
			t.Fatalf("checkPublic() error = %v", err)
		}
		if len(failures) != 0 {
			t.Errorf("checkPublic() failures = %v, want none", failures)
		}
	})

	for _, bad := range []string{
		".env",
		"web/.env.local",
		"AGENTS.md",
		"CLAUDE.md",
		".claude/settings.json",
		".cursor/rules.md",
		".kiro/spec.md",
		".gemini/settings.json",
	} {
		t.Run("rejects tracked "+bad, func(t *testing.T) {
			dir := initRepo(t, map[string]string{bad: "secret\n"})
			failures, err := checkPublic(dir)
			if err != nil {
				t.Fatalf("checkPublic() error = %v", err)
			}
			if len(failures) == 0 || !strings.Contains(failures[0], bad) {
				t.Errorf("failures = %v, want tracked file %q reported", failures, bad)
			}
		})
	}
}

func TestRunPublic(t *testing.T) {
	t.Run("passes", func(t *testing.T) {
		dir := t.TempDir()
		writeValidPublicRepo(t, dir)
		var stdout, stderr bytes.Buffer
		got := run([]string{"public", "-dir", dir}, nil, &stdout, &stderr)
		if got != 0 {
			t.Errorf("exit code = %d, want 0\nstderr: %s", got, &stderr)
		}
		if !strings.Contains(stdout.String(), "ok") {
			t.Errorf("stdout = %q, want ok", stdout.String())
		}
	})

	t.Run("fails", func(t *testing.T) {
		dir := t.TempDir()
		var stdout, stderr bytes.Buffer
		got := run([]string{"public", "-dir", dir}, nil, &stdout, &stderr)
		if got != 1 {
			t.Errorf("exit code = %d, want 1\nstderr: %s", got, &stderr)
		}
		if !strings.Contains(stderr.String(), "failed") {
			t.Errorf("stderr = %q, want failed summary", stderr.String())
		}
	})

	t.Run("usage error", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		got := run([]string{"public", "extra"}, nil, &stdout, &stderr)
		if got != 2 {
			t.Errorf("exit code = %d, want 2\nstderr: %s", got, &stderr)
		}
	})
}

func TestRepoGoingPublic(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	failures, err := checkPublic(root)
	if err != nil {
		t.Fatalf("checkPublic(%q) error = %v", root, err)
	}
	if len(failures) != 0 {
		t.Errorf("repo going-public checks failed:\n%s", strings.Join(failures, "\n"))
	}

	readme, err := os.ReadFile(filepath.Join(root, "README.md")) //nolint:gosec
	if err != nil {
		t.Fatalf("read README.md: %v", err)
	}
	if !strings.Contains(string(readme), "AGPL-3.0-only") || !strings.Contains(string(readme), "LICENSE") {
		t.Errorf("README.md must reference AGPL-3.0-only and LICENSE")
	}

	pkgData, err := os.ReadFile(filepath.Join(root, "web", "package.json")) //nolint:gosec
	if err != nil {
		t.Fatalf("read web/package.json: %v", err)
	}
	var pkg struct {
		License string `json:"license"`
	}
	if err := json.Unmarshal(pkgData, &pkg); err != nil {
		t.Fatalf("parse web/package.json: %v", err)
	}
	if pkg.License != "AGPL-3.0-only" {
		t.Errorf("web/package.json license = %q, want AGPL-3.0-only", pkg.License)
	}
}
