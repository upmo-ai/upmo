package postgres

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
)

// Store is the identity schema database adapter.
type Store struct {
	Pool *pgxpool.Pool
}

// indexEmailHashes returns the index hashes of address under every loaded key,
// current key first. Without a ready sealer it fails before any lookup runs.
func indexEmailHashes(ctx context.Context, emails app.EmailSealer, address string) ([][]byte, error) {
	if emails == nil || !emails.Ready() {
		return nil, app.ErrNoEmailSealer
	}
	return emails.IndexHashes(ctx, address)
}

// openStoredEmail returns the address of stored, which was sealed for hash. A
// value that does not open is ErrStoredEmail; the text never carries the value.
func openStoredEmail(emails app.EmailSealer, stored string, hash []byte) (string, error) {
	if emails == nil || !emails.Ready() {
		return "", app.ErrNoEmailSealer
	}
	return emails.Open(stored, hash)
}

// sealEmail returns the stored form of address and its index hashes under
// every loaded key, the current key's hash first, without duplicates.
func sealEmail(ctx context.Context, emails app.EmailSealer, address string) (stored string, hashes [][]byte, hashKeyID string, err error) {
	if emails == nil || !emails.Ready() {
		return "", nil, "", app.ErrNoEmailSealer
	}
	stored, hash, hashKeyID, err := emails.Seal(ctx, address)
	if err != nil {
		return "", nil, "", err
	}
	all, err := emails.IndexHashes(ctx, address)
	if err != nil {
		return "", nil, "", err
	}
	hashes = [][]byte{hash}
	for _, other := range all {
		if !bytes.Equal(other, hash) {
			hashes = append(hashes, other)
		}
	}
	return stored, hashes, hashKeyID, nil
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
	if err := tx.QueryRow(ctx, `SELECT true FROM identity.users WHERE id = $1 AND disabled_at IS NULL FOR NO KEY UPDATE`, user).Scan(&hasPassword); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return app.ErrNotSignedIn
		}
		return err
	}
	// Under the row lock; brain reads only whether a password is set.
	if err := tx.QueryRow(ctx, `SELECT identity.has_password_of($1)`, user).Scan(&hasPassword); err != nil {
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
// The passkey.add event is recorded in the same transaction.
func (p Passkeys) AddPasskey(ctx context.Context, user domain.UserID, keep string, item domain.Credential, name string, now time.Time) (string, error) {
	var id string
	err := p.ChangeCredentials(ctx, user, keep,
		func(c *domain.Credentials) error {
			c.AddPasskey()
			return nil
		},
		func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `
				INSERT INTO identity.passkeys (
				  user_id, credential_id, public_key, sign_count, aaguid, transports,
				  backup_eligible, backup_state, attestation, name, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
				RETURNING id::text`,
				user, item.ID, item.PublicKey, item.SignCount, item.AAGUID, item.Transports,
				item.BackupEligible, item.BackupState, item.Attestation, name, now).Scan(&id); err != nil {
				return err
			}
			return audit.Record(ctx, tx, audit.Event{
				Action:   audit.PasskeyAdd,
				Outcome:  audit.Success,
				TargetID: id,
			})
		},
	)
	return id, err
}

