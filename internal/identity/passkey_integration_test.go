//go:build integration

package identity_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	httpapi "github.com/upmo-ai/upmo/internal/identity/adapters/http"
	"github.com/upmo-ai/upmo/internal/identity/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/identity/adapters/webauthn"
	"github.com/upmo-ai/upmo/internal/identity/app"

	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/testutil/sealer"
)

func TestPasskeySignIn(t *testing.T) {
	ownerPool, pool, ctx := upBoth(t)
	// The worker checks links and sessions against the database clock, so
	// the rig's clock starts at the database's now.
	now := dbNow(t, ctx, ownerPool)
	var clock time.Time
	clock = now
	sessions := identity.Sessions(pool, domain.IdleTimeout, domain.MaxAge)
	sessions.Now = func() time.Time { return clock }
	passkeys, err := identity.Passkeys(pool, sealer.WorkerEmail(t, ownerPool), sessions, "http://localhost:5173", "localhost", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	passkeys.Now = sessions.Now
	_, network, err := net.ParseCIDR("203.0.113.8/32")
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.Passkeys{Pass: passkeys, Trusted: []*net.IPNet{network}}
	mux := http.NewServeMux()
	identity.MountSessions(mux, sessions, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	handler.Mount(mux)
	soft, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}

	token, err := passkeys.Bootstrap(ctx, "ada@example.com", "Ada")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := passkeys.Bootstrap(ctx, "bea@example.com", "Bea"); !errorsIsExists(err) {
		t.Fatalf("second bootstrap: %v", err)
	}

	link := finishLink(t, mux, soft, token)
	session := call(mux, http.MethodGet, "/api/session", link, nil)
	if session.Code != http.StatusOK || !bytes.Contains(session.Body.Bytes(), []byte(`"sign_in_method":"link"`)) {
		t.Fatalf("session = %d %s", session.Code, session.Body.String())
	}

	other, err := passkeys.IssueLink(ctx, adaID(t, ctx, pool), "recover")
	if err != nil {
		t.Fatal(err)
	}
	unknown := callJSON(mux, http.MethodPost, "/api/passkeys/link/options", "", map[string]string{"token": "nope"})
	if used := spend(t, mux, other); used.Code != http.StatusNoContent {
		t.Fatalf("recover finish %d %s", used.Code, used.Body.String())
	}
	again := callJSON(mux, http.MethodPost, "/api/passkeys/link/options", "", map[string]string{"token": other})
	fresh, err := passkeys.IssueLink(ctx, adaID(t, ctx, pool), "recover")
	if err != nil {
		t.Fatal(err)
	}
	clock = now.Add(16 * time.Minute)
	expired := callJSON(mux, http.MethodPost, "/api/passkeys/link/options", "", map[string]string{"token": fresh})
	if unknown.Body.String() != again.Body.String() || unknown.Body.String() != expired.Body.String() || unknown.Code != http.StatusBadRequest {
		t.Fatalf("links differ: %d %s / %d %s / %d %s", unknown.Code, unknown.Body.String(), again.Code, again.Body.String(), expired.Code, expired.Body.String())
	}

	var bob string
	err = ownerPool.QueryRow(ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
		VALUES ('bob@example.com', sha256('bob@example.com'::bytea), 'k1', 'Bob', (SELECT id FROM identity.roles WHERE key = 'member'), now())
		RETURNING id::text`).Scan(&bob)
	if err != nil {
		t.Fatal(err)
	}
	bobID, err := domain.ParseUserID(bob)
	if err != nil {
		t.Fatal(err)
	}
	bobLink, err := passkeys.IssueLink(ctx, bobID, "recover")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ownerPool.Exec(ctx, `UPDATE identity.users SET disabled_at = now() WHERE id = $1`, bobID); err != nil {
		t.Fatal(err)
	}
	disabled := callJSON(mux, http.MethodPost, "/api/passkeys/link/options", "", map[string]string{"token": bobLink})
	if disabled.Body.String() != unknown.Body.String() {
		t.Fatalf("disabled link = %s", disabled.Body.String())
	}

	handle := handleOf(t, ctx, pool)
	signIn := callJSON(mux, http.MethodPost, "/api/passkeys/sign-in/options", "", map[string]string{})
	if signIn.Code != http.StatusOK {
		t.Fatalf("sign-in options %d %s", signIn.Code, signIn.Body.String())
	}
	assertion, err := soft.Login("localhost", "http://localhost:5173", publicChallenge(t, signIn.Body.Bytes()), handle)
	if err != nil {
		t.Fatal(err)
	}
	finishReq := httptest.NewRequest(http.MethodPost, "/api/passkeys/sign-in/finish", bytes.NewReader(assertion))
	finishReq.RemoteAddr = "198.51.100.9:1000"
	finishReq.AddCookie(&http.Cookie{Name: httpapi.ChallengeCookie, Value: cookie(t, signIn, httpapi.ChallengeCookie)})
	signed := httptest.NewRecorder()
	mux.ServeHTTP(signed, finishReq)
	if signed.Code != http.StatusNoContent {
		t.Fatalf("sign-in finish %d %s", signed.Code, signed.Body.String())
	}
	replayReq := httptest.NewRequest(http.MethodPost, "/api/passkeys/sign-in/finish", bytes.NewReader(assertion))
	replayReq.RemoteAddr = "198.51.100.9:1000"
	replayReq.AddCookie(&http.Cookie{Name: httpapi.ChallengeCookie, Value: cookie(t, signIn, httpapi.ChallengeCookie)})
	replay := httptest.NewRecorder()
	mux.ServeHTTP(replay, replayReq)
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("reused challenge %d", replay.Code)
	}
	passkeyCookie := cookie(t, signed, httpapi.CookieName)

	held := callJSON(mux, http.MethodPost, "/api/passkeys/sign-in/options", "", map[string]string{})
	if held.Code != http.StatusOK {
		t.Fatalf("held options %d %s", held.Code, held.Body.String())
	}
	lateAssertion, err := soft.Login("localhost", "http://localhost:5173", publicChallenge(t, held.Body.Bytes()), handle)
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(domain.ChallengeLifetime)
	lateReq := httptest.NewRequest(http.MethodPost, "/api/passkeys/sign-in/finish", bytes.NewReader(lateAssertion))
	lateReq.RemoteAddr = "198.51.100.9:1000"
	lateReq.AddCookie(&http.Cookie{Name: httpapi.ChallengeCookie, Value: cookie(t, held, httpapi.ChallengeCookie)})
	late := httptest.NewRecorder()
	mux.ServeHTTP(late, lateReq)
	if late.Code != http.StatusUnauthorized {
		t.Fatalf("expired challenge %d %s", late.Code, late.Body.String())
	}
	clock = clock.Add(-domain.ChallengeLifetime)

	second := callJSON(mux, http.MethodPost, "/api/passkeys/sign-in/options", "", map[string]string{})
	if second.Code != http.StatusOK {
		t.Fatalf("second options %d %s", second.Code, second.Body.String())
	}
	secondAssertion, err := soft.Login("localhost", "http://localhost:5173", publicChallenge(t, second.Body.Bytes()), handle)
	if err != nil {
		t.Fatal(err)
	}
	secondReq := httptest.NewRequest(http.MethodPost, "/api/passkeys/sign-in/finish", bytes.NewReader(secondAssertion))
	secondReq.RemoteAddr = "198.51.100.9:1000"
	secondReq.AddCookie(&http.Cookie{Name: httpapi.ChallengeCookie, Value: cookie(t, second, httpapi.ChallengeCookie)})
	secondFinish := httptest.NewRecorder()
	mux.ServeHTTP(secondFinish, secondReq)
	if secondFinish.Code != http.StatusNoContent {
		t.Fatalf("second sign-in %d %s", secondFinish.Code, secondFinish.Body.String())
	}
	otherCookie := cookie(t, secondFinish, httpapi.CookieName)
	added, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	add := call(mux, http.MethodPost, "/api/passkeys/options", passkeyCookie, nil)
	if add.Code != http.StatusOK {
		t.Fatalf("add options %d %s", add.Code, add.Body.String())
	}
	created, err := added.Register("localhost", "http://localhost:5173", publicChallenge(t, add.Body.Bytes()), nil)
	if err != nil {
		t.Fatal(err)
	}
	addReq := httptest.NewRequest(http.MethodPost, "/api/passkeys/finish", bytes.NewReader(created))
	addReq.RemoteAddr = "198.51.100.9:1000"
	addReq.AddCookie(&http.Cookie{Name: httpapi.CookieName, Value: passkeyCookie})
	addReq.AddCookie(&http.Cookie{Name: httpapi.ChallengeCookie, Value: cookie(t, add, httpapi.ChallengeCookie)})
	addFinish := httptest.NewRecorder()
	mux.ServeHTTP(addFinish, addReq)
	if addFinish.Code != http.StatusNoContent {
		t.Fatalf("add finish %d %s", addFinish.Code, addFinish.Body.String())
	}
	if ended := call(mux, http.MethodGet, "/api/session", otherCookie, nil); ended.Code != http.StatusUnauthorized {
		t.Fatalf("other session survived an added passkey: %d %s", ended.Code, ended.Body.String())
	}
	if kept := call(mux, http.MethodGet, "/api/session", passkeyCookie, nil); kept.Code != http.StatusOK {
		t.Fatalf("adding session ended itself: %d %s", kept.Code, kept.Body.String())
	}

	clock = clock.Add(11 * time.Minute)
	stale := call(mux, http.MethodPost, "/api/passkeys/options", passkeyCookie, nil)
	if stale.Code != http.StatusForbidden || stale.Body.String() != "{\"error\":\"sign in again\"}\n" {
		t.Fatalf("stale add %d %s", stale.Code, stale.Body.String())
	}

	list := call(mux, http.MethodGet, "/api/passkeys", passkeyCookie, nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list passkeys %d %s", list.Code, list.Body.String())
	}
	var passkeysList []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &passkeysList); err != nil {
		t.Fatal(err)
	}
	if len(passkeysList) < 2 {
		t.Fatalf("expected at least 2 passkeys, got %d", len(passkeysList))
	}
	staleDel := call(mux, http.MethodDelete, "/api/passkeys/"+passkeysList[0].ID, passkeyCookie, nil)
	if staleDel.Code != http.StatusForbidden || staleDel.Body.String() != "{\"error\":\"sign in again\"}\n" {
		t.Fatalf("stale delete %d %s", staleDel.Code, staleDel.Body.String())
	}

	stepUpOpt := call(mux, http.MethodPost, "/api/passkeys/step-up/options", passkeyCookie, nil)
	if stepUpOpt.Code != http.StatusOK {
		t.Fatalf("step-up options %d %s", stepUpOpt.Code, stepUpOpt.Body.String())
	}
	stepUpAssertion, err := soft.Login("localhost", "http://localhost:5173", publicChallenge(t, stepUpOpt.Body.Bytes()), handle)
	if err != nil {
		t.Fatal(err)
	}
	stepUpReq := httptest.NewRequest(http.MethodPost, "/api/passkeys/step-up/finish", bytes.NewReader(stepUpAssertion))
	stepUpReq.RemoteAddr = "198.51.100.9:1000"
	stepUpReq.AddCookie(&http.Cookie{Name: httpapi.CookieName, Value: passkeyCookie})
	stepUpReq.AddCookie(&http.Cookie{Name: httpapi.ChallengeCookie, Value: cookie(t, stepUpOpt, httpapi.ChallengeCookie)})
	stepUpFinish := httptest.NewRecorder()
	mux.ServeHTTP(stepUpFinish, stepUpReq)
	if stepUpFinish.Code != http.StatusNoContent {
		t.Fatalf("step-up finish %d %s", stepUpFinish.Code, stepUpFinish.Body.String())
	}

	third := callJSON(mux, http.MethodPost, "/api/passkeys/sign-in/options", "", map[string]string{})
	if third.Code != http.StatusOK {
		t.Fatalf("third sign-in options %d %s", third.Code, third.Body.String())
	}
	thirdAssertion, err := soft.Login("localhost", "http://localhost:5173", publicChallenge(t, third.Body.Bytes()), handle)
	if err != nil {
		t.Fatal(err)
	}
	thirdReq := httptest.NewRequest(http.MethodPost, "/api/passkeys/sign-in/finish", bytes.NewReader(thirdAssertion))
	thirdReq.RemoteAddr = "198.51.100.9:1000"
	thirdReq.AddCookie(&http.Cookie{Name: httpapi.ChallengeCookie, Value: cookie(t, third, httpapi.ChallengeCookie)})
	thirdFinish := httptest.NewRecorder()
	mux.ServeHTTP(thirdFinish, thirdReq)
	if thirdFinish.Code != http.StatusNoContent {
		t.Fatalf("third sign-in finish %d %s", thirdFinish.Code, thirdFinish.Body.String())
	}
	otherSession := cookie(t, thirdFinish, httpapi.CookieName)

	freshAdded, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	freshAddOpt := call(mux, http.MethodPost, "/api/passkeys/options", passkeyCookie, nil)
	if freshAddOpt.Code != http.StatusOK {
		t.Fatalf("fresh add options %d %s", freshAddOpt.Code, freshAddOpt.Body.String())
	}
	freshCreated, err := freshAdded.Register("localhost", "http://localhost:5173", publicChallenge(t, freshAddOpt.Body.Bytes()), nil)
	if err != nil {
		t.Fatal(err)
	}
	freshAddReq := httptest.NewRequest(http.MethodPost, "/api/passkeys/finish", bytes.NewReader(freshCreated))
	freshAddReq.RemoteAddr = "198.51.100.9:1000"
	freshAddReq.AddCookie(&http.Cookie{Name: httpapi.CookieName, Value: passkeyCookie})
	freshAddReq.AddCookie(&http.Cookie{Name: httpapi.ChallengeCookie, Value: cookie(t, freshAddOpt, httpapi.ChallengeCookie)})
	freshAddFinish := httptest.NewRecorder()
	mux.ServeHTTP(freshAddFinish, freshAddReq)
	if freshAddFinish.Code != http.StatusNoContent {
		t.Fatalf("fresh add finish %d %s", freshAddFinish.Code, freshAddFinish.Body.String())
	}

	if ended := call(mux, http.MethodGet, "/api/session", otherSession, nil); ended.Code != http.StatusUnauthorized {
		t.Fatalf("other session survived: %d %s", ended.Code, ended.Body.String())
	}
	if kept := call(mux, http.MethodGet, "/api/session", passkeyCookie, nil); kept.Code != http.StatusOK {
		t.Fatalf("kept session ended: %d %s", kept.Code, kept.Body.String())
	}

	for i := 0; i < domain.ThrottleBurst; i++ {
		rec := limited(mux, "198.51.100.50:9", "", "")
		if rec.Code == http.StatusTooManyRequests {
			t.Fatalf("limited on request %d", i+1)
		}
	}
	blocked := limited(mux, "198.51.100.50:9", "", "")
	if blocked.Code != http.StatusTooManyRequests || blocked.Header().Get("Retry-After") == "" {
		t.Fatalf("rate limit %d retry %q", blocked.Code, blocked.Header().Get("Retry-After"))
	}
	shared := limited(mux, "198.51.100.50:9", "X-Forwarded-For", "203.0.113.9")
	if shared.Code != http.StatusTooManyRequests {
		t.Fatalf("forged header escaped the limit: %d", shared.Code)
	}
	trusted := limited(mux, "203.0.113.8:9", "X-Forwarded-For", "198.51.100.77")
	if trusted.Code == http.StatusTooManyRequests {
		t.Fatal("trusted proxy used the connection address")
	}
}

func TestTwoRemovalsCannotBothPass(t *testing.T) {
	ownerPool, pool, ctx := upBoth(t)
	var user string
	if err := ownerPool.QueryRow(ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
		VALUES ('ada@example.com', sha256('ada@example.com'::bytea), 'k1', 'Ada', (SELECT id FROM identity.roles WHERE key = 'owner'), now())
		RETURNING id::text`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	uid, err := domain.ParseUserID(user)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 2)
	for i := range ids {
		if err := pool.QueryRow(ctx, `
			INSERT INTO identity.passkeys (
			  user_id, credential_id, public_key, sign_count, aaguid, transports,
			  backup_eligible, backup_state, attestation, name, created_at)
			VALUES ($1, $2, $2, 0, '00000000-0000-0000-0000-000000000000', '{}', false, false, 'none', $3, now())
			RETURNING id::text`, uid, []byte{byte(i + 1)}, "Passkey").Scan(&ids[i]); err != nil {
			t.Fatal(err)
		}
	}
	store := postgres.Passkeys{Pool: pool}
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.DeletePasskey(ctx, uid, id)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	refused := 0
	for err := range errs {
		if errors.Is(err, app.ErrLastPasskey) {
			refused++
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if refused != 1 {
		t.Fatalf("refusals = %d, want 1", refused)
	}
}

func TestRecoverLinkJoinsAnInvitedPerson(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	ownerPool, pool, mux, passkeys, ctx := identityMuxWithOwner(t, now)
	token, err := passkeys.Bootstrap(ctx, "ada@example.com", "Ada")
	if err != nil {
		t.Fatal(err)
	}
	var invited, joined time.Time
	var expires time.Time
	if err := pool.QueryRow(ctx, `SELECT invited_at, joined_at FROM identity.users WHERE id = $1`, userByEmail(t, pool, ctx, "ada@example.com")).Scan(&invited, &joined); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT expires_at FROM identity.sign_in_links`).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	if !invited.Equal(now) || !joined.Equal(now) || !expires.Equal(now.Add(domain.RecoverLifetime)) {
		t.Fatalf("clock invited %s joined %s expires %s", invited, joined, expires)
	}
	soft, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	if session := finishLink(t, mux, soft, token); session == "" {
		t.Fatal("bootstrap link did not sign in")
	}

	var lee string
	if err := ownerPool.QueryRow(ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, invited_at)
		VALUES ($1, $2, $3, 'Lee', (SELECT id FROM identity.roles WHERE key = 'member'), $4)
		RETURNING id::text`, append(sealUser(t, sealer.NewEmail(t), "lee@example.com"), now)...).Scan(&lee); err != nil {
		t.Fatal(err)
	}
	leeID, err := domain.ParseUserID(lee)
	if err != nil {
		t.Fatal(err)
	}
	recover, err := passkeys.IssueLink(ctx, leeID, "recover")
	if err != nil {
		t.Fatal(err)
	}
	leeSoft, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	if finishLink(t, mux, leeSoft, recover) == "" {
		t.Fatal("recover link did not sign in")
	}
	var leeJoined *time.Time
	if err := pool.QueryRow(ctx, `SELECT joined_at FROM identity.users WHERE id = $1`, leeID).Scan(&leeJoined); err != nil {
		t.Fatal(err)
	}
	if leeJoined == nil {
		t.Fatal("recover left the person unjoined")
	}
	var team string
	if err := pool.QueryRow(ctx, `INSERT INTO identity.teams (name) VALUES ('Ship') RETURNING id`).Scan(&team); err != nil {
		t.Fatal(err)
	}
	if _, err := ownerPool.Exec(ctx, `INSERT INTO identity.team_members (team_id, user_id) VALUES ($1, $2)`, team, leeID); err != nil {
		t.Fatal(err)
	}
}

func TestTwoFinishesRedeemOneLink(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	ownerPool, pool, mux, passkeys, ctx := identityMuxWithOwner(t, now)
	if _, err := passkeys.Bootstrap(ctx, "ada@example.com", "Ada"); err != nil {
		t.Fatal(err)
	}
	var lee string
	if err := ownerPool.QueryRow(ctx, `
		INSERT INTO identity.users (email, email_hash, email_hash_key_id, name, role_id, joined_at)
		VALUES ($1, $2, $3, 'Lee', (SELECT id FROM identity.roles WHERE key = 'member'), $4)
		RETURNING id::text`, append(sealUser(t, sealer.NewEmail(t), "lee@example.com"), now)...).Scan(&lee); err != nil {
		t.Fatal(err)
	}
	leeID, err := domain.ParseUserID(lee)
	if err != nil {
		t.Fatal(err)
	}
	token, err := passkeys.IssueLink(ctx, leeID, "recover")
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan finishResult, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- finishOnce(mux, token)
		}()
	}
	wg.Wait()
	close(results)
	ok, bad := 0, 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		switch result.code {
		case http.StatusNoContent:
			ok++
		case http.StatusBadRequest:
			bad++
		default:
			t.Fatalf("finish %d %s", result.code, result.body)
		}
	}
	if ok != 1 || bad != 1 {
		t.Fatalf("finishes ok %d bad %d", ok, bad)
	}
	var passkeysKept int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM identity.passkeys WHERE user_id = $1`, leeID).Scan(&passkeysKept); err != nil {
		t.Fatal(err)
	}
	if passkeysKept != 1 {
		t.Fatalf("passkeys = %d", passkeysKept)
	}
}

// Saving a challenge sweeps links that expired over a day ago; a link expired
// an hour ago and a valid one stay.
func TestChallengeSaveSweepsExpiredLinks(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	ownerPool, pool, ctx := upBoth(t)
	sessions := identity.Sessions(pool, domain.IdleTimeout, domain.MaxAge)
	sessions.Now = func() time.Time { return now }
	passkeys, err := identity.Passkeys(pool, nil, sessions, "http://localhost:5173", "localhost", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	passkeys.Now = sessions.Now
	ada, err := domain.ParseUserID(seedOwner(t, ownerPool, ctx, "ada@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	expired := now.Add(-48 * time.Hour)
	for _, at := range []time.Time{expired, now.Add(-time.Hour), now.Add(time.Hour)} {
		token, _, err := domain.NewToken()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO identity.sign_in_links (token_hash, user_id, purpose, expires_at)
			VALUES ($1, $2, 'recover', $3)`, domain.Hash(token), ada, at); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := passkeys.BeginSignIn(ctx); err != nil {
		t.Fatal(err)
	}
	var left []time.Time
	rows, err := pool.Query(ctx, `SELECT expires_at FROM identity.sign_in_links WHERE user_id = $1`, ada)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var at time.Time
		if err := rows.Scan(&at); err != nil {
			t.Fatal(err)
		}
		left = append(left, at)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []time.Time{now.Add(-time.Hour), now.Add(time.Hour)}
	if len(left) != len(want) {
		t.Fatalf("links left = %v, want %v", left, want)
	}
	for _, at := range left {
		if at.Equal(expired) {
			t.Fatalf("links left = %v, want the two-day-old link gone", left)
		}
	}
}

