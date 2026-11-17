package provision

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/platform/svcsig"
)

func testSvcKey(b byte) string {
	raw := bytes.Repeat([]byte{b}, 32)
	return svcsig.KeyPrefix + base64.RawStdEncoding.EncodeToString(raw)
}

func TestParseKeys(t *testing.T) {
	keys, err := ParseKeys(nil)
	if err != nil || keys != nil {
		t.Fatalf("ParseKeys(nil) = (%v, %v), want (nil, nil)", keys, err)
	}
	keys, err = ParseKeys([]string{"  ", ""})
	if err != nil || keys != nil {
		t.Fatalf("ParseKeys(blank) = (%v, %v), want (nil, nil)", keys, err)
	}
	keys, err = ParseKeys([]string{testSvcKey('a'), testSvcKey('b')})
	if err != nil || len(keys) != 2 {
		t.Fatalf("ParseKeys(2) = (%v, %v)", keys, err)
	}
	if keys[0].ID == "" || keys[1].ID == "" || keys[0].ID == keys[1].ID {
		t.Fatalf("expected distinct non-empty key IDs, got %+v", keys)
	}
	if _, err := ParseKeys([]string{"not-a-key"}); err == nil {
		t.Fatal("expected error for malformed key")
	}
}

func TestDisabledProvisioningReturns404(t *testing.T) {
	mux := http.NewServeMux()
	Handler{}.Mount(mux)

	routes := []struct {
		method, path string
	}{
		{http.MethodPost, "/api/v1/provision/members"},
		{http.MethodPut, "/api/v1/provision/members/ext-1"},
		{http.MethodDelete, "/api/v1/provision/members/ext-1"},
		{http.MethodPost, "/api/v1/provision/teams"},
		{http.MethodPut, "/api/v1/provision/teams/team-1"},
		{http.MethodDelete, "/api/v1/provision/teams/team-1"},
		{http.MethodPost, "/api/v1/provision/sign-in-links"},
		{http.MethodPut, "/api/v1/provision/providers"},
		{http.MethodPut, "/api/v1/provision/quotas"},
		{http.MethodDelete, "/api/v1/provision/quotas"},
		{http.MethodPut, "/api/v1/provision/packs"},
		{http.MethodGet, "/api/v1/provision/status"},
		{http.MethodGet, "/api/v1/provision/usage"},
		{http.MethodGet, "/api/v1/provision/unknown"},
	}
	for _, tc := range routes {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404 when provisioning keys unset", tc.method, tc.path, rec.Code)
		}
	}
}

func TestGuardRejectsBadBearers(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	k1Raw := testSvcKey('1')
	k2Raw := testSvcKey('2')
	keys, err := ParseKeys([]string{k1Raw, k2Raw})
	if err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	h := Handler{
		Keys: keys,
		Now:  func() time.Time { return now },
	}
	// Mount a dummy guarded route to verify middleware behavior without DB.
	// Only refusals run here: an accepted bearer consumes a nonce, which
	// needs Postgres and is proven by the integration tests.
	mux.Handle("GET /api/v1/provision/ping", h.guard(maxBodyBytes, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(keyIDFrom(r.Context())))
	})))

	sign := func(rawKey, audience, method, path string, at time.Time) string {
		master, err := svcsig.ParseKey(rawKey)
		if err != nil {
			t.Fatal(err)
		}
		derived, err := svcsig.Derive(master, audience)
		if err != nil {
			t.Fatal(err)
		}
		tok, err := svcsig.SignRequest(derived, audience, method, path, "", nil, at)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}

	// Missing bearer -> 401
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/provision/ping", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing bearer = %d, want 401", rec.Code)
	}

	// Wrong audience -> 401
	req := httptest.NewRequest(http.MethodGet, "/api/v1/provision/ping", nil)
	req.Header.Set("Authorization", "Bearer "+sign(k1Raw, svcsig.AudienceGateway, http.MethodGet, "/api/v1/provision/ping", now))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong audience = %d, want 401", rec.Code)
	}

	// Expired token -> 401
	req = httptest.NewRequest(http.MethodGet, "/api/v1/provision/ping", nil)
	req.Header.Set("Authorization", "Bearer "+sign(k1Raw, svcsig.AudienceProvision, http.MethodGet, "/api/v1/provision/ping", now.Add(-time.Minute)))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired bearer = %d, want 401", rec.Code)
	}

	// Signed for different path -> 401
	req = httptest.NewRequest(http.MethodGet, "/api/v1/provision/ping", nil)
	req.Header.Set("Authorization", "Bearer "+sign(k1Raw, svcsig.AudienceProvision, http.MethodGet, "/api/v1/provision/other", now))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong path bearer = %d, want 401", rec.Code)
	}

	// v1 bearer (no query or body binding) -> 401
	master, err := svcsig.ParseKey(k2Raw)
	if err != nil {
		t.Fatal(err)
	}
	derived, err := svcsig.Derive(master, svcsig.AudienceProvision)
	if err != nil {
		t.Fatal(err)
	}
	v1Tok, err := svcsig.Sign(derived, svcsig.AudienceProvision, http.MethodGet, "/api/v1/provision/ping", now)
	if err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/provision/ping", nil)
	req.Header.Set("Authorization", "Bearer "+v1Tok)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("v1 bearer = %d, want 401", rec.Code)
	}
}
