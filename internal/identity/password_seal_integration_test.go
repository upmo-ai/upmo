//go:build integration

package identity_test

// A stored password hash is sealed under its own data key, which the worker
// wraps, so a database dump holds ciphertext only. Brain never opens it: the
// worker checks every password. These tests read the stored value through
// the owner pool and prove the app round-trips it through the real worker
// and refuses plain values.

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
	"github.com/upmo-ai/upmo/internal/testutil/workerkeys"
)

// sealKit is the password use case over one database and a real worker,
// reachable as both the owner and upmo_app.
type sealKit struct {
	owner     *pgxpool.Pool
	pool      *pgxpool.Pool
	passwords app.Passwords
	worker    *workerkeys.Worker
	emails    identity.EmailSealer
	ctx       context.Context
}

func newSealKit(t *testing.T) *sealKit {
	t.Helper()
	owner, ctx := upOwner(t)
	pool := pgtest.App(t, owner)
	emails := sealer.NewEmail(t)
	worker := workerkeys.Start(t, owner, sealer.Ring(t))
	passwords := identity.Passwords(pool, emails, identity.Sessions(pool, domain.IdleTimeout, domain.MaxAge))
	passwords.Keys = sealer.PasswordsOn(t, worker)
	return &sealKit{owner: owner, pool: pool, passwords: passwords, worker: worker, emails: emails, ctx: ctx}
}

func (k *sealKit) seed(t *testing.T, email string) domain.UserID {
	t.Helper()
	var id string
	if err := k.owner.QueryRow(k.ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
		VALUES ($1, $2, $3, 'Ada', (SELECT id FROM identity.roles WHERE key = 'owner'), now())
		RETURNING id::text`, sealUser(t, k.emails, email)...).Scan(&id); err != nil {
		t.Fatal(err)
	}
	user, err := domain.ParseUserID(id)
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func (k *sealKit) stored(t *testing.T, user domain.UserID) *string {
	t.Helper()
	var stored *string
	if err := k.owner.QueryRow(k.ctx, `SELECT password_hash FROM identity.users WHERE id = $1`, user).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	return stored
}

// Invariant 1: Set stores five fields under k1, never the PHC string, and
// the hash opens only for this person.
func TestPasswordIsStoredSealed(t *testing.T) {
	k := newSealKit(t)
	user := k.seed(t, "ada@example.com")
	const password = "correct horse battery"
	if err := k.passwords.Set(k.ctx, user, "", "ada@example.com", "Ada", password); err != nil {
		t.Fatal(err)
	}
	stored := *k.stored(t, user)
	if strings.Contains(stored, "$argon2id$") {
		t.Fatalf("stored hash holds a plain argon2id string: %s", stored)
	}
	parts := strings.Split(stored, ":")
	if len(parts) != 5 || parts[0] != "k1" {
		t.Fatalf("stored hash = %s, want five fields under k1", stored)
	}
	hash, err := sealer.Open(t, user, stored)
	if err != nil {
		t.Fatal(err)
	}
	if ok, rehash, err := domain.VerifyPassword(password, hash); err != nil || !ok || rehash {
		t.Fatalf("unsealed hash verifies %v %v %v", ok, rehash, err)
	}
	// PasswordAccount finds the person without reading the hash.
	found, _, ok, err := (postgres.Passkeys{Pool: k.pool, Emails: k.emails}).PasswordAccount(k.ctx, "ada@example.com")
	if err != nil || !ok || found != user {
		t.Fatalf("account %v %v %v, want Ada", found, ok, err)
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
	if _, _, _, err := k.passwords.SignIn(k.ctx, "ada@example.com", "correct horse battery", "198.51.100.20", ""); !errors.Is(err, domain.ErrBusy) {
		t.Fatalf("a plain stored hash = %v, want try again", err)
	}
}

func TestPasswordSetWithoutAKeyService(t *testing.T) {
	k := newSealKit(t)
	user := k.seed(t, "ada@example.com")
	k.passwords.Keys = nil
	if err := k.passwords.Set(k.ctx, user, "", "ada@example.com", "Ada", "correct horse battery"); !errors.Is(err, identity.ErrNoSealer) {
		t.Fatalf("set = %v, want ErrNoSealer", err)
	}
	if _, _, _, err := k.passwords.SignIn(k.ctx, "ada@example.com", "correct horse battery", "198.51.100.20", ""); !errors.Is(err, identity.ErrNoSealer) {
		t.Fatalf("sign-in = %v, want ErrNoSealer", err)
	}
	if stored := k.stored(t, user); stored != nil {
		t.Fatalf("stored %q, want nothing", *stored)
	}
}

// Invariant 9, the brain part: with the worker down, set and sign-in fail
// with try again, never a match, and nothing is stored.
func TestPasswordWithTheWorkerDown(t *testing.T) {
	k := newSealKit(t)
	user := k.seed(t, "ada@example.com")
	const password = "correct horse battery"
	if err := k.passwords.Set(k.ctx, user, "", "ada@example.com", "Ada", password); err != nil {
		t.Fatal(err)
	}
	before := *k.stored(t, user)
	k.worker.Stop()
	if err := k.passwords.Set(k.ctx, user, "", "ada@example.com", "Ada", "another horse battery"); !errors.Is(err, domain.ErrBusy) {
		t.Fatalf("set = %v, want try again", err)
	}
	if token, _, _, err := k.passwords.SignIn(k.ctx, "ada@example.com", password, "198.51.100.20", ""); !errors.Is(err, domain.ErrBusy) || token != "" {
		t.Fatalf("sign-in = %q %v, want try again", token, err)
	}
	if after := *k.stored(t, user); after != before {
		t.Fatal("the stored hash changed while the worker was down")
	}
	var sessions int
	if err := k.owner.QueryRow(k.ctx, `SELECT count(*) FROM identity.sessions WHERE user_id = $1`, user).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatalf("sessions = %d %v, want none", sessions, err)
	}
}
