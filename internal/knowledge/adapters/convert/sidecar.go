// Package convert talks to the markitdown-compatible conversion sidecar.
package convert

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/upmo-ai/upmo/internal/knowledge/app"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
)

// Sidecar POSTs raw bytes to the conversion service and returns markdown.
type Sidecar struct {
	BaseURL string
	Client  *http.Client
}

// OpenSidecar builds a sidecar whose calls carry the v2 bearer for the
// markitdown audience. A missing or malformed key is an error, so brain
// cannot hold a sidecar that will never authenticate.
func OpenSidecar(baseURL, markitdownKey string) (*Sidecar, error) {
	key, err := svcsig.ParseAudienceKey(markitdownKey, svcsig.AudienceMarkitdown)
	if err != nil {
		return nil, err
	}
	transport, err := svcsig.Transport(nil, key, svcsig.AudienceMarkitdown)
	if err != nil {
		return nil, err
	}
	return &Sidecar{BaseURL: baseURL, Client: &http.Client{Timeout: 120 * time.Second, Transport: transport}}, nil
}

// Client converts bodies: text skips the sidecar; other types call it.
type Client struct {
	Text    app.PassThroughText
	Sidecar *Sidecar
}

// ToMarkdown implements app.BodyConverter.
func (c Client) ToMarkdown(ctx context.Context, contentType string, body []byte) (string, error) {
	media := contentType
	if i := strings.Index(media, ";"); i >= 0 {
		media = media[:i]
	}
	media = strings.TrimSpace(strings.ToLower(media))
	switch media {
	case "text/plain", "text/markdown", "text/x-markdown":
		return c.Text.ToMarkdown(ctx, contentType, body)
	}
	if c.Sidecar == nil || strings.TrimSpace(c.Sidecar.BaseURL) == "" {
		return "", app.ErrConvert
	}
	return c.Sidecar.ToMarkdown(ctx, contentType, body)
}

// ToMarkdown implements app.BodyConverter for non-text bodies.
func (s *Sidecar) ToMarkdown(ctx context.Context, contentType string, body []byte) (string, error) {
	if s == nil || strings.TrimSpace(s.BaseURL) == "" {
		return "", app.ErrConvert
	}
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 120 * time.Second}
	}
	url := strings.TrimRight(s.BaseURL, "/") + "/convert"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", app.ErrConvert
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if contentType != "" {
		req.Header.Set("X-Mimetype", contentType)
		switch {
		case strings.Contains(contentType, "pdf"):
			req.Header.Set("X-Filename", "document.pdf")
		case strings.Contains(contentType, "html"):
			req.Header.Set("X-Filename", "document.html")
		case strings.Contains(contentType, "wordprocessingml"):
			req.Header.Set("X-Filename", "document.docx")
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", app.ErrConvert, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20+1))
	if err != nil {
		return "", app.ErrConvert
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("%w: status %d", app.ErrConvert, resp.StatusCode)
	}
	var out struct {
		Markdown string `json:"markdown"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || strings.TrimSpace(out.Markdown) == "" {
		return "", app.ErrConvert
	}
	return out.Markdown, nil
}
