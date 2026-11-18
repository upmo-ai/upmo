//go:build integration

package brain

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestStreamFlushesThroughBrainHandler proves an event reaches the client
// while the handler is still running, so no wrapper in the chain swallows Flush.
func TestStreamFlushesThroughBrainHandler(t *testing.T) {
	srv := httptest.NewServer(securityHeadersHandler(t))
	defer srv.Close()

	client := &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 3 * time.Second}}
	res, err := client.Get(srv.URL + "/api/test-flush")
	if err != nil {
		t.Fatalf("no flushed response before the handler returned: %v", err)
	}
	defer res.Body.Close()

	got, err := bufio.NewReader(res.Body).ReadString('\n')
	if err != nil || got != "data: x\n" {
		t.Fatalf("first line = %q, err %v, want %q", got, err, "data: x\n")
	}
}
