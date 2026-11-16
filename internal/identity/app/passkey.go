package app

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"time"

	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
)

var (
	// ErrInvalidLink is the one answer for an unknown, used, expired or disabled link.
	ErrInvalidLink = errors.New("identity: invalid link")
	// ErrStale is a passkey change without a fresh sign-in.
	ErrStale = errors.New("identity: sign in again")
	// ErrLastPasskey refuses removal of the only way to sign in.
	ErrLastPasskey = domain.ErrLastPasskey
	// ErrChallenge is a missing, used or expired challenge.
	ErrChallenge = errors.New("identity: challenge")
	// ErrExists refuses a second bootstrap.
	ErrExists = errors.New("identity: someone already exists")
)

// Ceremonies is the WebAuthn relying party.
type Ceremonies interface {
	BeginDiscoverable() (options, session []byte, err error)
	FinishDiscoverable(session, body []byte, lookup func(credentialID, userHandle []byte) (domain.Account, error)) (domain.Account, domain.Credential, error)
	BeginRegistration(account domain.Account) (options, session []byte, err error)
	FinishRegistration(account domain.Account, session, body []byte) (domain.Credential, error)
}

// PassStore is the passkey tables.
type PassStore interface {
	SweepChallenges(ctx context.Context, now time.Time) error
	SweepLinks(ctx context.Context, now time.Time) error
	SaveChallenge(ctx context.Context, hash, session []byte, purpose string, user *domain.UserID, expires time.Time) error
	TakeChallenge(ctx context.Context, hash []byte, now time.Time) (session []byte, purpose string, user domain.UserID, ok bool, err error)
	InsertLink(ctx context.Context, hash []byte, user domain.UserID, purpose string, expires time.Time) error
	FindLink(ctx context.Context, hash []byte, now time.Time) (user domain.UserID, purpose string, ok bool, err error)
	SpendLink(ctx context.Context, hash []byte, now time.Time) error
	Handle(ctx context.Context, user domain.UserID, fresh []byte) ([]byte, error)
	Account(ctx context.Context, user domain.UserID) (domain.Account, error)
	AccountByCredential(ctx context.Context, credentialID, userHandle []byte) (domain.Account, error)
	InsertPasskey(ctx context.Context, user domain.UserID, item domain.Credential, name string, now time.Time) (string, error)
	TouchPasskey(ctx context.Context, credentialID []byte, count uint32, now time.Time) error
	Join(ctx context.Context, user domain.UserID, now time.Time) error
	Take(ctx context.Context, key string, now time.Time, burst, perSecond float64) (bool, time.Duration, error)
	UserByEmail(ctx context.Context, email string) (domain.UserID, bool, error)
	Bootstrap(ctx context.Context, email, name string, hash []byte, now, expires time.Time) (domain.UserID, error)
	SignInPolicy(ctx context.Context, user domain.UserID) (ssoRequired bool, roleKey string, err error)
	ListPasskeys(ctx context.Context, user domain.UserID) ([]PasskeyRecord, error)
	RenamePasskey(ctx context.Context, user domain.UserID, id, name string) error
	DeletePasskey(ctx context.Context, user domain.UserID, id string) ([]byte, error)
}

// PasskeyRecord is one passkey a person can see.
type PasskeyRecord struct {
	ID           string
	Name         string
	CredentialID []byte
	Added        time.Time
	LastUsed     *time.Time
}

// Passkeys runs sign-in, links and passkey changes.
type Passkeys struct {
	Sessions  Service
	Store     PassStore
	Ceremony  Ceremonies
	Now       func() time.Time
	Log       *slog.Logger
	PublicURL string
}

func (p Passkeys) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// BeginSignIn starts a discoverable passkey sign-in. The cookie text is new.
func (p Passkeys) BeginSignIn(ctx context.Context) (cookie string, options []byte, err error) {
	return p.begin(ctx, "sign-in", nil)
}

// FinishSignIn opens a session when the passkey checks out.
func (p Passkeys) FinishSignIn(ctx context.Context, cookie string, body []byte, agent string) (text string, err error) {
	session, _, _, err := p.take(ctx, cookie, "sign-in")
	if err != nil {
		audit.Annotate(ctx, audit.Event{Reason: "bad_credential"})
		return "", err
	}
	account, credential, err := p.Ceremony.FinishDiscoverable(session, body, func(credentialID, userHandle []byte) (domain.Account, error) {
		return p.Store.AccountByCredential(ctx, credentialID, userHandle)
	})
	if err != nil {
		p.annotateAttempt(ctx, err)
		return "", err
	}
	if err := checkSSOSignIn(ctx, p.Store, account.ID, false); err != nil {
		return "", err
	}
	p.noteClone(credential)
	if err := p.Store.TouchPasskey(ctx, credential.ID, credential.SignCount, p.now()); err != nil {
		return "", err
	}
	text, _, err = p.Sessions.Start(ctx, account.ID, "passkey", agent)
	return text, err
}

