package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/memory/app"
)

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

func (p personTx) Memory(ctx context.Context) (string, []string, error) {
	var profile string
	err := p.tx.QueryRow(ctx, `
		SELECT body FROM memory.profiles WHERE user_id = $1::uuid`, p.userID).Scan(&profile)
	if errors.Is(err, pgx.ErrNoRows) {
		profile = ""
		err = nil
	}
	if err != nil {
		return "", nil, fmt.Errorf("memory: profile: %w", err)
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
