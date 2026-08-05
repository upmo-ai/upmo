package domain

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"
)

const (
	// TokenBytes is the size of an opaque session token.
	TokenBytes = 32
	// IdleTimeout is how long a session lasts without a request.
	IdleTimeout = 24 * time.Hour
	// MaxAge is the absolute lifetime of a session.
	MaxAge = 14 * 24 * time.Hour
	// TouchInterval is the shortest gap between last-seen writes.
	TouchInterval = time.Minute
)

// Token is the raw session secret. Only its hash is stored.
type Token [TokenBytes]byte

// NewToken returns a new session token and its canonical cookie text.
func NewToken() (Token, string, error) {
	var token Token
	if _, err := rand.Read(token[:]); err != nil {
		return Token{}, "", fmt.Errorf("identity: session token: %w", err)
	}
	return token, EncodeToken(token), nil
}

// EncodeToken is the canonical base64url form of a token.
func EncodeToken(token Token) string {
	return base64.RawURLEncoding.EncodeToString(token[:])
}

// ParseToken accepts only the canonical cookie text: 43 base64url characters
// with the unused bits zero.
func ParseToken(s string) (Token, error) {
	if len(s) != 43 {
		return Token{}, fmt.Errorf("identity: session token")
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(s)
	if err != nil || len(raw) != TokenBytes {
		return Token{}, fmt.Errorf("identity: session token")
	}
	var token Token
	copy(token[:], raw)
	return token, nil
}

// Hash is what the database stores. A stored hash does not open a session.
func Hash(token Token) []byte {
	sum := sha256.Sum256(token[:])
	return sum[:]
}

// Ended reports whether the session is over at now. Idle and the absolute
// expiry both end it, and the boundary itself is already over.
func Ended(lastSeen, expires, now time.Time, idle time.Duration) bool {
	if !now.Before(expires) {
		return true
	}
	return !now.Before(lastSeen.Add(idle))
}

// TouchDue reports whether last seen should be written. Writes are at most
// one a minute, and the minute boundary is due.
func TouchDue(lastSeen, now time.Time) bool {
	return !now.Before(lastSeen.Add(TouchInterval))
}
