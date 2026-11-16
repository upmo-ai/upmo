package brainops

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
)

// phc stands in for the argon2id string being sealed; the sealer treats it as
// opaque text.
const phc = "$argon2id$v=19$m=19456,t=2,p=1$c2FsdGlzdGFs$YWJjZGVmZ2hpamtsbW5vcA"

// ring builds one master key ring around 32 bytes of key material.
func ring(t *testing.T, id string, b byte) gateway.Keyring {
	t.Helper()
	return ringFrom(t, id, b, nil)
}

func ringFrom(t *testing.T, id string, b byte, environ []string) gateway.Keyring {
	t.Helper()
	env := map[string]string{
		gateway.EnvMasterKey:   base64.StdEncoding.EncodeToString(bytes32(b)),
		gateway.EnvMasterKeyID: id,
	}
	keys, err := gateway.KeyringFromEnv(func(name string) (string, bool) {
		v, ok := env[name]
		return v, ok
	}, environ)
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

// userRef is the password seal ref of one person.
func userRef(t *testing.T, id string) string {
	t.Helper()
	user, err := identity.ParseUserID(id)
	if err != nil {
		t.Fatal(err)
	}
	return identity.PasswordRef(user)
}

func bytes32(b byte) []byte {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = b
	}
	return raw
}

func TestPasswordSealerRoundTrip(t *testing.T) {
	sealer := NewPasswordSealer(ring(t, "k1", 'a'))
	ref := userRef(t, "11111111-1111-4111-8111-111111111111")
	stored, err := sealer.Seal(ref, phc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(stored, "$argon2id$") {
		t.Fatalf("stored hash is a plain argon2id string: %s", stored)
	}
	parts := strings.Split(stored, ":")
	if len(parts) != 3 {
		t.Fatalf("stored hash parts = %d, want 3: %s", len(parts), stored)
	}
	if parts[0] != "k1" {
		t.Fatalf("key id = %s, want k1", parts[0])
	}
	if _, err := base64.StdEncoding.DecodeString(parts[1]); err != nil {
		t.Fatalf("nonce is not base64: %v", err)
	}
	if _, err := base64.StdEncoding.DecodeString(parts[2]); err != nil {
		t.Fatalf("ciphertext is not base64: %v", err)
	}
	opened, err := sealer.Open(ref, stored)
	if err != nil {
		t.Fatal(err)
	}
	if opened != phc {
		t.Fatalf("opened = %s, want the sealed hash", opened)
	}
}

func TestPasswordSealerOpensAfterRotation(t *testing.T) {
	old := NewPasswordSealer(ring(t, "k1", 'a'))
	ref := userRef(t, "11111111-1111-4111-8111-111111111111")
	stored, err := old.Seal(ref, phc)
	if err != nil {
		t.Fatal(err)
	}
	previous := gateway.EnvMasterKeyPrevious + "k1=" + base64.StdEncoding.EncodeToString(bytes32('a'))
	next := NewPasswordSealer(ringFrom(t, "k2", 'b', []string{previous}))
	opened, err := next.Open(ref, stored)
	if err != nil {
		t.Fatal(err)
	}
	if opened != phc {
		t.Fatalf("opened = %s, want the sealed hash", opened)
	}
	// New seals name the current key.
	stored, err = next.Seal(ref, phc)
	if err != nil {
		t.Fatal(err)
	}
	if keyID, _, _ := strings.Cut(stored, ":"); keyID != "k2" {
		t.Fatalf("key id = %s, want k2", keyID)
	}
}

func TestPasswordSealerWrongKeyFails(t *testing.T) {
	sealer := NewPasswordSealer(ring(t, "k1", 'a'))
	ref := userRef(t, "11111111-1111-4111-8111-111111111111")
	stored, err := sealer.Seal(ref, phc)
	if err != nil {
		t.Fatal(err)
	}
	other := NewPasswordSealer(ring(t, "k1", 'b'))
	if opened, err := other.Open(ref, stored); err == nil {
		t.Fatalf("opened with the wrong key: %s", opened)
	}
}

func TestPasswordSealerBindsTheRef(t *testing.T) {
	sealer := NewPasswordSealer(ring(t, "k1", 'a'))
	ref := userRef(t, "11111111-1111-4111-8111-111111111111")
	stored, err := sealer.Seal(ref, phc)
	if err != nil {
		t.Fatal(err)
	}
	otherRef := userRef(t, "22222222-2222-4222-8222-222222222222")
	if opened, err := sealer.Open(otherRef, stored); err == nil {
		t.Fatalf("opened under another person's ref: %s", opened)
	}
	// A provider credential ref is not a password ref.
	if opened, err := sealer.Open("own_wiretest", stored); err == nil {
		t.Fatalf("opened under a provider credential ref: %s", opened)
	}
}

func TestPasswordSealerGarbageFails(t *testing.T) {
	sealer := NewPasswordSealer(ring(t, "k1", 'a'))
	ref := userRef(t, "11111111-1111-4111-8111-111111111111")
	stored, err := sealer.Seal(ref, phc)
	if err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]string{
		"empty":         "",
		"key id only":   "k1",
		"no ciphertext": "k1:AAAA",
		"extra part":    "k1:AAAA:BBBB:CCCC",
		"bad base64":    "k1:!!!:AAAA",
		"truncated":     stored[:len(stored)-8],
		"altered":       stored[:len(stored)-8] + "AAAAAAAA",
	} {
		if opened, err := sealer.Open(ref, bad); err == nil {
			t.Fatalf("%s opened: %s", name, opened)
		}
	}
}
