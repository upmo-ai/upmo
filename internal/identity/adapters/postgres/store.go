package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
)

// Store is the identity schema database adapter.
type Store struct {
	Pool *pgxpool.Pool
}

// ChangeCredentials executes one credentials change inside a transaction:
// locks the person row, loads the sign-in counts, evaluates the domain rule,
// applies the change, and ends other sessions.
func (p Passkeys) ChangeCredentials(ctx context.Context, user domain.UserID, keep string, rule func(*domain.Credentials) error, mutate func(tx pgx.Tx) error) error {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var hasPassword bool
	if err := tx.QueryRow(ctx, `SELECT password_hash IS NOT NULL FROM identity.users WHERE id = $1 AND disabled_at IS NULL FOR NO KEY UPDATE`, user).Scan(&hasPassword); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrNotSignedIn
		}
		return err
	}
	var passkeysCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM identity.passkeys WHERE user_id = $1`, user).Scan(&passkeysCount); err != nil {
		return err
	}
	creds := domain.Credentials{
		Passkeys:    passkeysCount,
		HasPassword: hasPassword,
	}
	if rule != nil {
		if err := rule(&creds); err != nil {
			return err
		}
	}
	if mutate != nil {
		if err := mutate(tx); err != nil {
			return err
		}
	}
	if keep != "" {
		if _, err := tx.Exec(ctx, `DELETE FROM identity.sessions WHERE user_id = $1 AND id <> $2`, user, keep); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// AddPasskey adds a passkey through ChangeCredentials and ends other sessions.
func (p Passkeys) AddPasskey(ctx context.Context, user domain.UserID, keep string, item domain.Credential, name string, now time.Time) (string, error) {
	var id string
	err := p.ChangeCredentials(ctx, user, keep,
		func(c *domain.Credentials) error {
			c.AddPasskey()
			return nil
		},
		func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `
				INSERT INTO identity.passkeys (
				  user_id, credential_id, public_key, sign_count, aaguid, transports,
				  backup_eligible, backup_state, attestation, name, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
				RETURNING id::text`,
				user, item.ID, item.PublicKey, item.SignCount, item.AAGUID, item.Transports,
				item.BackupEligible, item.BackupState, item.Attestation, name, now).Scan(&id)
		},
	)
	return id, err
}

// RemovePasskey removes a passkey through ChangeCredentials and ends other sessions.
func (p Passkeys) RemovePasskey(ctx context.Context, user domain.UserID, keep, id string) ([]byte, error) {
	var credentialID []byte
	err := p.ChangeCredentials(ctx, user, keep,
		func(c *domain.Credentials) error {
			return c.RemovePasskey()
		},
		func(tx pgx.Tx) error {
			err := tx.QueryRow(ctx, `
				DELETE FROM identity.passkeys WHERE id = $1 AND user_id = $2 RETURNING credential_id`, id, user).Scan(&credentialID)
			if errors.Is(err, pgx.ErrNoRows) {
				return app.ErrNotSignedIn
			}
			return err
		},
	)
	return credentialID, err
}

// RedeemLink spends a link, records the passkey, marks the user joined, and ends sessions.
func (p Passkeys) RedeemLink(ctx context.Context, user domain.UserID, linkHash []byte, credential domain.Credential, name string, now time.Time) error {
	return p.ChangeCredentials(ctx, user, "",
		func(c *domain.Credentials) error {
			c.AddPasskey()
			return nil
		},
		func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `
				UPDATE identity.sign_in_links SET used_at = $2
				WHERE token_hash = $1 AND used_at IS NULL AND expires_at > $2`, linkHash, now)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return app.ErrInvalidLink
			}
			_, err = tx.Exec(ctx, `
				INSERT INTO identity.passkeys (
				  user_id, credential_id, public_key, sign_count, aaguid, transports,
				  backup_eligible, backup_state, attestation, name, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
				user, credential.ID, credential.PublicKey, credential.SignCount, credential.AAGUID, credential.Transports,
				credential.BackupEligible, credential.BackupState, credential.Attestation, name, now)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				UPDATE identity.users SET joined_at = $2
				WHERE id = $1 AND joined_at IS NULL`, user, now); err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `DELETE FROM identity.sessions WHERE user_id = $1`, user)
			return err
		},
	)
}
