package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
)

// Passkeys is the passkey, link, challenge and throttle tables.
type Passkeys struct {
	Pool   *pgxpool.Pool
	Emails app.EmailSealer
	Blobs  app.AvatarBlobs
}

func (p Passkeys) SweepChallenges(ctx context.Context, now time.Time) error {
	_, err := p.Pool.Exec(ctx, `DELETE FROM identity.webauthn_challenges WHERE expires_at <= $1`, now)
	return err
}

// SweepLinks deletes links that expired over a day ago, so a used link with a
// future expiry stays until it expires.
func (p Passkeys) SweepLinks(ctx context.Context, now time.Time) error {
	_, err := p.Pool.Exec(ctx, `DELETE FROM identity.sign_in_links WHERE expires_at <= $1`, now.Add(-24*time.Hour))
	return err
}

func (p Passkeys) SaveChallenge(ctx context.Context, hash, session []byte, purpose string, user *domain.UserID, expires time.Time) error {
	_, err := p.Pool.Exec(ctx, `
		INSERT INTO identity.webauthn_challenges (token_hash, session, purpose, user_id, expires_at)
		VALUES ($1, $2, $3, $4, $5)`, hash, session, purpose, user, expires)
	return err
}

func (p Passkeys) TakeChallenge(ctx context.Context, hash []byte, now time.Time) (session []byte, purpose string, user domain.UserID, ok bool, err error) {
	var id *domain.UserID
	err = p.Pool.QueryRow(ctx, `
		SELECT session, purpose, user_id
		FROM identity.challenge_take($1, $2)`, hash, now).Scan(&session, &purpose, &id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", domain.UserID{}, false, nil
	}
	if err != nil {
		return nil, "", domain.UserID{}, false, err
	}
	if id != nil {
		user = *id
	}
	return session, purpose, user, true, nil
}

func (p Passkeys) InsertLink(ctx context.Context, hash []byte, user domain.UserID, purpose string, expires time.Time) error {
	_, err := p.Pool.Exec(ctx, `
		INSERT INTO identity.sign_in_links (token_hash, user_id, purpose, expires_at)
		VALUES ($1, $2, $3, $4)`, hash, user, purpose, expires)
	return err
}

func (p Passkeys) FindLink(ctx context.Context, hash []byte, now time.Time) (user domain.UserID, purpose string, ok bool, err error) {
	err = p.Pool.QueryRow(ctx, `
		SELECT user_id, purpose
		FROM identity.link_find($1, $2)`, hash, now).Scan(&user, &purpose)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.UserID{}, "", false, nil
	}
	if err != nil {
		return domain.UserID{}, "", false, err
	}
	return user, purpose, true, nil
}

func (p Passkeys) SpendLink(ctx context.Context, hash []byte, now time.Time) error {
	var spent bool
	if err := p.Pool.QueryRow(ctx, `SELECT identity.link_spend($1, $2)`, hash, now).Scan(&spent); err != nil {
		return err
	}
	if !spent {
		return errSpent
	}
	return nil
}

var errSpent = errors.New("identity: link spent")

func (p Passkeys) Handle(ctx context.Context, user domain.UserID, fresh []byte) ([]byte, error) {
	var handle []byte
	err := p.Pool.QueryRow(ctx, `
		UPDATE identity.users
		SET webauthn_handle = COALESCE(webauthn_handle, $2)
		WHERE id = $1
		RETURNING webauthn_handle`, user, fresh).Scan(&handle)
	return handle, err
}

func (p Passkeys) Account(ctx context.Context, user domain.UserID) (domain.Account, error) {
	var account domain.Account
	var stored string
	var hash []byte
	err := p.Pool.QueryRow(ctx, `
		SELECT id, webauthn_handle, email, email_hash, name
		FROM identity.users
		WHERE id = $1 AND disabled_at IS NULL`, user).Scan(&account.ID, &account.Handle, &stored, &hash, &account.Display)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Account{}, app.ErrNotSignedIn
	}
	if err != nil {
		return domain.Account{}, err
	}
	account.Name, err = openStoredEmail(p.Emails, stored, hash)
	if err != nil {
		return domain.Account{}, err
	}
	creds, err := p.credentials(ctx, user)
	if err != nil {
		return domain.Account{}, err
	}
	account.Credentials = creds
	return account, nil
}

func (p Passkeys) AccountByCredential(ctx context.Context, credentialID, userHandle []byte) (domain.Account, error) {
	var id domain.UserID
	err := p.Pool.QueryRow(ctx, `SELECT id FROM identity.account_by_credential($1, $2)`, credentialID, userHandle).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Account{}, app.ErrNotSignedIn
	}
	if err != nil {
		return domain.Account{}, err
	}
	return p.Account(ctx, id)
}

