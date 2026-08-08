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

// Passkeys is the passkey, link, challenge and throttle tables.
type Passkeys struct {
	Pool *pgxpool.Pool
}

func (p Passkeys) SweepChallenges(ctx context.Context, now time.Time) error {
	_, err := p.Pool.Exec(ctx, `DELETE FROM identity.webauthn_challenges WHERE expires_at <= $1`, now)
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
		DELETE FROM identity.webauthn_challenges
		WHERE token_hash = $1 AND expires_at > $2
		RETURNING session, purpose, user_id`, hash, now).Scan(&session, &purpose, &id)
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
		SELECT link.user_id, link.purpose
		FROM identity.sign_in_links AS link
		JOIN identity.users AS person ON person.id = link.user_id
		WHERE link.token_hash = $1
		  AND link.used_at IS NULL
		  AND link.expires_at > $2
		  AND person.disabled_at IS NULL`, hash, now).Scan(&user, &purpose)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.UserID{}, "", false, nil
	}
	if err != nil {
		return domain.UserID{}, "", false, err
	}
	return user, purpose, true, nil
}

func (p Passkeys) SpendLink(ctx context.Context, hash []byte, now time.Time) error {
	tag, err := p.Pool.Exec(ctx, `
		UPDATE identity.sign_in_links SET used_at = $2
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > $2`, hash, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
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
	err := p.Pool.QueryRow(ctx, `
		SELECT id, webauthn_handle, email, name
		FROM identity.users
		WHERE id = $1 AND disabled_at IS NULL`, user).Scan(&account.ID, &account.Handle, &account.Name, &account.Display)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Account{}, app.ErrNotSignedIn
	}
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
	err := p.Pool.QueryRow(ctx, `
		SELECT person.id
		FROM identity.passkeys AS passkey
		JOIN identity.users AS person ON person.id = passkey.user_id
		WHERE passkey.credential_id = $1
		  AND person.webauthn_handle = $2
		  AND person.disabled_at IS NULL`, credentialID, userHandle).Scan(&id)
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
	var id string
	err := p.Pool.QueryRow(ctx, `
		INSERT INTO identity.passkeys (
		  user_id, credential_id, public_key, sign_count, aaguid, transports,
		  backup_eligible, backup_state, attestation, name, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id::text`,
		user, item.ID, item.PublicKey, item.SignCount, item.AAGUID, item.Transports,
		item.BackupEligible, item.BackupState, item.Attestation, name, now).Scan(&id)
	return id, err
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

func (p Passkeys) RenamePasskey(ctx context.Context, user domain.UserID, id, name string) error {
	tag, err := p.Pool.Exec(ctx, `UPDATE identity.passkeys SET name = $3 WHERE id = $1 AND user_id = $2`, id, user, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

func (p Passkeys) DeletePasskey(ctx context.Context, user domain.UserID, id string) ([]byte, error) {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT id FROM identity.users WHERE id = $1 FOR NO KEY UPDATE`, user); err != nil {
		return nil, err
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM identity.passkeys WHERE user_id = $1`, user).Scan(&count); err != nil {
		return nil, err
	}
	var hasPassword bool
	if err := tx.QueryRow(ctx, `SELECT password_hash IS NOT NULL FROM identity.users WHERE id = $1`, user).Scan(&hasPassword); err != nil {
		return nil, err
	}
	if count < 2 && !hasPassword {
		return nil, app.ErrLastPasskey
	}
	var credentialID []byte
	err = tx.QueryRow(ctx, `
		DELETE FROM identity.passkeys WHERE id = $1 AND user_id = $2 RETURNING credential_id`, id, user).Scan(&credentialID)
	if err != nil {
		return nil, err
	}
	return credentialID, tx.Commit(ctx)
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
	var id domain.UserID
	err := p.Pool.QueryRow(ctx, `
		SELECT id FROM identity.users WHERE lower(email) = lower($1) AND disabled_at IS NULL`, email).Scan(&id)
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

func (p Passkeys) Bootstrap(ctx context.Context, email, name string, hash []byte, now, expires time.Time) (domain.UserID, error) {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return domain.UserID{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7480198)`); err != nil {
		return domain.UserID{}, err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM identity.users)`).Scan(&exists); err != nil {
		return domain.UserID{}, err
	}
	if exists {
		return domain.UserID{}, app.ErrExists
	}
	var user domain.UserID
	err = tx.QueryRow(ctx, `
		INSERT INTO identity.users (email, name, role_id, invited_at, joined_at)
		VALUES ($1, $2, (SELECT id FROM identity.roles WHERE key = 'owner'), $3, $3)
		RETURNING id`, email, name, now).Scan(&user)
	if err != nil {
		return domain.UserID{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO identity.sign_in_links (token_hash, user_id, purpose, expires_at)
		VALUES ($1, $2, 'bootstrap', $3)`, hash, user, expires); err != nil {
		return domain.UserID{}, err
	}
	return user, tx.Commit(ctx)
}
