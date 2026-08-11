package domain

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const APITokenPrefix = "upmo_pat_"

var (
	// ErrTokenName refuses a token with no name, or a name longer than 64 characters.
	ErrTokenName = errors.New("identity: a token needs a name")
	// ErrTokenDays refuses an expiry outside 1 to 365 days.
	ErrTokenDays = errors.New("identity: choose an expiry up to 365 days")
	// ErrTokenScope refuses a permission the token may not hold.
	ErrTokenScope = errors.New("identity: a token cannot hold that permission")
	// ErrToken is an unknown, expired or revoked token.
	ErrToken = errors.New("identity: invalid token")
)

// ParseTokenName trims the name and requires 1 to 64 characters.
func ParseTokenName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 64 {
		return "", ErrTokenName
	}
	return name, nil
}

// TokenDays returns the lifetime. Zero means the default of 90 days.
func TokenDays(days int) (int, error) {
	if days == 0 {
		return 90, nil
	}
	if days < 1 || days > 365 {
		return 0, ErrTokenDays
	}
	return days, nil
}

// TokenExpiryDate accepts a calendar date, YYYY-MM-DD. The token lasts through
// that UTC day. The date cannot be in the past, and it cannot be more than 365 days ahead.
func TokenExpiryDate(text string, now time.Time) (time.Time, error) {
	day, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(text), time.UTC)
	if err != nil {
		return time.Time{}, ErrTokenDays
	}
	now = now.UTC()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if day.Before(start) || day.After(start.AddDate(0, 0, 365)) {
		return time.Time{}, ErrTokenDays
	}
	expires := time.Date(day.Year(), day.Month(), day.Day(), 23, 59, 59, 0, time.UTC)
	if !expires.After(now) {
		return time.Time{}, ErrTokenDays
	}
	return expires, nil
}

// TokenScope is the set a token may be given. It must be object permissions
// the person holds, and it never includes a delete or an install permission.
func TokenScope(held []Permission, requested []string) ([]Permission, error) {
	if len(requested) == 0 {
		return nil, ErrTokenScope
	}
	var scope []Permission
	seen := map[Permission]bool{}
	for _, name := range requested {
		permission, err := ParsePermission(strings.TrimSpace(name))
		if err != nil || runForbidden(permission) || !has(held, permission) || seen[permission] {
			return nil, ErrTokenScope
		}
		seen[permission] = true
		scope = append(scope, permission)
	}
	return scope, nil
}

// NewAPITokenSecret returns the stored hash and the secret shown once.
func NewAPITokenSecret() (secret string, hash []byte, err error) {
	token, text, err := NewToken()
	if err != nil {
		return "", nil, err
	}
	return APITokenPrefix + text, Hash(token), nil
}

// ParseAPITokenSecret accepts only the prefixed canonical secret.
func ParseAPITokenSecret(secret string) (Token, error) {
	text, ok := strings.CutPrefix(secret, APITokenPrefix)
	if !ok {
		return Token{}, ErrToken
	}
	token, err := ParseToken(text)
	if err != nil {
		return Token{}, ErrToken
	}
	return token, nil
}

// TokenExpired reports whether the token is over at now. The boundary itself is over.
func TokenExpired(expires, now time.Time) bool {
	return !now.Before(expires)
}
