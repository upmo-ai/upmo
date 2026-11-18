// Package sealer gives integration tests a real password sealer: AES-256-GCM
// through the gateway master keyring, stored the way brainops.NewPasswordSealer
// stores one (key id, nonce and ciphertext joined by colons).
package sealer

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/brainops"
)

// errStored is every reason a stored password hash does not open.
var errStored = errors.New("sealer: stored password hash")

// Sealer seals and opens stored password hashes for the identity suites. The
// zero value holds no key: it refuses to seal and opens nothing, like a brain
// without a master key.
type Sealer struct {
	keys gateway.Keyring
}

// New builds a sealer over one master key, current id "k1".
func New(t *testing.T) Sealer {
	t.Helper()
	return Sealer{keys: ring(t)}
}

// NewEmail returns a real email sealer for the identity suites: the brainops
// sealer over one master key, current id "k1".
func NewEmail(t *testing.T) identity.EmailSealer {
	t.Helper()
	return brainops.NewEmailSealer(ring(t))
}

// ring builds one master keyring over 32 bytes of key material, current id "k1".
func ring(t *testing.T) gateway.Keyring {
	t.Helper()
	keys, err := gateway.KeyringFromEnv(func(name string) (string, bool) {
		if name == gateway.EnvMasterKey {
			return base64.StdEncoding.EncodeToString(bytes32('t')), true
		}
		return "", false
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func (s Sealer) Ready() bool { return s.keys.Ready() }

func (s Sealer) Seal(ref, hash string) (string, error) {
	sealed, err := s.keys.Seal(ref, hash)
	if err != nil {
		return "", err
	}
	return strings.Join([]string{
		sealed.KeyID,
		base64.StdEncoding.EncodeToString(sealed.Nonce),
		base64.StdEncoding.EncodeToString(sealed.Ciphertext),
	}, ":"), nil
}

func (s Sealer) Open(ref, stored string) (string, error) {
	id, rest, ok := strings.Cut(stored, ":")
	nonce, ciphertext, ok2 := strings.Cut(rest, ":")
	if !ok || !ok2 || strings.Contains(ciphertext, ":") {
		return "", errStored
	}
	rawNonce, err := base64.StdEncoding.DecodeString(nonce)
	if err != nil {
		return "", errStored
	}
	rawCiphertext, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", errStored
	}
	hash, err := s.keys.Open(ref, gateway.Sealed{Ciphertext: rawCiphertext, Nonce: rawNonce, KeyID: id})
	if err != nil {
		return "", errStored
	}
	return hash, nil
}

func bytes32(b byte) []byte {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = b
	}
	return raw
}
