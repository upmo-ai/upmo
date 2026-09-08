package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/memory/domain"
)

var (
	// ErrProfile is a profile longer than the cache prefix can hold.
	ErrProfile = errors.New("memory: profile too long")
	// ErrClaim is an empty claim.
	ErrClaim = errors.New("memory: empty claim")
	// ErrDuplicate is an active claim with the same hash.
	ErrDuplicate = errors.New("memory: duplicate claim")
	// ErrGone is a fact that is missing or already forgotten.
	ErrGone = errors.New("memory: fact not found")
)

// Page is what the personalisation screen reads.
type Page struct {
	Profile   string
	ProfileAt time.Time
	Facts     []FactRow
}

// FactRow is one active claim the person can edit or forget.
type FactRow struct {
	ID        string
	Claim     string
	Trust     string
	Confirmed time.Time
}

// Editor runs one transaction as the signed-in person.
type Editor interface {
	InPerson(ctx context.Context, userID string, fn func(Person) error) error
}

// Person is that transaction.
type Person interface {
	WriteProfile(ctx context.Context, body string, at time.Time) error
	ReadPage(ctx context.Context) (Page, error)
	InsertUser(ctx context.Context, claim string, hash [32]byte, at time.Time) (string, error)
	Tombstone(ctx context.Context, id string, at time.Time) error
	Supersede(ctx context.Context, id, claim string, hash [32]byte, at time.Time) (string, error)
}

// SaveProfile stores the person's profile. The body is what the cache prefix
// will hold, so it cannot exceed that budget.
func SaveProfile(ctx context.Context, editor Editor, userID, body string, at time.Time) error {
	if _, err := uuid.Parse(userID); err != nil {
		return err
	}
	body = strings.TrimSpace(body)
	if len(body) > ProfileTokens*4 {
		return ErrProfile
	}
	body = domain.Redact(body)
	return editor.InPerson(ctx, userID, func(p Person) error {
		return p.WriteProfile(ctx, body, at)
	})
}

// LoadPage reads the profile and the active facts.
func LoadPage(ctx context.Context, editor Editor, userID string) (Page, error) {
	if _, err := uuid.Parse(userID); err != nil {
		return Page{}, err
	}
	var page Page
	err := editor.InPerson(ctx, userID, func(p Person) error {
		var err error
		page, err = p.ReadPage(ctx)
		return err
	})
	return page, err
}

// AddFact stores a claim the person typed. Trust is user. A matching active
// claim is rejected rather than merged.
func AddFact(ctx context.Context, editor Editor, userID, claim string, at time.Time) (string, error) {
	claim, hash, err := preparedClaim(userID, claim)
	if err != nil {
		return "", err
	}
	var id string
	err = editor.InPerson(ctx, userID, func(p Person) error {
		var err error
		id, err = p.InsertUser(ctx, claim, hash, at)
		return err
	})
	return id, err
}

// Forget marks one fact forgotten. The row stays.
func Forget(ctx context.Context, editor Editor, userID, id string, at time.Time) error {
	if _, err := uuid.Parse(userID); err != nil {
		return err
	}
	if _, err := uuid.Parse(id); err != nil {
		return err
	}
	return editor.InPerson(ctx, userID, func(p Person) error {
		return p.Tombstone(ctx, id, at)
	})
}

// EditFact inserts the new wording and points the old row at it. Claim text
// is not updated in place.
func EditFact(ctx context.Context, editor Editor, userID, id, claim string, at time.Time) (string, error) {
	if _, err := uuid.Parse(id); err != nil {
		return "", err
	}
	claim, hash, err := preparedClaim(userID, claim)
	if err != nil {
		return "", err
	}
	var next string
	err = editor.InPerson(ctx, userID, func(p Person) error {
		var err error
		next, err = p.Supersede(ctx, id, claim, hash, at)
		return err
	})
	return next, err
}

func preparedClaim(userID, claim string) (string, [32]byte, error) {
	if _, err := uuid.Parse(userID); err != nil {
		return "", [32]byte{}, err
	}
	claim = strings.TrimSpace(claim)
	if claim == "" {
		return "", [32]byte{}, ErrClaim
	}
	claim = domain.Redact(claim)
	return claim, domain.ClaimHash(claim), nil
}
