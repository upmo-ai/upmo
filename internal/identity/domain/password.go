package domain

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
	"golang.org/x/text/unicode/norm"
)

const (
	// PasswordMin is the NIST single-factor minimum, counted in Unicode characters.
	PasswordMin = 15
	// PasswordMax is the longest password accepted.
	PasswordMax = 128
	// PasswordDistinct refuses a password made of fewer different characters.
	PasswordDistinct = 5
	// ArgonMemory is the OWASP minimum, in kibibytes (19 MiB).
	ArgonMemory = 19 * 1024
	// ArgonTime is the number of argon2id passes.
	ArgonTime = 2
	// ArgonThreads is the argon2id parallelism.
	ArgonThreads = 1
	// PasskeyOfferAfter is how long a password sign-in waits before offering a passkey again.
	PasskeyOfferAfter = 30 * 24 * time.Hour
	// AccountIPBurst is password guesses one account may take from one address at once.
	AccountIPBurst = 10
	// AccountIPPerSecond refills that bucket once every 30 seconds.
	AccountIPPerSecond = 1.0 / 30
	// AccountBurst is password guesses one account may take from anywhere at once.
	AccountBurst = 100
	// AccountPerSecond refills that bucket once a minute.
	AccountPerSecond = 1.0 / 60

	argonSaltLen = 16
	argonKeyLen  = 32
	hashSlots    = 4
)

var (
	// ErrPasswordShort is a password under the minimum.
	ErrPasswordShort = errors.New("use at least 15 characters")
	// ErrPasswordLong is a password over the maximum.
	ErrPasswordLong = errors.New("use at most 128 characters")
	// ErrPasswordRepetitive is a password with too few different characters.
	ErrPasswordRepetitive = errors.New("use at least 5 different characters")
	// ErrPasswordCommon is a password on the common list.
	ErrPasswordCommon = errors.New("that password is too common")
	// ErrPasswordPersonal is built from the person's email, name or "upmo".
	ErrPasswordPersonal = errors.New("do not build the password from your name, email or upmo")
	// ErrBusy means the hash gate is full.
	ErrBusy = errors.New("try again")

	errPHC = errors.New("identity: password hash")

	hashGate   = make(chan struct{}, hashSlots)
	dummySalt  = bytesRepeat(argonSaltLen, 0x3c)
	dummyPlain = "dummy-password-value"
)

// Normalize applies NFKC. The result is what is hashed and checked.
func Normalize(password string) string {
	return norm.NFKC.String(password)
}

// Check reports why a new password is refused. email and name are the person's.
func Check(password, email, name string) error {
	password = Normalize(password)
	n := utf8.RuneCountInString(password)
	switch {
	case n < PasswordMin:
		return ErrPasswordShort
	case n > PasswordMax:
		return ErrPasswordLong
	}
	seen := map[rune]struct{}{}
	for _, r := range password {
		seen[r] = struct{}{}
	}
	if len(seen) < PasswordDistinct {
		return ErrPasswordRepetitive
	}
	if _, ok := commonPasswords[strings.ToLower(password)]; ok {
		return ErrPasswordCommon
	}
	left := strings.ToLower(password)
	for _, ban := range personalBans(email, name) {
		left = strings.ReplaceAll(left, ban, "")
	}
	if utf8.RuneCountInString(left) < PasswordMin {
		return ErrPasswordPersonal
	}
	return nil
}

// HashPassword stores password as an argon2id PHC string.
func HashPassword(password string) (string, error) {
	if !enterHash() {
		return "", ErrBusy
	}
	defer leaveHash()
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(Normalize(password)), salt, ArgonTime, ArgonMemory, ArgonThreads, argonKeyLen)
	return formatPHC(salt, key, ArgonMemory, ArgonTime, ArgonThreads), nil
}

