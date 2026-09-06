package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/memory/app"
)

// Facts is the personal-fact store. One InTx call is one transaction.
type Facts struct {
	Pool *pgxpool.Pool
}

type personTx struct {
	tx     pgx.Tx
	userID string
}

func (f Facts) InTx(ctx context.Context, userID string, fn func(app.Tx) error) error {
	return f.inPersonTx(ctx, userID, func(tx personTx) error {
		return fn(tx)
	})
}

func (f Facts) inPersonTx(ctx context.Context, userID string, fn func(personTx) error) error {
	if f.Pool == nil {
		return fmt.Errorf("memory: no database")
	}
	tx, err := f.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('upmo.user_id', $1, true)`, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0::bigint))`, userID); err != nil {
		return err
	}
	if err := fn(personTx{tx: tx, userID: userID}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p personTx) Already(ctx context.Context, key app.Key) (bool, error) {
	var done bool
	err := p.tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM memory.extracts
			WHERE user_id = $1::uuid AND source_kind = $2 AND source_id = $3::uuid AND source_seq = $4
		)`, p.userID, key.Kind, key.ID, key.Seq).Scan(&done)
	if err != nil {
		return false, fmt.Errorf("memory: extract seen: %w", err)
	}
	return done, nil
}

func (p personTx) Record(ctx context.Context, key app.Key, at time.Time) error {
	_, err := p.tx.Exec(ctx, `
		INSERT INTO memory.extracts (user_id, source_kind, source_id, source_seq, created_at)
		VALUES ($1::uuid, $2, $3::uuid, $4, $5)`,
		p.userID, key.Kind, key.ID, key.Seq, at)
	if err != nil {
		return fmt.Errorf("memory: extract record: %w", err)
	}
	return nil
}

func (p personTx) Hashes(ctx context.Context) (active, forgotten [][32]byte, err error) {
	rows, err := p.tx.Query(ctx, `
		SELECT claim_hash, invalid_at IS NOT NULL
		FROM memory.facts
		WHERE user_id = $1::uuid`, p.userID)
	if err != nil {
		return nil, nil, fmt.Errorf("memory: hashes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var gone bool
		if err := rows.Scan(&raw, &gone); err != nil {
			return nil, nil, fmt.Errorf("memory: hashes: %w", err)
		}
		if len(raw) != 32 {
			return nil, nil, fmt.Errorf("memory: claim hash length %d", len(raw))
		}
		var hash [32]byte
		copy(hash[:], raw)
		if gone {
			forgotten = append(forgotten, hash)
		} else {
			active = append(active, hash)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("memory: hashes: %w", err)
	}
	return active, forgotten, nil
}

func (p personTx) Confirm(ctx context.Context, hash [32]byte, at time.Time) error {
	_, err := p.tx.Exec(ctx, `
		UPDATE memory.facts
		SET last_confirmed_at = $3
		WHERE user_id = $1::uuid AND claim_hash = $2 AND invalid_at IS NULL`,
		p.userID, hash[:], at)
	if err != nil {
		return fmt.Errorf("memory: confirm: %w", err)
	}
	return nil
}

func (p personTx) ConfirmID(ctx context.Context, id string, at time.Time) error {
	_, err := p.tx.Exec(ctx, `
		UPDATE memory.facts
		SET last_confirmed_at = $3
		WHERE user_id = $1::uuid AND id = $2::uuid AND invalid_at IS NULL`,
		p.userID, id, at)
	if err != nil {
		return fmt.Errorf("memory: confirm: %w", err)
	}
	return nil
}

func (p personTx) Near(ctx context.Context, vector []float32) (string, string, error) {
	return ScanNear(ctx, p.tx, p.userID, vector)
}

func (p personTx) Add(ctx context.Context, fact app.Fact) error {
	_, err := p.tx.Exec(ctx, `
		INSERT INTO memory.facts (
			user_id, trust, claim, claim_hash,
			valid_from, last_confirmed_at,
			embedding, embedding_model,
			source_kind, source_id, source_seq, created_at
		) VALUES (
			$1::uuid, 'tool', $2, $3,
			$4, $4,
			$5::halfvec(1024), $6,
			$7, $8::uuid, $9, $4
		)`,
		p.userID, fact.Claim, fact.Hash[:], fact.At,
		formatHalfvec(fact.Vector), fact.Model,
		fact.Kind, fact.SourceID, fact.Seq)
	if err != nil {
		return fmt.Errorf("memory: insert fact: %w", err)
	}
	return nil
}
