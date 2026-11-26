package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunTitle(t *testing.T) {
	const fix = "repolint: the pull request title is the squash commit subject on main; fix the title\n"
	long := "feat: " + strings.Repeat("a", 67)
	tests := []struct {
		name   string
		title  string
		want   int
		stdout string
		stderr string
	}{
		{"valid", "ci(repolint): check pull request titles", 0, "repolint: pull request title ok\n", ""},
		{"exactly 72 characters", "feat: " + strings.Repeat("a", 66), 0, "repolint: pull request title ok\n", ""},
		{"uppercase description", "feat: add Login", 1, "",
			`pull request title "feat: add Login": description must be lowercase` + "\n" + fix},
		{"trailing period", "feat: add login.", 1, "",
			`pull request title "feat: add login.": subject must not end with a period` + "\n" + fix},
		{"73 characters", long, 1, "",
			`pull request title "` + long + `": subject is 73 characters, the limit is 72` + "\n" + fix},
		{"not conventional", "Add login", 1, "",
			`pull request title "Add login": subject must look like type(optional scope): lowercase description, ` +
				"with type one of feat, fix, docs, style, refactor, perf, test, chore, build, ci, revert\n" + fix},
		{"attribution", "feat: add login \U0001F916", 1, "",
			"pull request title \"feat: add login \U0001F916\": AI or tool attribution is not allowed\n" + fix},
		{"empty", "", 2, "", "repolint: PR_TITLE is not set\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PR_TITLE", tt.title)
			var stdout, stderr bytes.Buffer
			got := run([]string{"title"}, nil, &stdout, &stderr)
			if got != tt.want {
				t.Errorf("exit code = %d, want %d", got, tt.want)
			}
			if stdout.String() != tt.stdout {
				t.Errorf("stdout = %q, want %q", &stdout, tt.stdout)
			}
			if stderr.String() != tt.stderr {
				t.Errorf("stderr = %q, want %q", &stderr, tt.stderr)
			}
		})
	}
}
