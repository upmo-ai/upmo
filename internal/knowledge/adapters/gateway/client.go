// Package gateway is brain's knowledge client for the model gateway Embed RPC.
// It signs each call with the service HMAC and does not import the gateway process.
package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	gatewayapi "github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/knowledge/app"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
)

const pathEmbed = "/upmo.gateway.v1.Gateway/Embed"

// Client calls the install embed route for knowledge work.
type Client struct {
	base       string
	http       *http.Client
	embedRoute string
}

// Call is one person's embed calls (ledger attribution).
type Call struct {
	client Client
	userID string
}

// Open builds a client. baseURL and serviceKey are the install settings.
// embedRoute is the gateway embed route name.
func Open(baseURL, serviceKey, embedRoute string) (Client, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return Client{}, fmt.Errorf("knowledge: gateway url")
	}
	if embedRoute == "" {
		return Client{}, fmt.Errorf("knowledge: gateway route")
	}
	master, err := svcsig.ParseKey(serviceKey)
	if err != nil {
		return Client{}, err
	}
	derived, err := svcsig.Derive(master, svcsig.AudienceGateway)
	if err != nil {
		return Client{}, err
	}
	transport, err := svcsig.Transport(nil, derived, svcsig.AudienceGateway)
	if err != nil {
		return Client{}, err
	}
	return Client{
		base:       strings.TrimRight(baseURL, "/"),
		http:       &http.Client{Transport: transport},
		embedRoute: embedRoute,
	}, nil
}

// For binds the client to one person. The gateway ledger records that id.
func (c Client) For(userID string) app.Embedder {
	return Call{client: c, userID: userID}
}

// Embed implements app.Embedder for one person.
func (c Call) Embed(ctx context.Context, texts []string) (string, [][]float32, error) {
	if c.userID == "" || len(texts) == 0 {
		return "", nil, fmt.Errorf("knowledge: embed input")
	}
	var out struct {
		Model   string      `json:"model"`
		Vectors [][]float64 `json:"vectors"`
	}
	if err := c.client.post(ctx, pathEmbed, map[string]any{
		"route":   c.client.embedRoute,
		"inputs":  texts,
		"user_id": c.userID,
		"source":  gatewayapi.SourceKnowledge,
	}, &out); err != nil {
		return "", nil, err
	}
	vectors := make([][]float32, len(out.Vectors))
	for i, row := range out.Vectors {
		vectors[i] = make([]float32, len(row))
		for j, n := range row {
			vectors[i][j] = float32(n)
		}
	}
	return out.Model, vectors, nil
}

func (c Client) post(ctx context.Context, path string, body any, dest any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("knowledge: gateway: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("knowledge: gateway: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("knowledge: gateway %d", res.StatusCode)
	}
	if err := json.Unmarshal(payload, dest); err != nil {
		return fmt.Errorf("knowledge: gateway body")
	}
	return nil
}
