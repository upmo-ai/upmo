package domain

import (
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	// TokenTTL is how long an editor token stays valid after it is issued.
	TokenTTL = 15 * time.Minute
	// GalleryHost is the only extension gallery host an editor may name.
	GalleryHost = "open-vsx.org"
)

// Token binds editor access to one person, one task and one session until it expires.
type Token struct {
	User    string
	Task    string
	Session string
	Expires time.Time
}

// canonicalID returns raw as a lowercase hyphenated UUID, and refuses a blank, malformed or nil UUID.
func canonicalID(raw string) (string, bool) {
	parsed, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == uuid.Nil {
		return "", false
	}
	return parsed.String(), true
}

// Issue returns an unsigned token for distinct user, task and session ids that expires TokenTTL after now.
func Issue(user, task, session string, now time.Time) (Token, error) {
	if now.IsZero() {
		return Token{}, ErrEditor
	}
	u, okUser := canonicalID(user)
	k, okTask := canonicalID(task)
	s, okSession := canonicalID(session)
	if !okUser || !okTask || !okSession || u == k || u == s || k == s {
		return Token{}, ErrEditor
	}
	return Token{User: u, Task: k, Session: s, Expires: now.UTC().Add(TokenTTL)}, nil
}

// Accept checks that token names the given user, task and session and that now is before its expiry.
func Accept(token Token, user, task, session string, now time.Time) error {
	if now.IsZero() {
		return ErrEditor
	}
	u, okUser := canonicalID(user)
	k, okTask := canonicalID(task)
	s, okSession := canonicalID(session)
	if !okUser || !okTask || !okSession {
		return ErrEditor
	}
	tu, okTokenUser := canonicalID(token.User)
	tk, okTokenTask := canonicalID(token.Task)
	ts, okTokenSession := canonicalID(token.Session)
	if !okTokenUser || !okTokenTask || !okTokenSession {
		return ErrEditor
	}
	if tu != u || tk != k || ts != s || u == k || u == s || k == s {
		return ErrEditor
	}
	if now.UTC().Before(token.Expires.UTC()) {
		return nil
	}
	return ErrEditor
}

// SeparateOrigin refuses an editor URL that shares an origin with the app URL, and never dials either.
func SeparateOrigin(app, editor string) error {
	appKey, okApp := originKey(app)
	editorKey, okEditor := originKey(editor)
	if !okApp || !okEditor || appKey == editorKey {
		return ErrEditor
	}
	return nil
}

// originKey returns the scheme, host and non-default port of an absolute http or https URL with no userinfo.
func originKey(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "\\") {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || parsed.User != nil {
		return "", false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "" || strings.ContainsAny(host, "\\% ") {
		return "", false
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	key := scheme + "://" + host
	port := parsed.Port()
	if port == "" {
		return key, true
	}
	if len(port) > 5 || port[0] == '0' || strings.Trim(port, "0123456789") != "" {
		return "", false
	}
	n := 0
	for _, c := range port {
		n = n*10 + int(c-'0')
	}
	if n < 1 || n > 65535 {
		return "", false
	}
	if (scheme == "http" && n == 80) || (scheme == "https" && n == 443) {
		return key, true
	}
	return key + ":" + port, true
}

// Gallery refuses any extension gallery host other than GalleryHost, and a parent host does not cover a child.
func Gallery(host string) error {
	d, err := AdmitEgress(host, []string{GalleryHost})
	if err != nil {
		return ErrEditor
	}
	if d.Verdict != VerdictAllow || d.Reason != ReasonListed || d.Port != "" || d.Host != GalleryHost {
		return ErrEditor
	}
	return nil
}
