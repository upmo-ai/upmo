package domain

import (
	"errors"
	"testing"
)

func TestCloneURL(t *testing.T) {
	cases := map[string]string{
		"":                                   "",
		"   ":                                "",
		"org/name":                           "https://github.com/org/name.git",
		" org/name.git ":                     "https://github.com/org/name.git",
		"Org_1/my.repo-x":                    "https://github.com/Org_1/my.repo-x.git",
		"https://git.example.com/team/r.git": "https://git.example.com/team/r.git",
	}
	for repo, want := range cases {
		got, err := CloneURL(repo)
		if err != nil || got != want {
			t.Errorf("CloneURL(%q) = %q %v, want %q", repo, got, err, want)
		}
	}
}

func TestCloneURLRefuses(t *testing.T) {
	for _, repo := range []string{
		"name",
		"org/",
		"/name",
		"org/name/extra",
		"-org/name",
		"org/-name",
		".org/name",
		"org/..",
		"org/na me",
		"org/na\nme",
		"org/na\x00me",
		"org;rm/name",
		"http://example.com/org/name",
		"ssh://git@example.com/org/name",
		"file:///etc/passwd",
		"https://ada:x@example.com/org/name",
		"https://token@example.com/org/name",
		"https://example.com/org/name?x=1",
		"https://example.com/org/name#main",
		"https://example.com/",
		"https:///org/name",
		"--upload-pack=sh",
	} {
		if got, err := CloneURL(repo); !errors.Is(err, ErrRepo) {
			t.Errorf("CloneURL(%q) = %q %v, want ErrRepo", repo, got, err)
		}
	}
}
