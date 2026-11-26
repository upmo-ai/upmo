//go:build integration

package identity_test

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/platform/brainops"
	"github.com/upmo-ai/upmo/internal/platform/keys"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/testutil/sealer"
	"github.com/upmo-ai/upmo/internal/testutil/workerkeys"
)

// Invariant 6 (4d1): brain finds and seals every address through the
// worker's index-mac and wrap. With that worker up, bootstrap, invite,
// recover and password sign-in work; with it stopped, each email lookup and
// write fails closed as keys: worker unavailable, writes no row, and a
// password sign-in says try again.
func TestBrainEmailThroughTheWorker(t *testing.T) {
	k := newEmailKit(t)
	ring := sealer.Ring(t)
	// Two workers over the same database and master: one checks passwords
	// and stays up, the other serves brain's email calls and is stopped.
	checks := workerkeys.Start(t, k.owner, ring)
	index := workerkeys.Start(t, k.owner, ring)
	emails := brainops.WorkerEmails{Keys: index.Client(t, svcsig.AudienceWorkerBrain), Ring: ring}
	mail := &sentMail{}
	members := identity.Members(k.pool, emails, "https://upmo.test", mail, nil)
	passkeys := k.passkeys(t, emails)
	passwords := identity.Passwords(k.pool, emails, k.sessions)
	passwords.Keys = sealer.PasswordsOn(t, checks)
	mux := http.NewServeMux()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	identity.MountSessions(mux, k.sessions, log, passwords.HasPassword)
	identity.MountPasskeys(mux, passkeys, passwords, nil)
	identity.MountMembers(mux, members, k.sessions)

	// Worker up. The rows are five fields, under the MAC the ring computes.
	if text, err := passkeys.Bootstrap(k.ctx, "First@Example.com", "First"); err != nil || text == "" {
		t.Fatalf("bootstrap = %q %v", text, err)
	}
	first := parseUser(t, userByEmail(t, k.owner, k.ctx, "first@example.com"))
	assertSealed(t, k.row(t, first), "First@Example.com")
	ada := parseUser(t, seedSealedOwner(t, k.owner, k.ctx, emails, "Ada@Example.com"))
	if got := userByEmail(t, k.owner, k.ctx, "ada@example.com"); got != ada.String() {
		t.Fatalf("ada by the ring's MAC = %s, want %s", got, ada)
	}
	hash, err := domain.HashPassword(emailPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k.owner.Exec(k.ctx, `UPDATE identity.users SET password_hash = $2 WHERE id = $1`, ada, sealer.Seal(t, ada, hash)); err != nil {
		t.Fatal(err)
	}
	cookie, session, err := k.sessions.Start(k.ctx, ada, "passkey", "")
	if err != nil {
		t.Fatal(err)
	}
	role := k.role(t, "member")
	bob, _, err := members.Invite(k.ctx, session, "Bob@Example.com", "Bob", role)
	if err != nil {
		t.Fatalf("invite = %v", err)
	}
	assertSealed(t, k.row(t, bob.ID), "Bob@Example.com")
	if token, err := members.Recover(k.ctx, " ADA@example.com "); err != nil || token == "" {
		t.Fatalf("recover = %q %v", token, err)
	}
	if token, _, _, err := passwords.SignIn(k.ctx, "ada@EXAMPLE.com", emailPassword, "192.0.2.1", ""); err != nil || token == "" {
		t.Fatalf("sign-in = %q %v", token, err)
	}
	// A wrong password runs the annotation lookup, and is wrong.
	if _, _, _, err := passwords.SignIn(k.ctx, "ada@example.com", "wrong horse battery", "192.0.2.1", ""); !errors.Is(err, app.ErrWrong) {
		t.Fatalf("wrong sign-in = %v, want ErrWrong", err)
	}
	sent := len(mail.sent)
	users := k.count(t, `SELECT count(*) FROM identity.users`)

	index.Stop()
	store := k.store(emails)
	now := time.Now()
	token := make([]byte, 32) // a link hash; nothing is stored
	for name, call := range map[string]func() error{
		"PasswordAccount": func() error { _, _, _, err := store.PasswordAccount(k.ctx, "ada@example.com"); return err },
		"PersonByEmail":   func() error { _, _, _, err := store.PersonByEmail(k.ctx, "ada@example.com"); return err },
		"UserByEmail":     func() error { _, _, err := store.UserByEmail(k.ctx, "ada@example.com"); return err },
		"InviteMember": func() error {
			_, err := store.InviteMember(k.ctx, session.TokenHash, "carol@example.com", "Carol", role, []byte("handle-of-carol"), token, now, now.Add(time.Hour))
			return err
		},
		"Bootstrap": func() error {
			_, err := store.Bootstrap(k.ctx, "dave@example.com", "Dave", token, now, now.Add(time.Hour))
			return err
		},
	} {
		err := call()
		if !errors.Is(err, keys.ErrUnavailable) || err.Error() != "keys: worker unavailable: try again" {
			t.Errorf("%s with the worker stopped = %v, want keys: worker unavailable", name, err)
		}
	}
	if _, err := members.Recover(k.ctx, "ada@example.com"); !errors.Is(err, keys.ErrUnavailable) {
		t.Errorf("recover with the worker stopped = %v", err)
	}
	if len(mail.sent) != sent {
		t.Errorf("mail sent with the worker stopped: %v", mail.sent[sent:])
	}

	// The password check itself still answers; the email lookup after it
	// fails, so both a right and a wrong password say try again.
	for _, password := range []string{emailPassword, "wrong horse battery"} {
		rec := postPassword(t, mux, http.MethodPost, "/api/sign-in/password", "", map[string]string{"email": "ada@example.com", "password": password})
		if rec.Code != http.StatusServiceUnavailable || rec.Body.String() != "{\"error\":\"try again\"}\n" {
			t.Errorf("sign-in with the worker stopped = %d %q, want 503 try again", rec.Code, rec.Body.String())
		}
	}
	body, err := json.Marshal(map[string]string{"email": "erin@example.com", "name": "Erin", "role_id": role.String()})
	if err != nil {
		t.Fatal(err)
	}
	if rec := call(mux, http.MethodPost, "/api/members", cookie, body); rec.Code != http.StatusInternalServerError || rec.Body.String() != "members" {
		t.Errorf("invite over http with the worker stopped = %d %q, want 500 members", rec.Code, rec.Body.String())
	}
	if n := k.count(t, `SELECT count(*) FROM identity.users`); n != users {
		t.Errorf("users with the worker stopped = %d, want %d", n, users)
	}
	if n := k.count(t, `SELECT count(*) FROM identity.users WHERE email !~ '^[A-Za-z0-9_]{1,32}:[^:]+:[^:]+:[^:]+:[^:]+$'`); n != 0 {
		t.Errorf("%d rows are not five fields", n)
	}
}
