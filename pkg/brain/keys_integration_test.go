//go:build integration

package brain

// Starting the brain with sealed rows whose master key id is not loaded warns
// once per affected store without stopping start-up, and the metrics registry
// the brain built exposes the count per store, zero included.

import (
	"bytes"
	"context"
	"encoding/base64"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/brainops"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

// keyringFromEnv builds the ring the environment names, for seeding.
func keyringFromEnv(t *testing.T) gateway.Keyring {
	t.Helper()
	ring, err := gateway.KeyringFromEnv(os.LookupEnv, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	return ring
}

func TestStartupWarnsAndExportsSealedRowsOfAnUnloadedKey(t *testing.T) {
	owner := pgtest.New(t)
	ctx := context.Background()
	if err := brainops.MigrateAll(ctx, owner); err != nil {
		t.Fatal(err)
	}
	appPool := pgtest.App(t, owner)

	// Seed one credential under k0, which the brain will not load.
	t.Setenv(gateway.EnvMasterKey, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x30}, 32)))
	t.Setenv(gateway.EnvMasterKeyID, "k0")
	sealed, err := keyringFromEnv(t).Seal("own_gone", "sk-gone-secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO gateway.credentials (ref, ciphertext, nonce, key_id)
		VALUES ($1, $2, $3, $4)`,
		"own_gone", sealed.Ciphertext, sealed.Nonce, sealed.KeyID); err != nil {
		t.Fatal(err)
	}

	t.Setenv(gateway.EnvMasterKey, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x31}, 32)))
	t.Setenv(gateway.EnvMasterKeyID, "k1")
	logs := &bytes.Buffer{}
	cfg := Config{
		Env:             "development",
		SessionIdle:     identity.IdleTimeout,
		SessionMaxAge:   identity.MaxAge,
		PublicURL:       "http://localhost:5173",
		WebAuthnRPID:    "localhost",
		ShutdownTimeout: 2 * time.Second,
		MetricsAddr:     "127.0.0.1:0", // never bound: the test reads the registry directly
	}
	app, err := New(ctx, cfg,
		WithPools(owner, appPool),
		WithLogger(slog.New(slog.NewTextHandler(logs, nil))),
	)
	if err != nil {
		t.Fatalf("brain.New: %v", err)
	}
	defer app.Close()

	out := logs.String()
	if !strings.Contains(out, `msg="sealed rows use a master key that is not loaded"`) ||
		!strings.Contains(out, "store=gateway.credentials") ||
		!strings.Contains(out, "rows=1") || !strings.Contains(out, "key_ids=[k0]") {
		t.Fatalf("start-up warning = %s", out)
	}
	if strings.Contains(out, "own_gone") || strings.Contains(out, "sk-gone-secret") {
		t.Fatalf("start-up warning carries the row: %s", out)
	}

	// WriteTo runs the registered collector against the owner pool, the way a
	// scrape of the metrics listener would.
	var expo bytes.Buffer
	if err := app.metricsReg.WriteTo(ctx, &expo); err != nil {
		t.Fatal(err)
	}
	got := expo.String()
	for _, want := range []string{
		`upmo_sealed_rows_unknown_key{store="gateway.credentials"} 1`,
		`upmo_sealed_rows_unknown_key{store="identity.users.password_hash"} 0`,
		`upmo_sealed_rows_unknown_key{store="identity.users.email"} 0`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("metrics exposition:\n%s\nwant %s", got, want)
		}
	}
}
