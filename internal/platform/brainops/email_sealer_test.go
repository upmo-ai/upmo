package brainops

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
)

func TestEmailSealerRoundTrip(t *testing.T) {
	sealer := NewEmailSealer(ring(t, "k1", 'a'))
	stored, hash, keyID, err := sealer.Seal(t.Context(), " Ada@Example.com ")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(stored, ":")
	if len(parts) != 5 {
		t.Fatalf("stored parts = %d, want 5: %s", len(parts), stored)
	}
	if parts[0] != "k1" {
		t.Fatalf("key id = %s, want k1", parts[0])
	}
	low := strings.ToLower(stored)
	if strings.Contains(low, "ada") || strings.Contains(low, "example") {
		t.Fatalf("stored form carries the address: %s", stored)
	}
	if len(hash) != 32 {
		t.Fatalf("index hash = %d bytes, want 32", len(hash))
	}
	if keyID != "k1" {
		t.Fatalf("hash key id = %s, want k1", keyID)
	}
	got, err := sealer.Open(stored, hash)
	if err != nil || got != "Ada@Example.com" {
		t.Fatalf("Open = %q, %v", got, err)
	}
}

func TestEmailSealerIndexIsCaseInsensitive(t *testing.T) {
	sealer := NewEmailSealer(ring(t, "k1", 'a'))
	_, lower, _, err := sealer.Seal(t.Context(), "ada@example.com")
	if err != nil {
		t.Fatal(err)
	}
	_, mixed, _, err := sealer.Seal(t.Context(), "  ADA@example.COM ")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(lower, mixed) {
		t.Fatal("the index hash depends on casing or surrounding space")
	}
	if hashes := indexHashes(t, sealer, "Ada@Example.com"); !bytes.Equal(hashes[0], lower) {
		t.Fatal("IndexHashes[0] is not Seal's hash")
	}
}

func TestEmailSealerBindsTheHash(t *testing.T) {
	sealer := NewEmailSealer(ring(t, "k1", 'a'))
	stored, _, _, err := sealer.Seal(t.Context(), "ada@example.com")
	if err != nil {
		t.Fatal(err)
	}
	_, other, _, err := sealer.Seal(t.Context(), "grace@example.org")
	if err != nil {
		t.Fatal(err)
	}
	got, err := sealer.Open(stored, other)
	if !errors.Is(err, identity.ErrStoredEmail) || got != "" {
		t.Fatalf("Open under another row's hash = %q, %v", got, err)
	}
	if got, err := sealer.Open(stored, nil); !errors.Is(err, identity.ErrStoredEmail) || got != "" {
		t.Fatalf("Open with no hash = %q, %v", got, err)
	}
}

func TestEmailSealerRefusesPlainValues(t *testing.T) {
	sealer := NewEmailSealer(ring(t, "k1", 'a'))
	_, hash, _, err := sealer.Seal(t.Context(), "ada@example.com")
	if err != nil {
		t.Fatal(err)
	}
	got, err := sealer.Open("ada@example.com", hash)
	if !errors.Is(err, identity.ErrStoredEmail) || got != "" {
		t.Fatalf("Open = %q, %v; want ErrStoredEmail and no address", got, err)
	}
	if strings.Contains(err.Error(), "ada") {
		t.Fatal("the error text carries the address")
	}
}

func TestEmailSealerSeesThroughRotation(t *testing.T) {
	old := NewEmailSealer(ring(t, "k1", 'a'))
	stored, k1hash, _, err := old.Seal(t.Context(), "ada@example.com")
	if err != nil {
		t.Fatal(err)
	}
	previous := gateway.EnvMasterKeyPrevious + "k1=" + base64.StdEncoding.EncodeToString(bytes32('a'))
	rotated := NewEmailSealer(ringFrom(t, "k2", 'b', []string{previous}))
	hashes := indexHashes(t, rotated, "ada@example.com")
	if len(hashes) != 2 {
		t.Fatalf("index hashes = %d, want 2", len(hashes))
	}
	found := false
	for _, h := range hashes {
		if bytes.Equal(h, k1hash) {
			found = true
		}
	}
	if !found {
		t.Fatal("the rotated ring does not know the k1 index hash")
	}
	got, err := rotated.Open(stored, k1hash)
	if err != nil || got != "ada@example.com" {
		t.Fatalf("Open after rotation = %q, %v", got, err)
	}
	// A ring that only holds the new key neither finds nor opens the old row.
	only := NewEmailSealer(ring(t, "k2", 'b'))
	for _, h := range indexHashes(t, only, "ada@example.com") {
		if bytes.Equal(h, k1hash) {
			t.Fatal("the k2-only ring knows the k1 index hash")
		}
	}
	if got, err := only.Open(stored, k1hash); !errors.Is(err, identity.ErrStoredEmail) {
		t.Fatalf("Open with only k2 = %q, %v", got, err)
	}
}

func TestEmailSealerWithoutKeys(t *testing.T) {
	sealer := NewEmailSealer(gateway.Keyring{})
	if sealer.Ready() {
		t.Fatal("the zero ring is ready")
	}
	stored, _, _, err := sealer.Seal(t.Context(), "ada@example.com")
	if !errors.Is(err, identity.ErrNoEmailSealer) || stored != "" {
		t.Fatalf("Seal = %q, %v; want ErrNoEmailSealer and no stored form", stored, err)
	}
}

func TestEmailSealerRefusesABlankAddress(t *testing.T) {
	sealer := NewEmailSealer(ring(t, "k1", 'a'))
	stored, _, _, err := sealer.Seal(t.Context(), "   ")
	if err == nil || stored != "" {
		t.Fatalf("Seal of a blank address = %q, %v", stored, err)
	}
	if strings.Contains(err.Error(), "ada") {
		t.Fatal("the error text carries the address")
	}
}

func indexHashes(t *testing.T, s identity.EmailSealer, address string) [][]byte {
	t.Helper()
	hashes, err := s.IndexHashes(t.Context(), address)
	if err != nil {
		t.Fatal(err)
	}
	return hashes
}
