//go:build integration

package brainops

// The reseal engine moves every sealed row to the ring's current key: all
// three stores, in batches that survive being stopped half way, without ever
// touching rows whose key is gone and without ever printing what it resealed.

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io/fs"
	"maps"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

// newResealDB starts a fresh database with the audit and gateway schemas;
// identity is already in the seeded template.
func newResealDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	owner := pgtest.New(t)
	ctx := context.Background()
	for schema, files := range map[string]fs.FS{
		audit.Schema:   audit.Migrations(),
		gateway.Schema: gateway.Migrations(),
	} {
		if err := migrate.Up(ctx, owner, schema, files); err != nil {
			t.Fatal(err)
		}
	}
	return owner
}

// reseedRing builds a ring over fixed 32-byte keys from the environment
// variables, the way brain reads them: current, plus any previous ones.
func reseedRing(t *testing.T, current string, previous ...string) gateway.Keyring {
	t.Helper()
	raw := func(id string) string {
		return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{id[len(id)-1]}, 32))
	}
	environ := make([]string, 0, len(previous))
	for _, id := range previous {
		environ = append(environ, gateway.EnvMasterKeyPrevious+id+"="+raw(id))
	}
	ring, err := gateway.KeyringFromEnv(func(name string) (string, bool) {
		switch name {
		case gateway.EnvMasterKey:
			return raw(current), true
		case gateway.EnvMasterKeyID:
			return current, true
		}
		return "", false
	}, environ)
	if err != nil {
		t.Fatal(err)
	}
	return ring
}

