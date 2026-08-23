package mcpauth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestCanonicalResource(t *testing.T) {
	got, err := CanonicalResource("HTTPS://MCP.Example.COM:443/mcp")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://mcp.example.com/mcp" {
		t.Fatalf("got %s", got)
	}
	if _, err := CanonicalResource("https://mcp.example.com/mcp#frag"); err == nil {
		t.Fatal("fragment")
	}
	if _, err := CanonicalResource("mcp.example.com"); err == nil {
		t.Fatal("missing scheme")
	}
}

func TestTokenValuesRequireResourceAndCIMD(t *testing.T) {
	req := TokenRequest{
		GrantType: "authorization_code",
		ClientID:  "https://upmo.example/client.json",
		Resource:  "https://mcp.example.com/mcp",
		Code:      "abc",
		Scope:     "files:read",
	}
	v, err := req.Values()
	if err != nil {
		t.Fatal(err)
	}
	if v.Get("resource") != "https://mcp.example.com/mcp" {
		t.Fatalf("resource = %s", v.Get("resource"))
	}
	req.ClientID = "upmo-web"
	if _, err := req.Values(); err == nil {
		t.Fatal("static client_id")
	}
	req.ClientID = "https://upmo.example/client.json"
	req.Resource = ""
	if _, err := req.Values(); err == nil {
		t.Fatal("empty resource")
	}
	req.Resource = "https://mcp.example.com"
	req.ClientSecret = "upmo_pat_abc"
	if _, err := req.Values(); err == nil {
		t.Fatal("passthrough secret")
	}
}

func TestAuthorizationQueryIncludesResource(t *testing.T) {
	got, err := AuthorizationQuery(
		"https://as.example.com/authorize",
		"https://upmo.example/client.json",
		"https://upmo.example/cb",
		"https://mcp.example.com/mcp",
		"files:read",
		"st",
		"challenge",
	)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("resource") != "https://mcp.example.com/mcp" || q.Get("code_challenge_method") != "S256" {
		t.Fatalf("query = %s", u.RawQuery)
	}
}

func TestUnionScopes(t *testing.T) {
	if got := UnionScopes("files:read", "files:write"); got != "files:read files:write" {
		t.Fatalf("got %s", got)
	}
	if got := UnionScopes("files:read", "files:read"); got != "files:read" {
		t.Fatalf("dedupe %s", got)
	}
}

func TestCheckAuthResponse(t *testing.T) {
	meta := ServerMetadata{Issuer: "https://as.example.com", ISSParameterSupported: true}
	if err := CheckAuthResponse(meta, "https://as.example.com", "https://as.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := CheckAuthResponse(meta, "https://as.example.com", ""); err == nil {
		t.Fatal("iss required when advertised")
	}
	meta.ISSParameterSupported = false
	if err := CheckAuthResponse(meta, "https://as.example.com", ""); err != nil {
		t.Fatal(err)
	}
}

func TestTokenPostsResource(t *testing.T) {
	var posted url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		posted, _ = url.ParseQuery(string(raw))
		_, _ = w.Write([]byte(`{"access_token":"mcp_tok"}`))
	}))
	t.Cleanup(srv.Close)
	c := Client{HTTP: srv.Client()}
	tok, err := c.Token(context.Background(), srv.URL, TokenRequest{
		GrantType: "authorization_code",
		ClientID:  "https://upmo.example/client.json",
		Resource:  "https://mcp.example.com/mcp",
		Code:      "abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if tok != "mcp_tok" {
		t.Fatalf("tok = %s", tok)
	}
	if posted.Get("resource") != "https://mcp.example.com/mcp" {
		t.Fatalf("posted = %v", posted)
	}
}

func TestTokenRefusesPassthroughAccessToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"upmo_pat_abc"}`))
	}))
	t.Cleanup(srv.Close)
	c := Client{HTTP: srv.Client()}
	if _, err := c.Token(context.Background(), srv.URL, TokenRequest{
		GrantType: "client_credentials",
		ClientID:  "https://upmo.example/client.json",
		Resource:  "https://mcp.example.com",
	}); err == nil {
		t.Fatal("accepted upmo token")
	}
}

func TestServerMetadataURLs(t *testing.T) {
	got, err := ServerMetadataURL("https://as.example.com/issuer1")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://as.example.com/.well-known/oauth-authorization-server/issuer1" {
		t.Fatalf("rfc8414 = %s", got)
	}
	oidc, err := OIDCMetadataURL("https://as.example.com/issuer1")
	if err != nil {
		t.Fatal(err)
	}
	if oidc != "https://as.example.com/issuer1/.well-known/openid-configuration" {
		t.Fatalf("oidc = %s", oidc)
	}
}

func TestFetchServerFallsBackToOIDC(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"issuer": "https://as.example.com",
			"token_endpoint": "https://as.example.com/token"
		}`))
	})
	c := Client{HTTP: &http.Client{Transport: handlerRT{mux}}}
	got, err := c.FetchServer(context.Background(), "https://as.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got.TokenEndpoint != "https://as.example.com/token" {
		t.Fatalf("got = %+v", got)
	}
}

func TestDiscoverPrefersChallenge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "from-challenge") {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{
			"resource": "https://mcp.example.com/api",
			"authorization_servers": ["https://as.example.com"],
			"scopes_supported": ["files:read"]
		}`))
	}))
	t.Cleanup(srv.Close)
	c := Client{HTTP: srv.Client()}
	got, err := c.Discover(context.Background(), "https://mcp.example.com/api", Challenge{
		ResourceMetadata: srv.URL + "/from-challenge",
	})
	if err != nil {
		t.Fatal(err)
	}
	if InitialScope(Challenge{}, got) != "files:read" {
		t.Fatalf("scope = %s", InitialScope(Challenge{}, got))
	}
}

type handlerRT struct{ h http.Handler }

func (r handlerRT) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	return rec.Result(), nil
}