func TestIPv6PrefixSharesAThrottleBucket(t *testing.T) {
	_, mux, _, _ := identityMux(t, time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC))
	for range domain.ThrottleBurst {
		rec := limited(mux, "[2001:db8:1:2::1]:9", "", "")
		if rec.Code == http.StatusTooManyRequests {
			t.Fatal("limited before the burst")
		}
	}
	blocked := limited(mux, "[2001:db8:1:2::abcd]:9", "", "")
	if blocked.Code != http.StatusTooManyRequests {
		t.Fatalf("same prefix %d", blocked.Code)
	}
	other := limited(mux, "[2001:db8:9:9::1]:9", "", "")
	if other.Code == http.StatusTooManyRequests {
		t.Fatal("a different prefix was limited")
	}
}

func identityMux(t *testing.T, now time.Time) (*pgxpool.Pool, http.Handler, app.Passkeys, context.Context) {
	t.Helper()
	_, pool, mux, passkeys, ctx := identityMuxWithOwner(t, now)
	return pool, mux, passkeys, ctx
}

func identityMuxWithOwner(t *testing.T, now time.Time) (*pgxpool.Pool, *pgxpool.Pool, http.Handler, app.Passkeys, context.Context) {
	t.Helper()
	owner, pool, ctx := upBoth(t)
	// The worker opens a link's address against the database clock: now is
	// a time near it.
	emails := sealer.WorkerEmail(t, owner)
	sessions := identity.Sessions(pool, domain.IdleTimeout, domain.MaxAge)
	sessions.Now = func() time.Time { return now }
	passkeys, err := identity.Passkeys(pool, emails, sessions, "http://localhost:5173", "localhost", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	passkeys.Now = sessions.Now
	mux := http.NewServeMux()
	identity.MountSessions(mux, sessions, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	identity.MountPasskeys(mux, passkeys, identity.Passwords(pool, emails, sessions), nil)
	return owner, pool, mux, passkeys, ctx
}

type finishResult struct {
	code int
	body string
	err  error
}

func finishOnce(mux http.Handler, token string) finishResult {
	soft, err := webauthn.NewSoft()
	if err != nil {
		return finishResult{err: err}
	}
	rec := callJSON(mux, http.MethodPost, "/api/passkeys/link/options", "", map[string]string{"token": token})
	if rec.Code != http.StatusOK {
		return finishResult{code: rec.Code, body: rec.Body.String()}
	}
	challenge, err := challengeOf(rec.Body.Bytes())
	if err != nil {
		return finishResult{err: err}
	}
	body, err := soft.Register("localhost", "http://localhost:5173", challenge, nil)
	if err != nil {
		return finishResult{err: err}
	}
	var challengeCookie string
	for _, item := range rec.Result().Cookies() {
		if item.Name == httpapi.ChallengeCookie {
			challengeCookie = item.Value
		}
	}
	finish := callJSON(mux, http.MethodPost, "/api/passkeys/link/finish", challengeCookie, map[string]any{"token": token, "response": json.RawMessage(body)})
	return finishResult{code: finish.Code, body: finish.Body.String()}
}

func challengeOf(options []byte) (string, error) {
	var payload map[string]any
	if err := json.Unmarshal(options, &payload); err != nil {
		return "", err
	}
	public, _ := payload["publicKey"].(map[string]any)
	challenge, _ := public["challenge"].(string)
	if challenge == "" {
		return "", errors.New("identity: no challenge")
	}
	return challenge, nil
}

func limited(mux http.Handler, remote, header, value string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/passkeys/sign-in/options", bytes.NewReader([]byte("{}")))
	req.RemoteAddr = remote
	if header != "" {
		req.Header.Set(header, value)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func finishLink(t *testing.T, mux http.Handler, soft *webauthn.Soft, token string) string {
	t.Helper()
	rec := callJSON(mux, http.MethodPost, "/api/passkeys/link/options", "", map[string]string{"token": token})
	if rec.Code != http.StatusOK {
		t.Fatalf("link options %d %s", rec.Code, rec.Body.String())
	}
	body, err := soft.Register("localhost", "http://localhost:5173", publicChallenge(t, rec.Body.Bytes()), userHandle(t, rec.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	finish := callJSON(mux, http.MethodPost, "/api/passkeys/link/finish", cookie(t, rec, httpapi.ChallengeCookie), map[string]any{"token": token, "response": json.RawMessage(body)})
	if finish.Code != http.StatusNoContent {
		t.Fatalf("link finish %d %s", finish.Code, finish.Body.String())
	}
	return cookie(t, finish, httpapi.CookieName)
}

func spend(t *testing.T, mux http.Handler, token string) *httptest.ResponseRecorder {
	t.Helper()
	soft, err := webauthn.NewSoft()
	if err != nil {
		t.Fatal(err)
	}
	rec := callJSON(mux, http.MethodPost, "/api/passkeys/link/options", "", map[string]string{"token": token})
	if rec.Code != http.StatusOK {
		t.Fatalf("spend options %d %s", rec.Code, rec.Body.String())
	}
	body, err := soft.Register("localhost", "http://localhost:5173", publicChallenge(t, rec.Body.Bytes()), nil)
	if err != nil {
		t.Fatal(err)
	}
	return callJSON(mux, http.MethodPost, "/api/passkeys/link/finish", cookie(t, rec, httpapi.ChallengeCookie), map[string]any{"token": token, "response": json.RawMessage(body)})
}

func call(mux http.Handler, method, path, session string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.RemoteAddr = "198.51.100.9:1000"
	if session != "" {
		req.AddCookie(&http.Cookie{Name: httpapi.CookieName, Value: session})
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func callJSON(mux http.Handler, method, path, challenge string, payload any) *httptest.ResponseRecorder {
	raw, _ := json.Marshal(payload)
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.RemoteAddr = "198.51.100.9:1000"
	if challenge != "" {
		req.AddCookie(&http.Cookie{Name: httpapi.ChallengeCookie, Value: challenge})
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func cookie(t *testing.T, rec *httptest.ResponseRecorder, name string) string {
	t.Helper()
	for _, item := range rec.Result().Cookies() {
		if item.Name == name && item.Value != "" {
			return item.Value
		}
	}
	t.Fatalf("no cookie %s", name)
	return ""
}

func publicChallenge(t *testing.T, options []byte) string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(options, &payload); err != nil {
		t.Fatal(err)
	}
	public, _ := payload["publicKey"].(map[string]any)
	challenge, _ := public["challenge"].(string)
	if challenge == "" {
		t.Fatalf("no challenge in %s", options)
	}
	return challenge
}

func userHandle(t *testing.T, options []byte) []byte {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(options, &payload); err != nil {
		t.Fatal(err)
	}
	public, _ := payload["publicKey"].(map[string]any)
	user, _ := public["user"].(map[string]any)
	encoded, _ := user["id"].(string)
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func handleOf(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []byte {
	t.Helper()
	var handle []byte
	if err := pool.QueryRow(ctx, `SELECT webauthn_handle FROM identity.users WHERE id = $1`, adaID(t, ctx, pool)).Scan(&handle); err != nil {
		t.Fatal(err)
	}
	return handle
}

func adaID(t *testing.T, ctx context.Context, pool *pgxpool.Pool) domain.UserID {
	t.Helper()
	parsed, err := identity.ParseUserID(userByEmail(t, pool, ctx, "ada@example.com"))
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func errorsIsExists(err error) bool {
	return err != nil && err.Error() == "identity: someone already exists"
}
