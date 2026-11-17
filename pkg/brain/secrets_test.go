package brain

import (
	"context"
	"strings"
	"testing"
)

// New builds the secret chain on every start, so a plaintext Vault address
// with a token fails it even with no audit webhook or syslog configured. The
// refusal comes before any pool opens, so no database is needed.
func TestNewRefusesAPlaintextVaultAddressWithoutAuditPush(t *testing.T) {
	t.Setenv("UPMO_VAULT_ADDR", "http://vault.example")
	t.Setenv("UPMO_VAULT_TOKEN", "t")
	_, err := New(context.Background(), Config{PublicURL: "http://localhost:5173"})
	if err == nil || !strings.Contains(err.Error(), "UPMO_VAULT_ADDR") {
		t.Fatalf("err = %v, want it to name UPMO_VAULT_ADDR", err)
	}
}
