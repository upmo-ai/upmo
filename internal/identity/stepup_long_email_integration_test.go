//go:build integration

package identity_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/testutil/sealer"
)

// The password step-up sends no address to the worker, so a person whose
// stored address is over 320 bytes still passes it with the right password
// and fails it with a wrong one.
func TestStepUpPasswordVerifyWithLongStoredEmail(t *testing.T) {
	r := newStepUpRig(t)
	long := strings.Repeat("a", 309) + "@example.com"
	if len(long) != 321 {
		t.Fatalf("address is %d bytes", len(long))
	}
	var id string
	if err := r.owner.QueryRow(r.ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
		VALUES ($1, $2, $3, 'Cy', (SELECT id FROM identity.roles WHERE key = 'member'), now())
		RETURNING id::text`, sealUser(t, sealer.NewEmail(t), long)...).Scan(&id); err != nil {
		t.Fatal(err)
	}
	cy, err := domain.ParseUserID(id)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := domain.HashPassword(stepUpPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.owner.Exec(r.ctx, `UPDATE identity.users SET password_hash = $2 WHERE id = $1`, cy, sealer.Seal(t, cy, hash)); err != nil {
		t.Fatal(err)
	}
	cookie, _, err := r.passkeys.Sessions.Start(r.ctx, cy, "password", "")
	if err != nil {
		t.Fatal(err)
	}
	stale := r.when(-24, 11)
	r.at(t, cy, "password", stale)

	wrong, _ := json.Marshal(map[string]string{"password": "wrong horse battery"})
	if rec := call(r.mux, http.MethodPost, "/api/session/verify/password", cookie, wrong); rec.Code != http.StatusUnauthorized || rec.Body.String() != "{\"error\":\"Email or password is wrong\"}\n" {
		t.Fatalf("wrong verify = %d %q", rec.Code, rec.Body.String())
	}
	if method, signed := r.signedIn(t, cy); method != "password" || !signed.Equal(stale) {
		t.Fatalf("session after wrong verify = %s %s, want password %s", method, signed, stale)
	}

	right, _ := json.Marshal(map[string]string{"password": stepUpPassword})
	if rec := call(r.mux, http.MethodPost, "/api/session/verify/password", cookie, right); rec.Code != http.StatusNoContent {
		t.Fatalf("right verify = %d %q", rec.Code, rec.Body.String())
	}
	if method, signed := r.signedIn(t, cy); method != "password" || !signed.Equal(*r.clock) {
		t.Fatalf("session after right verify = %s %s, want password %s", method, signed, *r.clock)
	}
}
