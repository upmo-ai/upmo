//go:build integration

package gateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	workerkeys "github.com/upmo-ai/upmo/internal/platform/keys"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

// keyWorker is the worker's real key service over the rig's owner pool.
type keyWorker struct {
	srv *httptest.Server
	key map[string]string
}

func startKeyWorker(t *testing.T, r *rig, ring Keyring) *keyWorker {
	t.Helper()
	w := &keyWorker{key: map[string]string{}}
	verify := KeysVerify{}
	for _, audience := range []string{svcsig.AudienceWorkerBrain, svcsig.AudienceWorkerGateway, svcsig.AudienceWorkerSandboxd} {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			t.Fatal(err)
		}
		w.key[audience] = "upmo_svc_" + audience + "_" + base64.RawStdEncoding.EncodeToString(raw)
		verify[audience] = [][]byte{raw}
	}
	h, err := KeysHandler(r.owner, ring, verify, nil)
	if err != nil {
		t.Fatal(err)
	}
	w.srv = httptest.NewServer(h)
	t.Cleanup(w.srv.Close)
	return w
}

func (w *keyWorker) client(t *testing.T, audience string) *workerkeys.Client {
	t.Helper()
	c, err := workerkeys.Open(workerkeys.Options{URL: w.srv.URL, Key: w.key[audience], Audience: audience, Development: true})
	if err != nil {
		t.Fatal(err)
	}
	return &c
}

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// Brain saves and replaces a key through the worker's wrap as upmo_app,
// gateway opens it as upmo_gateway through the worker's unwrap, and with the
// worker gone a save is a 503, never a key under the master.
func TestBrainSavesAKeyThroughTheWorkerAndGatewayOpensIt(t *testing.T) {
	ring := masterKeys(t, 'a')
	r := newRig(t, ring)
	worker := startKeyWorker(t, r, ring)
	r.svc.Keys = WorkerSealer{Keys: worker.client(t, svcsig.AudienceWorkerBrain)}
	r.mount(slog.New(slog.NewJSONHandler(r.logs, nil)))

	prov := newFakeProvider(t)
	made := r.mustAdd(ada, body("Mine", prov.url(), adaKey))
	if code, raw := r.call(ada, http.MethodPut, "/api/providers/"+made.ID, map[string]any{"api_key": newKey}); code != http.StatusOK {
		t.Fatalf("replace the key = %d %s", code, raw)
	}
	ref := refOf(t, r, made.ID)
	var ciphertext, nonce, wrapped, wrapNonce []byte
	var keyID string
	if err := r.owner.QueryRow(context.Background(), `
		SELECT ciphertext, nonce, key_id, wrapped_key, wrap_nonce FROM gateway.credentials WHERE ref = $1`, ref).
		Scan(&ciphertext, &nonce, &keyID, &wrapped, &wrapNonce); err != nil {
		t.Fatal(err)
	}
	if keyID != "k1" || len(wrapped) != 48 || len(wrapNonce) != 12 || bytes.Contains(ciphertext, []byte(newKey)) {
		t.Fatalf("stored row: key %q wrapped %d nonce %d", keyID, len(wrapped), len(wrapNonce))
	}
	// The master does not open the payload: it is under its own data key.
	if _, err := ring.Open(ref, domain.Sealed{Ciphertext: ciphertext, Nonce: nonce, KeyID: keyID}); !errors.Is(err, domain.ErrUnseal) {
		t.Fatalf("the master opened the payload: %v", err)
	}

	linkRoute(t, r, "draft", made.ID)
	mux := http.NewServeMux()
	Mount(mux, r.owner, Chain{EnvSecrets{}, DBSecrets{Pool: pgtest.Login(t, r.owner, "upmo_gateway"), Unwrap: worker.client(t, svcsig.AudienceWorkerGateway)}}, nil)
	code, out := chatVia(t, mux, "draft", ref)
	if code != http.StatusOK || !strings.Contains(out, "hello from provider") || prov.lastAuth() != "Bearer "+newKey {
		t.Fatalf("chat = %d %s, provider saw %q", code, out, prov.lastAuth())
	}

	worker.srv.Close()
	code, raw := r.add(ada, body("Second", prov.url(), adaKey))
	if code != http.StatusServiceUnavailable || !strings.Contains(string(raw), "Provider keys cannot be saved right now.") {
		t.Fatalf("add with the worker gone = %d %s", code, raw)
	}
	if n := r.count(`SELECT count(*) FROM gateway.provider_accounts WHERE name = 'Second'`); n != 0 {
		t.Fatalf("an account was saved without its key: %d", n)
	}
	r.assertNoSecrets(adaKey, newKey)
}

// Invariant 9, brain's part: upmo_app writes a saved key and reads back only
// its ref, key id and age; a row without its wrap does not commit.
func TestBrainCannotReadASavedKeyBack(t *testing.T) {
	r := newRig(t, masterKeys(t, 'a'))
	made := r.mustAdd(ada, body("Mine", "https://127.0.0.1", adaKey))
	ref := refOf(t, r, made.ID)
	ctx := context.Background()
	who := app.Caller{User: ada}
	as := func(query string) error {
		tx, err := r.app.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := r.asPerson(ctx, tx, ada, ""); err != nil {
			t.Fatal(err)
		}
		rows, err := tx.Query(ctx, query, ref)
		if err != nil {
			return err
		}
		n := 0
		for rows.Next() {
			n++
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if n != 1 {
			t.Fatalf("%s saw %d rows", query, n)
		}
		return nil
	}
	for _, column := range []string{"ciphertext", "nonce", "wrapped_key", "wrap_nonce"} {
		if err := as(`SELECT ` + column + ` FROM gateway.credentials WHERE ref = $1`); pgCode(err) != "42501" {
			t.Errorf("upmo_app SELECT %s = %v, want 42501", column, err)
		}
	}
	if err := as(`SELECT ref, key_id, created_at FROM gateway.credentials WHERE ref = $1`); err != nil {
		t.Fatalf("upmo_app SELECT ref, key_id, created_at = %v", err)
	}
	// The real write path, with a sealed value that lacks its wrap.
	accounts := postgres.Providers{Pool: r.app}
	cur, err := accounts.Get(ctx, who, made.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = accounts.Update(ctx, who, cur, &domain.Sealed{Ciphertext: []byte("payload"), Nonce: make([]byte, 12), KeyID: "k1"}, false)
	if pgCode(err) != "23502" {
		t.Fatalf("a key without its wrap = %v, want 23502", err)
	}
}
