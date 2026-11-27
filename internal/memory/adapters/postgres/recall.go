package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/memory/app"
)

// Profile reads one person's profile body under row security.
func (f Facts) Profile(ctx context.Context, userID string) (string, error) {
	var profile string
	err := f.inPersonTx(ctx, userID, func(p personTx) error {
		var err error
		profile, err = p.Profile(ctx)
		return err
	})
	return profile, err
}

// Load reads the profile and the active claims for one person.
func (f Facts) Load(ctx context.Context, userID string) (app.Memory, error) {
	var mem app.Memory
	err := f.InTx(ctx, userID, func(tx app.Tx) error {
		reader, ok := tx.(memoryReader)
		if !ok {
			return fmt.Errorf("memory: reader")
		}
		profile, claims, err := reader.Memory(ctx)
		if err != nil {
			return err
		}
		mem.Profile = profile
		mem.Claims = claims
		return nil
	})
	return mem, err
}

type memoryReader interface {
	Memory(ctx context.Context) (string, []string, error)
}

func (p personTx) Profile(ctx context.Context) (string, error) {
	var profile string
	err := p.tx.QueryRow(ctx, `
		SELECT body FROM memory.profiles WHERE user_id = $1::uuid`, p.userID).Scan(&profile)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("memory: profile: %w", err)
	}
	return profile, nil
}

func (p personTx) Memory(ctx context.Context) (string, []string, error) {
	profile, err := p.Profile(ctx)
	if err != nil {
		return "", nil, err
	}
	rows, err := p.tx.Query(ctx, `
		SELECT claim FROM memory.facts
		WHERE user_id = $1::uuid AND invalid_at IS NULL
		ORDER BY last_confirmed_at DESC`, p.userID)
	if err != nil {
		return "", nil, fmt.Errorf("memory: claims: %w", err)
	}
	defer rows.Close()
	var claims []string
	for rows.Next() {
		var claim string
		if err := rows.Scan(&claim); err != nil {
			return "", nil, fmt.Errorf("memory: claims: %w", err)
		}
		claims = append(claims, claim)
	}
	if err := rows.Err(); err != nil {
		return "", nil, fmt.Errorf("memory: claims: %w", err)
	}
	return profile, claims, nil
}
