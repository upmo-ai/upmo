package mcpauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/upmo-ai/upmo/internal/platform/netguard"
)

// Client fetches protected-resource metadata. Production uses netguard;
// tests inject HTTP.
type Client struct {
	HTTP *http.Client
}

func (c Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Transport: (&netguard.Guard{}).Transport()}
}

// FetchMetadata GETs an RFC 9728 document. The URL is already the well-known
// location; ForResource builds that from a resource identifier.
func (c Client) FetchMetadata(ctx context.Context, metadataURL string) (ResourceMetadata, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return ResourceMetadata{}, err
	}
	res, err := c.client().Do(req)
	if err != nil {
		return ResourceMetadata{}, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return ResourceMetadata{}, fmt.Errorf("mcp: metadata %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return ResourceMetadata{}, err
	}
	return ParseResourceMetadata(raw)
}

// ForResource loads metadata for one MCP resource identifier.
func (c Client) ForResource(ctx context.Context, resource string) (ResourceMetadata, error) {
	loc, err := MetadataURL(resource)
	if err != nil {
		return ResourceMetadata{}, err
	}
	return c.FetchMetadata(ctx, loc)
}

// ErrPassthrough is returned when an Upmo token would be sent to an MCP server.
var ErrPassthrough = fmt.Errorf("mcp: token passthrough is forbidden")

// ResourceMetadata is RFC 9728 protected resource metadata.
type ResourceMetadata struct {
	Resource             string
	AuthorizationServers []string
}

type metadataJSON struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
}

// ParseResourceMetadata reads the JSON document a resource server publishes.
func ParseResourceMetadata(raw []byte) (ResourceMetadata, error) {
	var doc metadataJSON
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ResourceMetadata{}, err
	}
	if strings.TrimSpace(doc.Resource) == "" {
		return ResourceMetadata{}, fmt.Errorf("mcp: resource is empty")
	}
	if len(doc.AuthorizationServers) == 0 {
		return ResourceMetadata{}, fmt.Errorf("mcp: authorization_servers is empty")
	}
	for _, as := range doc.AuthorizationServers {
		if _, err := httpsURL(as); err != nil {
			return ResourceMetadata{}, fmt.Errorf("mcp: authorization_server: %w", err)
		}
	}
	if _, err := httpsURL(doc.Resource); err != nil {
		return ResourceMetadata{}, fmt.Errorf("mcp: resource: %w", err)
	}
	return ResourceMetadata{Resource: doc.Resource, AuthorizationServers: doc.AuthorizationServers}, nil
}

// MetadataURL is the RFC 9728 well-known location for a resource URL.
func MetadataURL(resource string) (string, error) {
	u, err := httpsURL(resource)
	if err != nil {
		return "", err
	}
	path := strings.TrimSuffix(u.Path, "/")
	u.Path = "/.well-known/oauth-protected-resource" + path
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// Challenge is a WWW-Authenticate Bearer challenge (step-up).
type Challenge struct {
	Error            string
	Scope            string
	ResourceMetadata string
}

// ParseWWWAuthenticate reads a Bearer challenge. Other schemes are ignored.
func ParseWWWAuthenticate(header string) (Challenge, error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return Challenge{}, fmt.Errorf("mcp: WWW-Authenticate is empty")
	}
	if !strings.HasPrefix(strings.ToLower(header), "bearer ") {
		return Challenge{}, fmt.Errorf("mcp: WWW-Authenticate is not Bearer")
	}
	rest := strings.TrimSpace(header[len("Bearer "):])
	out := Challenge{}
	for _, part := range splitAuthParams(rest) {
		key, val, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.Trim(strings.TrimSpace(val), `"`)
		switch key {
		case "error":
			out.Error = val
		case "scope":
			out.Scope = val
		case "resource_metadata":
			out.ResourceMetadata = val
		}
	}
	return out, nil
}

// NeedsStepUp is true when the server asked for a richer token.
func NeedsStepUp(c Challenge) bool {
	return c.Error == "insufficient_scope"
}

// RefusePassthrough rejects Upmo personal, service and HMAC tokens so they
// never become an MCP Authorization header.
func RefusePassthrough(token string) error {
	t := strings.TrimSpace(token)
	if t == "" {
		return nil
	}
	for _, prefix := range []string{"upmo_pat_", "upmo_s2s_", "upmo_svc_"} {
		if strings.HasPrefix(t, prefix) {
			return ErrPassthrough
		}
	}
	if strings.HasPrefix(strings.ToLower(t), "bearer upmo_") {
		return ErrPassthrough
	}
	return nil
}

// CheckClientID requires a Client ID Metadata Document HTTPS URL (CIMD).
func CheckClientID(clientID string) error {
	if _, err := httpsURL(clientID); err != nil {
		return fmt.Errorf("mcp: client_id must be an https URL")
	}
	return nil
}

// CheckIssuer compares the RFC 9207 iss value to the authorization server.
func CheckIssuer(expected, got string) error {
	if expected == "" || got == "" {
		return fmt.Errorf("mcp: issuer is empty")
	}
	if expected != got {
		return fmt.Errorf("mcp: issuer %q != %q", got, expected)
	}
	return nil
}

func httpsURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("mcp: %q is not an https URL", raw)
	}
	return u, nil
}

func splitAuthParams(s string) []string {
	var out []string
	var b strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			inQuote = !inQuote
			b.WriteByte(c)
		case c == ',' && !inQuote:
			if part := strings.TrimSpace(b.String()); part != "" {
				out = append(out, part)
			}
			b.Reset()
		default:
			b.WriteByte(c)
		}
	}
	if part := strings.TrimSpace(b.String()); part != "" {
		out = append(out, part)
	}
	return out
}

// Header is a convenience for tests and callers reading a response.
func Header(h http.Header) string {
	return h.Get("WWW-Authenticate")
}
