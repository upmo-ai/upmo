package brainops

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
)

// RingEmails is the in-process EmailSealer, which also opens a stored
// address with the ring.
type RingEmails interface {
	identity.EmailSealer
	// Open returns the address of stored, which was sealed for hash.
	Open(stored string, hash []byte) (string, error)
}

// NewEmailSealer returns the in-process EmailSealer backed by keys. Only the
// worker and the upmo commands hold a ring; brain uses WorkerEmails.
func NewEmailSealer(keys gateway.Keyring) RingEmails {
	return emailSealer{keys: keys}
}

// errNoEmailOp is an email op asked of the ring sealer: the worker opens an
// address for a proof only on its key route, never for an in-process caller.
var errNoEmailOp = errors.New("brain: the ring sealer answers no email op")

// errEmptyEmail means the address to seal is blank or longer than the
// worker takes. The text never carries it.
var errEmptyEmail = errors.New("brain: email address")

// emailSealer stores a person's email address as the five-field text: a
// data key wrapped under the master for v1:email:<hex MAC>, and the address
// sealed under it for identity:email:<hex MAC>. It satisfies
// identity.EmailSealer by structure.
type emailSealer struct {
	keys gateway.Keyring
}

func (s emailSealer) Ready() bool { return s.keys.Ready() }

func (s emailSealer) Seal(_ context.Context, address string) (string, []byte, string, error) {
	if !s.keys.Ready() {
		return "", nil, "", identity.ErrNoEmailSealer
	}
	a := strings.TrimSpace(address)
	if a == "" || len(a) > maxEmailBytes {
		return "", nil, "", errEmptyEmail
	}
	keyID, hash, err := s.keys.IndexMAC([]byte(identity.NormalEmail(a)))
	if err != nil {
		return "", nil, "", err
	}
	w, err := s.keys.WrapNew(gateway.KindEmail, hex.EncodeToString(hash))
	if err != nil {
		return "", nil, "", err
	}
	defer clear(w.DataKey)
	stored, err := sealEmailWith(w, hash, a)
	if err != nil {
		return "", nil, "", err
	}
	return stored, hash, keyID, nil
}

func (s emailSealer) Open(stored string, hash []byte) (string, error) {
	return openEmail(s.keys, stored, hash)
}

func (emailSealer) OwnEmail(context.Context, []byte) (string, error)  { return "", errNoEmailOp }
func (emailSealer) LinkEmail(context.Context, []byte) (string, error) { return "", errNoEmailOp }
func (emailSealer) MemberEmail(context.Context, []byte, identity.UserID) (string, error) {
	return "", errNoEmailOp
}
func (emailSealer) EmailDirectory(context.Context, []byte) (map[identity.UserID]string, error) {
	return nil, errNoEmailOp
}

func (s emailSealer) IndexHashes(_ context.Context, address string) ([][]byte, error) {
	macs := s.keys.IndexMACs([]byte(identity.NormalEmail(address)))
	if len(macs) == 0 {
		return nil, identity.ErrNoEmailSealer
	}
	return macs, nil
}

// sealEmailWith seals address under w's data key as the stored five-field
// text, for the index MAC hash.
func sealEmailWith(w gateway.Wrapped, hash []byte, address string) (string, error) {
	sealed, err := gateway.SealWithDataKey(w, identity.EmailRef(hash), address)
	if err != nil {
		return "", err
	}
	return gateway.SealedText(sealed), nil
}

// openEmail opens a five-field stored address sealed for hash with ring. Any
// other shape, an unknown key, another row's hash or altered bytes is
// identity.ErrStoredEmail.
func openEmail(ring gateway.Keyring, stored string, hash []byte) (string, error) {
	if len(hash) == 0 {
		return "", identity.ErrStoredEmail
	}
	s, err := gateway.ParseSealedText(stored)
	if err != nil {
		return "", identity.ErrStoredEmail
	}
	dataKey, err := ring.Unwrap(gateway.KindEmail, hex.EncodeToString(hash), s.KeyID, s.WrapNonce, s.WrappedKey)
	if err != nil {
		return "", identity.ErrStoredEmail
	}
	defer clear(dataKey)
	address, err := gateway.OpenData(dataKey, identity.EmailRef(hash), s.Ciphertext, s.Nonce)
	if err != nil {
		return "", identity.ErrStoredEmail
	}
	return address, nil
}
