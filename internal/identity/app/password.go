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

// ErrNoSealer means no master key is wired, so no password hash can be stored.
var ErrNoSealer = errors.New("identity: password storage needs a master key")

// PasswordSealer seals and opens a stored password hash. The brain wires it
// from the master keyring.
type PasswordSealer interface {
	Ready() bool
	Seal(ref, hash string) (string, error)
	Open(ref, stored string) (string, error)
}

// PasswordRef is the seal ref of one person's stored password hash: a hash
// sealed for one person opens for no one else and for no provider credential.
func PasswordRef(user domain.UserID) string {
	return "identity:password:" + user.String()
}

// PasswordStore is the password columns and the shared throttle.
type PasswordStore interface {
	Take(ctx context.Context, key string, now time.Time, burst, perSecond float64) (bool, time.Duration, error)
	PasswordAccount(ctx context.Context, email string) (user domain.UserID, hash, personName string, found bool, err error)
	// PersonByEmail resolves a person for sign-in failure audit only.
	PersonByEmail(ctx context.Context, email string) (user domain.UserID, disabled, found bool, err error)
	Profile(ctx context.Context, user domain.UserID) (email, name string, err error)
	SetPassword(ctx context.Context, user domain.UserID, hash string) error
	ClearPassword(ctx context.Context, user domain.UserID) error
	HasPassword(ctx context.Context, user domain.UserID) (bool, error)
	ClaimOffer(ctx context.Context, user domain.UserID, now time.Time) (bool, error)
}

// Passwords is the password sign-in use case.
type Passwords struct {
	Sessions Service
	Store    PasswordStore
	Sealer   PasswordSealer
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
	user, stored, _, found, err := p.Store.PasswordAccount(ctx, email)
	if err != nil {
		return "", false, 0, err
	}
	if !found || stored == "" {
		if err := p.guardMissingAccount(ctx, email); err != nil {
			return "", false, 0, err
		}
		return "", false, 0, ErrWrong
	}
	hash, err := p.open(user, stored)
	if err != nil {
		return "", false, 0, err
	}
	ok, rehash, err := domain.VerifyPassword(password, hash)
	if err != nil {
		return "", false, 0, err
	}
	if !ok {
		audit.Annotate(ctx, audit.Event{Reason: "bad_credential", TargetType: "user", TargetID: user.String()})
		return "", false, 0, ErrWrong
	}
	if rehash {
		next, err := domain.HashPassword(password)
		if err != nil {
			return "", false, 0, err
		}
		// A rehash that cannot be sealed keeps the stored hash; the sign-in stands.
		if sealed, err := p.seal(user, next); err == nil {
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
	hash, err := domain.HashPassword(password)
	if err != nil {
		return err
	}
	stored, err := p.seal(user, hash)
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

// Verify checks the password and refreshes the session. It counts as a guess.
func (p Passwords) Verify(ctx context.Context, user domain.UserID, sessionID, email, password, ip string) (time.Duration, error) {
	now := p.now()
	retry, err := p.limit(ctx, email, ip, now)
	if err != nil {
		return retry, err
	}
	account, stored, _, found, err := p.Store.PasswordAccount(ctx, email)
	if err != nil {
		return 0, err
	}
	if !found || stored == "" {
		if err := p.guardMissingAccount(ctx, email); err != nil {
			return 0, err
		}
		return 0, ErrWrong
	}
	hash, err := p.open(account, stored)
	if err != nil {
		return 0, err
	}
	ok, _, err := domain.VerifyPassword(password, hash)
	if err != nil || !ok {
		if err != nil {
			return 0, err
		}
		audit.Annotate(ctx, audit.Event{Reason: "bad_credential", TargetType: "user", TargetID: user.String()})
		return 0, ErrWrong
	}
	return 0, p.Sessions.Refresh(ctx, sessionID, "password", now)
}

// HasPassword reports whether the person has a password set.
func (p Passwords) HasPassword(ctx context.Context, user domain.UserID) (bool, error) {
	return p.Store.HasPassword(ctx, user)
}

// guardMissingAccount names an unknown or disabled person and does the same hash
// work as a wrong password, without returning the email.
func (p Passwords) guardMissingAccount(ctx context.Context, email string) error {
	id, disabled, found, err := p.Store.PersonByEmail(ctx, email)
	if err != nil {
		return err
	}
	if found && disabled {
		audit.Annotate(ctx, audit.Event{Reason: "disabled", TargetType: "user", TargetID: id.String()})
	} else {
		audit.Annotate(ctx, audit.Event{Reason: "unknown_account"})
	}
	return domain.DummyPasswordHash()
}

// seal returns hash in the stored form, sealed for this person alone. Without
// a ready sealer nothing is stored.
func (p Passwords) seal(user domain.UserID, hash string) (string, error) {
	if p.Sealer == nil || !p.Sealer.Ready() {
		return "", ErrNoSealer
	}
	return p.Sealer.Seal(PasswordRef(user), hash)
}

// open returns the hash stored for user. A value that does not open is an
// error, never a wrong-password answer: the sign-in fails loudly instead.
func (p Passwords) open(user domain.UserID, stored string) (string, error) {
	if p.Sealer == nil || !p.Sealer.Ready() {
		return "", ErrNoSealer
	}
	return p.Sealer.Open(PasswordRef(user), stored)
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
