package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/upmo-ai/upmo/internal/memory/app"
)

// InPerson runs one transaction as the signed-in person.
func (f Facts) InPerson(ctx context.Context, userID string, fn func(app.Person) error) error {
	return f.InTx(ctx, userID, func(tx app.Tx) error {
		person, ok := tx.(app.Person)
		if !ok {
			return fmt.Errorf("memory: person")
		}
		return fn(person)
	})
}

var _ app.Person = personTx{}

func (p personTx) WriteProfile(ctx context.Context, body string, at time.Time) error {
	_, err := p.tx.Exec(ctx, `
		INSERT INTO memory.profiles (user_id, body, updated_at)
		VALUES ($1::uuid, $2, $3)
		ON CONFLICT (user_id) DO UPDATE
		SET body = EXCLUDED.body, updated_at = EXCLUDED.updated_at`,
		p.userID, body, at)
	if err != nil {
		return fmt.Errorf("memory: profile: %w", err)
	}
	return nil
}

func (p personTx) ReadPage(ctx context.Context) (app.Page, error) {
	var page app.Page
	err := p.tx.QueryRow(ctx, `
		SELECT body, updated_at FROM memory.profiles WHERE user_id = $1::uuid`, p.userID).
		Scan(&page.Profile, &page.ProfileAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = nil
	}
	if err != nil {
		return app.Page{}, fmt.Errorf("memory: profile: %w", err)
	}
	rows, err := p.tx.Query(ctx, `
		SELECT id::text, claim, trust, last_confirmed_at
		FROM memory.facts
		WHERE user_id = $1::uuid AND invalid_at IS NULL
		ORDER BY last_confirmed_at DESC, id`, p.userID)
	if err != nil {
		return app.Page{}, fmt.Errorf("memory: facts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var row app.FactRow
		if err := rows.Scan(&row.ID, &row.Claim, &row.Trust, &row.Confirmed); err != nil {
			return app.Page{}, fmt.Errorf("memory: facts: %w", err)
		}
		page.Facts = append(page.Facts, row)
	}
	if err := rows.Err(); err != nil {
		return app.Page{}, fmt.Errorf("memory: facts: %w", err)
	}
	return page, nil
}

func (p personTx) InsertUser(ctx context.Context, claim string, hash [32]byte, at time.Time) (string, error) {
	var id string
	err := p.tx.QueryRow(ctx, `
		INSERT INTO memory.facts (
			user_id, trust, claim, claim_hash,
			valid_from, last_confirmed_at,
			source_kind, source_id, source_seq, created_at
		) VALUES (
			$1::uuid, 'user', $2, $3,
			$4, $4,
			'person', $1::uuid, 0, $4
		) RETURNING id::text`,
		p.userID, claim, hash[:], at).Scan(&id)
	if err != nil {
		if dup(err) {
			return "", app.ErrDuplicate
		}
		return "", fmt.Errorf("memory: insert fact: %w", err)
	}
	return id, nil
}

func (p personTx) Tombstone(ctx context.Context, id string, at time.Time) error {
	tag, err := p.tx.Exec(ctx, `
		UPDATE memory.facts
		SET invalid_at = $3
		WHERE user_id = $1::uuid AND id = $2::uuid AND invalid_at IS NULL`,
		p.userID, id, at)
	if err != nil {
		return fmt.Errorf("memory: forget: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return app.ErrGone
	}
	return nil
}

func (p personTx) Supersede(ctx context.Context, id, claim string, hash [32]byte, at time.Time) (string, error) {
	var current []byte
	err := p.tx.QueryRow(ctx, `
		SELECT claim_hash FROM memory.facts
		WHERE user_id = $1::uuid AND id = $2::uuid AND invalid_at IS NULL`,
		p.userID, id).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", app.ErrGone
	}
	if err != nil {
		return "", fmt.Errorf("memory: edit: %w", err)
	}
	if len(current) == len(hash) && string(current) == string(hash[:]) {
		return id, nil
	}
	next, err := p.InsertUser(ctx, claim, hash, at)
	if err != nil {
		return "", err
	}
	tag, err := p.tx.Exec(ctx, `
		UPDATE memory.facts
		SET invalid_at = $3, superseded_by = $4::uuid
		WHERE user_id = $1::uuid AND id = $2::uuid AND invalid_at IS NULL`,
		p.userID, id, at, next)
	if err != nil {
		return "", fmt.Errorf("memory: edit: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return "", app.ErrGone
	}
	return next, nil
}

func dup(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
