//go:build integration

package identity_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/upmo-ai/upmo/internal/identity"
	httpapi "github.com/upmo-ai/upmo/internal/identity/adapters/http"
	"github.com/upmo-ai/upmo/internal/identity/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/testutil/sealer"
)

func TestPasswordSignIn(t *testing.T) {
	pool, ctx := up(t)
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	var clock time.Time
	clock = now
	sessions := identity.Sessions(pool, domain.IdleTimeout, domain.MaxAge)
	sessions.Now = func() time.Time { return clock }
	passwords := identity.Passwords(pool, sessions)
	passwords.Sealer = sealer.New(t)
	passwords.Now = sessions.Now
	passkeys, err := identity.Passkeys(pool, sessions, "http://localhost:5173", "localhost", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	passkeys.Now = sessions.Now
	mux := http.NewServeMux()
	identity.MountSessions(mux, sessions, slog.New(slog.NewTextHandler(io.Discard, nil)), passwords.HasPassword)
	identity.MountPasskeys(mux, passkeys, passwords, nil)

	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO identity.users (email, name, role_id, joined_at)
		VALUES ('ada@example.com', 'Ada Lovelace', (SELECT id FROM identity.roles WHERE key = 'owner'), now())
		RETURNING id::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	user, err := domain.ParseUserID(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO identity.passkeys (
		  user_id, credential_id, public_key, sign_count, aaguid, transports,
		  backup_eligible, backup_state, attestation, name, created_at)
		VALUES ($1, $2, $2, 0, '00000000-0000-0000-0000-000000000000', '{}', false, false, 'none', 'Passkey', now())`, user, []byte{1}); err != nil {
		t.Fatal(err)
	}
	token, _, err := sessions.Start(ctx, user, "passkey", "")
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := sessions.Start(ctx, user, "passkey", "")
	if err != nil {
		t.Fatal(err)
	}

	const password = "correct horse battery"
	set := postPassword(t, mux, http.MethodPut, "/api/password", token, map[string]string{"password": password})
	if set.Code != http.StatusNoContent {
		t.Fatalf("set %d %s", set.Code, set.Body.String())
	}
	if ended := call(mux, http.MethodGet, "/api/session", other, nil); ended.Code != http.StatusUnauthorized {
		t.Fatalf("other session survived a password change: %d", ended.Code)
	}
	current := call(mux, http.MethodGet, "/api/session", token, nil)
	if current.Code != http.StatusOK || !bytes.Contains(current.Body.Bytes(), []byte(`"has_password":true`)) {
		t.Fatalf("session %d %s", current.Code, current.Body.String())
	}

	short := postPassword(t, mux, http.MethodPut, "/api/password", token, map[string]string{"password": "too-short"})
	if short.Code != http.StatusBadRequest || !bytes.Contains(short.Body.Bytes(), []byte(domain.ErrPasswordShort.Error())) {
		t.Fatalf("short %d %s", short.Code, short.Body.String())
	}

	clock = clock.Add(11 * time.Minute)
	stale := postPassword(t, mux, http.MethodPut, "/api/password", token, map[string]string{"password": "another horse battery"})
	if stale.Code != http.StatusForbidden {
		t.Fatalf("stale %d %s", stale.Code, stale.Body.String())
	}
	verified := postPassword(t, mux, http.MethodPost, "/api/session/verify/password", token, map[string]string{"password": password})
	if verified.Code != http.StatusNoContent {
		t.Fatalf("verify %d %s", verified.Code, verified.Body.String())
	}
	again := postPassword(t, mux, http.MethodPut, "/api/password", token, map[string]string{"password": password})
	if again.Code != http.StatusNoContent {
		t.Fatalf("set after verify %d %s", again.Code, again.Body.String())
	}

	signed := postPassword(t, mux, http.MethodPost, "/api/sign-in/password", "", map[string]string{"email": "ada@example.com", "password": password})
	if signed.Code != http.StatusOK || !bytes.Contains(signed.Body.Bytes(), []byte(`"offer_passkey":true`)) {
		t.Fatalf("sign-in %d %s", signed.Code, signed.Body.String())
	}
	passwordCookie := cookie(t, signed, httpapi.CookieName)
	body := call(mux, http.MethodGet, "/api/session", passwordCookie, nil)
	if body.Code != http.StatusOK || !bytes.Contains(body.Body.Bytes(), []byte(`"sign_in_method":"password"`)) {
		t.Fatalf("password session %d %s", body.Code, body.Body.String())
	}
	second := postPassword(t, mux, http.MethodPost, "/api/sign-in/password", "", map[string]string{"email": "ada@example.com", "password": password})
	if second.Code != http.StatusOK || !bytes.Contains(second.Body.Bytes(), []byte(`"offer_passkey":false`)) {
		t.Fatalf("second offer %d %s", second.Code, second.Body.String())
	}

	salt := []byte("salt-is-16-bytes")
	key := argon2.IDKey([]byte(password), salt, 1, 8, 1, 16)
	weak := "$argon2id$v=19$m=8,t=1,p=1$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(key)
	sealedWeak, err := passwords.Sealer.Seal(identity.PasswordRef(user), weak)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE identity.users SET password_hash = $2 WHERE id = $1`, user, sealedWeak); err != nil {
		t.Fatal(err)
	}
	replaced := postPassword(t, mux, http.MethodPost, "/api/sign-in/password", "", map[string]string{"email": "ada@example.com", "password": password})
	if replaced.Code != http.StatusOK {
		t.Fatalf("rehash sign-in %d %s", replaced.Code, replaced.Body.String())
	}
	var stored string
	if err := pool.QueryRow(ctx, `SELECT password_hash FROM identity.users WHERE id = $1`, user).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(stored, "$argon2id$") {
		t.Fatalf("the rehash left a plain hash: %s", stored)
	}
	rehashed, err := passwords.Sealer.Open(identity.PasswordRef(user), stored)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains([]byte(rehashed), []byte("m=19456,t=2,p=1")) {
		t.Fatalf("stored hash = %s", rehashed)
	}

	wrong := postPassword(t, mux, http.MethodPost, "/api/sign-in/password", "", map[string]string{"email": "ada@example.com", "password": "not the password!!"})
	unknown := postPassword(t, mux, http.MethodPost, "/api/sign-in/password", "", map[string]string{"email": "noone@example.com", "password": password})
	if _, err := pool.Exec(ctx, `
		INSERT INTO identity.users (email, name, role_id, joined_at)
		VALUES ('bob@example.com', 'Bob', (SELECT id FROM identity.roles WHERE key = 'member'), now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO identity.users (email, name, role_id, joined_at, disabled_at)
		VALUES ('gone@example.com', 'Gone', (SELECT id FROM identity.roles WHERE key = 'member'), now(), now())`); err != nil {
		t.Fatal(err)
	}
	none := postPassword(t, mux, http.MethodPost, "/api/sign-in/password", "", map[string]string{"email": "bob@example.com", "password": password})
	disabled := postPassword(t, mux, http.MethodPost, "/api/sign-in/password", "", map[string]string{"email": "gone@example.com", "password": password})
	if wrong.Code != http.StatusUnauthorized || wrong.Body.String() != unknown.Body.String() || none.Body.String() != wrong.Body.String() || disabled.Body.String() != wrong.Body.String() {
		t.Fatalf("answers differ: %d %s / %d %s / %d %s / %d %s", wrong.Code, wrong.Body.String(), unknown.Code, unknown.Body.String(), none.Code, none.Body.String(), disabled.Code, disabled.Body.String())
	}

	for range 10 {
		_, _, _, err := passwords.SignIn(ctx, "limited@example.com", password, "198.51.100.77", "")
		if !errors.Is(err, app.ErrWrong) {
			t.Fatal(err)
		}
	}
	_, _, retry, err := passwords.SignIn(ctx, "limited@example.com", password, "198.51.100.77", "")
	if !errors.Is(err, app.ErrLimited) || retry < time.Second {
		t.Fatalf("account limit %v retry %s", err, retry)
	}

	release, ok := domain.FillHashGate()
	if !ok {
		t.Fatal("hash gate")
	}
	busy := postPassword(t, mux, http.MethodPost, "/api/sign-in/password", "", map[string]string{"email": "ada@example.com", "password": password})
	release()
	if busy.Code != http.StatusServiceUnavailable || busy.Header().Get("Retry-After") != "1" {
		t.Fatalf("busy %d retry %q %s", busy.Code, busy.Header().Get("Retry-After"), busy.Body.String())
	}

	if _, err := pool.Exec(ctx, `DELETE FROM identity.passkeys WHERE user_id = $1`, user); err != nil {
		t.Fatal(err)
	}
	removed := postPassword(t, mux, http.MethodDelete, "/api/password", passwordCookie, nil)
	if removed.Code != http.StatusConflict || !bytes.Contains(removed.Body.Bytes(), []byte("the last way to sign in cannot be removed")) {
		t.Fatalf("last way %d %s", removed.Code, removed.Body.String())
	}
}

func TestPasswordAndPasskeyRemovalsCannotBothPass(t *testing.T) {
	pool, ctx := up(t)
	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO identity.users (email, name, password_hash, role_id, joined_at)
		VALUES ('ada@example.com', 'Ada', 'hash', (SELECT id FROM identity.roles WHERE key = 'owner'), now())
		RETURNING id::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	user, err := domain.ParseUserID(id)
	if err != nil {
		t.Fatal(err)
	}
	var passkey string
	if err := pool.QueryRow(ctx, `
		INSERT INTO identity.passkeys (
		  user_id, credential_id, public_key, sign_count, aaguid, transports,
		  backup_eligible, backup_state, attestation, name, created_at)
		VALUES ($1, $2, $2, 0, '00000000-0000-0000-0000-000000000000', '{}', false, false, 'none', 'Passkey', now())
		RETURNING id::text`, user, []byte{7}).Scan(&passkey); err != nil {
		t.Fatal(err)
	}
	store := postgres.Passkeys{Pool: pool}
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := store.DeletePasskey(ctx, user, passkey)
		errs <- err
	}()
	go func() {
		defer wg.Done()
		errs <- store.ClearPassword(ctx, user)
	}()
	wg.Wait()
	close(errs)
	last := 0
	for err := range errs {
		if errorsIsLast(err) {
			last++
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if last != 1 {
		t.Fatalf("last-way errors = %d, want 1", last)
	}
}

func TestPasswordStore(t *testing.T) {
	pool, ctx := up(t)
	var id string
	if err := pool.QueryRow(ctx, `
		INSERT INTO identity.users (email, name, role_id, joined_at)
		VALUES ('ada@example.com', 'Ada', (SELECT id FROM identity.roles WHERE key = 'owner'), now())
		RETURNING id::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	user, err := domain.ParseUserID(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO identity.passkeys (
		  user_id, credential_id, public_key, sign_count, aaguid, transports,
		  backup_eligible, backup_state, attestation, name, created_at)
		VALUES ($1, $2, $2, 0, '00000000-0000-0000-0000-000000000000', '{}', false, false, 'none', 'Passkey', now())`, user, []byte{9}); err != nil {
		t.Fatal(err)
	}
	store := postgres.Passkeys{Pool: pool}
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	if err := store.SetPassword(ctx, user, "first-hash"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPassword(ctx, user, "replacement-hash"); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := pool.QueryRow(ctx, `SELECT password_hash FROM identity.users WHERE id = $1`, user).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != "replacement-hash" {
		t.Fatalf("hash = %s", stored)
	}
	offer, err := store.ClaimOffer(ctx, user, now)
	if err != nil || !offer {
		t.Fatalf("first offer %v %v", offer, err)
	}
	offer, err = store.ClaimOffer(ctx, user, now.Add(time.Hour))
	if err != nil || offer {
		t.Fatalf("second offer %v %v", offer, err)
	}
	offer, err = store.ClaimOffer(ctx, user, now.Add(domain.PasskeyOfferAfter))
	if err != nil || !offer {
		t.Fatalf("due offer %v %v", offer, err)
	}
	if err := store.ClearPassword(ctx, user); err != nil {
		t.Fatal(err)
	}
	var gone *string
	if err := pool.QueryRow(ctx, `SELECT password_hash FROM identity.users WHERE id = $1`, user).Scan(&gone); err != nil {
		t.Fatal(err)
	}
	if gone != nil {
		t.Fatalf("password remained %q", *gone)
	}
}

// e2e/reset.sql is what e2e/ensure-brain.sh runs before each browser run. The
// offer is claimed once, the reset makes it available again for the e2e person
// only, and the throttle starts empty.
func TestE2EResetMakesTheOfferAvailable(t *testing.T) {
	pool, ctx := up(t)
	store := postgres.Passkeys{Pool: pool}
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	seed := func(email, role string) domain.UserID {
		t.Helper()
		var id string
		if err := pool.QueryRow(ctx, `
			INSERT INTO identity.users (email, name, password_hash, role_id, joined_at)
			VALUES ($1, 'Person', 'hash', (SELECT id FROM identity.roles WHERE key = $2), now())
			RETURNING id::text`, email, role).Scan(&id); err != nil {
			t.Fatal(err)
		}
		user, err := domain.ParseUserID(id)
		if err != nil {
			t.Fatal(err)
		}
		return user
	}
	ada := seed("ada@example.com", "owner")
	bob := seed("bob@example.com", "member")
	for _, user := range []domain.UserID{ada, bob} {
		offer, err := store.ClaimOffer(ctx, user, now)
		if err != nil || !offer {
			t.Fatalf("first offer %v %v", offer, err)
		}
		offer, err = store.ClaimOffer(ctx, user, now.Add(time.Hour))
		if err != nil || offer {
			t.Fatalf("second offer %v %v", offer, err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO identity.throttle (key, tokens, updated_at) VALUES ('address:198.51.100.1', -3, now())`); err != nil {
		t.Fatal(err)
	}

	script, err := os.ReadFile("../../e2e/reset.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(script)); err != nil {
		t.Fatal(err)
	}

	offer, err := store.ClaimOffer(ctx, ada, now.Add(2*time.Hour))
	if err != nil || !offer {
		t.Fatalf("offer after the reset %v %v", offer, err)
	}
	offer, err = store.ClaimOffer(ctx, bob, now.Add(2*time.Hour))
	if err != nil || offer {
		t.Fatalf("another person's offer came back %v %v", offer, err)
	}
	for user, want := range map[domain.UserID]bool{ada: false, bob: true} {
		has, err := store.HasPassword(ctx, user)
		if err != nil || has != want {
			t.Fatalf("has password %v, want %v (%v)", has, want, err)
		}
	}
	var throttled int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM identity.throttle`).Scan(&throttled); err != nil {
		t.Fatal(err)
	}
	if throttled != 0 {
		t.Fatalf("%d throttle rows remain", throttled)
	}
}

func TestThrottleSweepAndRetry(t *testing.T) {
	pool, ctx := up(t)
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `INSERT INTO identity.throttle (key, tokens, updated_at) VALUES ('stale', 0, $1), ('fresh', 3, $2)`, now.Add(-time.Hour), now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	store := postgres.Passkeys{Pool: pool}
	key := domain.AccountIPKey("ada@example.com", "198.51.100.9")
	for range domain.AccountIPBurst {
		ok, _, err := store.Take(ctx, key, now, domain.AccountIPBurst, domain.AccountIPPerSecond)
		if err != nil || !ok {
			t.Fatalf("take %v %v", ok, err)
		}
	}
	ok, retry, err := store.Take(ctx, key, now, domain.AccountIPBurst, domain.AccountIPPerSecond)
	if err != nil || ok || retry != 60*time.Second {
		t.Fatalf("blocked ok %v retry %s err %v", ok, retry, err)
	}
	ok, _, err = store.Take(ctx, key, now.Add(retry), domain.AccountIPBurst, domain.AccountIPPerSecond)
	if err != nil || !ok {
		t.Fatalf("after the wait %v %v", ok, err)
	}
	var stale, fresh int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM identity.throttle WHERE key = 'stale'`).Scan(&stale); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM identity.throttle WHERE key = 'fresh'`).Scan(&fresh); err != nil {
		t.Fatal(err)
	}
	if stale != 0 || fresh != 1 {
		t.Fatalf("swept stale %d fresh %d", stale, fresh)
	}
}

func errorsIsLast(err error) bool {
	return errors.Is(err, app.ErrLastPasskey)
}

func postPassword(t *testing.T, mux http.Handler, method, path, session string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	body := []byte("{}")
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = raw
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.RemoteAddr = "198.51.100.20:1000"
	if session != "" {
		req.AddCookie(&http.Cookie{Name: httpapi.CookieName, Value: session})
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}
