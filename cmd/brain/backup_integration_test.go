//go:build integration

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/agents"
	"github.com/upmo-ai/upmo/internal/artifacts"
	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/knowledge"
	"github.com/upmo-ai/upmo/internal/platform/audit"
	"github.com/upmo-ai/upmo/internal/platform/backup"
	"github.com/upmo-ai/upmo/internal/platform/brainops"
	"github.com/upmo-ai/upmo/internal/platform/migrate"
	"github.com/upmo-ai/upmo/internal/testutil/pgtest"
)

func poolURL(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	u, err := url.Parse(pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + pool.Config().ConnConfig.Database
	return u.String()
}

func TestBackupRestoreFullRoundTrip(t *testing.T) {
	ctx := context.Background()
	srcOwner := pgtest.New(t)
	if err := brainops.MigrateAll(ctx, srcOwner); err != nil {
		t.Fatalf("brainops.MigrateAll(src): %v", err)
	}

	const (
		key1ID           = "k1"
		key2ID           = "k2"
		apiKeyPlaintext  = "sk-upmo-secret-provider-key-987654321"                        // #nosec G101 -- test fixture secret
		passwordHashText = "$argon2id$v=19$m=65536,t=1,p=4$c2FsdHlzYWx0$aGFzaGhhc2hoYXNo" // #nosec G101 -- test fixture hash
		credRef          = "provider_cred_1"
	)
	key1B64 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x31}, 32))
	key2B64 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))

	t.Setenv("UPMO_DATABASE_URL", poolURL(t, srcOwner))
	t.Setenv(gateway.EnvMasterKey, key1B64)
	t.Setenv(gateway.EnvMasterKeyID, key1ID)

	ring1, err := gateway.KeyringFromEnv(os.LookupEnv, os.Environ())
	if err != nil {
		t.Fatalf("KeyringFromEnv(k1): %v", err)
	}

	srcArtDir := t.TempDir()
	srcKnowDir := t.TempDir()
	t.Setenv("UPMO_ARTIFACTS_DIR", srcArtDir)
	t.Setenv("UPMO_KNOWLEDGE_DIR", srcKnowDir)

	// Seed identity user + sealed password hash.
	var ownerRoleID uuid.UUID
	if err := srcOwner.QueryRow(ctx, `SELECT id FROM identity.roles WHERE key = 'owner'`).Scan(&ownerRoleID); err != nil {
		t.Fatalf("lookup owner role: %v", err)
	}
	userID := uuid.New()
	sealedPW, err := brainops.NewPasswordSealer(ring1).Seal(userID.String(), passwordHashText)
	if err != nil {
		t.Fatalf("seal password hash: %v", err)
	}
	if _, err := srcOwner.Exec(ctx, `
		INSERT INTO identity.users (id, email, name, role_id, invited_at, joined_at, password_hash)
		VALUES ($1, 'founder@example.com', 'Founder', $2, now(), now(), $3)`,
		userID, ownerRoleID, sealedPW,
	); err != nil {
		t.Fatalf("seed identity.users: %v", err)
	}

	// Seed agent + lesson.
	agentID := uuid.New()
	if _, err := srcOwner.Exec(ctx, `
		INSERT INTO agents.agents (id, owner_user_id, visibility, name, slug, instructions)
		VALUES ($1, $2, 'everyone', 'Ops Agent', 'ops-agent', 'Keep backups verified.')`,
		agentID, userID,
	); err != nil {
		t.Fatalf("seed agents.agents: %v", err)
	}

	// Seed gateway sealed credential + provider account + route + route link.
	sealedCred, err := ring1.Seal(credRef, apiKeyPlaintext)
	if err != nil {
		t.Fatalf("seal provider cred: %v", err)
	}
	if _, err := srcOwner.Exec(ctx, `
		INSERT INTO gateway.credentials (ref, ciphertext, nonce, key_id)
		VALUES ($1, $2, $3, $4)`,
		credRef, sealedCred.Ciphertext, sealedCred.Nonce, sealedCred.KeyID,
	); err != nil {
		t.Fatalf("seed gateway.credentials: %v", err)
	}
	accountID := uuid.New()
	if _, err := srcOwner.Exec(ctx, `
		INSERT INTO gateway.provider_accounts (id, name, adapter, credential_ref, plan_kind, house, owner_user_id, provider, status, visibility)
		VALUES ($1, 'Primary OpenAI', 'openai', $2, 'metered', false, $3, 'openai', 'Connected', 'private')`,
		accountID, credRef, userID,
	); err != nil {
		t.Fatalf("seed gateway.provider_accounts: %v", err)
	}
	routeID := uuid.New()
	if _, err := srcOwner.Exec(ctx, `
		INSERT INTO gateway.routes (id, name, capability, decision, owner_user_id, visibility)
		VALUES ($1, 'chat', 'chat', '', $2, 'private')`,
		routeID, userID,
	); err != nil {
		t.Fatalf("seed gateway.routes: %v", err)
	}
	if _, err := srcOwner.Exec(ctx, `
		INSERT INTO gateway.route_links (id, route_id, position, provider_account_id, model, weight, retry_budget)
		VALUES ($1, $2, 1, $3, 'gpt-4.1', 1, 1)`,
		uuid.New(), routeID, accountID,
	); err != nil {
		t.Fatalf("seed gateway.route_links: %v", err)
	}

	// Seed chat + lines.
	chatID := uuid.New()
	if _, err := srcOwner.Exec(ctx, `
		INSERT INTO chat.chats (id, owner_user_id, visibility)
		VALUES ($1, $2, 'private')`,
		chatID, userID,
	); err != nil {
		t.Fatalf("seed chat.chats: %v", err)
	}
	if _, err := srcOwner.Exec(ctx, `
		INSERT INTO chat.lines (chat_id, seq, body)
		VALUES ($1, 1, 'Hello from backup test')`,
		chatID,
	); err != nil {
		t.Fatalf("seed chat.lines: %v", err)
	}

	// Seed task + task_event (within one tx for deferred tasks_born constraint trigger).
	taskID := uuid.New()
	tx, err := srcOwner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO tasks.tasks (id, owner_user_id, visibility, goal, status)
		VALUES ($1, $2, 'private', 'Verify backup round-trip', 'Preparing')`,
		taskID, userID,
	); err != nil {
		t.Fatalf("seed tasks.tasks: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO tasks.task_events (task_id, seq, from_status, to_status, input)
		VALUES ($1, 1, '', 'Preparing', 'open')`,
		taskID,
	); err != nil {
		t.Fatalf("seed tasks.task_events: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seeded task: %v", err)
	}

	// Seed artifact row + file on disk.
	artifactBody := []byte("binary\x00artifact-payload\nline2")
	artifactRel := taskID.String() + "/output.bin"
	artifactDiskPath := filepath.Join(srcArtDir, filepath.FromSlash(artifactRel))
	if err := os.MkdirAll(filepath.Dir(artifactDiskPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifactDiskPath, artifactBody, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := srcOwner.Exec(ctx, `
		INSERT INTO artifacts.artifacts (id, owner_user_id, visibility, task_id, name, store, object_key)
		VALUES ($1, $2, 'private', $3, 'output.bin', 'volume', $4)`,
		uuid.New(), userID, taskID, artifactRel,
	); err != nil {
		t.Fatalf("seed artifacts.artifacts: %v", err)
	}

	// Seed knowledge folder + source + chunk (with GENERATED ALWAYS body_tsv) + blob file on disk.
	folderID := uuid.New()
	sourceID := uuid.New()
	blobKey := uuid.New().String()
	knowBody := []byte("original uploaded knowledge document bytes")
	knowVol := knowledge.Volume{Root: srcKnowDir}
	if err := knowVol.Put(ctx, blobKey, knowBody); err != nil {
		t.Fatalf("seed knowledge volume: %v", err)
	}
	srcHash := sha256.Sum256([]byte("runbook text"))
	if _, err := srcOwner.Exec(ctx, `
		INSERT INTO knowledge.folders (id, owner_user_id, visibility, name)
		VALUES ($1, $2, 'private', 'Ops Runbooks')`,
		folderID, userID,
	); err != nil {
		t.Fatalf("seed knowledge.folders: %v", err)
	}
	if _, err := srcOwner.Exec(ctx, `
		INSERT INTO knowledge.sources (id, folder_id, title, body, sha256, revision, updated_at, kind, blob_key)
		VALUES ($1, $2, 'Backup Runbook', 'runbook text', $3, 1, $4, 'file', $5)`,
		sourceID, folderID, srcHash[:], time.Now().UTC(), blobKey,
	); err != nil {
		t.Fatalf("seed knowledge.sources: %v", err)
	}
	if _, err := srcOwner.Exec(ctx, `
		INSERT INTO knowledge.chunks (source_id, revision, seq, body)
		VALUES ($1, 1, 0, 'PostgreSQL backup and restore runbook')`,
		sourceID,
	); err != nil {
		t.Fatalf("seed knowledge.chunks: %v", err)
	}

	// Record and seal audit events so audit.head.position > 0.
	if err := audit.Record(ctx, srcOwner, audit.Event{
		Action:  audit.SystemMigrated,
		Outcome: audit.Success,
		Changes: audit.Changes{
			Fields: []string{"versions"},
			Values: map[string]audit.Change{"versions": {To: map[string]int64{"audit": 2}}},
		},
	}); err != nil {
		t.Fatalf("record audit event: %v", err)
	}
	sealedCount, err := audit.SealOnce(ctx, srcOwner, io.Discard)
	if err != nil || sealedCount == 0 {
		t.Fatalf("SealOnce: count=%d err=%v", sealedCount, err)
	}

	// 1. Run backup command.
	archivePath := filepath.Join(t.TempDir(), "backup.tar.gz")
	var backupOut bytes.Buffer
	if err := backupCommand([]string{"--out", archivePath}, &backupOut); err != nil {
		t.Fatalf("backupCommand: %v", err)
	}
	if !strings.Contains(backupOut.String(), "backup complete:") {
		t.Fatalf("unexpected backup stdout: %q", backupOut.String())
	}

	// Inspect manifest and verify no secrets leaked into archive bytes.
	manifest, err := backup.Inspect(archivePath)
	if err != nil {
		t.Fatalf("backup.Inspect: %v", err)
	}
	if manifest.FormatVersion != backup.FormatVersion {
		t.Fatalf("format_version = %d, want %d", manifest.FormatVersion, backup.FormatVersion)
	}
	if manifest.MasterKeyID != key1ID {
		t.Fatalf("master_key_id = %q, want %q", manifest.MasterKeyID, key1ID)
	}
	if !manifest.Encrypted || manifest.SealedKey == nil {
		t.Fatal("expected encrypted backup with sealed_key")
	}
	wantSchemas, err := migrate.SchemaVersions(ctx, srcOwner)
	if err != nil {
		t.Fatal(err)
	}
	for s, v := range wantSchemas {
		if manifest.SchemaVersions[s] != v {
			t.Fatalf("manifest schema_versions[%s] = %d, want %d", s, manifest.SchemaVersions[s], v)
		}
	}
	if len(manifest.Entries) == 0 || len(manifest.Entries[0].SHA256) != 64 {
		t.Fatalf("expected SHA-256 entry checksums, got %+v", manifest.Entries)
	}
	rawArchive, err := os.ReadFile(archivePath) //nolint:gosec // test temp path
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{key1B64, apiKeyPlaintext, passwordHashText} {
		if bytes.Contains(rawArchive, []byte(secret)) {
			t.Fatalf("backup archive contains secret plaintext %q", secret)
		}
	}

	// 2. Negative test: corrupted archive fails BEFORE wiping live DB/files.
	liveOwner := pgtest.New(t)
	if err := brainops.MigrateAll(ctx, liveOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := liveOwner.Exec(ctx, `
		INSERT INTO identity.users (email, name, role_id, invited_at, joined_at)
		SELECT 'live@example.com', 'Live User', id, now(), now() FROM identity.roles WHERE key = 'owner'`); err != nil {
		t.Fatal(err)
	}
	liveArtDir := t.TempDir()
	liveKnowDir := t.TempDir()
	sentinelArt := filepath.Join(liveArtDir, "live-sentinel.txt")
	if err := os.WriteFile(sentinelArt, []byte("must-survive"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UPMO_DATABASE_URL", poolURL(t, liveOwner))
	t.Setenv("UPMO_ARTIFACTS_DIR", liveArtDir)
	t.Setenv("UPMO_KNOWLEDGE_DIR", liveKnowDir)

	var sink bytes.Buffer
	for _, offset := range []int{len(rawArchive) / 2, len(rawArchive) - 4} {
		corruptedPath := filepath.Join(t.TempDir(), "corrupted.tar.gz")
		corruptedBytes := append([]byte(nil), rawArchive...)
		corruptedBytes[offset] ^= 0xff
		if err := os.WriteFile(corruptedPath, corruptedBytes, 0o600); err != nil { //nolint:gosec // test temp path
			t.Fatal(err)
		}
		sink.Reset()
		if err := restoreCommand([]string{"--in", corruptedPath}, &sink); err == nil {
			t.Fatalf("expected restore of corrupted archive at offset %d to fail", offset)
		}
	}
	var liveCount int
	if err := liveOwner.QueryRow(ctx, `SELECT count(*) FROM identity.users WHERE email = 'live@example.com'`).Scan(&liveCount); err != nil || liveCount != 1 {
		t.Fatalf("corrupted restore modified live DB: count=%d err=%v", liveCount, err)
	}
	if got, err := os.ReadFile(sentinelArt); err != nil || string(got) != "must-survive" { //nolint:gosec // test temp path
		t.Fatalf("corrupted restore modified live volume: got=%q err=%v", got, err)
	}

	// 3. Negative test: wrong UPMO_MASTER_KEY_ID fails cleanly BEFORE wiping DB/files.
	t.Setenv(gateway.EnvMasterKey, key2B64)
	t.Setenv(gateway.EnvMasterKeyID, key2ID)
	err = restoreCommand([]string{"--in", archivePath}, &sink)
	if err == nil || !strings.Contains(err.Error(), key1ID) {
		t.Fatalf("expected error naming missing key %q, got %v", key1ID, err)
	}
	if err := liveOwner.QueryRow(ctx, `SELECT count(*) FROM identity.users WHERE email = 'live@example.com'`).Scan(&liveCount); err != nil || liveCount != 1 {
		t.Fatalf("wrong-key restore modified live DB: count=%d err=%v", liveCount, err)
	}
	if got, err := os.ReadFile(sentinelArt); err != nil || string(got) != "must-survive" { //nolint:gosec // test temp path
		t.Fatalf("wrong-key restore modified live volume: got=%q err=%v", got, err)
	}

	// 4. Positive test: fresh empty DB + wiped volume dirs, restore with key1.
	freshOwner := pgtest.New(t)
	dstArtDir := t.TempDir()
	dstKnowDir := t.TempDir()
	t.Setenv("UPMO_DATABASE_URL", poolURL(t, freshOwner))
	t.Setenv("UPMO_ARTIFACTS_DIR", dstArtDir)
	t.Setenv("UPMO_KNOWLEDGE_DIR", dstKnowDir)
	t.Setenv(gateway.EnvMasterKey, key1B64)
	t.Setenv(gateway.EnvMasterKeyID, key1ID)

	var restoreOut bytes.Buffer
	if err := restoreCommand([]string{"--in", archivePath}, &restoreOut); err != nil {
		t.Fatalf("restoreCommand(fresh): %v", err)
	}
	if !strings.Contains(restoreOut.String(), "audit chain valid") {
		t.Fatalf("restore stdout did not report valid audit chain: %q", restoreOut.String())
	}

	// Verify restored audit chain, rows, volume files, and unsealing.
	verifyRes, err := audit.Verify(ctx, freshOwner, 0, 0)
	if err != nil || !verifyRes.OK {
		t.Fatalf("audit.Verify after restore: res=%+v err=%v", verifyRes, err)
	}
	var gotPassword string
	var gotRoleID uuid.UUID
	if err := freshOwner.QueryRow(ctx, `SELECT role_id, password_hash FROM identity.users WHERE id = $1`, userID).Scan(&gotRoleID, &gotPassword); err != nil {
		t.Fatalf("query restored user: %v", err)
	}
	if gotRoleID != ownerRoleID {
		t.Fatalf("restored role_id = %s, want %s", gotRoleID, ownerRoleID)
	}
	unsealedPW, err := brainops.NewPasswordSealer(ring1).Open(userID.String(), gotPassword)
	if err != nil || unsealedPW != passwordHashText {
		t.Fatalf("unseal restored password hash: got=%q err=%v", unsealedPW, err)
	}

	var gotAgentSlug string
	if err := freshOwner.QueryRow(ctx, `SELECT slug FROM agents.agents WHERE id = $1`, agentID).Scan(&gotAgentSlug); err != nil || gotAgentSlug != "ops-agent" {
		t.Fatalf("query restored agent: slug=%q err=%v", gotAgentSlug, err)
	}
	var gotChatBody string
	if err := freshOwner.QueryRow(ctx, `SELECT body FROM chat.lines WHERE chat_id = $1 AND seq = 1`, chatID).Scan(&gotChatBody); err != nil || gotChatBody != "Hello from backup test" {
		t.Fatalf("query restored chat line: body=%q err=%v", gotChatBody, err)
	}
	var gotTaskGoal string
	if err := freshOwner.QueryRow(ctx, `SELECT goal FROM tasks.tasks WHERE id = $1`, taskID).Scan(&gotTaskGoal); err != nil || gotTaskGoal != "Verify backup round-trip" {
		t.Fatalf("query restored task: goal=%q err=%v", gotTaskGoal, err)
	}
	var gotRouteName string
	if err := freshOwner.QueryRow(ctx, `SELECT name FROM gateway.routes WHERE id = $1`, routeID).Scan(&gotRouteName); err != nil || gotRouteName != "chat" {
		t.Fatalf("query restored route: name=%q err=%v", gotRouteName, err)
	}
	var tsvMatch bool
	if err := freshOwner.QueryRow(ctx, `SELECT body_tsv @@ to_tsquery('english', 'runbook') FROM knowledge.chunks WHERE source_id = $1`, sourceID).Scan(&tsvMatch); err != nil || !tsvMatch {
		t.Fatalf("generated tsvector check failed: match=%v err=%v", tsvMatch, err)
	}

	unsealedAPIKey, err := (gateway.DBSecrets{Pool: freshOwner, Keys: ring1}).Get(credRef)
	if err != nil || unsealedAPIKey != apiKeyPlaintext {
		t.Fatalf("DBSecrets.Get after restore: got=%q err=%v", unsealedAPIKey, err)
	}

	gotArtBytes, err := os.ReadFile(filepath.Join(dstArtDir, filepath.FromSlash(artifactRel)))
	if err != nil || !bytes.Equal(gotArtBytes, artifactBody) {
		t.Fatalf("restored artifact bytes mismatch: len=%d err=%v", len(gotArtBytes), err)
	}
	gotKnowRC, err := (knowledge.Volume{Root: dstKnowDir}).Open(ctx, blobKey)
	if err != nil {
		t.Fatalf("open restored knowledge blob: %v", err)
	}
	gotKnowBytes, err := io.ReadAll(gotKnowRC)
	_ = gotKnowRC.Close()
	if err != nil || !bytes.Equal(gotKnowBytes, knowBody) {
		t.Fatalf("restored knowledge bytes mismatch: got=%q err=%v", gotKnowBytes, err)
	}

	// Verify artifacts volume reader also opens restored artifact.
	artRC, err := (artifacts.Volume{Root: dstArtDir}).Open(ctx, artifacts.StoreVolume, artifactRel)
	if err != nil {
		t.Fatalf("artifacts.Volume.Open: %v", err)
	}
	artRead, err := io.ReadAll(artRC)
	_ = artRC.Close()
	if err != nil || !bytes.Equal(artRead, artifactBody) {
		t.Fatalf("artifacts.Volume content mismatch: err=%v", err)
	}

	// 5. Positive test: key rotation (k2 current, UPMO_MASTER_KEY_PREVIOUS_k1=k1) restores k1 backup.
	rotatedOwner := pgtest.New(t)
	t.Setenv("UPMO_DATABASE_URL", poolURL(t, rotatedOwner))
	t.Setenv("UPMO_ARTIFACTS_DIR", t.TempDir())
	t.Setenv("UPMO_KNOWLEDGE_DIR", t.TempDir())
	t.Setenv(gateway.EnvMasterKey, key2B64)
	t.Setenv(gateway.EnvMasterKeyID, key2ID)
	t.Setenv(gateway.EnvMasterKeyPrevious+key1ID, key1B64)

	var rotOut bytes.Buffer
	if err := restoreCommand([]string{"--in", archivePath}, &rotOut); err != nil {
		t.Fatalf("restoreCommand(rotated): %v", err)
	}
	rotatedRing, err := gateway.KeyringFromEnv(os.LookupEnv, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	unsealedRotated, err := (gateway.DBSecrets{Pool: rotatedOwner, Keys: rotatedRing}).Get(credRef)
	if err != nil || unsealedRotated != apiKeyPlaintext {
		t.Fatalf("DBSecrets.Get after rotated restore: got=%q err=%v", unsealedRotated, err)
	}
}

func TestBackupRestoreForwardMigratesOlderBackup(t *testing.T) {
	ctx := context.Background()
	oldOwner := pgtest.New(t)

	// Prepare an older migration head (tasks at v3, chat at v6).
	if err := brainops.PrepareSchemasAt(ctx, oldOwner, map[string]int64{
		"audit":     2,
		"identity":  3,
		"memory":    1,
		"knowledge": 5,
		"tasks":     3,
		"chat":      6,
		"decisions": 1,
		"artifacts": 1,
		"runtime":   2,
		"gateway":   7,
		"notices":   2,
		"platform":  2,
	}); err != nil {
		t.Fatalf("prepareSchemasAt(old): %v", err)
	}

	var roleID uuid.UUID
	if err := oldOwner.QueryRow(ctx, `SELECT id FROM identity.roles WHERE key = 'owner'`).Scan(&roleID); err != nil {
		t.Fatal(err)
	}
	userID := uuid.New()
	if _, err := oldOwner.Exec(ctx, `
		INSERT INTO identity.users (id, email, name, role_id, invited_at, joined_at)
		VALUES ($1, 'old@example.com', 'Old User', $2, now(), now())`,
		userID, roleID,
	); err != nil {
		t.Fatal(err)
	}
	taskID := uuid.New()
	tx, err := oldOwner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO tasks.tasks (id, owner_user_id, visibility, goal, status)
		VALUES ($1, $2, 'private', 'Older schema task', 'Preparing')`,
		taskID, userID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO tasks.task_events (task_id, seq, from_status, to_status, input)
		VALUES ($1, 1, '', 'Preparing', 'open')`,
		taskID,
	); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	key1B64 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x55}, 32))
	t.Setenv("UPMO_DATABASE_URL", poolURL(t, oldOwner))
	t.Setenv(gateway.EnvMasterKey, key1B64)
	t.Setenv(gateway.EnvMasterKeyID, "k1")
	t.Setenv("UPMO_ARTIFACTS_DIR", "")
	t.Setenv("UPMO_KNOWLEDGE_DIR", "")

	archivePath := filepath.Join(t.TempDir(), "old-backup.tar.gz")
	var out bytes.Buffer
	if err := backupCommand([]string{"--out", archivePath}, &out); err != nil {
		t.Fatalf("backupCommand(old): %v", err)
	}
	m, err := backup.Inspect(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if m.SchemaVersions["tasks"] != 3 || m.SchemaVersions["chat"] != 6 {
		t.Fatalf("unexpected old schema_versions: %v", m.SchemaVersions)
	}

	// Restore onto a fresh DB -> must forward-migrate tasks->4, chat->7, and create agents.
	newOwner := pgtest.New(t)
	t.Setenv("UPMO_DATABASE_URL", poolURL(t, newOwner))
	var restoreOut bytes.Buffer
	if err := restoreCommand([]string{"--in", archivePath}, &restoreOut); err != nil {
		t.Fatalf("restoreCommand(old->new): %v", err)
	}

	afterVersions, err := migrate.SchemaVersions(ctx, newOwner)
	if err != nil {
		t.Fatal(err)
	}
	if afterVersions["tasks"] < 4 || afterVersions["chat"] < 7 || afterVersions[agents.Schema] < 1 {
		t.Fatalf("expected forward migrations to head, got %v", afterVersions)
	}
	var gotGoal string
	if err := newOwner.QueryRow(ctx, `SELECT goal FROM tasks.tasks WHERE id = $1`, taskID).Scan(&gotGoal); err != nil || gotGoal != "Older schema task" {
		t.Fatalf("restored task missing after forward migration: goal=%q err=%v", gotGoal, err)
	}
	// Verify JSON encoding of manifest works cleanly.
	if _, err := json.Marshal(m); err != nil {
		t.Fatal(err)
	}
}

func TestBackupRestoreWipesExistingTargetData(t *testing.T) {
	ctx := context.Background()
	srcOwner := pgtest.New(t)
	if err := brainops.MigrateAll(ctx, srcOwner); err != nil {
		t.Fatal(err)
	}
	var roleID uuid.UUID
	if err := srcOwner.QueryRow(ctx, `SELECT id FROM identity.roles WHERE key = 'owner'`).Scan(&roleID); err != nil {
		t.Fatal(err)
	}
	srcUserID := uuid.New()
	if _, err := srcOwner.Exec(ctx, `
		INSERT INTO identity.users (id, email, name, role_id, invited_at, joined_at)
		VALUES ($1, 'src@example.com', 'Src', $2, now(), now())`,
		srcUserID, roleID,
	); err != nil {
		t.Fatal(err)
	}

	key1B64 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x66}, 32))
	t.Setenv(gateway.EnvMasterKey, key1B64)
	t.Setenv(gateway.EnvMasterKeyID, "k1")
	t.Setenv("UPMO_DATABASE_URL", poolURL(t, srcOwner))
	srcArt := t.TempDir()
	t.Setenv("UPMO_ARTIFACTS_DIR", srcArt)
	t.Setenv("UPMO_KNOWLEDGE_DIR", t.TempDir())
	if err := os.WriteFile(filepath.Join(srcArt, "kept.txt"), []byte("kept"), 0o600); err != nil {
		t.Fatal(err)
	}

	archivePath := filepath.Join(t.TempDir(), "wipe-test.tar.gz")
	var out bytes.Buffer
	if err := backupCommand([]string{"--out", archivePath}, &out); err != nil {
		t.Fatal(err)
	}

	// Target DB already has schemas and different data + stale artifact file.
	dstOwner := pgtest.New(t)
	if err := brainops.MigrateAll(ctx, dstOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := dstOwner.Exec(ctx, `
		INSERT INTO identity.users (email, name, role_id, invited_at, joined_at)
		SELECT 'stale@example.com', 'Stale', id, now(), now() FROM identity.roles WHERE key = 'owner'`); err != nil {
		t.Fatal(err)
	}
	dstArt := t.TempDir()
	if err := os.WriteFile(filepath.Join(dstArt, "stale.txt"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UPMO_DATABASE_URL", poolURL(t, dstOwner))
	t.Setenv("UPMO_ARTIFACTS_DIR", dstArt)
	t.Setenv("UPMO_KNOWLEDGE_DIR", t.TempDir())

	var restoreOut bytes.Buffer
	if err := restoreCommand([]string{"--in", archivePath}, &restoreOut); err != nil {
		t.Fatalf("restoreCommand(wipe): %v", err)
	}
	var staleCount, srcCount int
	if err := dstOwner.QueryRow(ctx, `SELECT count(*) FROM identity.users WHERE email = 'stale@example.com'`).Scan(&staleCount); err != nil || staleCount != 0 {
		t.Fatalf("stale user survived restore: count=%d err=%v", staleCount, err)
	}
	if err := dstOwner.QueryRow(ctx, `SELECT count(*) FROM identity.users WHERE id = $1`, srcUserID).Scan(&srcCount); err != nil || srcCount != 1 {
		t.Fatalf("source user missing after restore: count=%d err=%v", srcCount, err)
	}
	if _, err := os.Stat(filepath.Join(dstArt, "stale.txt")); err == nil {
		t.Fatal("stale.txt survived volume restore")
	}
	if got, err := os.ReadFile(filepath.Join(dstArt, "kept.txt")); err != nil || string(got) != "kept" {
		t.Fatalf("kept.txt = %q, err=%v", got, err)
	}
}

func TestBackupRestoreModesPassphraseNoEncryptAndRequireEncrypted(t *testing.T) {
	ctx := context.Background()
	srcOwner := pgtest.New(t)
	if err := brainops.MigrateAll(ctx, srcOwner); err != nil {
		t.Fatal(err)
	}

	key1B64 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x12}, 32))
	key2B64 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x23}, 32))
	t.Setenv(gateway.EnvMasterKey, key1B64)
	t.Setenv(gateway.EnvMasterKeyID, "k1")
	ring1, err := gateway.KeyringFromEnv(os.LookupEnv, os.Environ())
	if err != nil {
		t.Fatal(err)
	}

	const credRef = "pass_cred_1"
	const credSecret = "sk-passphrase-test-secret" // #nosec G101 -- test fixture secret
	sealedCred, err := ring1.Seal(credRef, credSecret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srcOwner.Exec(ctx, `
		INSERT INTO gateway.credentials (ref, ciphertext, nonce, key_id)
		VALUES ($1, $2, $3, $4)`,
		credRef, sealedCred.Ciphertext, sealedCred.Nonce, sealedCred.KeyID,
	); err != nil {
		t.Fatal(err)
	}

	srcArt := t.TempDir()
	srcKnow := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcArt, "a.txt"), []byte("art"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcKnow, "k.txt"), []byte("know"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("UPMO_DATABASE_URL", poolURL(t, srcOwner))
	t.Setenv("UPMO_ARTIFACTS_DIR", srcArt)
	t.Setenv("UPMO_KNOWLEDGE_DIR", srcKnow)

	// 1. --no-encrypt backup + --require-encrypted rejection + normal restore.
	plainArchive := filepath.Join(t.TempDir(), "plain.tar.gz")
	var out bytes.Buffer
	if err := backupCommand([]string{"--out", plainArchive, "--no-encrypt"}, &out); err != nil {
		t.Fatalf("backupCommand(--no-encrypt): %v", err)
	}
	mPlain, err := backup.Inspect(plainArchive)
	if err != nil {
		t.Fatal(err)
	}
	if mPlain.Encrypted {
		t.Fatal("expected encrypted=false for --no-encrypt backup")
	}

	dstPlain := pgtest.New(t)
	dstPlainArt := t.TempDir()
	dstPlainKnow := t.TempDir()
	t.Setenv("UPMO_DATABASE_URL", poolURL(t, dstPlain))
	t.Setenv("UPMO_ARTIFACTS_DIR", dstPlainArt)
	t.Setenv("UPMO_KNOWLEDGE_DIR", dstPlainKnow)

	var restoreOut bytes.Buffer
	if err := restoreCommand([]string{"--in", plainArchive, "--require-encrypted"}, &restoreOut); err == nil {
		t.Fatal("expected --require-encrypted to reject unencrypted archive")
	}
	if err := restoreCommand([]string{"--in", plainArchive}, &restoreOut); err != nil {
		t.Fatalf("restoreCommand(--no-encrypt): %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dstPlainArt, "a.txt")); err != nil || string(got) != "art" { //nolint:gosec // test temp path
		t.Fatalf("plain restore artifact = %q, err=%v", got, err)
	}

	// 2. --passphrase-env backup + pre-flight meta.KeyIDs check + round-trip.
	t.Setenv("UPMO_DATABASE_URL", poolURL(t, srcOwner))
	t.Setenv("UPMO_ARTIFACTS_DIR", srcArt)
	t.Setenv("UPMO_KNOWLEDGE_DIR", srcKnow)
	t.Setenv("UPMO_BACKUP_PASS", "correct-horse-battery-staple")

	passArchive := filepath.Join(t.TempDir(), "pass.tar.gz")
	out.Reset()
	if err := backupCommand([]string{"--out", passArchive, "--passphrase-env", "UPMO_BACKUP_PASS"}, &out); err != nil {
		t.Fatalf("backupCommand(--passphrase-env): %v", err)
	}

	// Target populated DB with wrong master key ID (k2 instead of k1) must fail pre-flight
	// on meta.KeyIDs BEFORE touching live DB or volumes, even in passphrase mode!
	liveTarget := pgtest.New(t)
	if err := brainops.MigrateAll(ctx, liveTarget); err != nil {
		t.Fatal(err)
	}
	if _, err := liveTarget.Exec(ctx, `
		INSERT INTO identity.users (email, name, role_id, invited_at, joined_at)
		SELECT 'keep@example.com', 'Keep', id, now(), now() FROM identity.roles WHERE key = 'owner'`); err != nil {
		t.Fatal(err)
	}
	liveArt := t.TempDir()
	if err := os.WriteFile(filepath.Join(liveArt, "sentinel.txt"), []byte("intact"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UPMO_DATABASE_URL", poolURL(t, liveTarget))
	t.Setenv("UPMO_ARTIFACTS_DIR", liveArt)
	t.Setenv("UPMO_KNOWLEDGE_DIR", t.TempDir())
	t.Setenv(gateway.EnvMasterKey, key2B64)
	t.Setenv(gateway.EnvMasterKeyID, "k2")

	restoreOut.Reset()
	if err := restoreCommand([]string{"--in", passArchive, "--passphrase-env", "UPMO_BACKUP_PASS"}, &restoreOut); err == nil || !strings.Contains(err.Error(), "k1") {
		t.Fatalf("expected passphrase restore with missing DB key k1 to fail in pre-flight, got %v", err)
	}
	var liveUsers int
	if err := liveTarget.QueryRow(ctx, `SELECT count(*) FROM identity.users WHERE email = 'keep@example.com'`).Scan(&liveUsers); err != nil || liveUsers != 1 {
		t.Fatalf("pre-flight failure modified live DB: count=%d err=%v", liveUsers, err)
	}
	if got, err := os.ReadFile(filepath.Join(liveArt, "sentinel.txt")); err != nil || string(got) != "intact" {
		t.Fatalf("pre-flight failure modified live volume: got=%q err=%v", got, err)
	}

	// Restore --passphrase-env with matching k1 + --require-encrypted succeeds.
	t.Setenv(gateway.EnvMasterKey, key1B64)
	t.Setenv(gateway.EnvMasterKeyID, "k1")
	dstPass := pgtest.New(t)
	dstPassArt := t.TempDir()
	dstPassKnow := t.TempDir()
	t.Setenv("UPMO_DATABASE_URL", poolURL(t, dstPass))
	t.Setenv("UPMO_ARTIFACTS_DIR", dstPassArt)
	t.Setenv("UPMO_KNOWLEDGE_DIR", dstPassKnow)
	restoreOut.Reset()
	if err := restoreCommand([]string{"--in", passArchive, "--passphrase-env", "UPMO_BACKUP_PASS", "--require-encrypted"}, &restoreOut); err != nil {
		t.Fatalf("restoreCommand(--passphrase-env): %v", err)
	}
	unsealed, err := (gateway.DBSecrets{Pool: dstPass, Keys: ring1}).Get(credRef)
	if err != nil || unsealed != credSecret {
		t.Fatalf("DBSecrets.Get after passphrase restore: got=%q err=%v", unsealed, err)
	}
}

func TestRestoreRejectsTamperedAndFutureManifestBeforeMutation(t *testing.T) {
	ctx := context.Background()
	srcOwner := pgtest.New(t)
	if err := brainops.MigrateAll(ctx, srcOwner); err != nil {
		t.Fatal(err)
	}
	key1B64 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x21}, 32))
	t.Setenv(gateway.EnvMasterKey, key1B64)
	t.Setenv(gateway.EnvMasterKeyID, "k1")
	t.Setenv("UPMO_DATABASE_URL", poolURL(t, srcOwner))
	t.Setenv("UPMO_ARTIFACTS_DIR", "")
	t.Setenv("UPMO_KNOWLEDGE_DIR", "")

	encArchive := filepath.Join(t.TempDir(), "enc.tar.gz")
	var out bytes.Buffer
	if err := backupCommand([]string{"--out", encArchive}, &out); err != nil {
		t.Fatal(err)
	}

	// Live populated DB + volume that MUST NOT be touched by any rejected restore.
	liveOwner := pgtest.New(t)
	if err := brainops.MigrateAll(ctx, liveOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := liveOwner.Exec(ctx, `
		INSERT INTO identity.users (email, name, role_id, invited_at, joined_at)
		SELECT 'stay@example.com', 'Stay', id, now(), now() FROM identity.roles WHERE key = 'owner'`); err != nil {
		t.Fatal(err)
	}
	liveArt := t.TempDir()
	liveKnow := t.TempDir()
	if err := os.WriteFile(filepath.Join(liveArt, "art.keep"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(liveKnow, "know.keep"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UPMO_DATABASE_URL", poolURL(t, liveOwner))
	t.Setenv("UPMO_ARTIFACTS_DIR", liveArt)
	t.Setenv("UPMO_KNOWLEDGE_DIR", liveKnow)

	cases := []struct {
		name   string
		mutate func(*backup.Manifest)
		want   string
	}{
		{
			name: "altered_schema_versions",
			mutate: func(m *backup.Manifest) {
				m.SchemaVersions["audit"] = 1
			},
		},
		{
			name: "altered_master_key_id",
			mutate: func(m *backup.Manifest) {
				m.MasterKeyID = "k999"
			},
		},
		{
			name: "altered_encrypted_false",
			mutate: func(m *backup.Manifest) {
				m.Encrypted = false
			},
		},
		{
			name: "future_schema_version",
			mutate: func(m *backup.Manifest) {
				m.SchemaVersions["audit"] = 9999
			},
			want: "exceeds supported version",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tampered := rewriteManifestInOuterArchive(t, encArchive, tc.mutate)
			var sink bytes.Buffer
			err := restoreCommand([]string{"--in", tampered}, &sink)
			if err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
			var count int
			if err := liveOwner.QueryRow(ctx, `SELECT count(*) FROM identity.users WHERE email = 'stay@example.com'`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("live DB modified on %s: count=%d err=%v", tc.name, count, err)
			}
			if got, err := os.ReadFile(filepath.Join(liveArt, "art.keep")); err != nil || string(got) != "keep" {
				t.Fatalf("live artifacts modified on %s: got=%q err=%v", tc.name, got, err)
			}
			if got, err := os.ReadFile(filepath.Join(liveKnow, "know.keep")); err != nil || string(got) != "keep" {
				t.Fatalf("live knowledge modified on %s: got=%q err=%v", tc.name, got, err)
			}
		})
	}
}

func TestRestoreCatchesCorruptAuditChainInTxBeforeCommit(t *testing.T) {
	ctx := context.Background()
	srcOwner := pgtest.New(t)
	if err := brainops.MigrateAll(ctx, srcOwner); err != nil {
		t.Fatal(err)
	}
	if err := audit.Record(ctx, srcOwner, audit.Event{
		Action:  audit.SystemMigrated,
		Outcome: audit.Success,
		Changes: audit.Changes{
			Fields: []string{"versions"},
			Values: map[string]audit.Change{"versions": {To: map[string]int64{"audit": 2}}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := audit.SealOnce(ctx, srcOwner, io.Discard); err != nil {
		t.Fatal(err)
	}
	// Tamper with a sealed audit row in srcOwner so the backup carries a broken audit chain.
	if _, err := srcOwner.Exec(ctx, `ALTER TABLE audit.events DISABLE TRIGGER events_no_change`); err != nil {
		t.Fatal(err)
	}
	if _, err := srcOwner.Exec(ctx, `UPDATE audit.events SET reason = 'corrupted' WHERE position = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := srcOwner.Exec(ctx, `ALTER TABLE audit.events ENABLE TRIGGER events_no_change`); err != nil {
		t.Fatal(err)
	}

	key1B64 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x33}, 32))
	t.Setenv(gateway.EnvMasterKey, key1B64)
	t.Setenv(gateway.EnvMasterKeyID, "k1")
	t.Setenv("UPMO_DATABASE_URL", poolURL(t, srcOwner))
	srcArt := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcArt, "bad-backup.txt"), []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UPMO_ARTIFACTS_DIR", srcArt)
	t.Setenv("UPMO_KNOWLEDGE_DIR", "")

	badArchive := filepath.Join(t.TempDir(), "corrupt-audit.tar.gz")
	var out bytes.Buffer
	if err := backupCommand([]string{"--out", badArchive}, &out); err != nil {
		t.Fatal(err)
	}

	// Populated live target DB at same head (Case A): VerifyTx must roll back tx before commit!
	liveOwner := pgtest.New(t)
	if err := brainops.MigrateAll(ctx, liveOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := liveOwner.Exec(ctx, `
		INSERT INTO identity.users (email, name, role_id, invited_at, joined_at)
		SELECT 'intact@example.com', 'Intact', id, now(), now() FROM identity.roles WHERE key = 'owner'`); err != nil {
		t.Fatal(err)
	}
	liveArt := t.TempDir()
	if err := os.WriteFile(filepath.Join(liveArt, "live.txt"), []byte("live"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UPMO_DATABASE_URL", poolURL(t, liveOwner))
	t.Setenv("UPMO_ARTIFACTS_DIR", liveArt)
	t.Setenv("UPMO_KNOWLEDGE_DIR", "")

	var sink bytes.Buffer
	err := restoreCommand([]string{"--in", badArchive}, &sink)
	if err == nil || !strings.Contains(err.Error(), "audit chain verification failed") {
		t.Fatalf("expected audit chain verification error, got %v", err)
	}
	var intactCount int
	if err := liveOwner.QueryRow(ctx, `SELECT count(*) FROM identity.users WHERE email = 'intact@example.com'`).Scan(&intactCount); err != nil || intactCount != 1 {
		t.Fatalf("live DB was altered despite VerifyTx failure: count=%d err=%v", intactCount, err)
	}
	if got, err := os.ReadFile(filepath.Join(liveArt, "live.txt")); err != nil || string(got) != "live" {
		t.Fatalf("live volume was altered despite VerifyTx failure: got=%q err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(liveArt, "bad-backup.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bad-backup.txt leaked into live volume: err=%v", err)
	}
}

func TestRestoreOntoPopulatedDifferentHeadTargetStagingAndDrop(t *testing.T) {
	ctx := context.Background()
	srcOwner := pgtest.New(t)
	if err := brainops.MigrateAll(ctx, srcOwner); err != nil {
		t.Fatal(err)
	}
	var roleID uuid.UUID
	if err := srcOwner.QueryRow(ctx, `SELECT id FROM identity.roles WHERE key = 'owner'`).Scan(&roleID); err != nil {
		t.Fatal(err)
	}
	srcUserID := uuid.New()
	if _, err := srcOwner.Exec(ctx, `
		INSERT INTO identity.users (id, email, name, role_id, invited_at, joined_at)
		VALUES ($1, 'newhead@example.com', 'NewHead', $2, now(), now())`,
		srcUserID, roleID,
	); err != nil {
		t.Fatal(err)
	}

	key1B64 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x44}, 32))
	t.Setenv(gateway.EnvMasterKey, key1B64)
	t.Setenv(gateway.EnvMasterKeyID, "k1")
	t.Setenv("UPMO_DATABASE_URL", poolURL(t, srcOwner))
	t.Setenv("UPMO_ARTIFACTS_DIR", "")
	t.Setenv("UPMO_KNOWLEDGE_DIR", "")

	archivePath := filepath.Join(t.TempDir(), "case-c.tar.gz")
	var out bytes.Buffer
	if err := backupCommand([]string{"--out", archivePath}, &out); err != nil {
		t.Fatal(err)
	}

	// Target DB is populated at an OLDER head (Case C).
	dstOwner := pgtest.New(t)
	if err := brainops.PrepareSchemasAt(ctx, dstOwner, map[string]int64{
		"audit":     2,
		"identity":  3,
		"memory":    1,
		"knowledge": 5,
		"tasks":     3,
		"chat":      6,
		"decisions": 1,
		"artifacts": 1,
		"runtime":   2,
		"gateway":   7,
		"notices":   2,
		"platform":  2,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := dstOwner.Exec(ctx, `
		INSERT INTO identity.users (email, name, role_id, invited_at, joined_at)
		SELECT 'stale-old@example.com', 'StaleOld', id, now(), now() FROM identity.roles WHERE key = 'owner'`); err != nil {
		t.Fatal(err)
	}

	t.Setenv("UPMO_DATABASE_URL", poolURL(t, dstOwner))
	var restoreOut bytes.Buffer
	if err := restoreCommand([]string{"--in", archivePath}, &restoreOut); err != nil {
		t.Fatalf("restoreCommand(Case C): %v", err)
	}
	var staleCount, restoredCount int
	if err := dstOwner.QueryRow(ctx, `SELECT count(*) FROM identity.users WHERE email = 'stale-old@example.com'`).Scan(&staleCount); err != nil || staleCount != 0 {
		t.Fatalf("stale user survived Case C restore: count=%d err=%v", staleCount, err)
	}
	if err := dstOwner.QueryRow(ctx, `SELECT count(*) FROM identity.users WHERE id = $1`, srcUserID).Scan(&restoredCount); err != nil || restoredCount != 1 {
		t.Fatalf("restored user missing after Case C restore: count=%d err=%v", restoredCount, err)
	}
	afterVersions, err := migrate.SchemaVersions(ctx, dstOwner)
	if err != nil {
		t.Fatal(err)
	}
	if afterVersions[agents.Schema] < 1 {
		t.Fatalf("expected agents schema after Case C restore, got %v", afterVersions)
	}
}

func TestBackupRestoreRejectsNonBypassRLSRole(t *testing.T) {
	ctx := context.Background()
	owner := pgtest.New(t)
	if err := brainops.MigrateAll(ctx, owner); err != nil {
		t.Fatal(err)
	}

	roleName := "backup_nobypass_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:12]
	const rolePass = "pgtest-nobypass-pw" // #nosec G101 -- test-only role password
	quotedRole := pgx.Identifier{roleName}.Sanitize()
	if _, err := owner.Exec(ctx, fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD '%s' NOSUPERUSER NOBYPASSRLS`, quotedRole, rolePass)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = owner.Exec(context.Background(), fmt.Sprintf(`DROP ROLE IF EXISTS %s`, quotedRole))
	})

	u, err := url.Parse(poolURL(t, owner))
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(roleName, rolePass)

	key1B64 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x51}, 32))
	t.Setenv(gateway.EnvMasterKey, key1B64)
	t.Setenv(gateway.EnvMasterKeyID, "k1")
	t.Setenv("UPMO_ARTIFACTS_DIR", "")
	t.Setenv("UPMO_KNOWLEDGE_DIR", "")

	// First create a valid archive as owner.
	t.Setenv("UPMO_DATABASE_URL", poolURL(t, owner))
	archivePath := filepath.Join(t.TempDir(), "priv-check.tar.gz")
	var sink bytes.Buffer
	if err := backupCommand([]string{"--out", archivePath}, &sink); err != nil {
		t.Fatal(err)
	}

	// Now connect as non-superuser / non-BYPASSRLS role.
	t.Setenv("UPMO_DATABASE_URL", u.String())
	err = backupCommand([]string{"--out", filepath.Join(t.TempDir(), "should-fail.tar.gz")}, &sink)
	if err == nil || (!strings.Contains(err.Error(), "SUPERUSER") && !strings.Contains(err.Error(), "BYPASSRLS")) {
		t.Fatalf("expected backup pre-flight privilege error, got %v", err)
	}
	err = restoreCommand([]string{"--in", archivePath}, &sink)
	if err == nil || (!strings.Contains(err.Error(), "SUPERUSER") && !strings.Contains(err.Error(), "BYPASSRLS")) {
		t.Fatalf("expected restore pre-flight privilege error, got %v", err)
	}
}

func rewriteManifestInOuterArchive(t *testing.T, srcArchive string, mutate func(*backup.Manifest)) string {
	t.Helper()
	f, err := os.Open(srcArchive) //nolint:gosec // test temp path
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)

	type rawEntry struct {
		hdr  *tar.Header
		body []byte
	}
	var entries []rawEntry
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Name == "manifest.json" {
			var m backup.Manifest
			if err := json.Unmarshal(body, &m); err != nil {
				t.Fatal(err)
			}
			mutate(&m)
			body, err = json.MarshalIndent(m, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			hdr.Size = int64(len(body))
		}
		entries = append(entries, rawEntry{hdr: hdr, body: body})
	}

	outPath := filepath.Join(t.TempDir(), "tampered.tar.gz")
	outF, err := os.Create(outPath) //nolint:gosec // test temp path
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = outF.Close() }()
	outGZ := gzip.NewWriter(outF)
	outTW := tar.NewWriter(outGZ)
	for _, e := range entries {
		if err := outTW.WriteHeader(&tar.Header{
			Name:     e.hdr.Name,
			Typeflag: e.hdr.Typeflag,
			Size:     int64(len(e.body)),
			Mode:     0o600,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := outTW.Write(e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := outTW.Close(); err != nil {
		t.Fatal(err)
	}
	if err := outGZ.Close(); err != nil {
		t.Fatal(err)
	}
	return outPath
}
