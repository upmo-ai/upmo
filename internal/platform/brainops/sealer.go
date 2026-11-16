package brainops

import (
	"encoding/base64"
	"errors"
	"strings"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
)

// NewPasswordSealer returns a PasswordSealer backed by keys.
func NewPasswordSealer(keys gateway.Keyring) identity.PasswordSealer {
	return passwordSealer{keys: keys}
}

// errStoredHash is every reason a stored password hash does not open. The text
// never says which part failed.
var errStoredHash = errors.New("brain: stored password hash")

// passwordSealer stores a password hash through the master keyring as
// "<key id>:<base64 nonce>:<base64 ciphertext>". It satisfies
// identity.PasswordSealer by structure.
type passwordSealer struct {
	keys gateway.Keyring
}

func (s passwordSealer) Ready() bool { return s.keys.Ready() }

func (s passwordSealer) Seal(ref, hash string) (string, error) {
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

func (s passwordSealer) Open(ref, stored string) (string, error) {
	id, rest, ok := strings.Cut(stored, ":")
	nonce, ciphertext, ok2 := strings.Cut(rest, ":")
	if !ok || !ok2 || strings.Contains(ciphertext, ":") {
		return "", errStoredHash
	}
	rawNonce, err := base64.StdEncoding.DecodeString(nonce)
	if err != nil {
		return "", errStoredHash
	}
	rawCiphertext, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", errStoredHash
	}
	hash, err := s.keys.Open(ref, gateway.Sealed{Ciphertext: rawCiphertext, Nonce: rawNonce, KeyID: id})
	if err != nil {
		return "", errStoredHash
	}
	return hash, nil
}
