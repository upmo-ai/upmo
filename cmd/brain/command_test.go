package main

import (
	"io"
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
