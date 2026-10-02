package fetch_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/knowledge/adapters/fetch"
	"github.com/upmo-ai/upmo/internal/platform/netguard"
)

func TestFetchPlainText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("hello knowledge"))
	}))
	t.Cleanup(srv.Close)
	host, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Transport: (&netguard.Guard{Allow: func(h string) bool { return h == host.Hostname() }}).Transport(),
		Timeout:   5 * time.Second,
	}
	ctype, body, err := fetch.HTTPFetcher{Client: client}.Fetch(context.Background(), srv.URL)
	if err != nil || ctype != "text/plain" || string(body) != "hello knowledge" {
		t.Fatalf("ctype %q body %q err %v", ctype, body, err)
	}
}

func TestFetchBlocksPrivate(t *testing.T) {
	_, _, err := fetch.New().Fetch(context.Background(), "http://127.0.0.1/")
	if !errors.Is(err, netguard.ErrBlocked) {
		t.Fatalf("err %v", err)
	}
}