func (p Passkeys) credentials(ctx context.Context, user domain.UserID) ([]domain.Credential, error) {
	rows, err := p.Pool.Query(ctx, `
		SELECT credential_id, public_key, sign_count, aaguid, transports, backup_eligible, backup_state, attestation
		FROM identity.passkeys WHERE user_id = $1`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.Credential
	for rows.Next() {
		var item domain.Credential
		if err := rows.Scan(&item.ID, &item.PublicKey, &item.SignCount, &item.AAGUID, &item.Transports, &item.BackupEligible, &item.BackupState, &item.Attestation); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (p Passkeys) InsertPasskey(ctx context.Context, user domain.UserID, item domain.Credential, name string, now time.Time) (string, error) {
	return p.AddPasskey(ctx, user, "", item, name, now)
}

func (p Passkeys) TouchPasskey(ctx context.Context, credentialID []byte, count uint32, now time.Time) error {
	_, err := p.Pool.Exec(ctx, `
		UPDATE identity.passkeys SET sign_count = $2, last_used_at = $3 WHERE credential_id = $1`, credentialID, count, now)
	return err
}

func (p Passkeys) ListPasskeys(ctx context.Context, user domain.UserID) ([]app.PasskeyRecord, error) {
	rows, err := p.Pool.Query(ctx, `
		SELECT id::text, name, credential_id, created_at, last_used_at
		FROM identity.passkeys WHERE user_id = $1 ORDER BY created_at`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []app.PasskeyRecord
	for rows.Next() {
		var row app.PasskeyRecord
		if err := rows.Scan(&row.ID, &row.Name, &row.CredentialID, &row.Added, &row.LastUsed); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// RenamePasskey renames one passkey and records the passkey.rename event in
// the same transaction. The new name is never recorded.
func (p Passkeys) RenamePasskey(ctx context.Context, user domain.UserID, id, name string) error {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `UPDATE identity.passkeys SET name = $3 WHERE id = $1 AND user_id = $2`, id, user, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	if err := audit.Record(ctx, tx, audit.Event{
		Action:   audit.PasskeyRename,
		Outcome:  audit.Success,
		TargetID: id,
	}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (p Passkeys) DeletePasskey(ctx context.Context, user domain.UserID, id string) ([]byte, error) {
	return p.RemovePasskey(ctx, user, "", id)
}

func (p Passkeys) Join(ctx context.Context, user domain.UserID, now time.Time) error {
	_, err := p.Pool.Exec(ctx, `
		UPDATE identity.users SET joined_at = $2
		WHERE id = $1 AND joined_at IS NULL`, user, now)
	return err
}

func (p Passkeys) Take(ctx context.Context, key string, now time.Time, burst, perSecond float64) (bool, time.Duration, error) {
	if perSecond > 0 {
		idle := time.Duration((2 * burst / perSecond) * float64(time.Second))
		if _, err := p.Pool.Exec(ctx, `DELETE FROM identity.throttle WHERE updated_at <= $1`, now.Add(-idle)); err != nil {
			return false, 0, err
		}
	}
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return false, 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		INSERT INTO identity.throttle (key, tokens, updated_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (key) DO NOTHING`, key, burst, now); err != nil {
		return false, 0, err
	}
	var tokens float64
	var updated time.Time
	if err := tx.QueryRow(ctx, `SELECT tokens, updated_at FROM identity.throttle WHERE key = $1 FOR UPDATE`, key).Scan(&tokens, &updated); err != nil {
		return false, 0, err
	}
	tokens = domain.Refill(tokens, updated, now, burst, perSecond)
	if tokens < 1 {
		tokens--
		if _, err := tx.Exec(ctx, `UPDATE identity.throttle SET tokens = $2, updated_at = $3 WHERE key = $1`, key, tokens, now); err != nil {
			return false, 0, err
		}
		if err := tx.Commit(ctx); err != nil {
			return false, 0, err
		}
		return false, domain.RetryAfter(tokens, perSecond), nil
	}
	tokens--
	if _, err := tx.Exec(ctx, `UPDATE identity.throttle SET tokens = $2, updated_at = $3 WHERE key = $1`, key, tokens, now); err != nil {
		return false, 0, err
	}
	return true, 0, tx.Commit(ctx)
}

func (p Passkeys) UserByEmail(ctx context.Context, email string) (domain.UserID, bool, error) {
	hashes, err := indexEmailHashes(p.Emails, email)
	if err != nil {
		return domain.UserID{}, false, err
	}
	var id domain.UserID
	err = p.Pool.QueryRow(ctx, `
		SELECT id FROM identity.users WHERE email_hash = ANY($1) AND disabled_at IS NULL`, hashes).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.UserID{}, false, nil
	}
	return id, err == nil, err
}

func (p Passkeys) Empty(ctx context.Context) (bool, error) {
	var exists bool
	err := p.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM identity.users)`).Scan(&exists)
	return !exists, err
}

func (p Passkeys) SignInPolicy(ctx context.Context, user domain.UserID) (bool, string, error) {
	var ssoRequired bool
	var roleKey string
	err := p.Pool.QueryRow(ctx, `
		SELECT sso_required, role_key
		FROM identity.sso_sign_in_state($1)`, user).Scan(&ssoRequired, &roleKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, "", app.ErrNotSignedIn
	}
	if err != nil {
		return false, "", err
	}
	return ssoRequired, roleKey, nil
}

func (p Passkeys) Bootstrap(ctx context.Context, email, name string, hash []byte, now, expires time.Time) (domain.UserID, error) {
	stored, hashes, keyID, err := sealEmail(p.Emails, email)
	if err != nil {
		return domain.UserID{}, err
	}
	var user domain.UserID
	err = p.Pool.QueryRow(ctx, `
		SELECT identity.member_invite(NULL, $1, $2, $3, $4, NULL, NULL, $5, $6, $7)`,
		stored, hashes, keyID, name, hash, now, expires).Scan(&user)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Message == "identity: someone already exists" {
			return domain.UserID{}, app.ErrExists
		}
		return domain.UserID{}, err
	}
	return user, nil
}
