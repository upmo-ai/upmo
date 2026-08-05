// Package app is the identity use cases. It does no HTTP and no SQL.
package app

import (
	"context"
	"errors"
	"time"

	"github.com/upmo-ai/upmo/internal/identity/domain"
)

// ErrNotFound is an unknown session. ErrEnded is a session that has timed out.
// ErrMalformed is a cookie that is not a token. Callers show one answer for all three.
var (
	ErrNotFound  = errors.New("identity: session not found")
	ErrEnded     = errors.New("identity: session ended")
	ErrMalformed = errors.New("identity: malformed session")
)

// Session is a live server-side session.
type Session struct {
	ID      string
	UserID  domain.UserID
	Expires time.Time
}

// Row is a stored session, including one that has ended.
type Row struct {
	ID       string
	UserID   domain.UserID
	Created  time.Time
	LastSeen time.Time
	Expires  time.Time
}

// Store is the session table.
type Store interface {
	Insert(ctx context.Context, user domain.UserID, hash []byte, created, expires time.Time) (string, error)
	// Find returns the row for hash. found is false when the hash is unknown
	// or the person is disabled.
	Find(ctx context.Context, hash []byte) (Row, bool, error)
	Touch(ctx context.Context, id string, seen time.Time) error
	Delete(ctx context.Context, id string) error
	DeleteUser(ctx context.Context, user domain.UserID) error
	DeleteEnded(ctx context.Context, user domain.UserID, now time.Time, idle time.Duration) error
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
func (s Service) Start(ctx context.Context, user domain.UserID) (text string, session Session, err error) {
	token, text, err := domain.NewToken()
	if err != nil {
		return "", Session{}, err
	}
	now := s.now()
	if err := s.Store.DeleteEnded(ctx, user, now, s.Idle); err != nil {
		return "", Session{}, err
	}
	id, err := s.Store.Insert(ctx, user, domain.Hash(token), now, now.Add(s.MaxAge))
	if err != nil {
		return "", Session{}, err
	}
	return text, Session{ID: id, UserID: user, Expires: now.Add(s.MaxAge)}, nil
}

// Lookup resolves a cookie value. An ended session is deleted.
func (s Service) Lookup(ctx context.Context, text string) (Session, error) {
	token, err := domain.ParseToken(text)
	if err != nil {
		return Session{}, ErrMalformed
	}
	row, found, err := s.Store.Find(ctx, domain.Hash(token))
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
	return Session{ID: row.ID, UserID: row.UserID, Expires: row.Expires}, nil
}

// End ends one session.
func (s Service) End(ctx context.Context, id string) error {
	return s.Store.Delete(ctx, id)
}

// EndSessions ends every session for user. Credential changes call this.
func (s Service) EndSessions(ctx context.Context, user domain.UserID) error {
	return s.Store.DeleteUser(ctx, user)
}
