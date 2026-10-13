package mcpauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// TokenRequest is an OAuth token request. Resource is required (RFC 8707).
type TokenRequest struct {
	GrantType    string
	ClientID     string
	ClientSecret string
	Resource     string
	Scope        string
	Code         string
	CodeVerifier string
	RefreshToken string
	RedirectURI  string
}

// Values encodes the form, always with a canonical resource.
func (r TokenRequest) Values() (url.Values, error) {
	if err := CheckClientID(r.ClientID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(r.GrantType) == "" {
		return nil, fmt.Errorf("mcp: grant_type is empty")
	}
	resource, err := CanonicalResource(r.Resource)
	if err != nil {
		return nil, err
	}
	if err := RefusePassthrough(r.ClientSecret); err != nil {
		return nil, err
	}
	v := url.Values{}
	v.Set("grant_type", r.GrantType)
	v.Set("client_id", r.ClientID)
	v.Set("resource", resource)
	if r.Scope != "" {
		v.Set("scope", r.Scope)
	}
	if r.Code != "" {
		v.Set("code", r.Code)
	}
	if r.CodeVerifier != "" {
		v.Set("code_verifier", r.CodeVerifier)
	}
	if r.RefreshToken != "" {
		v.Set("refresh_token", r.RefreshToken)
	}
	if r.RedirectURI != "" {
		v.Set("redirect_uri", r.RedirectURI)
	}
	if r.ClientSecret != "" {
		v.Set("client_secret", r.ClientSecret)
	}
	return v, nil
}

// CanonicalResource is the RFC 8707 resource identifier: https, no fragment,
// lowercase scheme and host, no default port, no trailing slash on root.
func CanonicalResource(raw string) (string, error) {
	u, err := httpsURL(raw)
	if err != nil {
		return "", err
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return "", fmt.Errorf("mcp: resource has a fragment")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port != "" && (u.Scheme != "https" || port != "443") && (u.Scheme != "http" || port != "80") {
		u.Host = net.JoinHostPort(host, port)
	} else {
		u.Host = host
	}
	u.Fragment = ""
	if u.Path == "/" {
		u.Path = ""
	}
	return u.String(), nil
}

// AuthorizationQuery builds an authorization URL that includes resource.
func AuthorizationQuery(authURL, clientID, redirectURI, resource, scope, state, codeChallenge string) (string, error) {
	if err := CheckClientID(clientID); err != nil {
		return "", err
	}
	u, err := httpsURL(authURL)
	if err != nil {
		return "", fmt.Errorf("mcp: authorization_endpoint: %w", err)
	}
	res, err := CanonicalResource(resource)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("resource", res)
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")
	if scope != "" {
		q.Set("scope", scope)
	}
	if state != "" {
		q.Set("state", state)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// UnionScopes is the step-up set: previous scopes plus the challenge.
func UnionScopes(prev, extra string) string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range strings.Fields(prev + " " + extra) {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return strings.Join(out, " ")
}

type tokenJSON struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
}

// Token POSTs a token request. Redirects are not followed.
func (c Client) Token(ctx context.Context, tokenURL string, req TokenRequest) (string, error) {
	vals, err := req.Values()
	if err != nil {
		return "", err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(vals.Encode()))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	cli := *c.client()
	cli.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	res, err := cli.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("mcp: token %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var doc tokenJSON
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", err
	}
	if strings.TrimSpace(doc.AccessToken) == "" {
		return "", fmt.Errorf("mcp: access_token is empty")
	}
	if err := RefusePassthrough(doc.AccessToken); err != nil {
		return "", err
	}
	return doc.AccessToken, nil
}