// VerifyPassword checks password against a PHC string. rehash is set when the
// stored settings are not the current ones. A damaged string still costs one hash.
func VerifyPassword(password, phc string) (ok, rehash bool, err error) {
	if !enterHash() {
		return false, false, ErrBusy
	}
	defer leaveHash()
	salt, key, memory, timeCost, threads, perr := parsePHC(phc)
	if perr != nil {
		argon2.IDKey([]byte(dummyPlain), dummySalt, ArgonTime, ArgonMemory, ArgonThreads, argonKeyLen)
		return false, false, nil
	}
	// argon2id keys we accept are at most 64 bytes, which fits in uint32.
	if len(key) > 64 {
		argon2.IDKey([]byte(dummyPlain), dummySalt, ArgonTime, ArgonMemory, ArgonThreads, argonKeyLen)
		return false, false, nil
	}
	got := argon2.IDKey([]byte(Normalize(password)), salt, timeCost, memory, threads, uint32(len(key))) //nolint:gosec // len(key) is at most 64
	if subtle.ConstantTimeCompare(got, key) != 1 {
		return false, false, nil
	}
	stale := memory != ArgonMemory || timeCost != ArgonTime || threads != ArgonThreads || len(salt) != argonSaltLen || len(key) != argonKeyLen
	return true, stale, nil
}

// DummyPasswordHash spends one hash so an unknown account takes the same time.
func DummyPasswordHash() error {
	if !enterHash() {
		return ErrBusy
	}
	defer leaveHash()
	argon2.IDKey([]byte(dummyPlain), dummySalt, ArgonTime, ArgonMemory, ArgonThreads, argonKeyLen)
	return nil
}

// OfferPasskey reports whether a password sign-in at now should offer a passkey.
// The boundary is included: an offer exactly PasskeyOfferAfter later is due.
func OfferPasskey(offered *time.Time, now time.Time) bool {
	if offered == nil {
		return true
	}
	return !now.Before(offered.Add(PasskeyOfferAfter))
}

// AccountKey is the throttle key for an account. It is a hash of the lowercased email.
func AccountKey(email string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return "account:" + hex.EncodeToString(sum[:])
}

// AccountIPKey is the throttle key for an account from one client address.
func AccountIPKey(email, ip string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return "account-ip:" + hex.EncodeToString(sum[:]) + ":" + ip
}

func personalBans(email, name string) []string {
	email = strings.ToLower(strings.TrimSpace(email))
	var bans []string
	if email != "" {
		bans = append(bans, email)
		if local, _, ok := strings.Cut(email, "@"); ok && local != "" {
			bans = append(bans, local)
		}
	}
	bans = append(bans, "upmo")
	for _, word := range strings.Fields(strings.ToLower(name)) {
		if word != "" {
			bans = append(bans, word)
		}
	}
	return bans
}

func formatPHC(salt, key []byte, memory, timeCost uint32, threads uint8) string {
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", memory, timeCost, threads, b64(salt), b64(key))
}

func parsePHC(s string) (salt, key []byte, memory, timeCost uint32, threads uint8, err error) {
	parts := strings.Split(s, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" {
		return nil, nil, 0, 0, 0, errPHC
	}
	var m, t, p int
	if _, scanErr := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); scanErr != nil {
		return nil, nil, 0, 0, 0, errPHC
	}
	if fmt.Sprintf("m=%d,t=%d,p=%d", m, t, p) != parts[3] || m < 1 || t < 1 || p < 1 || p > 255 {
		return nil, nil, 0, 0, 0, errPHC
	}
	salt, ok := exactB64(parts[4])
	if !ok || len(salt) == 0 {
		return nil, nil, 0, 0, 0, errPHC
	}
	key, ok = exactB64(parts[5])
	if !ok || len(key) == 0 {
		return nil, nil, 0, 0, 0, errPHC
	}
	return salt, key, uint32(m), uint32(t), uint8(p), nil
}

func exactB64(s string) ([]byte, bool) {
	if s == "" {
		return nil, false
	}
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '+', r == '/':
		default:
			return nil, false
		}
	}
	raw, err := base64.RawStdEncoding.Strict().DecodeString(s)
	if err != nil || b64(raw) != s {
		return nil, false
	}
	return raw, true
}

func b64(b []byte) string {
	return base64.RawStdEncoding.EncodeToString(b)
}

// FillHashGate takes every hash slot. The caller must release it.
// A sign-in while it is held answers 503.
func FillHashGate() (func(), bool) {
	held := 0
	for held < hashSlots {
		if !enterHash() {
			break
		}
		held++
	}
	if held != hashSlots {
		for range held {
			leaveHash()
		}
		return nil, false
	}
	return func() {
		for range held {
			leaveHash()
		}
	}, true
}

func enterHash() bool {
	select {
	case hashGate <- struct{}{}:
		return true
	default:
		return false
	}
}

func leaveHash() {
	<-hashGate
}

func bytesRepeat(n int, b byte) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}
