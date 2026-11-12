//go:build integration

package audit

// What the package alone can reach, over a real database: the cursor that
// never moves backwards, and the default webhook client that answers a
// redirect as the refusal it is.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/platform/netguard"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

// pushPool is one owner pool over a migrated audit schema.
func pushPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := pgtest.New(t)
	if err := Up(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

// fixedSecrets is a secrets chain that answers every ref alike.
type fixedSecrets string

func (s fixedSecrets) Get(string) (string, error) { return string(s), nil }

// whsec makes one whsec_ signing secret.
func whsec(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return "whsec_" + base64.StdEncoding.EncodeToString(raw)
}

// sealOneEvent writes one sealed event at position 1, the least a push pass
// needs to have something to deliver.
func sealOneEvent(t *testing.T, owner *pgxpool.Pool) {
	t.Helper()
	changes, err := json.Marshal(Changes{Fields: []string{}, Values: map[string]Change{}})
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte("one"))
	if _, err := owner.Exec(context.Background(), `
		INSERT INTO audit.events (
			position, sealed_at, prev_hash, hash,
			id, occurred_at, service, action, outcome, reason,
			actor_type, credential_type, target_type, target_id, changes, user_agent
		) VALUES (
			1, now(), $1, $2,
			$3::uuid, now(), 'brain', $4, $5, '',
			'system', 'none', 'session', $6, $7::jsonb, ''
		)`,
		make([]byte, 32), hash[:], uuid.NewString(), string(AuthSignOut), string(Success), uuid.NewString(), changes,
	); err != nil {
		t.Fatal(err)
	}
}

// The cursor never moves backwards: a lower position written through the same
// upsert leaves the higher one stored.
func TestWriteCursorNeverMovesBackwards(t *testing.T) {
	ctx := context.Background()
	pool := pushPool(t)

	if err := writeCursor(ctx, pool, "webhook", 7); err != nil {
		t.Fatal(err)
	}
	if err := writeCursor(ctx, pool, "webhook", 3); err != nil {
		t.Fatal(err)
	}
	var position int64
	if err := pool.QueryRow(ctx,
		`SELECT position FROM audit.push_cursor WHERE target = 'webhook'`,
	).Scan(&position); err != nil {
		t.Fatal(err)
	}
	if position != 7 {
		t.Fatalf("cursor = %d, want the higher 7", position)
	}
}

// The default webhook client refuses a redirect: the 307 answer reaches the
// pusher as itself, the landing URL sees nothing, and the cursor does not
// move. Only the transport is swapped, for a guard that lets the loopback
// servers through; the dial stays netguard's.
func TestWebhookClientRefusesARedirect(t *testing.T) {
	ctx := context.Background()
	pool := pushPool(t)
	sealOneEvent(t, pool)

	var landed atomic.Int32
	landing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		landed.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer landing.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", landing.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	client := newWebhookClient()
	client.Transport = (&netguard.Guard{Allow: func(string) bool { return true }}).Transport()
	pusher := NewPusher(pool, PushConfig{
		WebhookURL: redirect.URL,
		Secrets:    fixedSecrets(whsec(t)),
		Client:     client,
	})

	if err := pusher.PushOnce(ctx); err == nil {
		t.Fatal("a redirect counted as delivered")
	}
	if got := landed.Load(); got != 0 {
		t.Fatalf("%d requests reached the landing URL, want none", got)
	}
	var position int64
	err := pool.QueryRow(ctx,
		`SELECT position FROM audit.push_cursor WHERE target = 'webhook'`,
	).Scan(&position)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cursor = %d %v, want no row", position, err)
	}
}
