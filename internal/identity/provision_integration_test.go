//go:build integration

package identity_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/upmo-ai/upmo/internal/identity"
	httpapi "github.com/upmo-ai/upmo/internal/identity/adapters/http"
	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/httpserver"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
	"github.com/upmo-ai/upmo/internal/testutil/sealer"
)

func TestProvisionMigrationDownUp(t *testing.T) {
	owner, ctx := upOwner(t)
	if _, err := owner.Exec(ctx, `INSERT INTO identity.teams (name, external_id) VALUES ('Ops', 'ext-ops')`); err != nil {
		t.Fatalf("insert team with external_id: %v", err)
	}
	if _, err := owner.Exec(ctx, `UPDATE identity.workspace SET spend_ceiling_micro_usd = 2500000 WHERE id = true`); err != nil {
		t.Fatalf("set spend_ceiling_micro_usd: %v", err)
	}

	db := stdlib.OpenDB(*owner.Config().ConnConfig)
	defer func() { _ = db.Close() }()
	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		db,
		identity.Migrations(),
		goose.WithTableName(identity.Schema+".goose_db_version"),
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := provider.DownTo(ctx, 0); err != nil {
		t.Fatalf("down to 0: %v", err)
	}

	var hasTable bool
	if err := owner.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'identity' AND table_name = 'provision_nonces'
		)`).Scan(&hasTable); err != nil {
		t.Fatal(err)
	}
	if hasTable {
		t.Fatal("identity.provision_nonces still exists after down")
	}

	var hasCol bool
	if err := owner.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'identity' AND table_name = 'teams' AND column_name = 'external_id'
		)`).Scan(&hasCol); err != nil {
		t.Fatal(err)
	}
	if hasCol {
		t.Fatal("identity.teams.external_id still exists after down")
	}
	if err := owner.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'identity' AND table_name = 'workspace' AND column_name = 'spend_ceiling_micro_usd'
		)`).Scan(&hasCol); err != nil {
		t.Fatal(err)
	}
	if hasCol {
		t.Fatal("identity.workspace.spend_ceiling_micro_usd still exists after down")
	}

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("re-up: %v", err)
	}
	var version int
	if err := owner.QueryRow(ctx, `SELECT max(version_id) FROM identity.goose_db_version WHERE is_applied`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("version after re-up = %d, want 1", version)
	}
	if err := owner.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'identity' AND table_name = 'provision_nonces'
		)`).Scan(&hasTable); err != nil {
		t.Fatal(err)
	}
	if !hasTable {
		t.Fatal("identity.provision_nonces missing after re-up")
	}
	var nonceInsert bool
	if err := owner.QueryRow(ctx, `SELECT has_table_privilege('upmo_app', 'identity.provision_nonces', 'INSERT')`).Scan(&nonceInsert); err != nil {
		t.Fatal(err)
	}
	if nonceInsert {
		t.Fatal("upmo_app has INSERT on identity.provision_nonces after re-up")
	}
	var ceilingUpdate bool
	if err := owner.QueryRow(ctx, `SELECT has_column_privilege('upmo_app', 'identity.workspace', 'spend_ceiling_micro_usd', 'UPDATE')`).Scan(&ceilingUpdate); err != nil {
		t.Fatal(err)
	}
	if ceilingUpdate {
		t.Fatal("upmo_app has UPDATE on spend_ceiling_micro_usd after re-up")
	}
	var nameUpdate bool
	if err := owner.QueryRow(ctx, `SELECT has_column_privilege('upmo_app', 'identity.workspace', 'name', 'UPDATE')`).Scan(&nameUpdate); err != nil {
		t.Fatal(err)
	}
	if !nameUpdate {
		t.Fatal("upmo_app lost UPDATE on identity.workspace.name after re-up")
	}
}

