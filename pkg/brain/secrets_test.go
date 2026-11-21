package brain

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
)

// New builds the secret chain on every start, so a plaintext Vault address
// with a token fails it even with no audit webhook or syslog configured. The
// refusal comes before any pool opens, so no database is needed. Env is
// development with a master key so the guard reaches the Vault TLS check.
func TestNewRefusesAPlaintextVaultAddressWithoutAuditPush(t *testing.T) {
	t.Setenv("UPMO_MASTER_KEY", base64.StdEncoding.EncodeToString([]byte(strings.Repeat("m", 32))))
	t.Setenv("UPMO_VAULT_ADDR", "http://vault.example")
	t.Setenv("UPMO_VAULT_TOKEN", "t")
	_, err := New(context.Background(), Config{Env: "development", PublicURL: "http://localhost:5173"})
	if err == nil || !strings.Contains(err.Error(), "UPMO_VAULT_ADDR") {
		t.Fatalf("err = %v, want it to name UPMO_VAULT_ADDR", err)
	}
}
