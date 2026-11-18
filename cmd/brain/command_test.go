package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

// These commands read UPMO_DATABASE_URL directly instead of through
// config.Load, so the plaintext refusal has to be theirs. The URL never
// reaches a dial: the error is the check's.
func TestMigrateAndBackupRefusePlaintextDatabaseURL(t *testing.T) {
	t.Setenv("UPMO_DATABASE_URL", "postgres://db.example.com/upmo?sslmode=disable")
	err := command("migrate", nil)
	if err == nil || !strings.Contains(err.Error(), "config: UPMO_DATABASE_URL") {
		t.Fatalf("migrate err = %v, want it to name config: UPMO_DATABASE_URL", err)
	}
	err = backupCommand([]string{"--out", t.TempDir() + "/backup.tar.gz"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "config: UPMO_DATABASE_URL") {
		t.Fatalf("backup err = %v, want it to name config: UPMO_DATABASE_URL", err)
	}
}

// The same commands refuse the development database URL and master key
// outside development, before any pool or keyring is opened. An empty
// UPMO_ENV is production, like an unset one.
func TestMigrateBackupRestoreRefuseDevelopmentValuesInProduction(t *testing.T) {
	composeDBURL := func() string {
		return "postgres://upmo:upmo@postgres:5432/upmo?sslmode=disable" // #nosec G101 -- the compose development URL, never a real credential
	}
	freshURL := func(t *testing.T) string {
		t.Helper()
		raw := make([]byte, 16)
		if _, err := rand.Read(raw); err != nil {
			t.Fatal(err)
		}
		return "postgres://upmo:" + hex.EncodeToString(raw) + "@postgres:5432/upmo?sslmode=disable"
	}
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")

	t.Run("development database url", func(t *testing.T) {
		t.Setenv("UPMO_ENV", "")
		t.Setenv("UPMO_DATABASE_URL", composeDBURL())
		const want = "config: UPMO_DATABASE_URL uses a development or empty password"
		for name, err := range map[string]error{
			"migrate": command("migrate", nil),
			"backup":  backupCommand([]string{"--out", archive}, io.Discard),
			"restore": restoreCommand([]string{"--in", archive}, io.Discard),
		} {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("%s err = %v, want it to say %q", name, err, want)
			}
			if strings.Contains(err.Error(), "upmo:upmo") {
				t.Fatalf("%s err holds the credentials: %v", name, err)
			}
		}
	})
	t.Run("development master key", func(t *testing.T) {
		t.Setenv("UPMO_ENV", "")
		t.Setenv("UPMO_DATABASE_URL", freshURL(t))
		t.Setenv("UPMO_MASTER_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'m'}, 32)))
		const want = "config: UPMO_MASTER_KEY uses a development or weak key"
		for name, err := range map[string]error{
			"backup":  backupCommand([]string{"--out", archive}, io.Discard),
			"restore": restoreCommand([]string{"--in", archive}, io.Discard),
		} {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("%s err = %v, want it to say %q", name, err, want)
			}
		}
	})
}
