package domain

import (
	"errors"
	"net/url"
	"strings"
	"unicode"
)

// RepoHost is the host an owner/name repository is cloned from.
const RepoHost = "github.com"

// ErrRepo reports a repository that cannot be cloned: not an https URL and
// not an owner/name pair, or one that carries a credential.
var ErrRepo = errors.New("runtime: repo")

// CloneURL returns the URL a repository is cloned from, or an empty string
// for an empty repository. An owner/name pair is cloned from RepoHost over
// https. A URL must be https with a host and a path, and may not carry a
// user name, a password, a query or a fragment, so no credential ever sits
// in a command line.
func CloneURL(repo string) (string, error) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return "", nil
	}
	if strings.HasPrefix(repo, "-") || strings.ContainsFunc(repo, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) {
		return "", ErrRepo
	}
	if strings.Contains(repo, "://") {
		u, err := url.Parse(repo)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
			u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") == "" {
			return "", ErrRepo
		}
		return u.String(), nil
	}
	owner, name, ok := strings.Cut(strings.TrimSuffix(repo, ".git"), "/")
	if !ok || !repoPart(owner) || !repoPart(name) {
		return "", ErrRepo
	}
	return "https://" + RepoHost + "/" + owner + "/" + name + ".git", nil
}

// repoPart reports whether s is one owner or name segment: letters, digits,
// dot, hyphen and underscore, not starting with a dot or a hyphen.
func repoPart(s string) bool {
	if s == "" || s[0] == '.' || s[0] == '-' {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}
