// Package app is the identity use cases. It does no HTTP and no SQL.
package app

import (
	"context"
	"errors"
	"time"

	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
)

// ErrNotFound is an unknown session. ErrEnded is a session that has timed out.
// ErrMalformed is a cookie that is not a token. Callers show one answer for all three.
var (
	ErrNotFound    = errors.New("identity: session not found")
	ErrEnded       = errors.New("identity: session ended")
	ErrMalformed   = errors.New("identity: malformed session")
	ErrNotSignedIn = errors.New("identity: unauthorized")
)

// Session is a live server-side session.
type Session struct {
	ID        string
	UserID    domain.UserID
	Expires   time.Time
	Method    string
	SignedIn  time.Time
	RoleID    string
	TokenHash []byte
}

// Row is a stored session, including one that has ended.
type Row struct {
	ID       string
	UserID   domain.UserID
	Created  time.Time
	LastSeen time.Time
	Expires  time.Time
	Method   string
	SignedIn time.Time
	RoleID   string
}

// Store is the session table.
type Store interface {
	Insert(ctx context.Context, user domain.UserID, hash []byte, created, expires time.Time, method, agent string) (string, error)
	// Find returns the row for hash. found is false when the hash is unknown
	// or the person is disabled.
	Find(ctx context.Context, hash []byte) (Row, bool, error)
	Touch(ctx context.Context, id string, seen time.Time) error
	Delete(ctx context.Context, id string) error
	DeleteUser(ctx context.Context, user domain.UserID) error
	DeleteOthers(ctx context.Context, user domain.UserID, keep string) error
	Refresh(ctx context.Context, id, method string, at time.Time) error
	DeleteEnded(ctx context.Context, user domain.UserID, now time.Time, idle time.Duration) error
	List(ctx context.Context, user domain.UserID) ([]Device, error)
}

// Device is one session without its token.
type Device struct {
	ID       string
	Method   string
	SignedIn time.Time
	LastSeen time.Time
	Agent    string
}

// Service issues and checks sessions. Now is the clock; tests set it.
type Service struct {
	Store  Store
	Idle   time.Duration
	MaxAge time.Duration
	Now    func() time.Time
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Start opens a new session for user and removes that person's ended ones.
func (s Service) Start(ctx context.Context, user domain.UserID, method, agent string) (text string, session Session, err error) {
	token, text, err := domain.NewToken()
	if err != nil {
		return "", Session{}, err
	}
	now := s.now()
	if err := s.Store.DeleteEnded(ctx, user, now, s.Idle); err != nil {
		return "", Session{}, err
	}
	hash := domain.Hash(token)
	id, err := s.Store.Insert(ctx, user, hash, now, now.Add(s.MaxAge), method, agent)
	if err != nil {
		return "", Session{}, err
	}
	return text, Session{ID: id, UserID: user, Expires: now.Add(s.MaxAge), Method: method, SignedIn: now, TokenHash: hash}, nil
}

// Lookup resolves a cookie value. An ended session is deleted.
func (s Service) Lookup(ctx context.Context, text string) (Session, error) {
	token, err := domain.ParseToken(text)
	if err != nil {
		return Session{}, ErrMalformed
	}
	hash := domain.Hash(token)
	row, found, err := s.Store.Find(ctx, hash)
	if err != nil {
		return Session{}, err
	}
	if !found {
		return Session{}, ErrNotFound
	}
	now := s.now()
	if domain.Ended(row.LastSeen, row.Expires, now, s.Idle) {
		if err := s.Store.Delete(ctx, row.ID); err != nil {
			return Session{}, err
		}
		return Session{}, ErrEnded
	}
	if domain.TouchDue(row.LastSeen, now) {
		if err := s.Store.Touch(ctx, row.ID, now); err != nil {
			return Session{}, err
		}
	}
	return Session{ID: row.ID, UserID: row.UserID, Expires: row.Expires, Method: row.Method, SignedIn: row.SignedIn, RoleID: row.RoleID, TokenHash: hash}, nil
}

// EndOthers ends every session for user except keep.
func (s Service) EndOthers(ctx context.Context, user domain.UserID, keep string) error {
	return s.Store.DeleteOthers(ctx, user, keep)
}

// Refresh marks a session as freshly signed in.
func (s Service) Refresh(ctx context.Context, id, method string, at time.Time) error {
	return s.Store.Refresh(ctx, id, method, at)
}

// Devices lists the person's sessions.
func (s Service) Devices(ctx context.Context, user domain.UserID) ([]Device, error) {
	return s.Store.List(ctx, user)
}

// Revoker is a store that can end one session and record its event in one
// transaction.
type Revoker interface {
	RevokeAudited(ctx context.Context, id string) error
}

// End ends one session. A store that can transact records the revoke event
// with it.
func (s Service) End(ctx context.Context, id string) error {
	if revoker, ok := s.Store.(Revoker); ok {
		return revoker.RevokeAudited(ctx, id)
	}
	return s.Store.Delete(ctx, id)
}

// Ender is a store that can end a session and record its event in one
// transaction.
type Ender interface {
	EndAudited(ctx context.Context, id string, event audit.Event) error
}

// EndAudited ends one session and records the event in the same transaction,
// so a change without its event never commits. A store that cannot transact
// falls back to a plain delete.
func (s Service) EndAudited(ctx context.Context, id string, event audit.Event) error {
	if ender, ok := s.Store.(Ender); ok {
		return ender.EndAudited(ctx, id, event)
	}
	return s.Store.Delete(ctx, id)
}

// AllEnder is a store that can end every session of one person and record the
// event in one transaction.
type AllEnder interface {
	EndAllAudited(ctx context.Context, user domain.UserID) error
}

// EndSessions ends every session for user. Credential changes call this.
func (s Service) EndSessions(ctx context.Context, user domain.UserID) error {
	if ender, ok := s.Store.(AllEnder); ok {
		return ender.EndAllAudited(ctx, user)
	}
	return s.Store.DeleteUser(ctx, user)
}
