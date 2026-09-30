package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVaultHTTPReadsValue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Vault-Token") != "tok" {
			t.Fatalf("token = %q", r.Header.Get("X-Vault-Token"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"data": map[string]any{"value": "secret"}},
		})
	}))
	defer srv.Close()
	got, err := VaultHTTP(srv.URL, "tok", "secret/data", "house")
	if err != nil || got != "secret" {
		t.Fatalf("got %q %v", got, err)
	}
}
