package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StoredPassword is one person's stored password as the worker reads it on
// the owner pool. Stored is "" when no password is set.
type StoredPassword struct {
	User     string
	Stored   string
	Disabled bool
}

// StoredPasswordByAddress finds the person behind any of an address's index
// hashes, disabled or not. found is false for an unknown address.
func StoredPasswordByAddress(ctx context.Context, owner *pgxpool.Pool, hashes [][]byte) (StoredPassword, bool, error) {
	var out StoredPassword
	var stored *string
	err := owner.QueryRow(ctx, `
		SELECT id::text, password_hash, disabled_at IS NOT NULL
		FROM identity.users
		WHERE email_hash = ANY($1)
		LIMIT 1`, hashes).Scan(&out.User, &stored, &out.Disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return StoredPassword{}, false, nil
	}
	if err != nil {
		return StoredPassword{}, false, err
	}
	if stored != nil {
		out.Stored = *stored
	}
	return out, true, nil
}

// StoredPasswordOfSession finds the enabled person whose unexpired session
// hashes to tokenHash, with their stored address and its index hash. ok is
// false for any other hash.
func StoredPasswordOfSession(ctx context.Context, owner *pgxpool.Pool, tokenHash []byte) (out StoredPassword, email string, emailHash []byte, ok bool, err error) {
	var stored *string
	err = owner.QueryRow(ctx, `
		SELECT u.id::text, u.password_hash, u.email, u.email_hash
		FROM identity.sessions AS s
		JOIN identity.users AS u ON u.id = s.user_id
		WHERE s.token_hash = $1
		  AND s.expires_at > now()
		  AND u.disabled_at IS NULL`, tokenHash).Scan(&out.User, &stored, &email, &emailHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return StoredPassword{}, "", nil, false, nil
	}
	if err != nil {
		return StoredPassword{}, "", nil, false, err
	}
	if stored != nil {
		out.Stored = *stored
	}
	return out, email, emailHash, true, nil
}

// TakePasswordVerify counts one password check for subject in one statement:
// a new window starts at 1 when the last began 60 seconds ago or more, and
// otherwise the count grows to at most 6. It returns the count.
func TakePasswordVerify(ctx context.Context, owner *pgxpool.Pool, subject []byte) (int, error) {
	var attempts int
	err := owner.QueryRow(ctx, `
		INSERT INTO identity.password_verify_window AS w (subject, window_start, attempts)
		VALUES ($1, clock_timestamp(), 1)
		ON CONFLICT (subject) DO UPDATE SET
		  window_start = CASE WHEN w.window_start <= clock_timestamp() - interval '60 seconds'
		                      THEN clock_timestamp() ELSE w.window_start END,
		  attempts = CASE WHEN w.window_start <= clock_timestamp() - interval '60 seconds'
		                  THEN 1 ELSE least(w.attempts + 1, 6) END
		RETURNING attempts`, subject).Scan(&attempts)
	return attempts, err
}

// SweepPasswordVerify deletes the windows that began over 10 minutes ago,
// which bounds the rows unknown addresses leave.
func SweepPasswordVerify(ctx context.Context, owner *pgxpool.Pool) (int64, error) {
	tag, err := owner.Exec(ctx, `
		DELETE FROM identity.password_verify_window
		WHERE window_start < clock_timestamp() - interval '10 minutes'`)
	return tag.RowsAffected(), err
}
