package provision

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

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
