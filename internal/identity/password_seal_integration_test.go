//go:build integration

package identity_test

// A stored password hash is sealed with the master keyring, so a database
// dump holds ciphertext only. These tests read the stored value through the
// owner pool and prove the app round-trips it and refuses plain values.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/identity/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
	"github.com/upmo-ai/upmo/internal/testutil/sealer"
)

// sealKit is the password use case over one database with a real sealer,
// reachable as both the owner and upmo_app.
type sealKit struct {
	owner     *pgxpool.Pool
	pool      *pgxpool.Pool
	passwords app.Passwords
	ring      sealer.Sealer
	ctx       context.Context
}

func newSealKit(t *testing.T) *sealKit {
	t.Helper()
	owner, ctx := upOwner(t)
	pool := pgtest.App(t, owner)
	ring := sealer.New(t)
	passwords := identity.Passwords(pool, identity.Sessions(pool, domain.IdleTimeout, domain.MaxAge))
	passwords.Sealer = ring
	return &sealKit{owner: owner, pool: pool, passwords: passwords, ring: ring, ctx: ctx}
}

func (k *sealKit) seed(t *testing.T, email string) domain.UserID {
	t.Helper()
	var id string
	if err := k.owner.QueryRow(k.ctx, `
		INSERT INTO identity.users (email, name, role_id, joined_at)
		VALUES ($1, 'Ada', (SELECT id FROM identity.roles WHERE key = 'owner'), now())
		RETURNING id::text`, email).Scan(&id); err != nil {
		t.Fatal(err)
	}
	user, err := domain.ParseUserID(id)
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func TestPasswordIsStoredSealed(t *testing.T) {
	k := newSealKit(t)
	user := k.seed(t, "ada@example.com")
	const password = "correct horse battery"
	if err := k.passwords.Set(k.ctx, user, "", "ada@example.com", "Ada", password); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := k.owner.QueryRow(k.ctx, `SELECT password_hash FROM identity.users WHERE id = $1`, user).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(stored, "$argon2id$") {
		t.Fatalf("stored hash is a plain argon2id string: %s", stored)
	}
	parts := strings.Split(stored, ":")
	if len(parts) != 3 || parts[0] != "k1" {
		t.Fatalf("stored hash = %s, want <key id>:<nonce>:<ciphertext>", stored)
	}
	// PasswordAccount returns what is stored, and the sealer opens it to a
	// hash that verifies.
	_, sealed, _, found, err := (postgres.Passkeys{Pool: k.pool}).PasswordAccount(k.ctx, "ada@example.com")
	if err != nil || !found || sealed != stored {
		t.Fatalf("account %v %v %q, want the stored value", found, err, sealed)
	}
	hash, err := k.ring.Open(identity.PasswordRef(user), sealed)
	if err != nil {
		t.Fatal(err)
	}
	if ok, rehash, err := domain.VerifyPassword(password, hash); err != nil || !ok || rehash {
		t.Fatalf("unsealed hash verifies %v %v %v", ok, rehash, err)
	}
	token, _, _, err := k.passwords.SignIn(k.ctx, "ada@example.com", password, "198.51.100.20", "")
	if err != nil || token == "" {
		t.Fatalf("sign-in %q %v", token, err)
	}
	if _, _, _, err := k.passwords.SignIn(k.ctx, "ada@example.com", "not the password!!", "198.51.100.20", ""); !errors.Is(err, app.ErrWrong) {
		t.Fatalf("wrong password = %v, want ErrWrong", err)
	}
}

func TestPlainStoredHashIsRefusedAtSignIn(t *testing.T) {
	k := newSealKit(t)
	user := k.seed(t, "ada@example.com")
	hash, err := domain.HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	// A plain argon2id value planted by the database owner never opens: there
	// is no fallback to unsealed values.
	if _, err := k.owner.Exec(k.ctx, `UPDATE identity.users SET password_hash = $2 WHERE id = $1`, user, hash); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := k.passwords.SignIn(k.ctx, "ada@example.com", "correct horse battery", "198.51.100.20", ""); err == nil || errors.Is(err, app.ErrWrong) {
		t.Fatalf("a plain stored hash signed in: %v", err)
	}
}

func TestPasswordSetWithoutASealer(t *testing.T) {
	k := newSealKit(t)
	user := k.seed(t, "ada@example.com")
	k.passwords.Sealer = nil
	if err := k.passwords.Set(k.ctx, user, "", "ada@example.com", "Ada", "correct horse battery"); !errors.Is(err, identity.ErrNoSealer) {
		t.Fatalf("set = %v, want ErrNoSealer", err)
	}
	has, err := (postgres.Passkeys{Pool: k.pool}).HasPassword(k.ctx, user)
	if err != nil || has {
		t.Fatalf("has password %v %v, want nothing stored", has, err)
	}
}

func TestPasswordWithAnEmptyRing(t *testing.T) {
	k := newSealKit(t)
	user := k.seed(t, "ada@example.com")
	const password = "correct horse battery"
	if err := k.passwords.Set(k.ctx, user, "", "ada@example.com", "Ada", password); err != nil {
		t.Fatal(err)
	}
	k.passwords.Sealer = sealer.Sealer{}
	if err := k.passwords.Set(k.ctx, user, "", "ada@example.com", "Ada", password); !errors.Is(err, identity.ErrNoSealer) {
		t.Fatalf("set = %v, want ErrNoSealer", err)
	}
	// A stored hash cannot be opened, and the failure is not a wrong-password
	// answer.
	if _, _, _, err := k.passwords.SignIn(k.ctx, "ada@example.com", password, "198.51.100.20", ""); err == nil || errors.Is(err, app.ErrWrong) {
		t.Fatalf("sign-in with an empty ring = %v, want a failed lookup", err)
	}
}
