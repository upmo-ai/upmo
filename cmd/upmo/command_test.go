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

// pgxpool.New echoes the URL in its parse error (userinfo and query string,
// with only the password masked). pool_max_conns is read by pgxpool alone, so
// the URL passes the plaintext check and must still reach the terminal as the
// scrubbed message.
func TestMigrateErrorDoesNotEchoDatabaseURL(t *testing.T) {
	const marker = "s3cret-pw"
	t.Setenv("UPMO_DATABASE_URL", "postgres://opuser:"+marker+"@127.0.0.1:1/x?pool_max_conns=abc")
	err := command("migrate", nil)
	if err == nil {
		t.Fatal("migrate err = nil, want an error")
	}
	msg := err.Error()
	if strings.Contains(msg, marker) || strings.Contains(msg, "opuser") {
		t.Fatalf("migrate error leaked the URL credentials: %q", msg)
	}
	if want := "brain: UPMO_DATABASE_URL could not be opened"; !strings.Contains(msg, want) {
		t.Fatalf("migrate err = %q, want %q", msg, want)
	}
}

// A URL that parses and passes the plaintext check but cannot connect fails at
// the first dial, where pgx names the user and host. Every operator command
// must still report only the scrubbed message.
func TestOperatorCommandsDoNotEchoUserOrHostOnConnectFailure(t *testing.T) {
	const (
		marker = "s3cret-pw"
		user   = "opuser"
		host   = "127.0.0.1"
	)
	t.Setenv("UPMO_ENV", "development")
	t.Setenv("UPMO_MASTER_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'m'}, 32)))
	t.Setenv("UPMO_DATABASE_URL", "postgres://"+user+":"+marker+"@"+host+":1/x?sslmode=require")
	t.Setenv("UPMO_PUBLIC_URL", "http://localhost:8080")
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")

	for name, run := range map[string]func() error{
		"migrate":   func() error { return command("migrate", nil) },
		"bootstrap": func() error { return command("bootstrap", []string{"--email", "a@example.com", "--name", "A"}) },
		"recover":   func() error { return command("recover", []string{"--email", "a@example.com"}) },
		"backup": func() error {
			return backupCommand([]string{"--out", archive, "--no-encrypt"}, io.Discard)
		},
		"restore": func() error { return restoreCommand([]string{"--in", archive}, io.Discard) },
	} {
		t.Run(name, func(t *testing.T) {
			err := run()
			if err == nil {
				t.Fatal("err = nil, want an error")
			}
			msg := err.Error()
			for _, leak := range []string{marker, user, host} {
				if strings.Contains(msg, leak) {
					t.Fatalf("error leaked %q: %q", leak, msg)
				}
			}
			if want := "brain: UPMO_DATABASE_URL could not be opened"; !strings.Contains(msg, want) {
				t.Fatalf("err = %q, want %q", msg, want)
			}
		})
	}
}

func TestSecretCommandsRefuseMissingMasterKeyBeforeOpeningDatabase(t *testing.T) {
	t.Setenv("UPMO_ENV", "development")
	t.Setenv("UPMO_MASTER_KEY", "")
	t.Setenv("UPMO_DATABASE_URL", "postgres://upmo:upmo@postgres:5432/upmo?sslmode=disable")
	t.Setenv("UPMO_PUBLIC_URL", "http://localhost:8080")
	for name, args := range map[string][]string{
		"keys":      {"reseal"},
		"bootstrap": {"--email", "a@example.com", "--name", "A"},
		"recover":   {"--email", "a@example.com"},
	} {
		t.Run(name, func(t *testing.T) {
			err := command(name, args)
			if err == nil || err.Error() != "config: UPMO_MASTER_KEY is required; set a key" {
				t.Fatalf("command err = %v", err)
			}
		})
	}
}
