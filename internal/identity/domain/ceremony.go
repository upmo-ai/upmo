package domain

import (
	"net"
	"time"

	"github.com/google/uuid"
)

const (
	// ChallengeLifetime is how long a WebAuthn challenge may be used.
	ChallengeLifetime = 5 * time.Minute
	// RecoverLifetime is how long a recover or bootstrap link lasts.
	RecoverLifetime = 15 * time.Minute
	// InviteLifetime is how long an invite link lasts.
	InviteLifetime = 7 * 24 * time.Hour
	// StepUpWindow is how recently a person must have signed in to change a passkey.
	StepUpWindow = 10 * time.Minute
	// ThrottleBurst is the number of unauthenticated identity requests an address may send at once.
	ThrottleBurst = 10
	// ThrottlePerSecond is how fast that bucket refills.
	ThrottlePerSecond = 1.0 / 6
)

// ParseLinkToken accepts only the canonical link text.
func ParseLinkToken(s string) (Token, error) {
	return ParseToken(s)
}

// ParseChallengeID accepts only the canonical challenge-cookie text.
func ParseChallengeID(s string) (Token, error) {
	return ParseToken(s)
}

// Fresh reports whether a sign-in at signedIn is still inside the step-up window at now.
// The window includes its boundary.
func Fresh(signedIn, now time.Time) bool {
	if now.Before(signedIn) {
		return false
	}
	return !now.After(signedIn.Add(StepUpWindow))
}

// LinkLifetime is how long a link of that purpose lasts.
func LinkLifetime(purpose string) (time.Duration, bool) {
	switch purpose {
	case "recover", "bootstrap", "sign_in", "step_up", "sso", "sso_stepup":
		return RecoverLifetime, true
	case "invite":
		return InviteLifetime, true
	default:
		return 0, false
	}
}

// Refill adds tokens for the time since updated, capped at the burst.
func Refill(tokens float64, updated, now time.Time, burst, perSecond float64) float64 {
	if perSecond <= 0 {
		return tokens
	}
	elapsed := now.Sub(updated).Seconds()
	if elapsed < 0 {
		elapsed = 0
	}
	tokens += elapsed * perSecond
	if tokens > burst {
		return burst
	}
	return tokens
}

// RetryAfter is how long a caller must wait before one token is available.
func RetryAfter(tokens, perSecond float64) time.Duration {
	if tokens >= 1 || perSecond <= 0 {
		return 0
	}
	seconds := (1 - tokens) / perSecond
	return time.Duration(seconds * float64(time.Second))
}

// ThrottleAddress is the bucket key for a client address. IPv6 addresses in
// one /64 share a bucket. Anything that is not an IP is returned unchanged.
func ThrottleAddress(host string) string {
	ip := net.ParseIP(host)
	if ip == nil {
		return host
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String()
}

// Account is the person a ceremony is about.
type Account struct {
	ID          UserID
	Handle      []byte
	Name        string
	Display     string
	Credentials []Credential
}

// Credential is a stored passkey.
type Credential struct {
	ID             []byte
	PublicKey      []byte
	SignCount      uint32
	AAGUID         uuid.UUID
	Transports     []string
	BackupEligible bool
	BackupState    bool
	Attestation    string
	CloneWarning   bool
}

// PasskeyName is the label a new passkey gets from its authenticator.
// An unknown authenticator is just "Passkey".
func PasskeyName(aaguid uuid.UUID) string {
	switch aaguid.String() {
	case "fbfc3007-154e-4ecc-8c0b-6e020557d7bd":
		return "iCloud Keychain"
	case "bada5566-a7aa-401f-bd96-45619a55120d":
		return "1Password"
	case "d548826e-79b4-db40-a3d8-11116f7e8349":
		return "Bitwarden"
	case "ea9b8d66-4d01-1d21-3ce4-b6b48cb575d4":
		return "Google Password Manager"
	default:
		return "Passkey"
	}
}
