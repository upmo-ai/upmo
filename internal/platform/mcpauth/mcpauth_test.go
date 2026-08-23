package mcpauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseResourceMetadata(t *testing.T) {
	got, err := ParseResourceMetadata([]byte(`{
		"resource": "https://mcp.example.com/api",
		"authorization_servers": ["https://as.example.com"]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Resource != "https://mcp.example.com/api" || got.AuthorizationServers[0] != "https://as.example.com" {
		t.Fatalf("got = %+v", got)
	}
	if _, err := ParseResourceMetadata([]byte(`{"resource":"http://insecure.example"}`)); err == nil {
		t.Fatal("http resource")
	}
}

func TestMetadataURLInsertsPath(t *testing.T) {
	got, err := MetadataURL("https://mcp.example.com/api")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://mcp.example.com/.well-known/oauth-protected-resource/api" {
		t.Fatalf("url = %s", got)
	}
}

func TestParseWWWAuthenticateStepUp(t *testing.T) {
	c, err := ParseWWWAuthenticate(`Bearer error="insufficient_scope", scope="tools:write", resource_metadata="https://mcp.example.com/.well-known/oauth-protected-resource"`)
	if err != nil {
		t.Fatal(err)
	}
	if !NeedsStepUp(c) || c.Scope != "tools:write" {
		t.Fatalf("challenge = %+v", c)
	}
}

func TestRefusePassthrough(t *testing.T) {
	for _, token := range []string{"upmo_pat_abc", "upmo_s2s_v1.x", "upmo_svc_Z2d", "Bearer upmo_pat_abc"} {
		if err := RefusePassthrough(token); err == nil {
			t.Fatalf("accepted %q", token)
		}
	}
	if err := RefusePassthrough("mcp_access_token"); err != nil {
		t.Fatal(err)
	}
}

func TestFetchMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"resource": "https://mcp.example.com/api",
			"authorization_servers": ["https://as.example.com"]
		}`))
	}))
	t.Cleanup(srv.Close)
	c := Client{HTTP: srv.Client()}
	got, err := c.FetchMetadata(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if got.Resource != "https://mcp.example.com/api" {
		t.Fatalf("got = %+v", got)
	}
}

func TestCheckClientIDAndIssuer(t *testing.T) {
	if err := CheckClientID("https://upmo.example/client.json"); err != nil {
		t.Fatal(err)
	}
	if err := CheckClientID("upmo-web"); err == nil {
		t.Fatal("static client_id")
	}
	if err := CheckIssuer("https://as.example.com", "https://as.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := CheckIssuer("https://as.example.com", "https://other.example"); err == nil {
		t.Fatal("iss mismatch")
	}
}
