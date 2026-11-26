//go:build integration

package identity_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/identity"
	httpapi "github.com/upmo-ai/upmo/internal/identity/adapters/http"
	"github.com/upmo-ai/upmo/internal/identity/adapters/webauthn"
	"github.com/upmo-ai/upmo/internal/platform/keys"
	"github.com/upmo-ai/upmo/internal/testutil/sealer"
	"github.com/upmo-ai/upmo/internal/testutil/workerkeys"
)

// webAuthnName is the user name a registration's options carry.
func webAuthnName(t *testing.T, options []byte) string {
	t.Helper()
	var payload struct {
		PublicKey struct {
			User struct {
				Name string `json:"name"`
			} `json:"user"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(options, &payload); err != nil {
		t.Fatal(err)
	}
	return payload.PublicKey.User.Name
}

// Invariant 8b: brain opens no stored address. Every address it shows or
// mails comes from the worker's email ops: the members list, the profile
// behind a password set and a step-up check, invite and reset mail to the
// person managed, recover mail to the address typed, and the WebAuthn user
// name at link and add-passkey start. With that worker stopped, each fails
// closed with its existing answer and shows or mails no address, and a
// passkey sign-in still works.
func TestBrainOpensNoEmail(t *testing.T) {
	k := newEmailKit(t)
	ring := sealer.Ring(t)
	// One worker checks passwords and stays up; the other opens addresses
	// and is stopped.
	checks := workerkeys.Start(t, k.owner, ring)
	opens := workerkeys.Start(t, k.owner, ring)
	emails := sealer.WorkerEmailOn(t, opens)
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
	store := k.store(emails)

	ada := parseUser(t, seedSealedOwner(t, k.owner, k.ctx, emails, "Ada@Example.com"))
	var carolID string
	if err := k.owner.QueryRow(k.ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
		VALUES ($1, $2, $3, 'Carol', (SELECT id FROM identity.roles WHERE key = 'member'), now())
		RETURNING id::text`, sealUser(t, emails, "Carol@Example.com")...).Scan(&carolID); err != nil {
		t.Fatal(err)
	}
	carol := parseUser(t, carolID)

	// Link start names the passkey with the link person's address.
	adaLink, err := passkeys.IssueLink(k.ctx, ada, "recover")
	if err != nil {
		t.Fatal(err)
	}
	_, options, err := passkeys.BeginLink(k.ctx, adaLink)
	if err != nil || webAuthnName(t, options) != "Ada@Example.com" {
		t.Fatalf("link start = %v, user name %q, want Ada@Example.com", err, webAuthnName(t, options))
	}
	soft, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	finishLink(t, mux, soft, adaLink)
	adaCookie, session, err := k.sessions.Start(k.ctx, ada, "passkey", "")
	if err != nil {
		t.Fatal(err)
	}

	// Invite mail goes to the address invited.
	role := k.role(t, "member")
	rec := call(mux, http.MethodPost, "/api/members", adaCookie, jsonBody(t, map[string]string{"email": "Bob@Example.com", "name": "Bob", "role_id": role.String()}))
	var invited struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Token string `json:"token"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &invited) != nil || invited.Email != "Bob@Example.com" {
		t.Fatalf("invite = %d %s", rec.Code, rec.Body.String())
	}
	if want := "invite Bob@Example.com https://upmo.test/login/link#" + invited.Token; mail.last() != want {
		t.Fatalf("invite mail = %q, want %q", mail.last(), want)
	}

	// The members list carries every address, sorted by it.
	rec = call(mux, http.MethodGet, "/api/members", adaCookie, nil)
	var listed []struct {
		Email string `json:"email"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &listed) != nil {
		t.Fatalf("members = %d %s", rec.Code, rec.Body.String())
	}
	var got []string
	for _, m := range listed {
		got = append(got, m.Email)
	}
	if want := []string{"Ada@Example.com", "Bob@Example.com", "Carol@Example.com"}; !slices.Equal(got, want) {
		t.Fatalf("members list = %v, want %v", got, want)
	}

	// The profile, a password set and a step-up check read the person's own
	// address.
	if email, name, err := store.Profile(k.ctx, ada, session.TokenHash); err != nil || email != "Ada@Example.com" || name != "Ada" {
		t.Fatalf("profile = %q %q %v", email, name, err)
	}
	const password = "tide copper leaflet lantern"
	if rec := postPassword(t, mux, http.MethodPut, "/api/password", adaCookie, map[string]string{"password": password}); rec.Code != http.StatusNoContent {
		t.Fatalf("password set = %d %s", rec.Code, rec.Body.String())
	}
	if rec := postPassword(t, mux, http.MethodPost, "/api/session/verify/password", adaCookie, map[string]string{"password": password}); rec.Code != http.StatusNoContent {
		t.Fatalf("step-up verify = %d %s", rec.Code, rec.Body.String())
	}

	// Invite and reset mail go to the person managed, never the actor.
	rec = call(mux, http.MethodPost, "/api/members/"+invited.ID+"/invite-link", adaCookie, nil)
	var token struct {
		Token string `json:"token"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &token) != nil {
		t.Fatalf("invite link = %d %s", rec.Code, rec.Body.String())
	}
	if want := "invite Bob@Example.com https://upmo.test/login/link#" + token.Token; mail.last() != want {
		t.Fatalf("invite link mail = %q, want %q", mail.last(), want)
	}
	rec = call(mux, http.MethodPost, "/api/members/"+carolID+"/reset-link", adaCookie, nil)
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &token) != nil {
		t.Fatalf("reset link = %d %s", rec.Code, rec.Body.String())
	}
	if want := "recover Carol@Example.com https://upmo.test/login/link#" + token.Token; mail.last() != want {
		t.Fatalf("reset link mail = %q, want %q", mail.last(), want)
	}
	// upmo recover mails the address typed (R14).
	recovered, err := members.Recover(k.ctx, " carol@EXAMPLE.com ")
	if err != nil {
		t.Fatal(err)
	}
	if want := "recover carol@EXAMPLE.com https://upmo.test/login/link#" + recovered; mail.last() != want {
		t.Fatalf("recover mail = %q, want %q", mail.last(), want)
	}

	// Add-passkey start names the passkey with the session person's address.
	if _, options, err := passkeys.BeginAdd(k.ctx, ada, session.TokenHash); err != nil || webAuthnName(t, options) != "Ada@Example.com" {
		t.Fatalf("add start = %v, user name %q", err, webAuthnName(t, options))
	}
	if rec := call(mux, http.MethodPost, "/api/passkeys/options", adaCookie, nil); rec.Code != http.StatusOK || webAuthnName(t, rec.Body.Bytes()) != "Ada@Example.com" {
		t.Fatalf("add options = %d %s", rec.Code, rec.Body.String())
	}
	carolLink, err := passkeys.IssueLink(k.ctx, carol, "recover")
	if err != nil {
		t.Fatal(err)
	}
	if _, options, err := passkeys.BeginLink(k.ctx, carolLink); err != nil || webAuthnName(t, options) != "Carol@Example.com" {
		t.Fatalf("carol's link start = %v, user name %q", err, webAuthnName(t, options))
	}

	opens.Stop()
	sent := len(mail.sent)
	noAddress := func(what string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if strings.Contains(rec.Body.String(), "@") {
			t.Errorf("%s with the worker stopped carries an address: %s", what, rec.Body.String())
		}
	}
	if rec := call(mux, http.MethodGet, "/api/members", adaCookie, nil); rec.Code != http.StatusInternalServerError || rec.Body.String() != "members" {
		t.Errorf("members with the worker stopped = %d %q, want 500 members", rec.Code, rec.Body.String())
	}
	if email, _, err := store.Profile(k.ctx, ada, session.TokenHash); !errors.Is(err, keys.ErrUnavailable) || !errors.Is(err, identity.ErrBusy) || email != "" {
		t.Errorf("profile with the worker stopped = %q %v, want keys: worker unavailable", email, err)
	}
	for _, path := range []string{"/api/password", "/api/session/verify/password"} {
		method := http.MethodPost
		if path == "/api/password" {
			method = http.MethodPut
		}
		rec := postPassword(t, mux, method, path, adaCookie, map[string]string{"password": password})
		if rec.Code != http.StatusServiceUnavailable || rec.Body.String() != "{\"error\":\"try again\"}\n" {
			t.Errorf("%s %s with the worker stopped = %d %q, want 503 try again", method, path, rec.Code, rec.Body.String())
		}
	}
	// A link replaced for someone managed is still returned for copying;
	// no mail goes to any address.
	for _, path := range []string{"/api/members/" + invited.ID + "/invite-link", "/api/members/" + carolID + "/reset-link"} {
		rec := call(mux, http.MethodPost, path, adaCookie, nil)
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &token) != nil || token.Token == "" {
			t.Errorf("%s with the worker stopped = %d %s", path, rec.Code, rec.Body.String())
		}
		noAddress(path, rec)
	}
	if _, err := members.Recover(k.ctx, "carol@example.com"); !errors.Is(err, keys.ErrUnavailable) {
		t.Errorf("recover with the worker stopped = %v", err)
	}
	if len(mail.sent) != sent {
		t.Errorf("mail sent with the worker stopped: %v", mail.sent[sent:])
	}
	if rec := call(mux, http.MethodPost, "/api/passkeys/options", adaCookie, nil); rec.Code != http.StatusBadRequest || rec.Body.String() != "{\"error\":\"unauthorized\"}\n" {
		t.Errorf("add options with the worker stopped = %d %q, want 400 unauthorized", rec.Code, rec.Body.String())
	}
	if rec := callJSON(mux, http.MethodPost, "/api/passkeys/link/options", "", map[string]string{"token": carolLink}); rec.Code != http.StatusBadRequest || rec.Body.String() != "{\"error\":\"invalid link\"}\n" {
		t.Errorf("link options with the worker stopped = %d %q, want 400 invalid link", rec.Code, rec.Body.String())
	}

	// Passkey sign-in reads no address, so it still works.
	signIn := callJSON(mux, http.MethodPost, "/api/passkeys/sign-in/options", "", map[string]string{})
	if signIn.Code != http.StatusOK {
		t.Fatalf("sign-in options %d %s", signIn.Code, signIn.Body.String())
	}
	handle := userHandleOf(t, k, ada)
	assertion, err := soft.Login("localhost", "http://localhost:5173", publicChallenge(t, signIn.Body.Bytes()), handle)
	if err != nil {
		t.Fatal(err)
	}
	finishReq := httptest.NewRequest(http.MethodPost, "/api/passkeys/sign-in/finish", bytes.NewReader(assertion))
	finishReq.RemoteAddr = "198.51.100.9:1000"
	finishReq.AddCookie(&http.Cookie{Name: httpapi.ChallengeCookie, Value: cookie(t, signIn, httpapi.ChallengeCookie)})
	signed := httptest.NewRecorder()
	mux.ServeHTTP(signed, finishReq)
	if signed.Code != http.StatusNoContent || cookie(t, signed, httpapi.CookieName) == "" {
		t.Fatalf("passkey sign-in with the worker stopped = %d %s", signed.Code, signed.Body.String())
	}
}

// userHandleOf is user's WebAuthn handle.
func userHandleOf(t *testing.T, k *emailKit, user identity.UserID) []byte {
	t.Helper()
	var handle []byte
	if err := k.owner.QueryRow(k.ctx, `SELECT webauthn_handle FROM identity.users WHERE id = $1`, user).Scan(&handle); err != nil {
		t.Fatal(err)
	}
	return handle
}
