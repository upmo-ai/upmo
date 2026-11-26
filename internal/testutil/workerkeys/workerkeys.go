// Package workerkeys starts the worker's real key service for integration
// tests: brainops.KeysHandler over the owner pool and a real ring, behind an
// httptest listener, with one fresh audience key per caller. Callers reach
// it through the real keys client.
package workerkeys

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/brainops"
	"github.com/upmo-ai/upmo/internal/platform/keys"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
)

// Worker is one running key service.
type Worker struct {
	// URL is the listener, http on loopback.
	URL string
	// Key holds each worker audience's key, upmo_svc_<audience>_...
	Key    map[string]string
	server *httptest.Server
}

// Start migrates the audit schema, so audit.record_keys exists, and serves
// the key routes with ring until the test ends.
func Start(t *testing.T, owner *pgxpool.Pool, ring gateway.Keyring) *Worker {
	t.Helper()
	if err := audit.Up(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	w := &Worker{Key: map[string]string{}}
	verify := gateway.KeysVerify{}
	for _, audience := range []string{svcsig.AudienceWorkerBrain, svcsig.AudienceWorkerGateway, svcsig.AudienceWorkerSandboxd} {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			t.Fatal(err)
		}
		w.Key[audience] = "upmo_svc_" + audience + "_" + base64.RawStdEncoding.EncodeToString(raw)
		verify[audience] = [][]byte{raw}
	}
	h, err := brainops.KeysHandler(owner, ring, verify, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	w.server = httptest.NewServer(h)
	w.URL = w.server.URL
	t.Cleanup(w.server.Close)
	return w
}

// Client returns the real client for one audience.
func (w *Worker) Client(t *testing.T, audience string) *keys.Client {
	t.Helper()
	c, err := keys.Open(keys.Options{URL: w.URL, Key: w.Key[audience], Audience: audience, Development: true})
	if err != nil {
		t.Fatal(err)
	}
	return &c
}

// Sealer is brain's credential sealer over this worker.
func (w *Worker) Sealer(t *testing.T) gateway.WorkerSealer {
	t.Helper()
	return gateway.WorkerSealer{Keys: w.Client(t, svcsig.AudienceWorkerBrain)}
}

// Stop closes the listener; every later call is keys: worker unavailable.
func (w *Worker) Stop() { w.server.Close() }
