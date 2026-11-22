package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetiredInLine(t *testing.T) {
	tests := []struct {
		text string
		want string
	}{
		{"mission", "mission"},
		{"missions", "missions"},
		{"sub-mission", "sub-mission"},
		{"Sub-Mission", "Sub-Mission"},
		{"harness", "harness"},
		{"harnesses", "harnesses"},
		{"analytics", "analytics"},
		{"inbox", "inbox"},
		{"Inbox", "Inbox"},
		{"conversation", "conversation"},
		{"conversations", "conversations"},
		{"destination", "destination"},
		{"destinations", "destinations"},
		{"working-mission", "mission"},
		{"task-harness", "harness"},
		{"inbox_items", "inbox"},
		{"MissionID", "Mission"},
		{"HTTPInbox", "Inbox"},
		{"inbox and mission", "inbox,mission"},

		{"admission", ""},
		{"permissions", ""},
		{"inboxes", ""},
		{"conversational", ""},
		{"submission", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := strings.Join(retiredInLine(tt.text), ",")
		if got != tt.want {
			t.Errorf("retiredInLine(%q) = %q, want %q", tt.text, got, tt.want)
		}
	}
}

func TestBritishInLine(t *testing.T) {
	tests := []struct {
		text string
		want string
	}{
		{"ClassCancelled", "Cancelled"},
		{"aria-labelledby", ""},
		{"the colour grey", "colour"},
		{"color", ""},
		{"Unauthorised", "Unauthorised"},
		{"cancelled_at", "cancelled"},
		{"canceled", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := strings.Join(britishInLine(tt.text), ",")
		if got != tt.want {
			t.Errorf("britishInLine(%q) = %q, want %q", tt.text, got, tt.want)
		}
	}
}

func TestBritishSkipped(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{".github/workflows/ci.yml", true},
		{"./.github/workflows/ci.yml", true},
		{".github/workflows/other.yml", false},
		{".github/workflows/ci.yaml", false},
		{"README.md", false},
		{"internal/identity/domain/common_passwords.go", true},
		{"tools/repolint/terms.go", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := britishSkipped(tt.path); got != tt.want {
			t.Errorf("britishSkipped(%q) = %t, want %t", tt.path, got, tt.want)
		}
	}
}

func TestTermsSkipped(t *testing.T) {
	for _, path := range []string{
		"web/pnpm-lock.yaml",
		"./web/pnpm-lock.yaml",
		"THIRD_PARTY_NOTICES.md",
		"tools/repolint/terms.go",
		"tools/repolint/terms_test.go",
	} {
		if !termsSkipped(path) {
			t.Errorf("termsSkipped(%q) = false, want true", path)
		}
	}
	for _, path := range []string{
		"README.md",
		"tools/repolint/main.go",
		"tools/repolint/commits_test.go",
		"web/src/features/shell/api.tsx",
		"Makefile",
		"e2e/ensure-web.sh",
	} {
		if termsSkipped(path) {
			t.Errorf("termsSkipped(%q) = true, want false", path)
		}
	}
}

func TestTermsChecked(t *testing.T) {
	for _, path := range []string{
		"a.md", "a.go", "a.ts", "a.tsx", "a.css", "a.html", "a.json",
		"a.yml", "a.yaml", "a.sh", "a.sql", "Makefile", "dir/Makefile", "A.MD",
	} {
		if !termsChecked(path) {
			t.Errorf("termsChecked(%q) = false, want true", path)
		}
	}
	for _, path := range []string{"a.txt", "a.png", "makefile", "Dockerfile", "a.mdx"} {
		if termsChecked(path) {
			t.Errorf("termsChecked(%q) = true, want false", path)
		}
	}
}

func TestRunTerms(t *testing.T) {
	t.Run("British spelling", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "note.md")
		if err := os.WriteFile(path, []byte("hello\nbehaviour\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		got := run([]string{"terms"}, strings.NewReader(path+"\n"), &stdout, &stderr)
		if got != 1 {
			t.Errorf("exit code = %d, want 1\nstderr: %s", got, &stderr)
		}
		want := path + ":2: British spelling \"behaviour\", use \"behavior\"\n"
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr = %q, want it to contain %q", stderr.String(), want)
		}
	})

	t.Run("CI spelling exemption", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)
		if err := os.MkdirAll(".github/workflows", 0o750); err != nil {
			t.Fatal(err)
		}
		path := ".github/workflows/ci.yml"
		for _, tt := range []struct {
			text string
			want int
		}{
			{"cancelled\n", 0},
			{"cancelled inbox\n", 1},
		} {
			if err := os.WriteFile(path, []byte(tt.text), 0o600); err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			got := run([]string{"terms"}, strings.NewReader(path+"\n"), &stdout, &stderr)
			if got != tt.want {
				t.Errorf("exit code = %d, want %d\nstderr: %s", got, tt.want, &stderr)
			}
			if strings.Contains(stderr.String(), "British spelling") {
				t.Errorf("skipped spelling reported: %s", &stderr)
			}
			want := path + ":1: retired word \"inbox\"\n"
			if tt.want == 1 && !strings.Contains(stderr.String(), want) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), want)
			}
		}
	})

	t.Run("retired word", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "note.md")
		if err := os.WriteFile(path, []byte("hello\nInbox today\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		got := run([]string{"terms"}, strings.NewReader(path+"\n"), &stdout, &stderr)
		if got != 1 {
			t.Errorf("exit code = %d, want 1\nstderr: %s", got, &stderr)
		}
		want := path + ":2: retired word \"Inbox\"\n"
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr = %q, want it to contain %q", stderr.String(), want)
		}
	})

	t.Run("clean file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "note.md")
		if err := os.WriteFile(path, []byte("admission and permissions\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		got := run([]string{"terms"}, strings.NewReader(path+"\n"), &stdout, &stderr)
		if got != 0 {
			t.Errorf("exit code = %d, want 0\nstderr: %s", got, &stderr)
		}
		if !strings.Contains(stdout.String(), "ok") {
			t.Errorf("stdout = %q, want an ok line", stdout.String())
		}
	})

	t.Run("missing path", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		got := run([]string{"terms"}, strings.NewReader("no-such-file.md\n"), &stdout, &stderr)
		if got != 0 {
			t.Errorf("exit code = %d, want 0\nstderr: %s", got, &stderr)
		}
	})

	t.Run("skipped path", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)
		if err := os.MkdirAll("web", 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile("web/pnpm-lock.yaml", []byte("inbox: 1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		got := run([]string{"terms"}, strings.NewReader("web/pnpm-lock.yaml\n"), &stdout, &stderr)
		if got != 0 {
			t.Errorf("exit code = %d, want 0\nstderr: %s", got, &stderr)
		}
		if strings.Contains(stderr.String(), "retired word") {
			t.Errorf("skipped path reported: %s", &stderr)
		}
	})

	t.Run("unchecked extension", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "note.txt")
		if err := os.WriteFile(path, []byte("inbox\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		got := run([]string{"terms"}, strings.NewReader(path+"\n"), &stdout, &stderr)
		if got != 0 {
			t.Errorf("exit code = %d, want 0\nstderr: %s", got, &stderr)
		}
	})
}

func TestRunTermsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := run([]string{"terms", "extra"}, strings.NewReader(""), &stdout, &stderr); got != 2 {
		t.Errorf("exit code = %d, want 2\nstderr: %s", got, &stderr)
	}
}