// seedCredential seals one provider key under ring's current key.
func seedCredential(t *testing.T, owner *pgxpool.Pool, ring gateway.Keyring, ref, secret string) {
	t.Helper()
	sealed, err := ring.Seal(ref, secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(context.Background(), `
		INSERT INTO gateway.credentials (ref, ciphertext, nonce, key_id)
		VALUES ($1, $2, $3, $4)`, ref, sealed.Ciphertext, sealed.Nonce, sealed.KeyID); err != nil {
		t.Fatal(err)
	}
}

// seedPerson stores one joined owner whose password hash and email address are
// sealed under ring's current key. It returns the person's id.
func seedPerson(t *testing.T, owner *pgxpool.Pool, ring gateway.Keyring, address, hash string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := owner.QueryRow(ctx, `
		INSERT INTO identity.users (email, name, role_id, joined_at)
		VALUES ('', 'Ada', (SELECT id FROM identity.roles WHERE key = 'owner'), now())
		RETURNING id::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	user, err := identity.ParseUserID(id)
	if err != nil {
		t.Fatal(err)
	}
	storedHash, err := NewPasswordSealer(ring).Seal(identity.PasswordRef(user), hash)
	if err != nil {
		t.Fatal(err)
	}
	storedEmail, emailHash, emailKeyID, err := NewEmailSealer(ring).Seal(address)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		UPDATE identity.users
		SET password_hash = $2, email = $3, email_hash = $4, email_hash_key_id = $5
		WHERE id = $1`, id, storedHash, storedEmail, emailHash, emailKeyID); err != nil {
		t.Fatal(err)
	}
	return id
}

// storedCredential is one credential row as stored.
type storedCredential struct {
	ref        string
	ciphertext []byte
	nonce      []byte
	keyID      string
}

// readCredentials returns every credential row in primary-key order.
func readCredentials(t *testing.T, owner *pgxpool.Pool) []storedCredential {
	t.Helper()
	rows, err := owner.Query(context.Background(), `
		SELECT ref, ciphertext, nonce, key_id FROM gateway.credentials ORDER BY ref`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []storedCredential
	for rows.Next() {
		var c storedCredential
		if err := rows.Scan(&c.ref, &c.ciphertext, &c.nonce, &c.keyID); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// storedPerson is one person's sealed columns as stored.
type storedPerson struct {
	id             string
	passwordHash   string
	email          string
	emailHash      []byte
	emailHashKeyID string
}

// readPerson reads one person's sealed columns.
func readPerson(t *testing.T, owner *pgxpool.Pool, id string) storedPerson {
	t.Helper()
	var p storedPerson
	var hashKeyID *string
	if err := owner.QueryRow(context.Background(), `
		SELECT id::text, password_hash, email, email_hash, email_hash_key_id
		FROM identity.users WHERE id = $1`, id,
	).Scan(&p.id, &p.passwordHash, &p.email, &p.emailHash, &hashKeyID); err != nil {
		t.Fatal(err)
	}
	if hashKeyID != nil {
		p.emailHashKeyID = *hashKeyID
	}
	return p
}

// reseedAuditCounts sums the keys.reseal events' changed-row counts per store.
func reseedAuditCounts(t *testing.T, owner *pgxpool.Pool) map[string]int64 {
	t.Helper()
	rows, err := owner.Query(context.Background(), `
		SELECT changes->'values'->'store'->>'to' AS store,
		       sum((changes->'values'->'count'->>'to')::bigint)
		FROM audit.pending
		WHERE action = 'keys.reseal'
		GROUP BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var store string
		var n int64
		if err := rows.Scan(&store, &n); err != nil {
			t.Fatal(err)
		}
		out[store] = n
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// storeCount returns one named store's tally from a result.
func storeCount(t *testing.T, result ResealResult, store string) StoreCount {
	t.Helper()
	for _, count := range result.Stores {
		if count.Store == store {
			return count
		}
	}
	t.Fatalf("no tally for store %s in %v", store, result.Stores)
	return StoreCount{}
}

// checkPersonOpen asserts one person's password and email open under ring to
// the values they were seeded with, under the given key ids.
func checkPersonOpen(t *testing.T, p storedPerson, ring gateway.Keyring, hash, address, keyID string) {
	t.Helper()
	user, err := identity.ParseUserID(p.id)
	if err != nil {
		t.Fatal(err)
	}
	if prefix, _, _ := strings.Cut(p.passwordHash, ":"); prefix != keyID {
		t.Fatalf("password is under key %q, want %q", prefix, keyID)
	}
	gotHash, err := NewPasswordSealer(ring).Open(identity.PasswordRef(user), p.passwordHash)
	if err != nil || gotHash != hash {
		t.Fatalf("password opens to %q, %v", gotHash, err)
	}
	if prefix, _, _ := strings.Cut(p.email, ":"); prefix != keyID || p.emailHashKeyID != keyID {
		t.Fatalf("email is under keys %q and %q, want %q", prefix, p.emailHashKeyID, keyID)
	}
	gotAddress, err := NewEmailSealer(ring).Open(p.email, p.emailHash)
	if err != nil || gotAddress != address {
		t.Fatalf("email opens to %q, %v", gotAddress, err)
	}
}

// A full reseal moves every row of all three stores under the current key,
// keeps every value openable with the plaintext intact, writes audit events
// whose counts add up, and a second run finds nothing to do.
func TestResealMovesEveryRowToTheCurrentKey(t *testing.T) {
	owner := newResealDB(t)
	ctx := context.Background()
	ring1 := reseedRing(t, "k1")
	secrets := map[string]string{
		"own_alpha": "sk-alpha-secret",
		"own_beta":  "sk-beta-secret",
		"own_gamma": "sk-gamma-secret",
	}
	for ref, secret := range secrets {
		seedCredential(t, owner, ring1, ref, secret)
	}
	ada := seedPerson(t, owner, ring1, "ada@example.com", "ada-hash")
	lee := seedPerson(t, owner, ring1, "lee@example.com", "lee-hash")

	ring2 := reseedRing(t, "k2", "k1")
	result, err := Reseal(ctx, owner, ring2, ResealOptions{})
	if err != nil {
		t.Fatal(err)
	}

	creds := readCredentials(t, owner)
	if len(creds) != len(secrets) {
		t.Fatalf("%d credentials, want %d", len(creds), len(secrets))
	}
	for _, c := range creds {
		if c.keyID != "k2" {
			t.Fatalf("credential %s is under key %s, want k2", c.ref, c.keyID)
		}
		got, err := ring2.Open(c.ref, gateway.Sealed{Ciphertext: c.ciphertext, Nonce: c.nonce, KeyID: c.keyID})
		if err != nil || got != secrets[c.ref] {
			t.Fatalf("credential %s opens to %q, %v", c.ref, got, err)
		}
	}
	for person, address := range map[string]string{ada: "ada@example.com", lee: "lee@example.com"} {
		hash := "lee-hash"
		if address == "ada@example.com" {
			hash = "ada-hash"
		}
		checkPersonOpen(t, readPerson(t, owner, person), ring2, hash, address, "k2")
	}

	want := map[string]int64{
		StoreCredentials: int64(len(secrets)),
		StorePasswords:   2,
		StoreEmails:      2,
	}
	for store, n := range want {
		if got := storeCount(t, result, store); got.Resealed != n || got.Current != 0 || got.Unknown != 0 || got.Failed != 0 {
			t.Fatalf("%s tally = %+v, want resealed=%d and nothing else", store, got, n)
		}
	}
	counts := reseedAuditCounts(t, owner)
	for store, n := range want {
		if counts[store] != n {
			t.Fatalf("audit counts for %s = %d, want %d", store, counts[store], n)
		}
	}

	before := reseedAuditCounts(t, owner)
	again, err := Reseal(ctx, owner, ring2, ResealOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, count := range again.Stores {
		if count.Resealed != 0 {
			t.Fatalf("second run resealed %s: %+v", count.Store, count)
		}
	}
	if after := reseedAuditCounts(t, owner); !maps.Equal(after, before) {
		t.Fatalf("second run wrote audit events: %v", after)
	}
}

// funcWriter adapts a function to io.Writer.
type funcWriter func([]byte) (int, error)

func (f funcWriter) Write(p []byte) (int, error) { return f(p) }

// Stopping a reseal mid-run leaves the committed batches under the new key and
// everything else untouched and openable; a second run finishes the job.
func TestResealStoppedHalfWayFinishesOnTheNextRun(t *testing.T) {
	owner := newResealDB(t)
	ring1 := reseedRing(t, "k1")
	secrets := map[string]string{"own_one": "sk-one-secret", "own_two": "sk-two-secret"}
	for ref, secret := range secrets {
		seedCredential(t, owner, ring1, ref, secret)
	}
	ada := seedPerson(t, owner, ring1, "ada@example.com", "ada-hash")

	ring2 := reseedRing(t, "k2", "k1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := funcWriter(func(p []byte) (int, error) {
		cancel() // stop right after the first committed batch reports
		return len(p), nil
	})
	if _, err := Reseal(ctx, owner, ring2, ResealOptions{BatchSize: 1, Out: out}); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped reseal error = %v, want context.Canceled", err)
	}

	var moved int
	for _, c := range readCredentials(t, owner) {
		got, err := ring2.Open(c.ref, gateway.Sealed{Ciphertext: c.ciphertext, Nonce: c.nonce, KeyID: c.keyID})
		if err != nil || got != secrets[c.ref] {
			t.Fatalf("credential %s opens to %q, %v", c.ref, got, err)
		}
		if c.keyID == "k2" {
			moved++
		}
	}
	if moved == 0 || moved == len(secrets) {
		t.Fatalf("%d of %d credentials moved; the stop must land between batches", moved, len(secrets))
	}
	person := readPerson(t, owner, ada)
	if prefix, _, _ := strings.Cut(person.passwordHash, ":"); prefix != "k1" {
		t.Fatal("a batch past the stop point committed")
	}

	if _, err := Reseal(context.Background(), owner, ring2, ResealOptions{BatchSize: 1}); err != nil {
		t.Fatal(err)
	}
	for _, c := range readCredentials(t, owner) {
		if c.keyID != "k2" {
			t.Fatalf("credential %s is still under %s after the finishing run", c.ref, c.keyID)
		}
		if got, err := ring2.Open(c.ref, gateway.Sealed{Ciphertext: c.ciphertext, Nonce: c.nonce, KeyID: c.keyID}); err != nil || got != secrets[c.ref] {
			t.Fatalf("credential %s opens to %q, %v", c.ref, got, err)
		}
	}
	checkPersonOpen(t, readPerson(t, owner, ada), ring2, "ada-hash", "ada@example.com", "k2")
}

// After a reseal the old key can be dropped: everything opens and the person
// is still found by address through the new key's index hashes alone.
func TestResealedRowsOpenWithTheOldKeyRemoved(t *testing.T) {
	owner := newResealDB(t)
	ring1 := reseedRing(t, "k1")
	seedCredential(t, owner, ring1, "own_alpha", "sk-alpha-secret")
	ada := seedPerson(t, owner, ring1, "ada@example.com", "ada-hash")

	ring2 := reseedRing(t, "k2", "k1")
	if _, err := Reseal(context.Background(), owner, ring2, ResealOptions{}); err != nil {
		t.Fatal(err)
	}

	only2 := reseedRing(t, "k2")
	for _, c := range readCredentials(t, owner) {
		if got, err := only2.Open(c.ref, gateway.Sealed{Ciphertext: c.ciphertext, Nonce: c.nonce, KeyID: c.keyID}); err != nil || got != "sk-alpha-secret" {
			t.Fatalf("credential %s opens to %q, %v without the old key", c.ref, got, err)
		}
	}
	person := readPerson(t, owner, ada)
	user, err := identity.ParseUserID(person.id)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := NewPasswordSealer(only2).Open(identity.PasswordRef(user), person.passwordHash); err != nil || got != "ada-hash" {
		t.Fatalf("password opens to %q, %v without the old key", got, err)
	}

	var found int
	if err := owner.QueryRow(context.Background(), `
		SELECT count(*) FROM identity.users WHERE email_hash = ANY($1)`,
		NewEmailSealer(only2).IndexHashes("ada@example.com")).Scan(&found); err != nil {
		t.Fatal(err)
	}
	if found != 1 {
		t.Fatalf("index hash lookup found %d people, want 1", found)
	}
	if got, err := NewEmailSealer(only2).Open(person.email, person.emailHash); err != nil || got != "ada@example.com" {
		t.Fatalf("email opens to %q, %v without the old key", got, err)
	}
}

// Rows sealed with a key the ring does not hold are counted, never changed,
// and reported by UnknownKeyRows; with the zero ring every sealed row is
// unknown.
func TestResealLeavesRowsOfAnUnloadedKey(t *testing.T) {
	owner := newResealDB(t)
	ctx := context.Background()
	ring0 := reseedRing(t, "k0")
	ring1 := reseedRing(t, "k1")
	seedCredential(t, owner, ring0, "own_gone", "sk-gone-secret")
	seedCredential(t, owner, ring1, "own_here", "sk-here-secret")
	gone := seedPerson(t, owner, ring0, "gone@example.com", "gone-hash")
	here := seedPerson(t, owner, ring1, "here@example.com", "here-hash")

	credsBefore, peopleBefore := snapshotRows(t, owner, gone, here)

	ring2 := reseedRing(t, "k2", "k1")
	result, err := Reseal(ctx, owner, ring2, ResealOptions{})
	if err != nil {
		t.Fatal(err)
	}
	credsAfter, peopleAfter := snapshotRows(t, owner, gone, here)
	if credsAfter["own_gone"] != credsBefore["own_gone"] {
		t.Fatal("a row of the unloaded key changed")
	}
	if peopleAfter[gone] != peopleBefore[gone] {
		t.Fatal("a person of the unloaded key changed")
	}
	for store, want := range map[string]int64{StoreCredentials: 1, StorePasswords: 1, StoreEmails: 1} {
		count := storeCount(t, result, store)
		if count.Unknown != want || count.Resealed != 1 || len(count.UnknownKeyIDs) != 1 || count.UnknownKeyIDs[0] != "k0" {
			t.Fatalf("%s tally = %+v, want unknown=1 under k0 and one row resealed", store, count)
		}
	}

	dry, err := Reseal(ctx, owner, reseedRing(t, "k2", "k1"), ResealOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	for store := range map[string]int64{StoreCredentials: 1, StorePasswords: 1, StoreEmails: 1} {
		count := storeCount(t, dry, store)
		if count.Unknown != 1 || len(count.UnknownKeyIDs) != 1 || count.UnknownKeyIDs[0] != "k0" {
			t.Fatalf("dry %s tally = %+v, want the same unknown row under k0", store, count)
		}
	}
	credsAfter, peopleAfter = snapshotRows(t, owner, gone, here)
	if credsAfter["own_gone"] != credsBefore["own_gone"] || peopleAfter[gone] != peopleBefore[gone] {
		t.Fatal("the dry run changed rows")
	}

	unknown, err := UnknownKeyRows(ctx, owner, ring2)
	if err != nil {
		t.Fatal(err)
	}
	for _, count := range unknown {
		if count.Unknown != 1 || len(count.UnknownKeyIDs) != 1 || count.UnknownKeyIDs[0] != "k0" {
			t.Fatalf("unknown rows for %s = %+v, want the k0 row alone", count.Store, count)
		}
	}

	unknown, err = UnknownKeyRows(ctx, owner, gateway.Keyring{})
	if err != nil {
		t.Fatal(err)
	}
	for _, count := range unknown {
		if count.Unknown != 2 {
			t.Fatalf("zero-ring unknown rows for %s = %+v, want every sealed row", count.Store, count)
		}
		if strings.Join(count.UnknownKeyIDs, ",") != "k0,k2" {
			t.Fatalf("zero-ring unknown ids for %s = %v", count.Store, count.UnknownKeyIDs)
		}
	}
}

// snapshotRows captures every credential and the named people's sealed columns
// as comparable text.
func snapshotRows(t *testing.T, owner *pgxpool.Pool, people ...string) (map[string]string, map[string]string) {
	t.Helper()
	creds := map[string]string{}
	for _, c := range readCredentials(t, owner) {
		creds[c.ref] = string(c.ciphertext) + string(c.nonce) + c.keyID
	}
	snapshots := map[string]string{}
	for _, id := range people {
		p := readPerson(t, owner, id)
		snapshots[id] = p.passwordHash + p.email + string(p.emailHash) + p.emailHashKeyID
	}
	return creds, snapshots
}

// The progress and summary lines carry counts, store names and key ids only:
// no plaintext, no address, no ref, no user id, no sealed bytes.
func TestResealOutputCarriesCountsAndKeyIDsOnly(t *testing.T) {
	owner := newResealDB(t)
	ring1 := reseedRing(t, "k1")
	seedCredential(t, owner, ring1, "own_secret_ref", "sk-plaintext-secret")
	ada := seedPerson(t, owner, ring1, "ada@example.com", "ada-hash")

	var out bytes.Buffer
	if _, err := Reseal(context.Background(), owner, reseedRing(t, "k2", "k1"), ResealOptions{Out: &out}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "gateway.credentials: resealed=1 current=0 unknown_key=0 failed=0") {
		t.Fatalf("summary lines:\n%s", out.String())
	}

	var banned []string
	for _, c := range readCredentials(t, owner) {
		banned = append(banned,
			base64.StdEncoding.EncodeToString(c.ciphertext),
			base64.StdEncoding.EncodeToString(c.nonce),
		)
	}
	person := readPerson(t, owner, ada)
	banned = append(banned,
		"sk-plaintext-secret", "ada@example.com", "own_secret_ref", ada,
		base64.StdEncoding.EncodeToString([]byte(person.passwordHash)),
	)
	for _, c := range []string{person.passwordHash, person.email} {
		for _, part := range strings.Split(c, ":")[1:] {
			banned = append(banned, part)
		}
	}
	for _, secret := range banned {
		if secret != "" && strings.Contains(out.String(), secret) {
			t.Fatalf("output carries %q:\n%s", secret, out.String())
		}
	}
}

// A dry run changes nothing and reports, per store, exactly the rows a real
// run then resealed.
func TestResealDryRunCountsWhatTheRealRunReseals(t *testing.T) {
	owner := newResealDB(t)
	ctx := context.Background()
	ring1 := reseedRing(t, "k1")
	for ref, secret := range map[string]string{"own_alpha": "sk-alpha-secret", "own_beta": "sk-beta-secret"} {
		seedCredential(t, owner, ring1, ref, secret)
	}
	ada := seedPerson(t, owner, ring1, "ada@example.com", "ada-hash")

	ring2 := reseedRing(t, "k2", "k1")
	credsBefore, peopleBefore := snapshotRows(t, owner, ada)
	dry, err := Reseal(ctx, owner, ring2, ResealOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{StoreCredentials: 2, StorePasswords: 1, StoreEmails: 1}
	for store, n := range want {
		if got := storeCount(t, dry, store); got.Resealed != n || got.Current != 0 || got.Unknown != 0 || got.Failed != 0 {
			t.Fatalf("dry %s tally = %+v, want would_reseal=%d alone", store, got, n)
		}
	}
	credsAfter, peopleAfter := snapshotRows(t, owner, ada)
	if !maps.Equal(credsAfter, credsBefore) || !maps.Equal(peopleAfter, peopleBefore) {
		t.Fatal("the dry run changed rows")
	}

	result, err := Reseal(ctx, owner, ring2, ResealOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for store, n := range want {
		if got := storeCount(t, result, store); got.Resealed != n {
			t.Fatalf("%s resealed %d, want the dry run's %d", store, got.Resealed, n)
		}
	}
}

// A row whose stored address is under the current key while its index hash is
// under an older key still needs a reseal: the dry run counts it, and the run
// moves the hash to the current key.
func TestResealMovesAnOldIndexHashToTheCurrentKey(t *testing.T) {
	owner := newResealDB(t)
	ctx := context.Background()
	ring1 := reseedRing(t, "k1")
	ring2 := reseedRing(t, "k2", "k1")
	ada := seedPerson(t, owner, ring1, "mixed@example.com", "mixed-hash")
	if _, err := Reseal(ctx, owner, ring2, ResealOptions{}); err != nil {
		t.Fatal(err)
	}

	// Rebuild the row the way a partial rotation leaves it: the address sealed
	// under k2, the index hash still made under k1.
	hashKeyID, hash, err := ring1.IndexMAC([]byte(identity.NormalEmail("mixed@example.com")))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := ring2.Seal(identity.EmailRef(hash), "mixed@example.com")
	if err != nil {
		t.Fatal(err)
	}
	stored := strings.Join([]string{
		sealed.KeyID,
		base64.StdEncoding.EncodeToString(sealed.Nonce),
		base64.StdEncoding.EncodeToString(sealed.Ciphertext),
	}, ":")
	if _, err := owner.Exec(ctx, `
		UPDATE identity.users
		SET email = $2, email_hash = $3, email_hash_key_id = $4
		WHERE id = $1`, ada, stored, hash, hashKeyID); err != nil {
		t.Fatal(err)
	}

	dry, err := Reseal(ctx, owner, ring2, ResealOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := storeCount(t, dry, StoreEmails); got.Resealed != 1 || got.Current != 0 || got.Unknown != 0 || got.Failed != 0 {
		t.Fatalf("dry email tally = %+v, want would_reseal=1 alone", got)
	}

	result, err := Reseal(ctx, owner, ring2, ResealOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := storeCount(t, result, StoreEmails); got.Resealed != 1 || got.Failed != 0 {
		t.Fatalf("email tally = %+v, want the mixed row resealed", got)
	}
	checkPersonOpen(t, readPerson(t, owner, ada), ring2, "mixed-hash", "mixed@example.com", "k2")
}

// A credential a loaded key cannot open is counted Failed, left unchanged, and
// does not stop the other rows; the next run ends the same way instead of
// looping on it.
func TestResealCountsACorruptRowFailedAndMovesOn(t *testing.T) {
	owner := newResealDB(t)
	ctx := context.Background()
	ring1 := reseedRing(t, "k1")
	seedCredential(t, owner, ring1, "own_broken", "sk-broken-secret")
	seedCredential(t, owner, ring1, "own_whole", "sk-whole-secret")

	// Flip one ciphertext byte: the row still names k1, but no key opens it.
	var ciphertext []byte
	if err := owner.QueryRow(ctx, `
		SELECT ciphertext FROM gateway.credentials WHERE ref = 'own_broken'`).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	ciphertext[3] ^= 0xff
	if _, err := owner.Exec(ctx, `
		UPDATE gateway.credentials SET ciphertext = $1 WHERE ref = 'own_broken'`, ciphertext); err != nil {
		t.Fatal(err)
	}
	credsBefore, _ := snapshotRows(t, owner)

	ring2 := reseedRing(t, "k2", "k1")
	result, err := Reseal(ctx, owner, ring2, ResealOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := storeCount(t, result, StoreCredentials); got.Resealed != 1 || got.Failed != 1 || got.Unknown != 0 {
		t.Fatalf("credential tally = %+v, want one row resealed and the corrupt one failed", got)
	}
	credsAfter, _ := snapshotRows(t, owner)
	if credsAfter["own_broken"] != credsBefore["own_broken"] {
		t.Fatal("the corrupt row changed")
	}
	for _, c := range readCredentials(t, owner) {
		if c.ref != "own_whole" {
			continue
		}
		if c.keyID != "k2" {
			t.Fatalf("the whole row is under key %s, want k2", c.keyID)
		}
		if got, err := ring2.Open(c.ref, gateway.Sealed{Ciphertext: c.ciphertext, Nonce: c.nonce, KeyID: c.keyID}); err != nil || got != "sk-whole-secret" {
			t.Fatalf("the whole row opens to %q, %v", got, err)
		}
	}

	again, err := Reseal(ctx, owner, ring2, ResealOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := storeCount(t, again, StoreCredentials); got.Resealed != 0 || got.Failed != 1 {
		t.Fatalf("second credential tally = %+v, want the corrupt row failed again and no loop", got)
	}
}

// A stored email outside the sealed format is counted Failed, never printed
// and never touched, while the well-formed rows still resealed.
func TestResealCountsAMalformedEmailFailedAndNeverPrintsIt(t *testing.T) {
	owner := newResealDB(t)
	ctx := context.Background()
	ring1 := reseedRing(t, "k1")
	seedPerson(t, owner, ring1, "ada@example.com", "ada-hash")
	if _, err := owner.Exec(ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
		VALUES ('not-a-sealed-email', $1, 'k1', 'Mal', (SELECT id FROM identity.roles WHERE key = 'owner'), now())`,
		bytes.Repeat([]byte{7}, 32)); err != nil {
		t.Fatal(err)
	}

	ring2 := reseedRing(t, "k2", "k1")
	var out bytes.Buffer
	result, err := Reseal(ctx, owner, ring2, ResealOptions{Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	if got := storeCount(t, result, StoreEmails); got.Resealed != 1 || got.Failed != 1 || got.Unknown != 0 {
		t.Fatalf("email tally = %+v, want one row resealed and the malformed one failed", got)
	}
	if !strings.Contains(out.String(), StoreEmails+": resealed=1 current=0 unknown_key=0 failed=1") {
		t.Fatalf("summary lines:\n%s", out.String())
	}
	if strings.Contains(out.String(), "not-a-sealed-email") {
		t.Fatal("the malformed address was printed")
	}
	var stored string
	var hashKeyID *string
	if err := owner.QueryRow(ctx, `
		SELECT email, email_hash_key_id FROM identity.users WHERE name = 'Mal'`).Scan(&stored, &hashKeyID); err != nil {
		t.Fatal(err)
	}
	if stored != "not-a-sealed-email" || hashKeyID == nil || *hashKeyID != "k1" {
		t.Fatal("the malformed row changed")
	}
}

// Two people sharing an address across key generations collide on the index
// hash when the second is resealed: that row is counted Failed and left as it
// is, the batch still commits the other rows, and the next run ends the same
// way instead of wedging on the same page.
func TestResealCountsAnIndexHashCollisionFailedAndMovesOn(t *testing.T) {
	owner := newResealDB(t)
	ctx := context.Background()
	ring1 := reseedRing(t, "k1")
	ring2 := reseedRing(t, "k2", "k1")
	seedPerson(t, owner, ring2, "dup@example.com", "dup-hash")
	old := seedPerson(t, owner, ring1, "dup@example.com", "old-hash")
	oldBefore := readPerson(t, owner, old)

	result, err := Reseal(ctx, owner, ring2, ResealOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := storeCount(t, result, StoreEmails); got.Resealed != 0 || got.Current != 1 || got.Failed != 1 {
		t.Fatalf("email tally = %+v, want the k2 row current and the k1 row failed", got)
	}
	if got := storeCount(t, result, StorePasswords); got.Resealed != 1 {
		t.Fatalf("password tally = %+v, want the k1 password resealed", got)
	}
	oldAfter := readPerson(t, owner, old)
	if oldAfter.email != oldBefore.email || !bytes.Equal(oldAfter.emailHash, oldBefore.emailHash) ||
		oldAfter.emailHashKeyID != oldBefore.emailHashKeyID {
		t.Fatal("the colliding row's email columns changed")
	}

	again, err := Reseal(ctx, owner, ring2, ResealOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := storeCount(t, again, StoreEmails); got.Failed != 1 {
		t.Fatalf("second email tally = %+v, want the collision failed again", got)
	}
}
