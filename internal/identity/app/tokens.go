package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/upmo-ai/upmo/internal/identity/domain"
)

// TokenRecord is one personal token as the API lists it.
type TokenRecord struct {
	ID       string
	Name     string
	Scope    []string
	Created  time.Time
	Expires  time.Time
	LastUsed *time.Time
}

// Held is the role set a person holds now.
type Held struct {
	UserID      domain.UserID
	RoleID      string
	Disabled    bool
	Joined      bool
	Permissions []domain.Permission
}

// Bearer is a token that just authenticated.
type Bearer struct {
	ID     string
	UserID domain.UserID
	RoleID string
	Name   string
	Scope  []string
}

// TokenStore is the personal token table.
type TokenStore interface {
	HeldFor(ctx context.Context, user domain.UserID) (Held, error)
	SaveToken(ctx context.Context, actorTokenHash []byte, user domain.UserID, name string, hash []byte, scope []string, created, expires time.Time) (string, error)
	ListTokens(ctx context.Context, user domain.UserID) ([]TokenRecord, error)
	DeleteToken(ctx context.Context, user domain.UserID, id string) error
	FindToken(ctx context.Context, hash []byte, now time.Time) (TokenRecord, Held, error)
	TouchToken(ctx context.Context, id string, at time.Time) error
}

// APITokens is creating, listing, revoking and presenting a personal token.
type APITokens struct {
	Store TokenStore
	Now   func() time.Time
	Log   *slog.Logger
}

func (t APITokens) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

// Create stores a token and returns the secret once.
func (t APITokens) Create(ctx context.Context, session Session, name string, scope []string, days int, expires string) (string, string, error) {
	if !domain.Fresh(session.SignedIn, t.now()) {
		return "", "", ErrStale
	}
	name, err := domain.ParseTokenName(name)
	if err != nil {
		return "", "", err
	}
	created := t.now()
	var expiresAt time.Time
	if expires != "" {
		expiresAt, err = domain.TokenExpiryDate(expires, created)
	} else {
		days, err = domain.TokenDays(days)
		if err == nil {
			expiresAt = created.AddDate(0, 0, days)
		}
	}
	if err != nil {
		return "", "", err
	}
	held, err := t.Store.HeldFor(ctx, session.UserID)
	if err != nil {
		return "", "", err
	}
	parsed, err := domain.TokenScope(held.Permissions, scope)
	if err != nil {
		t.note("token refused", session.UserID, err)
		return "", "", err
	}
	names := make([]string, len(parsed))
	for i, permission := range parsed {
		names[i] = string(permission)
	}
	secret, hash, err := domain.NewAPITokenSecret()
	if err != nil {
		return "", "", err
	}
	id, err := t.Store.SaveToken(ctx, session.TokenHash, session.UserID, name, hash, names, created, expiresAt)
	if err != nil {
		return "", "", err
	}
	return id, secret, nil
}

// List returns the person's tokens.
func (t APITokens) List(ctx context.Context, user domain.UserID) ([]TokenRecord, error) {
	rows, err := t.Store.ListTokens(ctx, user)
	if err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []TokenRecord{}
	}
	return rows, nil
}

// Revoke removes one of the person's tokens.
func (t APITokens) Revoke(ctx context.Context, user domain.UserID, id string) error {
	if err := t.Store.DeleteToken(ctx, user, id); err != nil {
		t.note("token revoke refused", user, err)
		return err
	}
	return nil
}

// Authenticate checks a bearer secret and narrows it to the role held now.
func (t APITokens) Authenticate(ctx context.Context, secret string) (Bearer, error) {
	token, err := domain.ParseAPITokenSecret(secret)
	if err != nil {
		return Bearer{}, domain.ErrToken
	}
	now := t.now()
	row, held, err := t.Store.FindToken(ctx, domain.Hash(token), now)
	if err != nil {
		return Bearer{}, err
	}
	subject := domain.Subject{
		Kind:        domain.KindToken,
		UserID:      held.UserID,
		Joined:      held.Joined,
		Disabled:    held.Disabled,
		Permissions: held.Permissions,
	}
	var narrowed []string
	for _, name := range row.Scope {
		permission, err := domain.ParsePermission(name)
		if err != nil {
			continue
		}
		subject.Policy = []domain.Permission{permission}
		if domain.Permitted(subject, permission).Allow {
			narrowed = append(narrowed, name)
		}
	}
	if narrowed == nil {
		narrowed = []string{}
	}
	if row.LastUsed == nil || domain.TouchDue(*row.LastUsed, now) {
		if err := t.Store.TouchToken(ctx, row.ID, now); err != nil {
			return Bearer{}, err
		}
	}
	return Bearer{ID: row.ID, UserID: held.UserID, RoleID: held.RoleID, Name: row.Name, Scope: narrowed}, nil
}

func (t APITokens) note(message string, user domain.UserID, err error) {
	if t.Log != nil && err != nil {
		t.Log.Warn(message, "actor", user.String(), "err", err.Error())
	}
}