// BeginLink starts passkey registration for a one-time link.
func (p Passkeys) BeginLink(ctx context.Context, token string) (cookie string, options []byte, err error) {
	raw, err := domain.ParseLinkToken(token)
	if err != nil {
		return "", nil, ErrInvalidLink
	}
	user, _, ok, err := p.Store.FindLink(ctx, domain.Hash(raw), p.now())
	if err != nil || !ok {
		if err != nil {
			return "", nil, err
		}
		return "", nil, ErrInvalidLink
	}
	account, err := p.account(ctx, user)
	if err != nil {
		return "", nil, ErrInvalidLink
	}
	return p.begin(ctx, "link", &account.ID)
}

// FinishLink spends the link, keeps the passkey and opens a session.
func (p Passkeys) FinishLink(ctx context.Context, cookie, token string, body []byte, agent string) (text string, err error) {
	raw, err := domain.ParseLinkToken(token)
	if err != nil {
		return "", ErrInvalidLink
	}
	now := p.now()
	user, purpose, ok, err := p.Store.FindLink(ctx, domain.Hash(raw), now)
	if err != nil || !ok {
		if err != nil {
			return "", err
		}
		return "", ErrInvalidLink
	}
	ceremony, _, owner, err := p.take(ctx, cookie, "link")
	if err != nil {
		return "", err
	}
	if owner != user {
		return "", ErrInvalidLink
	}
	isSSO := purpose == "sso" || purpose == "sso_stepup"
	if err := checkSSOSignIn(ctx, p.Store, user, isSSO); err != nil {
		return "", err
	}
	account, err := p.account(ctx, user)
	if err != nil {
		return "", ErrInvalidLink
	}
	credential, err := p.Ceremony.FinishRegistration(account, ceremony, body)
	if err != nil {
		return "", err
	}
	if r, ok := p.Store.(interface {
		RedeemLink(ctx context.Context, user domain.UserID, linkHash []byte, credential domain.Credential, name string, now time.Time) error
	}); ok {
		if err := r.RedeemLink(ctx, user, domain.Hash(raw), credential, domain.PasskeyName(credential.AAGUID), now); err != nil {
			return "", err
		}
	} else {
		if err := p.Store.SpendLink(ctx, domain.Hash(raw), now); err != nil {
			return "", ErrInvalidLink
		}
		if _, err := p.Store.InsertPasskey(ctx, user, credential, domain.PasskeyName(credential.AAGUID), now); err != nil {
			return "", err
		}
		if err := p.Store.Join(ctx, user, now); err != nil {
			return "", err
		}
		if err := p.Sessions.EndSessions(ctx, user); err != nil {
			return "", err
		}
	}
	method := "link"
	if isSSO {
		method = "sso"
	}
	text, _, err = p.Sessions.Start(ctx, user, method, agent)
	return text, err
}

// RedeemSSOLink redeems an 'sso' or 'sso_stepup' one-time link without a
// passkey ceremony. For 'sso_stepup', when activeCookie identifies a live
// session belonging to the link's user, that session's signed_in_at is
// refreshed (and sign_in_method set to 'sso') so Fresh() step-up succeeds.
// Otherwise a new 'sso' session is created and its cookie token is returned.
func (p Passkeys) RedeemSSOLink(ctx context.Context, token, activeCookie, agent string) (newCookie string, refreshed bool, err error) {
	raw, err := domain.ParseLinkToken(token)
	if err != nil {
		return "", false, ErrInvalidLink
	}
	now := p.now()
	linkHash := domain.Hash(raw)
	if r, ok := p.Store.(interface {
		RedeemSSOLink(ctx context.Context, linkHash, activeSessionTokenHash, newSessionTokenHash []byte, now time.Time, idle, maxAge time.Duration, agent string) (bool, error)
	}); ok {
		var activeHash []byte
		if activeCookie != "" {
			if tok, parseErr := domain.ParseToken(activeCookie); parseErr == nil {
				activeHash = domain.Hash(tok)
			}
		}
		newToken, newText, err := domain.NewToken()
		if err != nil {
			return "", false, err
		}
		refreshed, err := r.RedeemSSOLink(ctx, linkHash, activeHash, domain.Hash(newToken), now, p.Sessions.Idle, p.Sessions.MaxAge, agent)
		if err != nil {
			return "", false, err
		}
		if refreshed {
			return "", true, nil
		}
		return newText, false, nil
	}
	user, purpose, ok, err := p.Store.FindLink(ctx, linkHash, now)
	if err != nil {
		return "", false, err
	}
	if !ok || (purpose != "sso" && purpose != "sso_stepup") {
		return "", false, ErrInvalidLink
	}
	if err := checkSSOSignIn(ctx, p.Store, user, true); err != nil {
		return "", false, err
	}
	if err := p.Store.SpendLink(ctx, linkHash, now); err != nil {
		return "", false, ErrInvalidLink
	}
	if err := p.Store.Join(ctx, user, now); err != nil {
		return "", false, err
	}
	if purpose == "sso_stepup" && activeCookie != "" {
		if sess, lookupErr := p.Sessions.Lookup(ctx, activeCookie); lookupErr == nil && sess.UserID == user {
			audit.WithActor(ctx, audit.Actor{
				Type:           "user",
				ID:             sess.UserID.String(),
				RoleID:         sess.RoleID,
				CredentialType: "session",
				CredentialID:   sess.ID,
			})
			if err := p.Sessions.Refresh(ctx, sess.ID, "sso", now); err != nil {
				return "", false, err
			}
			return "", true, nil
		}
	}
	text, _, err := p.Sessions.Start(ctx, user, "sso", agent)
	if err != nil {
		return "", false, err
	}
	return text, false, nil
}

