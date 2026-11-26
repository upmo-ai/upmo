//go:build integration

package identity_test

import (
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/platform/keys"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/testutil/sealer"
	"github.com/upmo-ai/upmo/internal/testutil/workerkeys"
)

// Invariant 3: the worker spends exactly one hash on every path of a
// sign-in check. With every hash slot taken, a known, a disabled, a
// password-less and an unknown address are each refused as busy, so none
// of them skips the hash; with the slots free each answers.
func TestPasswordVerifySpendsOneHashOnEveryPath(t *testing.T) {
	owner, ctx := upOwner(t)
	emails := sealer.NewEmail(t)
	client := workerkeys.Start(t, owner, sealer.Ring(t)).Client(t, svcsig.AudienceWorkerBrain)
	const password = "correct horse battery"
	hash, err := domain.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	seed := func(email string, withPassword, disabled bool) {
		t.Helper()
		var id string
		if err := owner.QueryRow(ctx, `
			INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
			VALUES ($1, $2, $3, 'Person', (SELECT id FROM identity.roles WHERE key = 'owner'), now())
			RETURNING id::text`, sealUser(t, emails, email)...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		user, err := domain.ParseUserID(id)
		if err != nil {
			t.Fatal(err)
		}
		if withPassword {
			if _, err := owner.Exec(ctx, `UPDATE identity.users SET password_hash = $2 WHERE id = $1`, user, sealer.Seal(t, user, hash)); err != nil {
				t.Fatal(err)
			}
		}
		if disabled {
			if _, err := owner.Exec(ctx, `UPDATE identity.users SET disabled_at = now() WHERE id = $1`, user); err != nil {
				t.Fatal(err)
			}
		}
	}
	seed("ada@example.com", true, false)
	seed("gone@example.com", true, true)
	seed("bob@example.com", false, false)
	addresses := []string{"ada@example.com", "gone@example.com", "bob@example.com", "nobody@example.com"}

	release, ok := domain.FillHashGate()
	if !ok {
		t.Fatal("hash gate")
	}
	for _, address := range addresses {
		if match, _, err := client.VerifyPassword(ctx, address, password); !errors.Is(err, keys.ErrUnavailable) || match {
			t.Errorf("%s with every slot taken = %v %v, want busy", address, match, err)
		}
	}
	release()
	for _, address := range addresses {
		match, rehash, err := client.VerifyPassword(ctx, address, password)
		if err != nil || match != (address == "ada@example.com") || rehash {
			t.Errorf("%s = %v %v %v", address, match, rehash, err)
		}
	}
}
