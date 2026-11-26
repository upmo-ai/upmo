//go:build integration

package main

// The keys command drives the reseal engine the way an operator rotates keys:
// rows under a loaded previous key move to the current one, rows under a key
// that is not loaded and rows no loaded key can open are left byte-for-byte
// alone and make the command fail, and the output carries counts, store names
// and key ids only.

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/platform/brainops"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

// keyringFromEnv builds the ring the environment names, for seeding and for
// the direct keysCommand calls.
func keyringFromEnv(t *testing.T) gateway.Keyring {
	t.Helper()
	ring, err := gateway.KeyringFromEnv(os.LookupEnv, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	return ring
}

// storedCredential is one credential row as stored.
type storedCredential struct {
	ciphertext []byte
	nonce      []byte
	keyID      string
	wrapped    []byte
	wrapNonce  []byte
}

// sameStored reports whether two reads of one credential row are
// byte-for-byte equal.
func (c storedCredential) same(other storedCredential) bool {
	return c.keyID == other.keyID && bytes.Equal(c.ciphertext, other.ciphertext) && bytes.Equal(c.nonce, other.nonce) &&
		bytes.Equal(c.wrapped, other.wrapped) && bytes.Equal(c.wrapNonce, other.wrapNonce)
}

// readCredential reads one credential's sealed columns.
func readCredential(t *testing.T, owner *pgxpool.Pool, ref string) storedCredential {
	t.Helper()
	var c storedCredential
	if err := owner.QueryRow(context.Background(),
		`SELECT ciphertext, nonce, key_id, wrapped_key, wrap_nonce FROM gateway.credentials WHERE ref = $1`, ref,
	).Scan(&c.ciphertext, &c.nonce, &c.keyID, &c.wrapped, &c.wrapNonce); err != nil {
		t.Fatal(err)
	}
	return c
}

// seedKeyCredential seals one provider key under the key the environment
// names and stores it, the way a rotation leaves rows behind.
func seedKeyCredential(t *testing.T, owner *pgxpool.Pool, ref, secret string) {
	t.Helper()
	sealed, err := keyringFromEnv(t).SealCredential(context.Background(), ref, secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(context.Background(), `
		INSERT INTO gateway.credentials (ref, ciphertext, nonce, key_id, wrapped_key, wrap_nonce)
		VALUES ($1, $2, $3, $4, $5, $6)`, ref, sealed.Ciphertext, sealed.Nonce, sealed.KeyID, sealed.WrappedKey, sealed.WrapNonce); err != nil {
		t.Fatal(err)
	}
}

func TestKeysResealCommandMovesLoadedKeyRowsAndRefusesUnloadedOnes(t *testing.T) {
	t.Setenv("UPMO_ENV", "development")
	owner := pgtest.New(t)
	ctx := context.Background()
	t.Setenv("UPMO_DATABASE_URL", poolURL(t, owner))
	t.Setenv("UPMO_PUBLIC_URL", "http://localhost:5173")
	if err := command("migrate", nil); err != nil {
		t.Fatalf("command(migrate): %v", err)
	}

	rawKey := func(b byte) string {
		return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32))
	}
	t.Setenv(gateway.EnvMasterKey, rawKey(0x31))
	t.Setenv(gateway.EnvMasterKeyID, "k1")
	seedKeyCredential(t, owner, "own_here", "sk-here-secret")
	seedKeyCredential(t, owner, "own_broken", "sk-broken-secret")
	// Flip one ciphertext byte: the row still names k1, but no loaded key opens it.
	broken := readCredential(t, owner, "own_broken")
	broken.ciphertext[3] ^= 0xff
	if _, err := owner.Exec(ctx, `
		UPDATE gateway.credentials SET ciphertext = $1 WHERE ref = 'own_broken'`, broken.ciphertext); err != nil {
		t.Fatal(err)
	}
	t.Setenv(gateway.EnvMasterKey, rawKey(0x30))
	t.Setenv(gateway.EnvMasterKeyID, "k0")
	seedKeyCredential(t, owner, "own_gone", "sk-gone-secret")

	// The loaded ring is a rotation: k2 current, k1 previous, k0 gone.
	t.Setenv(gateway.EnvMasterKey, rawKey(0x32))
	t.Setenv(gateway.EnvMasterKeyID, "k2")
	t.Setenv(gateway.EnvMasterKeyPrevious+"k1", rawKey(0x31))

	hereBefore, goneBefore := readCredential(t, owner, "own_here"), readCredential(t, owner, "own_gone")
	brokenBefore := readCredential(t, owner, "own_broken")
	err := command("keys", []string{"reseal", "--dry-run"})
	if err == nil || err.Error() != "brain: keys reseal left 1 rows unchanged: 1 under key ids that are not loaded (k0)" {
		t.Fatalf("dry-run error = %v, want the k0 row alone", err)
	}
	if here, gone, broken := readCredential(t, owner, "own_here"), readCredential(t, owner, "own_gone"), readCredential(t, owner, "own_broken"); !here.same(hereBefore) || !gone.same(goneBefore) || !broken.same(brokenBefore) {
		t.Fatal("the dry run changed rows")
	}

	err = command("keys", []string{"reseal"})
	if err == nil || err.Error() != "brain: keys reseal left 2 rows unchanged: 1 under key ids that are not loaded (k0), 1 that could not be opened" {
		t.Fatalf("reseal error = %v, want the k0 row and the corrupt row reported as left behind", err)
	}
	if gone, broken := readCredential(t, owner, "own_gone"), readCredential(t, owner, "own_broken"); !gone.same(goneBefore) || !broken.same(brokenBefore) {
		t.Fatal("a left-behind row changed")
	}
	t.Setenv(gateway.EnvMasterKeyPrevious+"k1", "")
	only2 := keyringFromEnv(t)
	here := readCredential(t, owner, "own_here")
	if here.keyID != "k2" {
		t.Fatalf("the k1 row is under key %s after the reseal, want k2", here.keyID)
	}
	if got, err := only2.OpenCredential("own_here", gateway.Sealed{Ciphertext: here.ciphertext, Nonce: here.nonce, KeyID: here.keyID, WrappedKey: here.wrapped, WrapNonce: here.wrapNonce}); err != nil || got != "sk-here-secret" {
		t.Fatalf("the resealed row opens to %q, %v with the k2-only ring", got, err)
	}
	var events int
	if err := owner.QueryRow(ctx, `
		SELECT count(*) FROM audit.pending
		WHERE action = 'keys.reseal'
		  AND changes->'values'->'store'->>'to' = 'gateway.credentials'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("%d keys.reseal events for gateway.credentials, want 1", events)
	}

	// Load k1 again: with it gone the corrupt k1 row would be unknown, not
	// failed, and the next runs must exercise the failed-rows message.
	t.Setenv(gateway.EnvMasterKeyPrevious+"k1", rawKey(0x31))
	if _, err := owner.Exec(ctx, `DELETE FROM gateway.credentials WHERE ref = 'own_gone'`); err != nil {
		t.Fatal(err)
	}
	err = command("keys", []string{"reseal"})
	if err == nil || err.Error() != "brain: keys reseal left 1 rows unchanged: 1 that could not be opened" {
		t.Fatalf("reseal error = %v, want only the corrupt row reported", err)
	}
	if _, err := owner.Exec(ctx, `DELETE FROM gateway.credentials WHERE ref = 'own_broken'`); err != nil {
		t.Fatal(err)
	}
	if err := command("keys", []string{"reseal"}); err != nil {
		t.Fatalf("reseal without left-behind rows = %v", err)
	}
}

// The progress and summary lines go to the writer the command is given and
// carry counts, store names and key ids only: no ref, no plaintext, no
// ciphertext.
func TestKeysResealOutputCarriesCountsAndKeyIDsOnly(t *testing.T) {
	t.Setenv("UPMO_ENV", "development")
	owner := pgtest.New(t)
	ctx := context.Background()
	dbURL := poolURL(t, owner)
	t.Setenv("UPMO_DATABASE_URL", dbURL)
	t.Setenv("UPMO_PUBLIC_URL", "http://localhost:5173")
	if err := command("migrate", nil); err != nil {
		t.Fatalf("command(migrate): %v", err)
	}

	rawKey := func(b byte) string {
		return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32))
	}
	t.Setenv(gateway.EnvMasterKey, rawKey(0x31))
	t.Setenv(gateway.EnvMasterKeyID, "k1")
	seedKeyCredential(t, owner, "own_secret_ref", "sk-plaintext-secret")
	t.Setenv(gateway.EnvMasterKey, rawKey(0x32))
	t.Setenv(gateway.EnvMasterKeyID, "k2")
	t.Setenv(gateway.EnvMasterKeyPrevious+"k1", rawKey(0x31))

	pool, err := brainops.OpenCommandPool(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var out bytes.Buffer
	if err := keysCommand(ctx, pool, keyringFromEnv(t), []string{"reseal"}, &out); err != nil {
		t.Fatalf("keysCommand: %v", err)
	}
	if !strings.Contains(out.String(), "gateway.credentials: resealed=1 current=0 unknown_key=0 failed=0") {
		t.Fatalf("summary lines:\n%s", out.String())
	}
	sealed := readCredential(t, owner, "own_secret_ref")
	for _, banned := range []string{
		"own_secret_ref", "sk-plaintext-secret",
		base64.StdEncoding.EncodeToString(sealed.ciphertext),
		base64.StdEncoding.EncodeToString(sealed.nonce),
	} {
		if strings.Contains(out.String(), banned) {
			t.Fatalf("output carries %q:\n%s", banned, out.String())
		}
	}
}

// The argument checks need no database: the subcommand, the master key and the
// batch bounds are all refused before anything opens.
func TestKeysResealArgumentChecks(t *testing.T) {
	ctx := context.Background()
	if err := keysCommand(ctx, nil, gateway.Keyring{}, nil, os.Stdout); err == nil ||
		err.Error() != "brain: keys needs a subcommand: reseal" {
		t.Fatalf("missing subcommand = %v", err)
	}
	if err := keysCommand(ctx, nil, gateway.Keyring{}, []string{"reseal"}, os.Stdout); err == nil ||
		err.Error() != "brain: keys reseal needs UPMO_MASTER_KEY" {
		t.Fatalf("missing master key = %v", err)
	}
	for _, size := range []string{"0", "10001"} {
		if err := keysCommand(ctx, nil, gateway.Keyring{}, []string{"reseal", "--batch-size", size}, os.Stdout); err == nil ||
			!strings.Contains(err.Error(), "--batch-size") {
			t.Fatalf("--batch-size %s = %v, want the bound named", size, err)
		}
	}
}