func checkSSOSignIn(ctx context.Context, store interface {
	SignInPolicy(ctx context.Context, user domain.UserID) (bool, string, error)
}, user domain.UserID, isSSO bool) error {
	ssoRequired, roleKey, err := store.SignInPolicy(ctx, user)
	if err != nil {
		return err
	}
	if _, err := domain.CheckSSOSignIn(ssoRequired, roleKey, isSSO); err != nil {
		audit.Annotate(ctx, audit.Event{Reason: "sso_required", TargetType: "user", TargetID: user.String()})
		return err
	}
	return nil
}

// BeginAdd starts another passkey. The caller has already checked the sign-in is fresh.
func (p Passkeys) BeginAdd(ctx context.Context, user domain.UserID) (cookie string, options []byte, err error) {
	account, err := p.account(ctx, user)
	if err != nil {
		return "", nil, err
	}
	options, session, err := p.Ceremony.BeginRegistration(account)
	if err != nil {
		return "", nil, err
	}
	cookie, err = p.save(ctx, session, "add", &user)
	return cookie, options, err
}

// FinishAdd stores the passkey and ends the person's other sessions.
func (p Passkeys) FinishAdd(ctx context.Context, user domain.UserID, keep, cookie string, body []byte) error {
	session, _, owner, err := p.take(ctx, cookie, "add")
	if err != nil {
		return err
	}
	if owner != user {
		return ErrChallenge
	}
	account, err := p.account(ctx, user)
	if err != nil {
		return err
	}
	credential, err := p.Ceremony.FinishRegistration(account, session, body)
	if err != nil {
		return err
	}
	if a, ok := p.Store.(interface {
		AddPasskey(ctx context.Context, user domain.UserID, keep string, item domain.Credential, name string, now time.Time) (string, error)
	}); ok {
		if _, err := a.AddPasskey(ctx, user, keep, credential, domain.PasskeyName(credential.AAGUID), p.now()); err != nil {
			return err
		}
		return nil
	}
	if _, err := p.Store.InsertPasskey(ctx, user, credential, domain.PasskeyName(credential.AAGUID), p.now()); err != nil {
		return err
	}
	return p.Sessions.EndOthers(ctx, user, keep)
}

// RemovePasskey removes one passkey and ends other sessions.
func (p Passkeys) RemovePasskey(ctx context.Context, user domain.UserID, keep, id string) ([]byte, error) {
	if r, ok := p.Store.(interface {
		RemovePasskey(ctx context.Context, user domain.UserID, keep, id string) ([]byte, error)
	}); ok {
		return r.RemovePasskey(ctx, user, keep, id)
	}
	credID, err := p.Store.DeletePasskey(ctx, user, id)
	if err != nil {
		return nil, err
	}
	return credID, p.Sessions.EndOthers(ctx, user, keep)
}

// BeginStepUp asks for the passkey again.
func (p Passkeys) BeginStepUp(ctx context.Context, user domain.UserID) (cookie string, options []byte, err error) {
	if _, err := p.Store.Account(ctx, user); err != nil {
		return "", nil, err
	}
	return p.BeginSignIn(ctx)
}