func TestSSOStepUpRefreshesSessionAndFreshSucceeds(t *testing.T) {
	owner, appPool, ctx := upBoth(t)
	now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	clock := now

	sessions := identity.Sessions(appPool, domain.IdleTimeout, domain.MaxAge)
	sessions.Now = func() time.Time { return clock }
	passkeys, err := identity.Passkeys(appPool, sealer.NewEmail(t), sessions, "http://localhost:5173", "localhost", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	passkeys.Now = sessions.Now
	idFacade := identity.New(sessions, passkeys, identity.Passwords(appPool, sealer.NewEmail(t), sessions))

	prov := identity.Provision(owner, sealer.NewEmail(t), "http://localhost:5173")
	member, err := prov.CreateMember(ctx, identity.ProvisionMemberInput{
		ExternalID: "sso-user-1",
		Email:      "sso@example.com",
		Name:       "SSO User",
		Role:       "owner",
		KeyID:      "k-test",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Mint and redeem an 'sso' link -> creates a fresh SSO session.
	ssoLink, err := prov.IssueProvisionLink(ctx, member.ExternalID, "sso", "k-test")
	if err != nil {
		t.Fatal(err)
	}
	cookie1, refreshed, err := idFacade.RedeemSSOLink(ctx, ssoLink.Token, "", "test-agent")
	if err != nil || refreshed || cookie1 == "" {
		t.Fatalf("RedeemSSOLink(sso) = (%q, %v, %v)", cookie1, refreshed, err)
	}
	sess1, err := sessions.Lookup(ctx, cookie1)
	if err != nil {
		t.Fatal(err)
	}
	if sess1.Method != "sso" || !idFacade.Fresh(sess1) {
		t.Fatalf("initial sso session = %+v, Fresh = %v", sess1, idFacade.Fresh(sess1))
	}

	// Reusing the one-time link fails.
	if _, _, err := idFacade.RedeemSSOLink(ctx, ssoLink.Token, "", "test-agent"); !errors.Is(err, identity.ErrInvalidLink) {
		t.Fatalf("reuse sso link = %v, want ErrInvalidLink", err)
	}

	// Advance clock beyond StepUpWindow -> session is no longer Fresh().
	clock = clock.Add(domain.StepUpWindow + time.Minute)
	sessStale, err := sessions.Lookup(ctx, cookie1)
	if err != nil {
		t.Fatal(err)
	}
	if idFacade.Fresh(sessStale) {
		t.Fatal("expected session to be stale after StepUpWindow")
	}

	// Mint and redeem an 'sso_stepup' link with the active session cookie ->
	// refreshes signed_in_at on the active session so Fresh() succeeds again!
	stepupLink, err := prov.IssueProvisionLink(ctx, member.ExternalID, "sso_stepup", "k-test")
	if err != nil {
		t.Fatal(err)
	}
	cookie2, refreshed, err := idFacade.RedeemSSOLink(ctx, stepupLink.Token, cookie1, "test-agent")
	if err != nil || !refreshed || cookie2 != "" {
		t.Fatalf("RedeemSSOLink(sso_stepup) = (%q, %v, %v), want (\"\", true, nil)", cookie2, refreshed, err)
	}
	sessFresh, err := sessions.Lookup(ctx, cookie1)
	if err != nil {
		t.Fatal(err)
	}
	if sessFresh.ID != sess1.ID || sessFresh.Method != "sso" || !idFacade.Fresh(sessFresh) {
		t.Fatalf("refreshed session = %+v, Fresh = %v", sessFresh, idFacade.Fresh(sessFresh))
	}

	// Redeeming 'sso_stepup' without an active session starts a fresh 'sso' session.
	stepupLink2, err := prov.IssueProvisionLink(ctx, member.ExternalID, "sso_stepup", "k-test")
	if err != nil {
		t.Fatal(err)
	}
	cookie3, refreshed, err := idFacade.RedeemSSOLink(ctx, stepupLink2.Token, "", "test-agent")
	if err != nil || refreshed || cookie3 == "" {
		t.Fatalf("RedeemSSOLink(sso_stepup without cookie) = (%q, %v, %v)", cookie3, refreshed, err)
	}
	sess3, err := sessions.Lookup(ctx, cookie3)
	if err != nil {
		t.Fatal(err)
	}
	if sess3.Method != "sso" || !idFacade.Fresh(sess3) {
		t.Fatalf("new sso_stepup session = %+v, Fresh = %v", sess3, idFacade.Fresh(sess3))
	}

	// upmo_app cannot directly update spend_ceiling_micro_usd.
	appLogin := pgtest.AppLogin(t, owner)
	_, err = appLogin.Exec(context.Background(), `UPDATE identity.workspace SET spend_ceiling_micro_usd = 999 WHERE id = true`)
	assertDenied(t, "upmo_app update spend_ceiling_micro_usd", err)
}

func TestSSORedeemHTTPAndFreshIntegration(t *testing.T) {
	owner, appPool, ctx := upBoth(t)
	now := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	clock := now

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	sessions := identity.Sessions(appPool, domain.IdleTimeout, domain.MaxAge)
	sessions.Now = func() time.Time { return clock }
	passkeys, err := identity.Passkeys(appPool, sealer.NewEmail(t), sessions, "http://localhost:5173", "localhost", log)
	if err != nil {
		t.Fatal(err)
	}
	passkeys.Now = sessions.Now
	passwords := identity.Passwords(appPool, sealer.NewEmail(t), sessions)

	mux := http.NewServeMux()
	identity.MountPasskeys(mux, passkeys, passwords, nil)
	identity.MountSessions(mux, sessions, log, passwords.HasPassword)
	handler := audit.Middleware(audit.Routes(), mux, appPool, "brain", nil, log)(httpserver.Protect(mux))

	prov := identity.Provision(owner, sealer.NewEmail(t), "http://localhost:5173")
	member, err := prov.CreateMember(ctx, identity.ProvisionMemberInput{
		ExternalID: "http-sso-user",
		Email:      "httpsso@example.com",
		Name:       "HTTP SSO",
		Role:       "owner",
		KeyID:      "k-test",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Case A: 'sso' link -> unauthenticated POST /api/sso/redeem returns 204 + session cookie.
	ssoLink, err := prov.IssueProvisionLink(ctx, member.ExternalID, "sso", "k-test")
	if err != nil {
		t.Fatal(err)
	}

	// Prove atomic rollback: if session insertion fails, the one-time link is NOT spent.
	if _, err := owner.Exec(ctx, `
		CREATE OR REPLACE FUNCTION identity.fail_session_write() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			RAISE EXCEPTION 'simulated session failure';
		END $$;
		CREATE TRIGGER test_fail_session BEFORE INSERT OR UPDATE ON identity.sessions
		FOR EACH ROW EXECUTE FUNCTION identity.fail_session_write();`); err != nil {
		t.Fatal(err)
	}
	failBody, _ := json.Marshal(map[string]string{"token": ssoLink.Token})
	failRec := httptest.NewRecorder()
	handler.ServeHTTP(failRec, httptest.NewRequest(http.MethodPost, "/api/sso/redeem", bytes.NewReader(failBody)))
	if failRec.Code != http.StatusInternalServerError {
		t.Fatalf("failed session write = %d, want 500", failRec.Code)
	}
	if _, err := owner.Exec(ctx, `DROP TRIGGER test_fail_session ON identity.sessions; DROP FUNCTION identity.fail_session_write();`); err != nil {
		t.Fatal(err)
	}

	// Retry with the same token succeeds because the transaction rolled back!
	redeemBody, _ := json.Marshal(map[string]string{"token": ssoLink.Token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/sso/redeem", bytes.NewReader(redeemBody)))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("POST /api/sso/redeem (sso) = %d %s, want 204", rec.Code, rec.Body.String())
	}
	var sessionCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == httpapi.CookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil || sessionCookie.Value == "" {
		t.Fatalf("expected %s cookie on sso redeem", httpapi.CookieName)
	}

	// Protected endpoint succeeds with the issued session cookie.
	sessReq := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	sessReq.AddCookie(sessionCookie)
	sessRec := httptest.NewRecorder()
	handler.ServeHTTP(sessRec, sessReq)
	if sessRec.Code != http.StatusOK || !strings.Contains(sessRec.Body.String(), `"sign_in_method":"sso"`) {
		t.Fatalf("GET /api/session = %d %s", sessRec.Code, sessRec.Body.String())
	}

	// Reusing the spent link fails with 400 invalid link.
	reuseRec := httptest.NewRecorder()
	handler.ServeHTTP(reuseRec, httptest.NewRequest(http.MethodPost, "/api/sso/redeem", bytes.NewReader(redeemBody)))
	if reuseRec.Code != http.StatusBadRequest || !strings.Contains(reuseRec.Body.String(), "invalid link") {
		t.Fatalf("reuse link = %d %s, want 400 invalid link", reuseRec.Code, reuseRec.Body.String())
	}

	// Case B: 'sso_stepup' -> advance beyond StepUpWindow so Fresh() fails (403),
	// then redeem step-up link with active session cookie -> Fresh() succeeds!
	clock = clock.Add(domain.StepUpWindow + time.Minute)
	staleReq := httptest.NewRequest(http.MethodPost, "/api/passkeys/options", nil)
	staleReq.AddCookie(sessionCookie)
	staleRec := httptest.NewRecorder()
	handler.ServeHTTP(staleRec, staleReq)
	if staleRec.Code != http.StatusForbidden {
		t.Fatalf("stale POST /api/passkeys/options = %d %s, want 403", staleRec.Code, staleRec.Body.String())
	}

	stepupLink, err := prov.IssueProvisionLink(ctx, member.ExternalID, "sso_stepup", "k-test")
	if err != nil {
		t.Fatal(err)
	}
	stepupBody, _ := json.Marshal(map[string]string{"token": stepupLink.Token})
	stepupReq := httptest.NewRequest(http.MethodPost, "/api/sso/redeem", bytes.NewReader(stepupBody))
	stepupReq.AddCookie(sessionCookie)
	stepupRec := httptest.NewRecorder()
	handler.ServeHTTP(stepupRec, stepupReq)
	if stepupRec.Code != http.StatusNoContent {
		t.Fatalf("POST /api/sso/redeem (sso_stepup) = %d %s, want 204", stepupRec.Code, stepupRec.Body.String())
	}
	if len(stepupRec.Result().Cookies()) != 0 {
		t.Fatalf("expected no new session cookie on step-up refresh, got %v", stepupRec.Result().Cookies())
	}

	// Endpoint requiring Fresh() now succeeds with the same session cookie.
	freshReq := httptest.NewRequest(http.MethodPost, "/api/passkeys/options", nil)
	freshReq.AddCookie(sessionCookie)
	freshRec := httptest.NewRecorder()
	handler.ServeHTTP(freshRec, freshReq)
	if freshRec.Code != http.StatusOK {
		t.Fatalf("fresh POST /api/passkeys/options after sso_stepup = %d %s, want 200", freshRec.Code, freshRec.Body.String())
	}

	if _, err := audit.SealOnce(ctx, owner, io.Discard); err != nil {
		t.Fatalf("audit.SealOnce: %v", err)
	}
	res, err := audit.Verify(ctx, owner, 0, 0)
	if err != nil || !res.OK {
		t.Fatalf("audit.Verify = %+v, err=%v", res, err)
	}
}
