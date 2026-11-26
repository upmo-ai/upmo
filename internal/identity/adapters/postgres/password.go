package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
)

// PasswordAccount finds the enabled person behind an address through the
// password_account definer, which never returns the stored hash.
func (p Passkeys) PasswordAccount(ctx context.Context, email string) (domain.UserID, string, bool, error) {
	hashes, err := indexEmailHashes(ctx, p.Emails, email)
	if err != nil {
		return domain.UserID{}, "", false, err
	}
	var id domain.UserID
	var name string
	err = p.Pool.QueryRow(ctx, `SELECT id, name FROM identity.password_account($1)`, hashes).Scan(&id, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.UserID{}, "", false, nil
	}
	if err != nil {
		return domain.UserID{}, "", false, err
	}
	return id, name, true, nil
}

func (p Passkeys) PersonByEmail(ctx context.Context, email string) (domain.UserID, bool, bool, error) {
	hashes, err := indexEmailHashes(ctx, p.Emails, email)
	if err != nil {
		return domain.UserID{}, false, false, err
	}
	var id domain.UserID
	var disabled bool
	err = p.Pool.QueryRow(ctx, `
		SELECT id, disabled_at IS NOT NULL
		FROM identity.users
		WHERE email_hash = ANY($1)`, hashes).Scan(&id, &disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.UserID{}, false, false, nil
	}
	if err != nil {
		return domain.UserID{}, false, false, err
	}
	return id, disabled, true, nil
}

func (p Passkeys) Profile(ctx context.Context, user domain.UserID) (string, string, error) {
	var stored string
	var hash []byte
	var name string
	err := p.Pool.QueryRow(ctx, `SELECT email, email_hash, name FROM identity.users WHERE id = $1 AND disabled_at IS NULL`, user).Scan(&stored, &hash, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", app.ErrNotSignedIn
	}
	if err != nil {
		return "", "", err
	}
	email, err := openStoredEmail(p.Emails, stored, hash)
	if err != nil {
		return "", "", err
	}
	return email, name, nil
}

func (p Passkeys) SetPasswordWithKeep(ctx context.Context, user domain.UserID, keep, hash string) error {
	return p.ChangeCredentials(ctx, user, keep,
		func(c *domain.Credentials) error {
			c.SetPassword()
			return nil
		},
		func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `UPDATE identity.users SET password_hash = $2 WHERE id = $1`, user, hash)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return pgx.ErrNoRows
			}
			return audit.Record(ctx, tx, audit.Event{
				Action:   audit.PasswordSet,
				Outcome:  audit.Success,
				TargetID: user.String(),
			})
		},
	)
}

// SetPassword stores a hash and records nothing: the sign-in rehash calls it,
// and a transparent hash upgrade is not a person's password change.
func (p Passkeys) SetPassword(ctx context.Context, user domain.UserID, hash string) error {
	tag, err := p.Pool.Exec(ctx, `UPDATE identity.users SET password_hash = $2 WHERE id = $1`, user, hash)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

func (p Passkeys) RemovePassword(ctx context.Context, user domain.UserID, keep string) error {
	return p.ChangeCredentials(ctx, user, keep,
		func(c *domain.Credentials) error {
			return c.RemovePassword()
		},
		func(tx pgx.Tx) error {
			// The rule above saw a password under the row lock, so the
			// WHERE need not read the column brain may not select.
			tag, err := tx.Exec(ctx, `UPDATE identity.users SET password_hash = NULL WHERE id = $1`, user)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return pgx.ErrNoRows
			}
			return audit.Record(ctx, tx, audit.Event{
				Action:   audit.PasswordRemove,
				Outcome:  audit.Success,
				TargetID: user.String(),
			})
		},
	)
}

func (p Passkeys) ClearPassword(ctx context.Context, user domain.UserID) error {
	return p.RemovePassword(ctx, user, "")
}

// HasPassword asks has_password_of, which answers only for an enabled person.
func (p Passkeys) HasPassword(ctx context.Context, user domain.UserID) (bool, error) {
	var has bool
	err := p.Pool.QueryRow(ctx, `SELECT identity.has_password_of($1)`, user).Scan(&has)
	return has, err
}

func (p Passkeys) ClaimOffer(ctx context.Context, user domain.UserID, now time.Time) (bool, error) {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var offered *time.Time
	if err := tx.QueryRow(ctx, `SELECT passkey_offered_at FROM identity.users WHERE id = $1 FOR NO KEY UPDATE`, user).Scan(&offered); err != nil {
		return false, err
	}
	if !domain.OfferPasskey(offered, now) {
		return false, tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `UPDATE identity.users SET passkey_offered_at = $2 WHERE id = $1`, user, now); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
