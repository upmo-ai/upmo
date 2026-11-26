package app

import (
	"context"
	"errors"
	"time"

	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
)

// ErrWrong is the one answer for a failed password sign-in.
var ErrWrong = errors.New("identity: email or password is wrong")

// ErrLimited is a password guess over a throttle bucket.
var ErrLimited = errors.New("identity: slow down")

// ErrNoSealer means no key service is wired, so no password hash can be
// stored or checked.
var ErrNoSealer = errors.New("identity: password storage needs the key service")

// MaxEmailBytes and MaxPasswordBytes bound what a password check sends to
// the worker; anything longer is a wrong password without a call.
const (
	MaxEmailBytes    = 320
	MaxPasswordBytes = 1024
)

// PasswordKeys is the worker's side of a password. Seal asks the worker to
// wrap a new data key for the person and seals hash under it here, as the
// stored text. The worker checks a password against the stored hash, which
// brain never opens: Verify by address for sign-in, VerifySession for the
// signed-in person behind a session token hash. A worker that cannot answer
// is domain.ErrBusy, and its per-address limit is ErrLimited.
type PasswordKeys interface {
	Seal(ctx context.Context, user domain.UserID, hash string) (string, error)
	Verify(ctx context.Context, email, password string) (match, rehash bool, err error)
	VerifySession(ctx context.Context, tokenHash []byte, password string) (match, rehash bool, err error)
}

// PasswordRef is the seal ref of one person's stored password hash: a hash
// sealed for one person opens for no one else and for no provider credential.
func PasswordRef(user domain.UserID) string {
	return "identity:password:" + user.String()
}

// PasswordStore is the password columns and the shared throttle.
type PasswordStore interface {
	Take(ctx context.Context, key string, now time.Time, burst, perSecond float64) (bool, time.Duration, error)
	// PasswordAccount finds the enabled person behind an address. It never
	// reads the stored hash.
	PasswordAccount(ctx context.Context, email string) (user domain.UserID, personName string, found bool, err error)
	// PersonByEmail resolves a person for sign-in failure audit only.
	PersonByEmail(ctx context.Context, email string) (user domain.UserID, disabled, found bool, err error)
	// Profile is the signed-in person's address, from the worker for the
	// session tokenHash, and name.
	Profile(ctx context.Context, user domain.UserID, tokenHash []byte) (email, name string, err error)
	SetPassword(ctx context.Context, user domain.UserID, hash string) error
	ClearPassword(ctx context.Context, user domain.UserID) error
	HasPassword(ctx context.Context, user domain.UserID) (bool, error)
	ClaimOffer(ctx context.Context, user domain.UserID, now time.Time) (bool, error)
	SignInPolicy(ctx context.Context, user domain.UserID) (ssoRequired bool, roleKey string, err error)
}

// Passwords is the password sign-in use case.
type Passwords struct {
	Sessions Service
	Store    PasswordStore
	Keys     PasswordKeys
	Now      func() time.Time
}

func (p Passwords) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// Fresh reports whether the session may change a password.
func (p Passwords) Fresh(session Session) bool {
	return domain.Fresh(session.SignedIn, p.now())
}

// SignIn starts a session when the password matches. offer is true when a passkey should be offered.
func (p Passwords) SignIn(ctx context.Context, email, password, ip, agent string) (token string, offer bool, retry time.Duration, err error) {
	now := p.now()
	retry, err = p.limit(ctx, email, ip, now)
	if err != nil {
		return "", false, retry, err
	}
	if p.Keys == nil {
		return "", false, 0, ErrNoSealer
	}
	match, rehash := false, false
	if sendable(email, password) {
		if match, rehash, err = p.Keys.Verify(ctx, email, password); err != nil {
			return "", false, keysRetry(ctx, err), err
		}
	}
	if !match {
		return "", false, 0, p.wrong(ctx, email)
	}
	user, _, found, err := p.Store.PasswordAccount(ctx, email)
	if err != nil {
		return "", false, 0, err
	}
	if !found {
		return "", false, 0, p.wrong(ctx, email)
	}
	if err := checkSSOSignIn(ctx, p.Store, user, false); err != nil {
		return "", false, 0, err
	}
	if rehash {
		next, err := domain.HashPassword(password)
		if err != nil {
			return "", false, 0, err
		}
		// A rehash that cannot be sealed keeps the stored hash; the sign-in stands.
		if sealed, err := p.Keys.Seal(ctx, user, next); err == nil {
			if err := p.Store.SetPassword(ctx, user, sealed); err != nil {
				return "", false, 0, err
			}
		}
	}
	offer, err = p.Store.ClaimOffer(ctx, user, now)
	if err != nil {
		return "", false, 0, err
	}
	token, _, err = p.Sessions.Start(ctx, user, "password", agent)
	return token, offer, 0, err
}

