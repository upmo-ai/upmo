// Package fetch loads public URLs for knowledge ingest through netguard.
package fetch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/upmo-ai/upmo/internal/knowledge/app"
	"github.com/upmo-ai/upmo/internal/platform/netguard"
)

const maxBody = 32 << 20

// HTTPFetcher GETs a URL with the SSRF dial guard.
type HTTPFetcher struct {
	Client *http.Client
}

// New returns a fetcher that dials only public addresses.
func New() HTTPFetcher {
	return HTTPFetcher{
		Client: &http.Client{
			Transport: (&netguard.Guard{}).Transport(),
			Timeout:   60 * time.Second,
		},
	}
}

// Fetch implements app.URLFetcher.
func (f HTTPFetcher) Fetch(ctx context.Context, rawURL string) (string, []byte, error) {
	client := f.Client
	if client == nil {
		client = New().Client
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", nil, app.ErrURL
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return "", nil, fmt.Errorf("fetch: status %d", resp.StatusCode)
	}
	limited := io.LimitReader(resp.Body, maxBody+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return "", nil, err
	}
	if len(body) > maxBody {
		return "", nil, fmt.Errorf("fetch: body too large")
	}
	return resp.Header.Get("Content-Type"), body, nil
}
