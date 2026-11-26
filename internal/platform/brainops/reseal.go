package brainops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
)

// Store names, used in output, audit and metrics.
const (
	StoreCredentials = "gateway.credentials"
	StorePasswords   = "identity.users.password_hash"
	StoreEmails      = "identity.users.email"
)

// defaultResealBatch is the batch size when ResealOptions.BatchSize is <= 0.
const defaultResealBatch = 200

// storedFormat matches a sealed email address as stored:
// "<key id>:<base64 nonce>:<base64 ciphertext>".
const storedFormat = `^[A-Za-z0-9_]{1,32}:[^:]+:[^:]+$`

// passwordFormat matches a sealed password hash as stored: "<key id>:<wrap
// nonce>:<wrapped key>:<payload nonce>:<payload>". Field 1 is the key id.
const passwordFormat = `^[A-Za-z0-9_]{1,32}:[^:]+:[^:]+:[^:]+:[^:]+$` // #nosec G101 -- a pattern, not a credential

// uniqueViolation is SQLSTATE 23505: a row's UPDATE hit a unique index.
const uniqueViolation = "23505"

// ResealOptions is how a reseal runs.
type ResealOptions struct {
	DryRun    bool
	BatchSize int       // <= 0 means 200
	Out       io.Writer // one progress line per committed batch, and the summary; nil discards
}

// StoreCount is one store's tally. Unknown counts rows whose key id is not
// loaded; Failed counts rows a loaded key could not open, and stored password
// hashes or emails outside the sealed format.
type StoreCount struct {
	Store    string
	Resealed int64 // in a dry run: rows that would be resealed
	Current  int64 // already under the current key
	Unknown  int64
	Failed   int64
	// UnknownKeyIDs are the sorted, distinct key ids behind Unknown.
	UnknownKeyIDs []string
}

// ResealResult is the stores' tallies in the order credentials, passwords,
// emails.
type ResealResult struct{ Stores []StoreCount }

// Reseal re-seals every row in the three stores under ring's current key. Rows
// whose opening key is not loaded are counted as Unknown and never touched; a
// row a loaded key cannot open is counted as Failed and left as it is. The
// run stops cleanly between batches when ctx is done; a batch is all or
// nothing. Unknown and Failed rows are not an error.
func Reseal(ctx context.Context, owner *pgxpool.Pool, ring gateway.Keyring, opts ResealOptions) (ResealResult, error) {
	if !ring.Ready() {
		return ResealResult{}, errors.New("brain: no master key to re-seal under")
	}
	steps := []func() (StoreCount, error){
		func() (StoreCount, error) { return resealCredentials(ctx, owner, ring, opts) },
		func() (StoreCount, error) { return resealPasswords(ctx, owner, ring, opts) },
		func() (StoreCount, error) { return resealEmails(ctx, owner, ring, opts) },
	}
	result := ResealResult{Stores: make([]StoreCount, 0, len(steps))}
	for _, step := range steps {
		count, err := step()
		if err != nil {
			return ResealResult{}, err
		}
		result.Stores = append(result.Stores, count)
	}
	for _, count := range result.Stores {
		if err := writeSummary(opts.Out, count, opts.DryRun); err != nil {
			return ResealResult{}, fmt.Errorf("brain: re-seal %s: report: %w", count.Store, err)
		}
	}
	return result, nil
}