// Set stores a password and ends the person's other sessions.
func (p Passwords) Set(ctx context.Context, user domain.UserID, keep, email, name, password string) error {
	if err := domain.Check(password, email, name); err != nil {
		return err
	}
	if p.Keys == nil {
		return ErrNoSealer
	}
	hash, err := domain.HashPassword(password)
	if err != nil {
		return err
	}
	stored, err := p.Keys.Seal(ctx, user, hash)
	if err != nil {
		return err
	}
	if s, ok := p.Store.(interface {
		SetPasswordWithKeep(ctx context.Context, user domain.UserID, keep, hash string) error
	}); ok {
		return s.SetPasswordWithKeep(ctx, user, keep, stored)
	}
	if err := p.Store.SetPassword(ctx, user, stored); err != nil {
		return err
	}
	return p.Sessions.EndOthers(ctx, user, keep)
}

// Clear removes the password when a passkey remains, and ends the other sessions.
func (p Passwords) Clear(ctx context.Context, user domain.UserID, keep string) error {
	if r, ok := p.Store.(interface {
		RemovePassword(ctx context.Context, user domain.UserID, keep string) error
	}); ok {
		return r.RemovePassword(ctx, user, keep)
	}
	if err := p.Store.ClearPassword(ctx, user); err != nil {
		return err
	}
	return p.Sessions.EndOthers(ctx, user, keep)
}

// Verify checks the signed-in person's password and refreshes the session.
// The worker checks the person behind tokenHash, never one named by email,
// which only keys the throttle. It counts as a guess.
func (p Passwords) Verify(ctx context.Context, user domain.UserID, sessionID string, tokenHash []byte, email, password, ip string) (time.Duration, error) {
	now := p.now()
	retry, err := p.limit(ctx, email, ip, now)
	if err != nil {
		return retry, err
	}
	if p.Keys == nil {
		return 0, ErrNoSealer
	}
	match := false
	// The session body sends no address, so only the password is bounded.
	if password != "" && len(password) <= MaxPasswordBytes {
		if match, _, err = p.Keys.VerifySession(ctx, tokenHash, password); err != nil {
			return keysRetry(ctx, err), err
		}
	}
	if !match {
		audit.Annotate(ctx, audit.Event{Reason: "bad_credential", TargetType: "user", TargetID: user.String()})
		return 0, ErrWrong
	}
	return 0, p.Sessions.Refresh(ctx, sessionID, "password", now)
}

// HasPassword reports whether the person has a password set.
func (p Passwords) HasPassword(ctx context.Context, user domain.UserID) (bool, error) {
	return p.Store.HasPassword(ctx, user)
}

// wrong names the person a failed sign-in was for, without the email, and
// returns ErrWrong. The worker already spent one hash on every path.
func (p Passwords) wrong(ctx context.Context, email string) error {
	id, disabled, found, err := p.Store.PersonByEmail(ctx, email)
	if err != nil {
		return err
	}
	switch {
	case found && disabled:
		audit.Annotate(ctx, audit.Event{Reason: "disabled", TargetType: "user", TargetID: id.String()})
	case found:
		audit.Annotate(ctx, audit.Event{Reason: "bad_credential", TargetType: "user", TargetID: id.String()})
	default:
		audit.Annotate(ctx, audit.Event{Reason: "unknown_account"})
	}
	return ErrWrong
}

// keysRetry is the wait to suggest after the worker refused a check: its
// window is one minute, the same for every address.
func keysRetry(ctx context.Context, err error) time.Duration {
	if !errors.Is(err, ErrLimited) {
		return 0
	}
	audit.Annotate(ctx, audit.Event{Reason: "throttled"})
	return time.Minute
}

// sendable reports whether an address and password fit what the worker
// checks; an empty or longer one is a wrong password without a call.
func sendable(email, password string) bool {
	return email != "" && password != "" && len(email) <= MaxEmailBytes && len(password) <= MaxPasswordBytes
}

func (p Passwords) limit(ctx context.Context, email, ip string, now time.Time) (time.Duration, error) {
	allowed, retry, err := p.Store.Take(ctx, domain.AccountKey(email), now, domain.AccountBurst, domain.AccountPerSecond)
	if err != nil {
		return 0, err
	}
	if !allowed {
		audit.Annotate(ctx, audit.Event{Reason: "throttled"})
		return retry, ErrLimited
	}
	allowed, retry, err = p.Store.Take(ctx, domain.AccountIPKey(email, ip), now, domain.AccountIPBurst, domain.AccountIPPerSecond)
	if err != nil {
		return 0, err
	}
	if !allowed {
		audit.Annotate(ctx, audit.Event{Reason: "throttled"})
		return retry, ErrLimited
	}
	return 0, nil
}
