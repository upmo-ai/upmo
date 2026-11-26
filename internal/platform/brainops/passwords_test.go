package brainops

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
)

// phc stands in for the argon2id string being sealed; the seal treats it as
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

// person parses one person's id.
func person(t *testing.T, id string) identity.UserID {
	t.Helper()
	user, err := identity.ParseUserID(id)
	if err != nil {
		t.Fatal(err)
	}
	return user
}

func bytes32(b byte) []byte {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = b
	}
	return raw
}

// Invariant 1: a stored password is five fields, the payload under its own
// data key bound to identity:password:<uuid>, the data key wrapped for
// v1:password:<uuid>.
func TestPasswordSealRoundTrip(t *testing.T) {
	keys := ring(t, "k1", 'a')
	user := person(t, "11111111-1111-4111-8111-111111111111")
	stored, err := sealPassword(keys, user, phc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "$argon2id$") {
		t.Fatalf("stored hash holds a plain argon2id string: %s", stored)
	}
	parts := strings.Split(stored, ":")
	if len(parts) != 5 || parts[0] != "k1" {
		t.Fatalf("stored hash = %s, want five fields under k1", stored)
	}
	for i, want := range map[int]int{1: 12, 2: 48, 3: 12} {
		raw, err := base64.StdEncoding.DecodeString(parts[i])
		if err != nil || len(raw) != want {
			t.Fatalf("field %d = %d bytes %v, want %d", i+1, len(raw), err, want)
		}
	}
	opened, err := openPassword(keys, user, stored)
	if err != nil || opened != phc {
		t.Fatalf("opened = %q %v, want the sealed hash", opened, err)
	}
	sealed, err := gateway.ParseSealedText(stored)
	if err != nil {
		t.Fatal(err)
	}
	// The master is not the payload key.
	if _, err := gateway.OpenData(bytes32('a'), identity.PasswordRef(user), sealed.Ciphertext, sealed.Nonce); err == nil {
		t.Fatal("the payload opened with the master as its key")
	}
	// The wrap is bound to this person's uuid.
	other := person(t, "22222222-2222-4222-8222-222222222222")
	if _, err := keys.Unwrap(gateway.KindPassword, other.String(), sealed.KeyID, sealed.WrapNonce, sealed.WrappedKey); err == nil {
		t.Fatal("the wrap opened for another person")
	}
	if _, err := keys.Unwrap("credential", user.String(), sealed.KeyID, sealed.WrapNonce, sealed.WrappedKey); err == nil {
		t.Fatal("the wrap opened as a credential")
	}
	dataKey, err := keys.Unwrap(gateway.KindPassword, user.String(), sealed.KeyID, sealed.WrapNonce, sealed.WrappedKey)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := gateway.OpenData(dataKey, identity.PasswordRef(user), sealed.Ciphertext, sealed.Nonce); err != nil || got != phc {
		t.Fatalf("payload with the unwrapped key = %q %v", got, err)
	}
	if _, err := gateway.OpenData(dataKey, identity.PasswordRef(other), sealed.Ciphertext, sealed.Nonce); err == nil {
		t.Fatal("the payload opened for another person")
	}
}

func TestPasswordSealOpensAfterRotation(t *testing.T) {
	user := person(t, "11111111-1111-4111-8111-111111111111")
	stored, err := sealPassword(ring(t, "k1", 'a'), user, phc)
	if err != nil {
		t.Fatal(err)
	}
	previous := gateway.EnvMasterKeyPrevious + "k1=" + base64.StdEncoding.EncodeToString(bytes32('a'))
	next := ringFrom(t, "k2", 'b', []string{previous})
	if opened, err := openPassword(next, user, stored); err != nil || opened != phc {
		t.Fatalf("opened = %q %v, want the sealed hash", opened, err)
	}
	// New seals name the current key.
	stored, err = sealPassword(next, user, phc)
	if err != nil {
		t.Fatal(err)
	}
	if keyID, _, _ := strings.Cut(stored, ":"); keyID != "k2" {
		t.Fatalf("key id = %s, want k2", keyID)
	}
}

func TestPasswordSealWrongKeyFails(t *testing.T) {
	user := person(t, "11111111-1111-4111-8111-111111111111")
	stored, err := sealPassword(ring(t, "k1", 'a'), user, phc)
	if err != nil {
		t.Fatal(err)
	}
	if opened, err := openPassword(ring(t, "k1", 'b'), user, stored); err == nil {
		t.Fatalf("opened with the wrong key: %s", opened)
	}
}

func TestPasswordSealGarbageFails(t *testing.T) {
	keys := ring(t, "k1", 'a')
	user := person(t, "11111111-1111-4111-8111-111111111111")
	stored, err := sealPassword(keys, user, phc)
	if err != nil {
		t.Fatal(err)
	}
	// The three-field form is gone: it never opens.
	old, err := keys.Seal(identity.PasswordRef(user), phc)
	if err != nil {
		t.Fatal(err)
	}
	three := "k1:" + base64.StdEncoding.EncodeToString(old.Nonce) + ":" + base64.StdEncoding.EncodeToString(old.Ciphertext)
	for name, bad := range map[string]string{
		"empty":       "",
		"key id only": "k1",
		"three":       three,
		"four":        "k1:AAAA:BBBB:CCCC",
		"six":         stored + ":AAAA",
		"bad base64":  "k1:!!!:AAAA:AAAA:AAAA",
		"bad key id":  "k-1" + stored[2:],
		"truncated":   stored[:len(stored)-8],
		"altered":     stored[:len(stored)-8] + "AAAAAAAA",
	} {
		if opened, err := openPassword(keys, user, bad); !errors.Is(err, errStoredHash) {
			t.Fatalf("%s opened: %q %v", name, opened, err)
		}
	}
}