// UnknownKeyRows counts, per store, the sealed rows whose key id ring does not
// hold, with counting queries only. It works with the zero ring: then every
// sealed row is unknown. For emails a row is unknown when either its stored
// address or its index hash names an unloaded key; a stored address outside
// the sealed format never names a key id.
func UnknownKeyRows(ctx context.Context, owner *pgxpool.Pool, ring gateway.Keyring) ([]StoreCount, error) {
	current := ring.CurrentID()
	out := make([]StoreCount, 0, 3)

	byKey, err := countByKey(ctx, owner, `
		SELECT key_id, count(*)
		FROM gateway.credentials
		WHERE key_id <> $1
		GROUP BY key_id`, current)
	if err != nil {
		return nil, fmt.Errorf("brain: unknown key rows in %s: %w", StoreCredentials, err)
	}
	_, unknown, ids := splitCounts(byKey, ring)
	out = append(out, StoreCount{Store: StoreCredentials, Unknown: unknown, UnknownKeyIDs: ids})

	byKey, err = countByKey(ctx, owner, `
		SELECT split_part(password_hash, ':', 1), count(*)
		FROM identity.users
		WHERE password_hash ~ '`+passwordFormat+`'
		  AND split_part(password_hash, ':', 1) <> $1
		GROUP BY 1`, current)
	if err != nil {
		return nil, fmt.Errorf("brain: unknown key rows in %s: %w", StorePasswords, err)
	}
	_, unknown, ids = splitCounts(byKey, ring)
	out = append(out, StoreCount{Store: StorePasswords, Unknown: unknown, UnknownKeyIDs: ids})

	var emailUnknown int64
	emailIDs := map[string]bool{}
	rows, err := owner.Query(ctx, `
		SELECT split_part(email, ':', 1), email_hash_key_id, count(*)
		FROM identity.users
		WHERE email ~ '`+storedFormat+`'
		  AND (split_part(email, ':', 1) <> $1 OR email_hash_key_id <> $1)
		GROUP BY 1, 2`, current)
	if err != nil {
		return nil, fmt.Errorf("brain: unknown key rows in %s: %w", StoreEmails, err)
	}
	defer rows.Close()
	for rows.Next() {
		var prefix, hashKeyID string
		var n int64
		if err := rows.Scan(&prefix, &hashKeyID, &n); err != nil {
			return nil, fmt.Errorf("brain: unknown key rows in %s: %w", StoreEmails, err)
		}
		if ring.Has(prefix) && ring.Has(hashKeyID) {
			continue
		}
		emailUnknown += n
		if !ring.Has(prefix) {
			emailIDs[prefix] = true
		}
		if !ring.Has(hashKeyID) {
			emailIDs[hashKeyID] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("brain: unknown key rows in %s: %w", StoreEmails, err)
	}
	out = append(out, StoreCount{Store: StoreEmails, Unknown: emailUnknown, UnknownKeyIDs: sortedKeys(emailIDs)})
	return out, nil
}

// resealCredentials re-seals gateway.credentials, whose ref is both the seal's
// bound data and the primary key the batches walk.
func resealCredentials(ctx context.Context, owner *pgxpool.Pool, ring gateway.Keyring, opts ResealOptions) (StoreCount, error) {
	count := StoreCount{Store: StoreCredentials}
	current := ring.CurrentID()
	byKey, err := countByKey(ctx, owner, `
		SELECT key_id, count(*)
		FROM gateway.credentials
		WHERE key_id <> $1
		GROUP BY key_id`, current)
	if err != nil {
		return count, fmt.Errorf("brain: re-seal %s: count: %w", StoreCredentials, err)
	}
	needs, unknown, unknownIDs := splitCounts(byKey, ring)
	count.Unknown, count.UnknownKeyIDs = unknown, unknownIDs
	if err := owner.QueryRow(ctx,
		`SELECT count(*) FROM gateway.credentials WHERE key_id = $1`, current,
	).Scan(&count.Current); err != nil {
		return count, fmt.Errorf("brain: re-seal %s: count: %w", StoreCredentials, err)
	}
	if opts.DryRun {
		count.Resealed = needs - unknown
		return count, nil
	}

	batch := opts.BatchSize
	if batch <= 0 {
		batch = defaultResealBatch
	}
	var last string
	for {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		tx, err := owner.Begin(ctx)
		if err != nil {
			return count, fmt.Errorf("brain: re-seal %s: begin: %w", StoreCredentials, err)
		}
		rows, err := tx.Query(ctx, `
			SELECT ref, ciphertext, nonce, key_id, wrapped_key, wrap_nonce
			FROM gateway.credentials
			WHERE key_id <> $1
			  AND key_id <> ALL (coalesce($2::text[], '{}'::text[]))
			  AND ref > $3
			ORDER BY ref
			LIMIT $4
			FOR UPDATE`, current, unknownIDs, last, batch)
		if err != nil {
			_ = tx.Rollback(ctx)
			return count, fmt.Errorf("brain: re-seal %s: select: %w", StoreCredentials, err)
		}
		type sealedRow struct {
			ref string
			gateway.Sealed
		}
		var page []sealedRow
		for rows.Next() {
			var r sealedRow
			if err := rows.Scan(&r.ref, &r.Ciphertext, &r.Nonce, &r.KeyID, &r.WrappedKey, &r.WrapNonce); err != nil {
				rows.Close()
				_ = tx.Rollback(ctx)
				return count, fmt.Errorf("brain: re-seal %s: scan: %w", StoreCredentials, err)
			}
			page = append(page, r)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			_ = tx.Rollback(ctx)
			return count, fmt.Errorf("brain: re-seal %s: read: %w", StoreCredentials, err)
		}
		rows.Close()
		if len(page) == 0 {
			_ = tx.Rollback(ctx)
			return count, nil
		}
		var changed int64
		var fromIDs []string
		for _, r := range page {
			if !ring.Has(r.KeyID) {
				continue // its key left the ring after the count
			}
			// A new data key, wrapped under the current master: a reseal
			// never leaves a payload under a key the retired master opens.
			plain, err := ring.OpenCredential(r.ref, r.Sealed)
			if err != nil {
				count.Failed++
				continue
			}
			sealed, err := ring.SealCredential(ctx, r.ref, plain)
			if err != nil {
				count.Failed++
				continue
			}
			tag, err := tx.Exec(ctx, `
				UPDATE gateway.credentials
				SET ciphertext = $2, nonce = $3, key_id = $4, wrapped_key = $5, wrap_nonce = $6
				WHERE ref = $1 AND ciphertext = $7 AND nonce = $8 AND key_id = $9`,
				r.ref, sealed.Ciphertext, sealed.Nonce, sealed.KeyID, sealed.WrappedKey, sealed.WrapNonce,
				r.Ciphertext, r.Nonce, r.KeyID)
			if err != nil {
				_ = tx.Rollback(ctx)
				return count, fmt.Errorf("brain: re-seal %s: update: %w", StoreCredentials, err)
			}
			if tag.RowsAffected() == 0 {
				continue // the row changed meanwhile; keep what is there now
			}
			changed++
			if !slices.Contains(fromIDs, r.KeyID) {
				fromIDs = append(fromIDs, r.KeyID)
			}
		}
		if err := recordReseal(ctx, tx, StoreCredentials, current, fromIDs, changed); err != nil {
			_ = tx.Rollback(ctx)
			return count, fmt.Errorf("brain: re-seal %s: audit: %w", StoreCredentials, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return count, fmt.Errorf("brain: re-seal %s: commit: %w", StoreCredentials, err)
		}
		count.Resealed += changed
		if changed > 0 {
			if err := reportBatch(opts.Out, StoreCredentials, changed); err != nil {
				return count, fmt.Errorf("brain: re-seal %s: report: %w", StoreCredentials, err)
			}
		}
		last = page[len(page)-1].ref
		if len(page) < batch {
			return count, nil
		}
	}
}

// resealPasswords re-seals identity.users.password_hash, walking the users by
// id: each row's data key is unwrapped under its key, the hash is sealed under
// a new data key wrapped by the current master, and five fields are written.
// A non-NULL stored hash outside the five-field format, three fields
// included, cannot be opened: it is counted Failed, left as it is and never
// printed. NULL is not a password row.
func resealPasswords(ctx context.Context, owner *pgxpool.Pool, ring gateway.Keyring, opts ResealOptions) (StoreCount, error) {
	count := StoreCount{Store: StorePasswords}
	current := ring.CurrentID()
	byKey, err := countByKey(ctx, owner, `
		SELECT split_part(password_hash, ':', 1), count(*)
		FROM identity.users
		WHERE password_hash ~ '`+passwordFormat+`'
		  AND split_part(password_hash, ':', 1) <> $1
		GROUP BY 1`, current)
	if err != nil {
		return count, fmt.Errorf("brain: re-seal %s: count: %w", StorePasswords, err)
	}
	needs, unknown, unknownIDs := splitCounts(byKey, ring)
	count.Unknown, count.UnknownKeyIDs = unknown, unknownIDs
	if err := owner.QueryRow(ctx, `
		SELECT count(*)
		FROM identity.users
		WHERE password_hash ~ '`+passwordFormat+`'
		  AND split_part(password_hash, ':', 1) = $1`, current,
	).Scan(&count.Current); err != nil {
		return count, fmt.Errorf("brain: re-seal %s: count: %w", StorePasswords, err)
	}
	if err := owner.QueryRow(ctx, `
		SELECT count(*)
		FROM identity.users
		WHERE password_hash IS NOT NULL
		  AND password_hash !~ '`+passwordFormat+`'`).Scan(&count.Failed); err != nil {
		return count, fmt.Errorf("brain: re-seal %s: count: %w", StorePasswords, err)
	}
	if opts.DryRun {
		count.Resealed = needs - unknown
		return count, nil
	}

	batch := opts.BatchSize
	if batch <= 0 {
		batch = defaultResealBatch
	}
	var last any // nil for the first page, then the last row's id
	for {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		tx, err := owner.Begin(ctx)
		if err != nil {
			return count, fmt.Errorf("brain: re-seal %s: begin: %w", StorePasswords, err)
		}
		rows, err := tx.Query(ctx, `
			SELECT id::text, password_hash
			FROM identity.users
			WHERE password_hash ~ '`+passwordFormat+`'
			  AND split_part(password_hash, ':', 1) <> $1
			  AND split_part(password_hash, ':', 1) <> ALL (coalesce($2::text[], '{}'::text[]))
			  AND ($3::uuid IS NULL OR id > $3)
			ORDER BY id
			LIMIT $4
			FOR UPDATE`, current, unknownIDs, last, batch)
		if err != nil {
			_ = tx.Rollback(ctx)
			return count, fmt.Errorf("brain: re-seal %s: select: %w", StorePasswords, err)
		}
		type storedRow struct {
			id, stored string
		}
		var page []storedRow
		for rows.Next() {
			var r storedRow
			if err := rows.Scan(&r.id, &r.stored); err != nil {
				rows.Close()
				_ = tx.Rollback(ctx)
				return count, fmt.Errorf("brain: re-seal %s: scan: %w", StorePasswords, err)
			}
			page = append(page, r)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			_ = tx.Rollback(ctx)
			return count, fmt.Errorf("brain: re-seal %s: read: %w", StorePasswords, err)
		}
		rows.Close()
		if len(page) == 0 {
			_ = tx.Rollback(ctx)
			return count, nil
		}
		var changed int64
		var fromIDs []string
		for _, r := range page {
			prefix, _, _ := strings.Cut(r.stored, ":")
			user, err := identity.ParseUserID(r.id)
			if err != nil {
				count.Failed++
				continue
			}
			if !ring.Has(prefix) {
				continue // its key left the ring after the count
			}
			hash, err := openPassword(ring, user, r.stored)
			if err != nil {
				count.Failed++
				continue
			}
			stored, err := sealPassword(ring, user, hash)
			if err != nil {
				count.Failed++
				continue
			}
			tag, err := tx.Exec(ctx, `
				UPDATE identity.users
				SET password_hash = $2
				WHERE id = $1 AND password_hash = $3`, r.id, stored, r.stored)
			if err != nil {
				_ = tx.Rollback(ctx)
				return count, fmt.Errorf("brain: re-seal %s: update: %w", StorePasswords, err)
			}
			if tag.RowsAffected() == 0 {
				continue // the row changed meanwhile; keep what is there now
			}
			changed++
			if !slices.Contains(fromIDs, prefix) {
				fromIDs = append(fromIDs, prefix)
			}
		}
		if err := recordReseal(ctx, tx, StorePasswords, current, fromIDs, changed); err != nil {
			_ = tx.Rollback(ctx)
			return count, fmt.Errorf("brain: re-seal %s: audit: %w", StorePasswords, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return count, fmt.Errorf("brain: re-seal %s: commit: %w", StorePasswords, err)
		}
		count.Resealed += changed
		if changed > 0 {
			if err := reportBatch(opts.Out, StorePasswords, changed); err != nil {
				return count, fmt.Errorf("brain: re-seal %s: report: %w", StorePasswords, err)
			}
		}
		last = page[len(page)-1].id
		if len(page) < batch {
			return count, nil
		}
	}
}

// resealEmails re-seals identity.users.email together with its index hash, so
// a resealed person stays findable by address. A stored address outside the
// sealed format can never be opened or counted under a key id: the row is
// counted Failed, left as it is and never printed.
func resealEmails(ctx context.Context, owner *pgxpool.Pool, ring gateway.Keyring, opts ResealOptions) (StoreCount, error) {
	count := StoreCount{Store: StoreEmails}
	current := ring.CurrentID()
	byKey, err := countByKey(ctx, owner, `
		SELECT split_part(email, ':', 1), count(*)
		FROM identity.users
		WHERE email ~ '`+storedFormat+`'
		  AND (split_part(email, ':', 1) <> $1 OR email_hash_key_id <> $1)
		GROUP BY 1`, current)
	if err != nil {
		return count, fmt.Errorf("brain: re-seal %s: count: %w", StoreEmails, err)
	}
	needs, unknown, unknownIDs := splitCounts(byKey, ring)
	count.Unknown, count.UnknownKeyIDs = unknown, unknownIDs
	if err := owner.QueryRow(ctx, `
		SELECT count(*)
		FROM identity.users
		WHERE email_hash_key_id = $1
		  AND email ~ '`+storedFormat+`'
		  AND split_part(email, ':', 1) = $1`, current,
	).Scan(&count.Current); err != nil {
		return count, fmt.Errorf("brain: re-seal %s: count: %w", StoreEmails, err)
	}
	if err := owner.QueryRow(ctx, `
		SELECT count(*)
		FROM identity.users
		WHERE email !~ '`+storedFormat+`'`,
	).Scan(&count.Failed); err != nil {
		return count, fmt.Errorf("brain: re-seal %s: count: %w", StoreEmails, err)
	}
	if opts.DryRun {
		count.Resealed = needs - unknown
		return count, nil
	}

	batch := opts.BatchSize
	if batch <= 0 {
		batch = defaultResealBatch
	}
	emails := NewEmailSealer(ring)
	var last any // nil for the first page, then the last row's id
	for {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		tx, err := owner.Begin(ctx)
		if err != nil {
			return count, fmt.Errorf("brain: re-seal %s: begin: %w", StoreEmails, err)
		}
		rows, err := tx.Query(ctx, `
			SELECT id::text, email, email_hash
			FROM identity.users
			WHERE email ~ '`+storedFormat+`'
			  AND (split_part(email, ':', 1) <> $1 OR email_hash_key_id <> $1)
			  AND split_part(email, ':', 1) <> ALL (coalesce($2::text[], '{}'::text[]))
			  AND ($3::uuid IS NULL OR id > $3)
			ORDER BY id
			LIMIT $4
			FOR UPDATE`, current, unknownIDs, last, batch)
		if err != nil {
			_ = tx.Rollback(ctx)
			return count, fmt.Errorf("brain: re-seal %s: select: %w", StoreEmails, err)
		}
		type storedRow struct {
			id, stored string
			hash       []byte
		}
		var page []storedRow
		for rows.Next() {
			var r storedRow
			if err := rows.Scan(&r.id, &r.stored, &r.hash); err != nil {
				rows.Close()
				_ = tx.Rollback(ctx)
				return count, fmt.Errorf("brain: re-seal %s: scan: %w", StoreEmails, err)
			}
			page = append(page, r)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			_ = tx.Rollback(ctx)
			return count, fmt.Errorf("brain: re-seal %s: read: %w", StoreEmails, err)
		}
		rows.Close()
		if len(page) == 0 {
			_ = tx.Rollback(ctx)
			return count, nil
		}
		var changed int64
		var fromIDs []string
		for _, r := range page {
			prefix, _, _ := strings.Cut(r.stored, ":")
			if !ring.Has(prefix) {
				continue // its key left the ring after the count
			}
			address, err := emails.Open(r.stored, r.hash)
			if err != nil {
				count.Failed++
				continue
			}
			stored, hash, keyID, err := emails.Seal(address)
			if err != nil {
				count.Failed++
				continue
			}
			affected, unique, err := updateResealEmail(ctx, tx, `
				UPDATE identity.users
				SET email = $2, email_hash = $3, email_hash_key_id = $4
				WHERE id = $1 AND email = $5`, r.id, stored, hash, keyID, r.stored)
			if err != nil {
				_ = tx.Rollback(ctx)
				return count, err
			}
			if unique {
				count.Failed++ // another person already carries this index hash
				continue
			}
			if affected == 0 {
				continue // the row changed meanwhile; keep what is there now
			}
			changed++
			if !slices.Contains(fromIDs, prefix) {
				fromIDs = append(fromIDs, prefix)
			}
		}
		if err := recordReseal(ctx, tx, StoreEmails, current, fromIDs, changed); err != nil {
			_ = tx.Rollback(ctx)
			return count, fmt.Errorf("brain: re-seal %s: audit: %w", StoreEmails, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return count, fmt.Errorf("brain: re-seal %s: commit: %w", StoreEmails, err)
		}
		count.Resealed += changed
		if changed > 0 {
			if err := reportBatch(opts.Out, StoreEmails, changed); err != nil {
				return count, fmt.Errorf("brain: re-seal %s: report: %w", StoreEmails, err)
			}
		}
		last = page[len(page)-1].id
		if len(page) < batch {
			return count, nil
		}
	}
}

// updateResealEmail runs one person's email UPDATE inside its own savepoint.
// Two people can hash to the same index value, so a unique violation on
// users_email_hash must fail that row alone instead of aborting the batch (and
// every later run with it); anything else still aborts the run. The returned
// error carries the store name and the SQLSTATE only: the server's detail
// holds the index bytes the row collided on.
func updateResealEmail(ctx context.Context, tx pgx.Tx, sql string, args ...any) (affected int64, unique bool, err error) {
	sp, err := tx.Begin(ctx) // a savepoint: this row alone rolls back on failure
	if err != nil {
		return 0, false, fmt.Errorf("brain: re-seal %s: savepoint: %w", StoreEmails, err)
	}
	tag, err := sp.Exec(ctx, sql, args...)
	if err != nil {
		_ = sp.Rollback(ctx)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) {
			return 0, false, fmt.Errorf("brain: re-seal %s: update: %w", StoreEmails, err)
		}
		if pgErr.Code == uniqueViolation {
			return 0, true, nil
		}
		return 0, false, fmt.Errorf("brain: re-seal %s: update: sqlstate %s", StoreEmails, pgErr.Code)
	}
	if err := sp.Commit(ctx); err != nil { // releases the savepoint
		return 0, false, fmt.Errorf("brain: re-seal %s: savepoint: %w", StoreEmails, err)
	}
	return tag.RowsAffected(), false, nil
}

// recordReseal writes one batch's keys.reseal event in the batch's own
// transaction. A batch that changed nothing records nothing.
func recordReseal(ctx context.Context, tx audit.Execer, store, current string, fromIDs []string, changed int64) error {
	if changed == 0 {
		return nil
	}
	sorted := append([]string(nil), fromIDs...)
	sort.Strings(sorted)
	return audit.Record(ctx, tx, audit.Event{
		Action:   audit.KeysReseal,
		Outcome:  audit.Success,
		TargetID: current,
		Changes: audit.Changes{
			Fields: []string{"count", "from_key_ids", "key_id", "store"},
			Values: map[string]audit.Change{
				"store":        {To: store},
				"key_id":       {To: current},
				"from_key_ids": {To: strings.Join(sorted, ",")},
				"count":        {To: changed},
			},
		},
	})
}

// countByKey runs one grouped counting query: $1 is the current key id, and
// every row reports one opening key id and how many rows carry it.
func countByKey(ctx context.Context, owner *pgxpool.Pool, sql, current string) (map[string]int64, error) {
	rows, err := owner.Query(ctx, sql, current)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byKey := map[string]int64{}
	for rows.Next() {
		var id string
		var n int64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		byKey[id] += n
	}
	return byKey, rows.Err()
}

// splitCounts divides one store's grouped counts into the rows needing a
// reseal and the unknown part whose key id the ring does not hold, with those
// ids sorted and distinct.
func splitCounts(byKey map[string]int64, ring gateway.Keyring) (needs, unknown int64, unknownIDs []string) {
	for id, n := range byKey {
		needs += n
		if !ring.Has(id) {
			unknown += n
			unknownIDs = append(unknownIDs, id)
		}
	}
	sort.Strings(unknownIDs)
	return needs, unknown, unknownIDs
}

// sortedKeys returns the map's keys sorted.
func sortedKeys(ids map[string]bool) []string {
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// reportBatch prints one committed batch's progress line.
func reportBatch(w io.Writer, store string, changed int64) error {
	if w == nil {
		return nil
	}
	_, err := fmt.Fprintf(w, "%s: resealed %d (batch)\n", store, changed)
	return err
}

// writeSummary prints one store's summary line: counts, store names and key
// ids only, never a ref, an address or sealed bytes.
func writeSummary(w io.Writer, count StoreCount, dryRun bool) error {
	if w == nil {
		return nil
	}
	label := "resealed"
	if dryRun {
		label = "would_reseal"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s=%d current=%d unknown_key=%d failed=%d",
		count.Store, label, count.Resealed, count.Current, count.Unknown, count.Failed)
	if len(count.UnknownKeyIDs) > 0 {
		fmt.Fprintf(&b, " unknown_key_ids=%s", strings.Join(count.UnknownKeyIDs, ","))
	}
	_, err := fmt.Fprintln(w, b.String())
	return err
}