// FinishStepUp refreshes the session that asked.
func (p Passkeys) FinishStepUp(ctx context.Context, user domain.UserID, sessionID, cookie string, body []byte) error {
	if _, err := p.Store.Account(ctx, user); err != nil {
		return err
	}
	session, _, _, err := p.take(ctx, cookie, "sign-in")
	if err != nil {
		audit.Annotate(ctx, audit.Event{Reason: "bad_credential"})
		return err
	}
	account, credential, err := p.Ceremony.FinishDiscoverable(session, body, func(credentialID, userHandle []byte) (domain.Account, error) {
		return p.Store.AccountByCredential(ctx, credentialID, userHandle)
	})
	if err != nil {
		p.annotateAttempt(ctx, err)
		return err
	}
	if account.ID != user {
		audit.Annotate(ctx, audit.Event{Reason: "bad_credential", TargetType: "user", TargetID: user.String()})
		return ErrChallenge
	}
	p.noteClone(credential)
	if err := p.Store.TouchPasskey(ctx, credential.ID, credential.SignCount, p.now()); err != nil {
		return err
	}
	return p.Sessions.Refresh(ctx, sessionID, "passkey", p.now())
}

// Fresh reports whether the session may change a passkey.
func (p Passkeys) Fresh(session Session) bool {
	return domain.Fresh(session.SignedIn, p.now())
}

// IssueLink creates a one-time link and returns its URL text, without the public prefix.
func (p Passkeys) IssueLink(ctx context.Context, user domain.UserID, purpose string) (string, error) {
	life, ok := domain.LinkLifetime(purpose)
	if !ok {
		return "", ErrInvalidLink
	}
	token, text, err := domain.NewToken()
	if err != nil {
		return "", err
	}
	if err := p.Store.InsertLink(ctx, domain.Hash(token), user, purpose, p.now().Add(life)); err != nil {
		return "", err
	}
	return text, nil
}

// LinkURL is the page a person opens. The token stays in the fragment.
func (p Passkeys) LinkURL(token string) string {
	return linkURL(p.PublicURL, token)
}

func linkURL(publicURL, token string) string {
	return publicURL + "/login/link#" + token
}

// Bootstrap creates the first owner and returns the link token text.
func (p Passkeys) Bootstrap(ctx context.Context, email, name string) (string, error) {
	token, text, err := domain.NewToken()
	if err != nil {
		return "", err
	}
	now := p.now()
	if _, err := p.Store.Bootstrap(ctx, email, name, domain.Hash(token), now, now.Add(domain.RecoverLifetime)); err != nil {
		return "", err
	}
	return text, nil
}

func (p Passkeys) begin(ctx context.Context, purpose string, user *domain.UserID) (string, []byte, error) {
	var options, session []byte
	var err error
	if purpose == "sign-in" {
		options, session, err = p.Ceremony.BeginDiscoverable()
	} else {
		account, accountErr := p.account(ctx, *user)
		if accountErr != nil {
			return "", nil, accountErr
		}
		options, session, err = p.Ceremony.BeginRegistration(account)
	}
	if err != nil {
		return "", nil, err
	}
	cookie, err := p.save(ctx, session, purpose, user)
	return cookie, options, err
}

func (p Passkeys) save(ctx context.Context, session []byte, purpose string, user *domain.UserID) (string, error) {
	if err := p.Store.SweepChallenges(ctx, p.now()); err != nil {
		return "", err
	}
	if err := p.Store.SweepLinks(ctx, p.now()); err != nil {
		return "", err
	}
	token, text, err := domain.NewToken()
	if err != nil {
		return "", err
	}
	err = p.Store.SaveChallenge(ctx, domain.Hash(token), session, purpose, user, p.now().Add(domain.ChallengeLifetime))
	return text, err
}

func (p Passkeys) take(ctx context.Context, cookie, purpose string) ([]byte, string, domain.UserID, error) {
	token, err := domain.ParseChallengeID(cookie)
	if err != nil {
		return nil, "", domain.UserID{}, ErrChallenge
	}
	session, got, user, ok, err := p.Store.TakeChallenge(ctx, domain.Hash(token), p.now())
	if err != nil || !ok || got != purpose {
		if err != nil {
			return nil, "", domain.UserID{}, err
		}
		return nil, "", domain.UserID{}, ErrChallenge
	}
	return session, got, user, nil
}

func (p Passkeys) account(ctx context.Context, user domain.UserID) (domain.Account, error) {
	fresh := make([]byte, 32)
	if _, err := rand.Read(fresh); err != nil {
		return domain.Account{}, err
	}
	if _, err := p.Store.Handle(ctx, user, fresh); err != nil {
		return domain.Account{}, err
	}
	return p.Store.Account(ctx, user)
}

// annotateAttempt names why a ceremony failed, for the denied event the
// middleware writes: unknown account or bad credential.
func (p Passkeys) annotateAttempt(ctx context.Context, err error) {
	if errors.Is(err, ErrNotSignedIn) {
		audit.Annotate(ctx, audit.Event{Reason: "unknown_account"})
		return
	}
	audit.Annotate(ctx, audit.Event{Reason: "bad_credential"})
}

func (p Passkeys) noteClone(credential domain.Credential) {
	if credential.CloneWarning && p.Log != nil {
		p.Log.Warn("passkey sign count did not grow")
	}
}
