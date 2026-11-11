package domain

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func testKey(b byte) []byte { return bytes.Repeat([]byte{b}, MasterKeyLen) }

func ring(t *testing.T, current string, keys map[string][]byte) Keyring {
	t.Helper()
	k, err := NewKeyring(current, keys)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSealOpenRoundTrip(t *testing.T) {
	k := ring(t, "k1", map[string][]byte{"k1": testKey('a')})
	sealed, err := k.Seal("own_abc", "sk-live-secret")
	if err != nil {
		t.Fatal(err)
	}
	if sealed.KeyID != "k1" || len(sealed.Nonce) != 12 {
		t.Fatalf("sealed = %+v", sealed)
	}
	if bytes.Contains(sealed.Ciphertext, []byte("sk-live-secret")) {
		t.Fatal("ciphertext holds the plain key")
	}
	got, err := k.Open("own_abc", sealed)
	if err != nil || got != "sk-live-secret" {
		t.Fatalf("Open = %q, %v", got, err)
	}
}

func TestSealUsesAFreshNonce(t *testing.T) {
	k := ring(t, "k1", map[string][]byte{"k1": testKey('a')})
	one, _ := k.Seal("r", "same")
	two, _ := k.Seal("r", "same")
	if bytes.Equal(one.Nonce, two.Nonce) || bytes.Equal(one.Ciphertext, two.Ciphertext) {
		t.Fatal("two seals of one value are identical")
	}
}

func TestOpenFailsClosed(t *testing.T) {
	k := ring(t, "k1", map[string][]byte{"k1": testKey('a')})
	sealed, err := k.Seal("own_abc", "sk-live-secret")
	if err != nil {
		t.Fatal(err)
	}
	flipped := func(b []byte) []byte {
		out := append([]byte(nil), b...)
		out[0] ^= 1
		return out
	}
	tests := map[string]struct {
		ring Keyring
		ref  string
		in   Sealed
	}{
		"wrong master key":  {ring(t, "k1", map[string][]byte{"k1": testKey('b')}), "own_abc", sealed},
		"wrong ref":         {k, "own_other", sealed},
		"tampered text":     {k, "own_abc", Sealed{flipped(sealed.Ciphertext), sealed.Nonce, "k1"}},
		"tampered nonce":    {k, "own_abc", Sealed{sealed.Ciphertext, flipped(sealed.Nonce), "k1"}},
		"truncated":         {k, "own_abc", Sealed{sealed.Ciphertext[:3], sealed.Nonce, "k1"}},
		"short nonce":       {k, "own_abc", Sealed{sealed.Ciphertext, sealed.Nonce[:4], "k1"}},
		"unknown key id":    {k, "own_abc", Sealed{sealed.Ciphertext, sealed.Nonce, "k9"}},
		"no keys at all":    {Keyring{}, "own_abc", sealed},
		"empty value":       {k, "own_abc", Sealed{}},
		"key id of another": {ring(t, "k2", map[string][]byte{"k2": testKey('a')}), "own_abc", sealed},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := tt.ring.Open(tt.ref, tt.in)
			if !errors.Is(err, ErrUnseal) || got != "" {
				t.Fatalf("Open = %q, %v; want ErrUnseal and no text", got, err)
			}
			if strings.Contains(err.Error(), "sk-live") {
				t.Fatal("error text carries the key")
			}
		})
	}
}

func TestRotationByKeyID(t *testing.T) {
	old := ring(t, "k1", map[string][]byte{"k1": testKey('a')})
	sealedOld, err := old.Seal("r", "old-secret")
	if err != nil {
		t.Fatal(err)
	}
	rotated := ring(t, "k2", map[string][]byte{"k1": testKey('a'), "k2": testKey('b')})
	if got, err := rotated.Open("r", sealedOld); err != nil || got != "old-secret" {
		t.Fatalf("old key no longer reads: %q, %v", got, err)
	}
	sealedNew, err := rotated.Seal("r", "new-secret")
	if err != nil {
		t.Fatal(err)
	}
	if sealedNew.KeyID != "k2" {
		t.Fatalf("new value sealed with %q", sealedNew.KeyID)
	}
	if _, err := old.Open("r", sealedNew); !errors.Is(err, ErrUnseal) {
		t.Fatal("the old ring opened a value sealed by the new key")
	}
}

func TestZeroKeyringCannotSeal(t *testing.T) {
	var k Keyring
	if k.Ready() {
		t.Fatal("zero ring is ready")
	}
	if _, err := k.Seal("r", "x"); !errors.Is(err, ErrNoMasterKey) {
		t.Fatalf("Seal error = %v", err)
	}
}

func TestNewKeyringRefusesBadInput(t *testing.T) {
	good := map[string][]byte{"k1": testKey('a')}
	tests := map[string]struct {
		current string
		keys    map[string][]byte
	}{
		"short key":           {"k1", map[string][]byte{"k1": []byte("short")}},
		"long key":            {"k1", map[string][]byte{"k1": append(testKey('a'), 1)}},
		"current not in ring": {"k2", good},
		"bad current id":      {"k 1", good},
		"bad id":              {"k1", map[string][]byte{"k1": testKey('a'), "bad id": testKey('b')}},
		"empty id":            {"", good},
		"no keys":             {"k1", nil},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := NewKeyring(tt.current, tt.keys); err == nil {
				t.Fatal("NewKeyring accepted it")
			}
		})
	}
}

func TestNewKeyringErrorNeverCarriesKeyBytes(t *testing.T) {
	_, err := NewKeyring("k1", map[string][]byte{"k1": []byte("super-secret-but-short")})
	if err == nil || strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("error = %v", err)
	}
}

func TestNewKeyringWipesTheRawKeyBytes(t *testing.T) {
	raw := testKey('a')
	if _, err := NewKeyring("k1", map[string][]byte{"k1": raw}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, make([]byte, MasterKeyLen)) {
		t.Fatal("the raw key bytes are not wiped after the ring is built")
	}
	// A ring that refuses to build wipes them too.
	rejected := testKey('b')
	if _, err := NewKeyring("nope", map[string][]byte{"k1": rejected}); err == nil {
		t.Fatal("NewKeyring accepted a current id that is not in the ring")
	}
	if !bytes.Equal(rejected, make([]byte, MasterKeyLen)) {
		t.Fatal("the refused key bytes are not wiped")
	}
}
