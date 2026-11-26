package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// StoredEmail is one person's stored address as the worker reads it on the
// owner pool: the person's id, the stored text and its index hash.
type StoredEmail struct {
	User   string
	Stored string
	Hash   []byte
}

// StoredEmailOfSession is the enabled person whose unexpired session hashes
// to tokenHash, with their stored address. ok is false for any other hash.
func StoredEmailOfSession(ctx context.Context, owner *pgxpool.Pool, tokenHash []byte) (StoredEmail, bool, error) {
	return storedEmail(owner.QueryRow(ctx, `
		SELECT u.id::text, u.email, u.email_hash
		FROM identity.sessions AS s
		JOIN identity.users AS u ON u.id = s.user_id
		WHERE s.token_hash = $1
		  AND s.expires_at > now()
		  AND u.disabled_at IS NULL`, tokenHash))
}

// StoredEmailOfLink is the enabled person an unused, unexpired sign-in link
// hashing to linkHash is for, with their stored address.
func StoredEmailOfLink(ctx context.Context, owner *pgxpool.Pool, linkHash []byte) (StoredEmail, bool, error) {
	return storedEmail(owner.QueryRow(ctx, `
		SELECT u.id::text, u.email, u.email_hash
		FROM identity.sign_in_links AS l
		JOIN identity.users AS u ON u.id = l.user_id
		WHERE l.token_hash = $1
		  AND l.used_at IS NULL
		  AND l.expires_at > now()
		  AND u.disabled_at IS NULL`, linkHash))
}

// StoredEmailOfPerson is user, when enabled, with their stored address.
func StoredEmailOfPerson(ctx context.Context, owner *pgxpool.Pool, user string) (StoredEmail, bool, error) {
	return storedEmail(owner.QueryRow(ctx, `
		SELECT id::text, email, email_hash
		FROM identity.users
		WHERE id = $1::uuid AND disabled_at IS NULL`, user))
}

// SessionPerson is the enabled person behind an unexpired session hash. It
// reads no API token: only a session opens an address.
func SessionPerson(ctx context.Context, owner *pgxpool.Pool, tokenHash []byte) (string, bool, error) {
	var user string
	err := owner.QueryRow(ctx, `
		SELECT u.id::text
		FROM identity.sessions AS s
		JOIN identity.users AS u ON u.id = s.user_id
		WHERE s.token_hash = $1
		  AND s.expires_at > now()
		  AND u.disabled_at IS NULL`, tokenHash).Scan(&user)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return user, true, nil
}

// StoredEmailDirectory is every enabled person's stored address, ordered by
// id.
func StoredEmailDirectory(ctx context.Context, owner *pgxpool.Pool) ([]StoredEmail, error) {
	rows, err := owner.Query(ctx, `
		SELECT id::text, email, email_hash
		FROM identity.users
		WHERE disabled_at IS NULL
		ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredEmail
	for rows.Next() {
		var row StoredEmail
		if err := rows.Scan(&row.User, &row.Stored, &row.Hash); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func storedEmail(row pgx.Row) (StoredEmail, bool, error) {
	var out StoredEmail
	err := row.Scan(&out.User, &out.Stored, &out.Hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return StoredEmail{}, false, nil
	}
	if err != nil {
		return StoredEmail{}, false, err
	}
	return out, true, nil
}
