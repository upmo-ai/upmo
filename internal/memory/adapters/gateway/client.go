// Package gateway is brain's client for the model gateway. It signs each
// call with the service HMAC and does not import the gateway process.
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
	"github.com/upmo-ai/upmo/internal/memory/app"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
)

const (
	pathChat  = "/upmo.gateway.v1.Gateway/Chat"
	pathEmbed = "/upmo.gateway.v1.Gateway/Embed"
)

// Client calls the propose and embed routes for one install.
type Client struct {
	base         string
	http         *http.Client
	proposeRoute string
	embedRoute   string
}

// Call is one person's propose and embed calls.
type Call struct {
	client Client
	userID string
	source string
}

// Open builds a client. baseURL is the install setting and serviceKey the
// gateway key (UPMO_GATEWAY_KEY).
// proposeRoute is the chat route for memory work. embedRoute is the embed route.
func Open(baseURL, serviceKey, proposeRoute, embedRoute string) (Client, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return Client{}, fmt.Errorf("memory: gateway url")
	}
	if proposeRoute == "" || embedRoute == "" {
		return Client{}, fmt.Errorf("memory: gateway route")
	}
	derived, err := svcsig.ParseAudienceKey(serviceKey, svcsig.AudienceGateway)
	if err != nil {
		return Client{}, err
	}
	transport, err := svcsig.Transport(nil, derived, svcsig.AudienceGateway)
	if err != nil {
		return Client{}, err
	}
	return Client{
		base:         strings.TrimRight(baseURL, "/"),
		http:         &http.Client{Transport: transport},
		proposeRoute: proposeRoute,
		embedRoute:   embedRoute,
	}, nil
}

// For binds the client to one person. The gateway ledger records that id.
func (c Client) For(userID string) Call {
	return Call{client: c, userID: userID, source: gatewayapi.SourceMemory}
}

// ForTask binds the client to a task's proposer.
func (c Client) ForTask(userID string) Call {
	return Call{client: c, userID: userID, source: gatewayapi.SourceTask}
}

func (c Call) Propose(ctx context.Context, prompt string) (string, error) {
	if c.userID == "" || prompt == "" {
		return "", fmt.Errorf("memory: propose input")
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := c.client.post(ctx, pathChat, map[string]string{
		"route":   c.client.proposeRoute,
		"prompt":  prompt,
		"user_id": c.userID,
		"source":  c.source,
	}, &out); err != nil {
		return "", err
	}
	return out.Text, nil
}

func (c Call) Embed(ctx context.Context, texts []string) (string, [][]float32, error) {
	if c.userID == "" || len(texts) == 0 {
		return "", nil, fmt.Errorf("memory: embed input")
	}
	var out struct {
		Model   string      `json:"model"`
		Vectors [][]float64 `json:"vectors"`
	}
	if err := c.client.post(ctx, pathEmbed, map[string]any{
		"route":   c.client.embedRoute,
		"inputs":  texts,
		"user_id": c.userID,
		"source":  c.source,
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
		return fmt.Errorf("%w: %s", app.ErrRetry, err.Error())
	}
	defer func() { _ = res.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("%w: %s", app.ErrRetry, err.Error())
	}
	if res.StatusCode != http.StatusOK {
		return statusError(res.StatusCode)
	}
	if err := json.Unmarshal(payload, dest); err != nil {
		return fmt.Errorf("memory: gateway body")
	}
	return nil
}

func statusError(code int) error {
	switch code {
	case http.StatusRequestTimeout, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return fmt.Errorf("%w: gateway %d", app.ErrRetry, code)
	default:
		return fmt.Errorf("memory: gateway %d", code)
	}
}
