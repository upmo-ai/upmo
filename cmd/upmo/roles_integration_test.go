//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/platform/brainops"
	"github.com/upmo-ai/upmo/internal/platform/config"
	"github.com/upmo-ai/upmo/internal/platform/jobs"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/provision"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

const (
	sandboxTask  = "11111111-1111-7111-8111-111111111111"
	sandboxOwner = "22222222-2222-7222-8222-222222222222"
	otherPerson  = "33333333-3333-7333-8333-333333333333"
)

// sandboxSecret is the SHA-256 of 32 bytes of b, the shape of a stored proxy
// secret hash.
func sandboxSecret(b byte) []byte {
	sum := sha256.Sum256(bytes.Repeat([]byte{b}, 32))
	return sum[:]
}

func migrated(t *testing.T) *pgxpool.Pool {
	t.Helper()
	owner := pgtest.New(t)
	if err := brainops.MigrateAll(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	return owner
}

func mustCode(t *testing.T, what string, err error, code string) {
	t.Helper()
	if got := pgCode(err); got != code {
		t.Fatalf("%s = %v, want SQLSTATE %s", what, err, code)
	}
}

func storedSandbox(t *testing.T, owner *pgxpool.Pool) (sandboxID string, hash []byte, expires time.Time) {
	t.Helper()
	var id *string
	if err := owner.QueryRow(context.Background(), `
		SELECT sandbox_id, proxy_secret_hash, expires_at FROM runtime.task_sandboxes WHERE task_id = $1`,
		sandboxTask).Scan(&id, &hash, &expires); err != nil {
		t.Fatal(err)
	}
	if id != nil {
		sandboxID = *id
	}
	return sandboxID, hash, expires.UTC()
}

// attachPending is the Create path: upmo_app claims the pending row with the
// SHA-256 of the proxy secret brain chose (32 bytes of 0x61), and upmo_sandbox
// binds sb-1 by presenting that hash. Before that, upmo_sandbox without the
// secret cannot bind: any other 32-byte hash is 42501 and the row is unchanged.
func attachPending(t *testing.T, owner, app, sandbox *pgxpool.Pool, expires time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, err := app.Exec(ctx, `
		INSERT INTO runtime.task_sandboxes (task_id, expires_at, proxy_secret_hash) VALUES ($1, $2, $3)`,
		sandboxTask, expires, sandboxSecret(0x61)); err != nil {
		t.Fatal(err)
	}
	if id, hash, _ := storedSandbox(t, owner); id != "" || !bytes.Equal(hash, sandboxSecret(0x61)) {
		t.Fatalf("pending row = %q %x, want no sandbox and the 0x61 digest", id, hash)
	}
	for name, wrong := range map[string][]byte{
		"wrong hash":        sandboxSecret(0x62),
		"the secret itself": bytes.Repeat([]byte{0x61}, 32),
		"32 zero bytes":     make([]byte, 32),
		"hash of no secret": func() []byte { s := sha256.Sum256(nil); return s[:] }(),
	} {
		_, err := sandbox.Exec(ctx, `SELECT gateway.attach_sandbox_credential($1, 'sb-1', $2)`, sandboxTask, wrong)
		mustCode(t, "attach with "+name, err, "42501")
		if id, hash, _ := storedSandbox(t, owner); id != "" || !bytes.Equal(hash, sandboxSecret(0x61)) {
			t.Fatalf("after attach with %s: %q %x, want the pending row unchanged", name, id, hash)
		}
	}
	if _, err := sandbox.Exec(ctx, `SELECT gateway.attach_sandbox_credential($1, 'sb-1', $2)`, sandboxTask, sandboxSecret(0x61)); err != nil {
		t.Fatal(err)
	}
	if id, hash, _ := storedSandbox(t, owner); id != "sb-1" || !bytes.Equal(hash, sandboxSecret(0x61)) {
		t.Fatalf("attached row = %q %x, want sb-1 and the 0x61 digest", id, hash)
	}
}

// TestAttachRefusesANullStoredHash: a pending row with no stored proxy
// secret hash can never be bound, whatever hash the caller presents.
func TestAttachRefusesANullStoredHash(t *testing.T) {
	owner := migrated(t)
	ctx := context.Background()
	sandbox := pgtest.Login(t, owner, "upmo_sandbox")
	if _, err := owner.Exec(ctx, `INSERT INTO runtime.task_sandboxes (task_id, expires_at) VALUES ($1, now() + interval '1 hour')`, sandboxTask); err != nil {
		t.Fatal(err)
	}
	for name, hash := range map[string][]byte{"0x61 digest": sandboxSecret(0x61), "32 zero bytes": make([]byte, 32)} {
		_, err := sandbox.Exec(ctx, `SELECT gateway.attach_sandbox_credential($1, 'sb-1', $2)`, sandboxTask, hash)
		mustCode(t, "attach on a null hash with "+name, err, "42501")
		if id, stored, _ := storedSandbox(t, owner); id != "" || stored != nil {
			t.Fatalf("after attach with %s: %q %x, want the pending row unchanged", name, id, stored)
		}
	}
}

// TestSandboxDefinerLiveSecret walks gateway.credential_for_sandbox through
// every check: the stored hash, the bound ref, a live task, and the caller.
// The function compares expires_at with the database clock, which a test
// cannot set, so the pinned instants keep their distance from the pinned
// clock 2026-11-25T00:11:00Z and are moved by the same amount onto now.
func TestSandboxDefinerLiveSecret(t *testing.T) {
	owner := migrated(t)
	ctx := context.Background()
	app := pgtest.AppLogin(t, owner)
	sandbox := pgtest.Login(t, owner, "upmo_sandbox")
	gw := pgtest.Login(t, owner, "upmo_gateway")
	clock := time.Date(2026, 11, 25, 0, 11, 0, 0, time.UTC)
	shift := time.Since(clock).Round(time.Second)
	at := func(minute int) time.Time { return time.Date(2026, 11, 25, 0, minute, 0, 0, time.UTC).Add(shift) }

	_, err := sandbox.Exec(ctx, `SELECT * FROM gateway.credentials`)
	mustCode(t, "upmo_sandbox SELECT gateway.credentials", err, "42501")
	attachPending(t, owner, app, sandbox, at(20))
	// A bound row never moves, not even with the right hash.
	for _, tc := range []struct {
		box  string
		hash []byte
	}{{"sb-2", sandboxSecret(0x61)}, {"sb-1", sandboxSecret(0x61)}, {"sb-2", sandboxSecret(0x62)}} {
		_, err = sandbox.Exec(ctx, `SELECT gateway.attach_sandbox_credential($1, $2, $3)`, sandboxTask, tc.box, tc.hash)
		mustCode(t, "second attach "+tc.box, err, "42501")
		if id, hash, _ := storedSandbox(t, owner); id != "sb-1" || !bytes.Equal(hash, sandboxSecret(0x61)) {
			t.Fatalf("after second attach %s: %q %x", tc.box, id, hash)
		}
	}
	_, err = app.Exec(ctx, `SELECT proxy_secret_hash FROM runtime.task_sandboxes`)
	mustCode(t, "upmo_app SELECT proxy_secret_hash", err, "42501")

	var filled bool
	if err := app.QueryRow(ctx, `SELECT runtime.fill_task_sandbox($1, 'sb-1', $2)`, sandboxTask, at(20)).Scan(&filled); err != nil || !filled {
		t.Fatalf("fill sb-1 = %t %v, want true", filled, err)
	}
	if err := app.QueryRow(ctx, `SELECT runtime.fill_task_sandbox($1, 'sb-2', $2)`, sandboxTask, at(20)).Scan(&filled); err != nil || filled {
		t.Fatalf("fill sb-2 = %t %v, want false", filled, err)
	}
	if id, _, _ := storedSandbox(t, owner); id != "sb-1" {
		t.Fatalf("sandbox_id after fills = %q, want sb-1", id)
	}

	if _, err := owner.Exec(ctx, `INSERT INTO gateway.sandbox_credential_refs (sandbox_id, ref) VALUES ('sb-1', 'GITHUB')`); err != nil {
		t.Fatal(err)
	}
	github := []byte("sealed-github-token")
	if _, err := owner.Exec(ctx, `
		INSERT INTO gateway.credentials (ref, ciphertext, nonce, key_id) VALUES
		  ('GITHUB', $1, $2, 'k1'), ('OTHER', 'sealed-other', $2, 'k1')`, github, bytes.Repeat([]byte{1}, 12)); err != nil {
		t.Fatal(err)
	}
	// The task row is set straight to each status: the transition triggers
	// would only let a task reach it through its events. CHECK constraints
	// still hold in replica mode.
	setTask := func(status string, paused bool) error {
		tx, err := owner.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM tasks.tasks WHERE id = $1`, sandboxTask); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO tasks.tasks (id, owner_user_id, goal, status, paused)
			VALUES ($1, $2, 'ship', $3, $4)`, sandboxTask, sandboxOwner, status, paused); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	open := func(pool *pgxpool.Pool, ref string, hash []byte) ([]byte, error) {
		var ciphertext, nonce []byte
		var keyID string
		err := pool.QueryRow(ctx, `SELECT * FROM gateway.credential_for_sandbox('sb-1', $1, $2)`, ref, hash).Scan(&ciphertext, &nonce, &keyID)
		return ciphertext, err
	}
	if err := setTask("Working", false); err != nil {
		t.Fatal(err)
	}
	got, err := open(sandbox, "GITHUB", sandboxSecret(0x61))
	if err != nil || !bytes.Equal(got, github) {
		t.Fatalf("live Working GITHUB = %q %v, want the GITHUB ciphertext", got, err)
	}
	got, err = open(sandbox, "OTHER", sandboxSecret(0x61))
	mustCode(t, "OTHER ref", err, "42501")
	if got != nil {
		t.Fatalf("OTHER returned ciphertext %q", got)
	}
	_, err = open(gw, "GITHUB", sandboxSecret(0x61))
	mustCode(t, "upmo_gateway credential_for_sandbox", err, "42501")

	// upmo.user_id is not who is calling: setting it changes nothing.
	tx, err := sandbox.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('upmo.user_id', $1, true)`, otherPerson); err != nil {
		t.Fatal(err)
	}
	var viaGUC []byte
	var nonce []byte
	var keyID string
	if err := tx.QueryRow(ctx, `SELECT * FROM gateway.credential_for_sandbox('sb-1', 'GITHUB', $1)`, sandboxSecret(0x61)).Scan(&viaGUC, &nonce, &keyID); err != nil || !bytes.Equal(viaGUC, github) {
		t.Fatalf("with upmo.user_id = B: %q %v", viaGUC, err)
	}
	_ = tx.Rollback(ctx)

	for _, tc := range []struct {
		status string
		paused bool
	}{{"Done", false}, {"Failed", false}, {"Needs approval", false}, {"Working", true}} {
		if err := setTask(tc.status, tc.paused); err != nil {
			t.Fatal(err)
		}
		got, err := open(sandbox, "GITHUB", sandboxSecret(0x61))
		mustCode(t, tc.status+" paused="+map[bool]string{true: "true", false: "false"}[tc.paused], err, "42501")
		if got != nil {
			t.Fatalf("%s returned ciphertext", tc.status)
		}
	}
	if err := setTask("Working", false); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `UPDATE runtime.task_sandboxes SET expires_at = $2 WHERE task_id = $1`, sandboxTask, at(10)); err != nil {
		t.Fatal(err)
	}
	_, err = open(sandbox, "GITHUB", sandboxSecret(0x61))
	mustCode(t, "expired sandbox", err, "42501")
	if _, err := owner.Exec(ctx, `UPDATE runtime.task_sandboxes SET expires_at = $2 WHERE task_id = $1`, sandboxTask, at(20)); err != nil {
		t.Fatal(err)
	}

	// UPM-289 renamed Checking to Verifying; only Verifying is live.
	mustCode(t, "status Checking", setTask("Checking", false), "23514")
	if err := setTask("Verifying", false); err != nil {
		t.Fatal(err)
	}
	if got, err := open(sandbox, "GITHUB", sandboxSecret(0x61)); err != nil || !bytes.Equal(got, github) {
		t.Fatalf("live Verifying GITHUB = %q %v", got, err)
	}
	if err := setTask("Working", false); err != nil {
		t.Fatal(err)
	}
	_, err = open(sandbox, "GITHUB", sandboxSecret(0x62))
	mustCode(t, "wrong secret hash", err, "42501")
	_, err = sandbox.Exec(ctx, `SELECT gateway.sandbox_may_open('sb-1', 'GITHUB', $1)`, sandboxSecret(0x61))
	mustCode(t, "upmo_sandbox sandbox_may_open", err, "42501")
	_, err = app.Exec(ctx, `UPDATE runtime.task_sandboxes SET proxy_secret_hash = $2 WHERE task_id = $1`, sandboxTask, sandboxSecret(0x62))
	mustCode(t, "upmo_app UPDATE proxy_secret_hash", err, "42501")
}

// TestSandboxBindingImmutable: once Create has bound the sandbox and its
// secret hash, upmo_app cannot move either, alone or with the expiry; Fill
// moves only the expiry, and only for the bound sandbox.
func TestSandboxBindingImmutable(t *testing.T) {
	owner := migrated(t)
	ctx := context.Background()
	app := pgtest.AppLogin(t, owner)
	sandbox := pgtest.Login(t, owner, "upmo_sandbox")
	at := func(minute int) time.Time { return time.Date(2026, 11, 25, 0, minute, 0, 0, time.UTC) }

	attachPending(t, owner, app, sandbox, at(20))
	_, err := app.Exec(ctx, `UPDATE runtime.task_sandboxes SET sandbox_id = 'sb-2' WHERE task_id = '11111111-1111-7111-8111-111111111111'`)
	mustCode(t, "upmo_app UPDATE sandbox_id", err, "42501")
	if id, _, _ := storedSandbox(t, owner); id != "sb-1" {
		t.Fatalf("sandbox_id = %q, want sb-1", id)
	}
	_, err = app.Exec(ctx, `UPDATE runtime.task_sandboxes SET proxy_secret_hash = decode(repeat('62', 32), 'hex') WHERE task_id = '11111111-1111-7111-8111-111111111111'`)
	mustCode(t, "upmo_app UPDATE proxy_secret_hash", err, "42501")
	if _, hash, _ := storedSandbox(t, owner); !bytes.Equal(hash, sandboxSecret(0x61)) {
		t.Fatalf("hash = %x, want the 0x61 digest", hash)
	}
	_, err = app.Exec(ctx, `UPDATE runtime.task_sandboxes SET sandbox_id = 'sb-2', expires_at = '2026-11-25T00:30:00Z' WHERE task_id = '11111111-1111-7111-8111-111111111111'`)
	mustCode(t, "upmo_app UPDATE sandbox_id and expires_at", err, "42501")
	if _, _, expires := storedSandbox(t, owner); !expires.Equal(at(20)) {
		t.Fatalf("expires_at = %s, want %s", expires, at(20))
	}

	var filled bool
	if err := app.QueryRow(ctx, `SELECT runtime.fill_task_sandbox('11111111-1111-7111-8111-111111111111', 'sb-1', timestamptz '2026-11-25T00:25:00Z')`).Scan(&filled); err != nil || !filled {
		t.Fatalf("fill sb-1 = %t %v, want true", filled, err)
	}
	id, hash, expires := storedSandbox(t, owner)
	if id != "sb-1" || !expires.Equal(at(25)) || !bytes.Equal(hash, sandboxSecret(0x61)) {
		t.Fatalf("after fill sb-1: %q %s %x", id, expires, hash)
	}
	if err := app.QueryRow(ctx, `SELECT runtime.fill_task_sandbox('11111111-1111-7111-8111-111111111111', 'sb-2', timestamptz '2026-11-25T00:30:00Z')`).Scan(&filled); err != nil || filled {
		t.Fatalf("fill sb-2 = %t %v, want false", filled, err)
	}
	if id, _, expires := storedSandbox(t, owner); id != "sb-1" || !expires.Equal(at(25)) {
		t.Fatalf("after fill sb-2: %q %s, want sb-1 and %s", id, expires, at(25))
	}
	_, err = sandbox.Exec(ctx, `SELECT runtime.fill_task_sandbox('11111111-1111-7111-8111-111111111111', 'sb-1', timestamptz '2026-11-25T00:25:00Z')`)
	mustCode(t, "upmo_sandbox fill_task_sandbox", err, "42501")
}

// The worker serves provisioning with its own key check, on the owner pool,
// next to the health routes compose waits on. Brain answers 404 there now.
func TestWorkerServesProvision(t *testing.T) {
	owner := migrated(t)
	provKey := svcsig.KeyPrefix + base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{'p'}, 32))
	keys, err := provision.ParseKeys([]string{provKey})
	if err != nil {
		t.Fatal(err)
	}
	h := workerHandler(owner, config.Config{PublicURL: "http://localhost:5173", AvatarsDir: t.TempDir()}, gateway.Keyring{}, keys, slog.New(slog.NewTextHandler(io.Discard, nil)))

	ready := httptest.NewRecorder()
	h.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if ready.Code != http.StatusOK {
		t.Fatalf("GET /readyz = %d", ready.Code)
	}
	unauth := httptest.NewRecorder()
	h.ServeHTTP(unauth, httptest.NewRequest(http.MethodGet, "/api/v1/provision/status", nil))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned /api/v1/provision/status = %d, want 401", unauth.Code)
	}
	master, err := svcsig.ParseKey(provKey)
	if err != nil {
		t.Fatal(err)
	}
	derived, err := svcsig.Derive(master, svcsig.AudienceProvision)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := svcsig.SignRequest(derived, svcsig.AudienceProvision, http.MethodGet, "/api/v1/provision/status", "", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/provision/status", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	status := httptest.NewRecorder()
	h.ServeHTTP(status, req)
	var payload struct {
		Ready          bool             `json:"ready"`
		SchemaVersions map[string]int64 `json:"schema_versions"`
	}
	if err := json.Unmarshal(status.Body.Bytes(), &payload); err != nil || status.Code != http.StatusOK || !payload.Ready || payload.SchemaVersions["identity"] != 1 {
		t.Fatalf("signed /api/v1/provision/status = %d %s %v", status.Code, status.Body.String(), err)
	}
	other := httptest.NewRecorder()
	h.ServeHTTP(other, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))
	if other.Code != http.StatusNotFound {
		t.Fatalf("GET /api/tasks on the worker = %d, want 404", other.Code)
	}
}

// TestWorkerRiverOwnSchema runs brain's River client on the app pool and the
// worker's on the owner pool, in the same database. River elects one leader
// per schema and only the leader inserts periodic jobs, so brain's client is
// made leader of its schema first: a worker client sharing that schema would
// never insert its periodic jobs. On its own schema, the worker's provision
// nonce sweep (run on start) completes.
func TestWorkerRiverOwnSchema(t *testing.T) {
	owner := migrated(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	brainJobs, err := jobs.Open(pgtest.AppLogin(t, owner), jobs.WithWorkers(river.NewWorkers()))
	if err != nil {
		t.Fatal(err)
	}
	if err := brainJobs.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = brainJobs.Stop(context.Background()) })
	waitFor(t, "brain River leader", func() bool {
		var n int
		err := owner.QueryRow(ctx, `SELECT count(*) FROM platform.river_leader WHERE expires_at > now()`).Scan(&n)
		return err == nil && n == 1
	})

	work, err := openWorkerJobs(owner, config.Config{PublicURL: "http://localhost:5173", AuditRetentionDays: 365}, nil, gateway.Keyring{}, nil, log)
	if err != nil {
		t.Fatal(err)
	}
	if err := work.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = work.Stop(context.Background()) })
	waitFor(t, "worker provision nonce sweep", func() bool {
		var n int
		err := owner.QueryRow(ctx, `
			SELECT (SELECT count(*) FROM worker.river_job WHERE kind = 'provision_nonce_sweep' AND state = 'completed')
			     + (SELECT count(*) FROM platform.river_job WHERE kind = 'provision_nonce_sweep' AND state = 'completed')`).Scan(&n)
		return err == nil && n > 0
	})
	// Brain's client never fetches a worker kind it cannot run.
	var stray int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM platform.river_job WHERE kind = 'provision_nonce_sweep'`).Scan(&stray); err != nil || stray != 0 {
		t.Fatalf("worker jobs in brain's schema = %d %v", stray, err)
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen in 30s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Starting the worker with sealed rows whose master key id is not loaded
// warns once per affected store without stopping start-up, and the metrics
// registry it builds exposes the count per store, zero included. Brain no
// longer counts sealed rows: it has no pool that may read them.
func TestWorkerWarnsAndExportsSealedRowsOfAnUnloadedKey(t *testing.T) {
	owner := migrated(t)
	ctx := context.Background()
	t.Setenv(gateway.EnvMasterKey, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x30}, 32)))
	t.Setenv(gateway.EnvMasterKeyID, "k0")
	old, err := gateway.KeyringFromEnv(os.LookupEnv, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := old.Seal("own_gone", "sk-gone-secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Exec(ctx, `
		INSERT INTO gateway.credentials (ref, ciphertext, nonce, key_id) VALUES ($1, $2, $3, $4)`,
		"own_gone", sealed.Ciphertext, sealed.Nonce, sealed.KeyID); err != nil {
		t.Fatal(err)
	}
	t.Setenv(gateway.EnvMasterKey, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x31}, 32)))
	t.Setenv(gateway.EnvMasterKeyID, "k1")
	ring, err := gateway.KeyringFromEnv(os.LookupEnv, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	reg := checkSealedKeys(ctx, owner, ring, true, slog.New(slog.NewTextHandler(logs, nil)))
	out := logs.String()
	if !strings.Contains(out, `msg="sealed rows use a master key that is not loaded"`) ||
		!strings.Contains(out, "store=gateway.credentials") ||
		!strings.Contains(out, "rows=1") || !strings.Contains(out, "key_ids=[k0]") {
		t.Fatalf("start-up warning = %s", out)
	}
	if strings.Contains(out, "own_gone") || strings.Contains(out, "sk-gone-secret") {
		t.Fatalf("start-up warning carries the row: %s", out)
	}
	var expo bytes.Buffer
	if err := reg.WriteTo(ctx, &expo); err != nil {
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
	if checkSealedKeys(ctx, owner, ring, false, slog.New(slog.NewTextHandler(io.Discard, nil))) != nil {
		t.Fatal("a registry without metrics on")
	}
}