// RemovePasskey removes a passkey through ChangeCredentials and ends other
// sessions. The passkey.remove event is recorded in the same transaction.
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
			if err != nil {
				return err
			}
			return audit.Record(ctx, tx, audit.Event{
				Action:   audit.PasskeyRemove,
				Outcome:  audit.Success,
				TargetID: id,
			})
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
			var spent bool
			if err := tx.QueryRow(ctx, `SELECT identity.link_spend($1, $2)`, linkHash, now).Scan(&spent); err != nil {
				return err
			}
			if !spent {
				return app.ErrInvalidLink
			}
			_, err := tx.Exec(ctx, `
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

// RedeemSSOLink atomically verifies and spends an 'sso' or 'sso_stepup' link,
// marks the user joined, and either refreshes the matching active session or
// opens a new 'sso' session in a single database transaction.
func (p Passkeys) RedeemSSOLink(
	ctx context.Context,
	linkHash, activeSessionTokenHash, newSessionTokenHash []byte,
	now time.Time,
	idle, maxAge time.Duration,
	agent string,
) (bool, error) {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var user domain.UserID
	var purpose string
	err = tx.QueryRow(ctx, `
		SELECT user_id, purpose
		FROM identity.link_find($1, $2)`, linkHash, now).Scan(&user, &purpose)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, app.ErrInvalidLink
	}
	if err != nil {
		return false, err
	}
	if purpose != "sso" && purpose != "sso_stepup" {
		return false, app.ErrInvalidLink
	}

	var ssoRequired bool
	var roleKey string
	err = tx.QueryRow(ctx, `
		SELECT sso_required, role_key
		FROM identity.sso_sign_in_state($1)`, user).Scan(&ssoRequired, &roleKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, app.ErrNotSignedIn
	}
	if err != nil {
		return false, err
	}
	breakGlass, err := domain.CheckSSOSignIn(ssoRequired, roleKey, true)
	if err != nil {
		audit.Annotate(ctx, audit.Event{Reason: "sso_required", TargetType: "user", TargetID: user.String()})
		return false, err
	}

	var spent bool
	if err := tx.QueryRow(ctx, `SELECT identity.link_spend($1, $2)`, linkHash, now).Scan(&spent); err != nil {
		return false, err
	}
	if !spent {
		return false, app.ErrInvalidLink
	}

	if _, err := tx.Exec(ctx, `
		UPDATE identity.users SET joined_at = $2
		WHERE id = $1 AND joined_at IS NULL`, user, now); err != nil {
		return false, err
	}

	if purpose == "sso_stepup" && len(activeSessionTokenHash) > 0 {
		var row app.Row
		err = tx.QueryRow(ctx, `
			SELECT id::text, user_id, created_at, last_seen_at, expires_at,
			       sign_in_method, signed_in_at, role_id::text
			FROM identity.session_find($1)`, activeSessionTokenHash).
			Scan(&row.ID, &row.UserID, &row.Created, &row.LastSeen, &row.Expires, &row.Method, &row.SignedIn, &row.RoleID)
		if err == nil && !domain.Ended(row.LastSeen, row.Expires, now, idle) && row.UserID == user {
			if _, err := tx.Exec(ctx, `
				UPDATE identity.sessions
				SET sign_in_method = 'sso', signed_in_at = $2
				WHERE id = $1`, row.ID, now); err != nil {
				return false, err
			}
			audit.WithActor(ctx, audit.Actor{
				Type:           "user",
				ID:             user.String(),
				RoleID:         row.RoleID,
				CredentialType: "session",
				CredentialID:   row.ID,
			})
			if err := audit.Record(ctx, tx, audit.Event{
				Action:   audit.AuthStepUp,
				Outcome:  audit.Success,
				TargetID: row.ID,
				Changes: audit.Changes{
					Values: map[string]audit.Change{"method": {To: "sso"}},
				},
			}); err != nil {
				return false, err
			}
			if err := tx.Commit(ctx); err != nil {
				return false, err
			}
			return true, nil
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return false, err
		}
	}

	if _, err := tx.Exec(ctx, `
		DELETE FROM identity.sessions
		WHERE user_id = $1
		  AND (expires_at <= $2 OR last_seen_at <= $3)`, user, now, now.Add(-idle)); err != nil {
		return false, err
	}

	var roleID string
	if err := tx.QueryRow(ctx, `SELECT role_id::text FROM identity.users WHERE id = $1`, user).Scan(&roleID); err != nil {
		return false, err
	}
	var sessionID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO identity.sessions (user_id, token_hash, created_at, last_seen_at, expires_at, sign_in_method, signed_in_at, user_agent)
		VALUES ($1, $2, $3, $3, $4, 'sso', $3, $5)
		RETURNING id::text`, user, newSessionTokenHash, now, now.Add(maxAge), agent).Scan(&sessionID); err != nil {
		return false, err
	}
	audit.WithActor(ctx, audit.Actor{
		Type:           "user",
		ID:             user.String(),
		RoleID:         roleID,
		CredentialType: "session",
		CredentialID:   sessionID,
	})
	reason := ""
	if breakGlass {
		reason = "sso_break_glass"
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.AuthSignIn,
		Outcome:  audit.Success,
		Reason:   reason,
		TargetID: sessionID,
		Changes: audit.Changes{
			Values: map[string]audit.Change{"method": {To: "sso"}},
		},
	}); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return false, nil
}
