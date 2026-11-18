package brainops

import (
	"encoding/base64"
	"errors"
	"strings"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
)

// NewEmailSealer returns an EmailSealer backed by keys.
func NewEmailSealer(keys gateway.Keyring) identity.EmailSealer {
	return emailSealer{keys: keys}
}

// errEmptyEmail means the address to seal is blank. The text never carries it.
var errEmptyEmail = errors.New("brain: email address")

// emailSealer stores a person's email address through the master keyring as
// "<key id>:<base64 nonce>:<base64 ciphertext>", bound to the address's index
// hash. It satisfies identity.EmailSealer by structure.
type emailSealer struct {
	keys gateway.Keyring
}

func (s emailSealer) Ready() bool { return s.keys.Ready() }

func (s emailSealer) Seal(address string) (string, []byte, string, error) {
	if !s.keys.Ready() {
		return "", nil, "", identity.ErrNoEmailSealer
	}
	a := strings.TrimSpace(address)
	if a == "" {
		return "", nil, "", errEmptyEmail
	}
	keyID, hash, err := s.keys.IndexMAC([]byte(identity.NormalEmail(a)))
	if err != nil {
		return "", nil, "", err
	}
	sealed, err := s.keys.Seal(identity.EmailRef(hash), a)
	if err != nil {
		return "", nil, "", err
	}
	return strings.Join([]string{
		sealed.KeyID,
		base64.StdEncoding.EncodeToString(sealed.Nonce),
		base64.StdEncoding.EncodeToString(sealed.Ciphertext),
	}, ":"), hash, keyID, nil
}

func (s emailSealer) Open(stored string, hash []byte) (string, error) {
	id, rest, ok := strings.Cut(stored, ":")
	nonce, ciphertext, ok2 := strings.Cut(rest, ":")
	if !ok || !ok2 || strings.Contains(ciphertext, ":") || len(hash) == 0 {
		return "", identity.ErrStoredEmail
	}
	rawNonce, err := base64.StdEncoding.DecodeString(nonce)
	if err != nil {
		return "", identity.ErrStoredEmail
	}
	rawCiphertext, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", identity.ErrStoredEmail
	}
	address, err := s.keys.Open(identity.EmailRef(hash), gateway.Sealed{Ciphertext: rawCiphertext, Nonce: rawNonce, KeyID: id})
	if err != nil {
		return "", identity.ErrStoredEmail
	}
	return address, nil
}

func (s emailSealer) IndexHashes(address string) [][]byte {
	return s.keys.IndexMACs([]byte(identity.NormalEmail(address)))
}
