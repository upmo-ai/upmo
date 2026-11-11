package domain

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
)

// MasterKeyLen is the length of a master key in bytes (AES-256).
const MasterKeyLen = 32

var (
	// ErrNoMasterKey means no master key is configured, so nothing can be sealed.
	ErrNoMasterKey = errors.New("gateway: no master key")
	// ErrUnseal is every reason a sealed credential cannot be opened: no key
	// for its key id, a wrong key, altered bytes or a different ref. The text
	// never says which, and never carries key or credential bytes.
	ErrUnseal = errors.New("gateway: cannot open credential")
)

var keyID = regexp.MustCompile(`^[A-Za-z0-9_]{1,32}$`)

// ValidKeyID reports whether id can name a master key.
func ValidKeyID(id string) bool { return keyID.MatchString(id) }

// Sealed is a credential as stored: AES-256-GCM ciphertext, its nonce and the
// id of the master key that sealed it.
type Sealed struct {
	Ciphertext []byte
	Nonce      []byte
	KeyID      string
}

// Keyring holds the master keys by id. New credentials are sealed with the
// current key; any key in the ring can open the credentials it sealed, so a
// key can be rotated by adding the new one as current and keeping the old.
// The zero Keyring holds no keys.
type Keyring struct {
	current string
	keys    map[string]cipher.AEAD
}

// NewKeyring builds a ring from raw 32-byte keys. current must be one of them.
// The returned error names an id or a length, never key bytes. The raw key
// bytes are wiped before NewKeyring returns: each cipher keeps its own copy.
func NewKeyring(current string, keys map[string][]byte) (Keyring, error) {
	defer wipeKeys(keys)
	if !ValidKeyID(current) {
		return Keyring{}, fmt.Errorf("gateway: master key id %q", current)
	}
	ring := Keyring{current: current, keys: map[string]cipher.AEAD{}}
	for id, raw := range keys {
		if !ValidKeyID(id) {
			return Keyring{}, fmt.Errorf("gateway: master key id %q", id)
		}
		if len(raw) != MasterKeyLen {
			return Keyring{}, fmt.Errorf("gateway: master key %q is not %d bytes", id, MasterKeyLen)
		}
		block, err := aes.NewCipher(raw)
		if err != nil {
			return Keyring{}, fmt.Errorf("gateway: master key %q", id)
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return Keyring{}, fmt.Errorf("gateway: master key %q", id)
		}
		ring.keys[id] = aead
	}
	if ring.keys[current] == nil {
		return Keyring{}, fmt.Errorf("gateway: master key %q is not in the ring", current)
	}
	return ring, nil
}

// wipeKeys clears the raw key bytes a ring was built from, so the plain key
// does not outlive the ciphers that copied it.
func wipeKeys(keys map[string][]byte) {
	for _, raw := range keys {
		clear(raw)
	}
}

// Ready reports whether the ring can seal.
func (k Keyring) Ready() bool { return k.keys[k.current] != nil }

// Seal encrypts plaintext with the current key. The ref is bound as
// additional authenticated data, so a sealed value only opens under the ref
// it was sealed for.
func (k Keyring) Seal(ref, plaintext string) (Sealed, error) {
	aead := k.keys[k.current]
	if aead == nil {
		return Sealed{}, ErrNoMasterKey
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Sealed{}, fmt.Errorf("gateway: seal")
	}
	return Sealed{
		Ciphertext: aead.Seal(nil, nonce, []byte(plaintext), []byte(ref)),
		Nonce:      nonce,
		KeyID:      k.current,
	}, nil
}

// Open decrypts s for ref. Any failure is ErrUnseal and returns no bytes.
// The buffer the decryption wrote is wiped on the way out; what the caller
// keeps is its own copy as a string.
func (k Keyring) Open(ref string, s Sealed) (string, error) {
	aead := k.keys[s.KeyID]
	if aead == nil || len(s.Nonce) != aead.NonceSize() {
		return "", ErrUnseal
	}
	plain, err := aead.Open(nil, s.Nonce, s.Ciphertext, []byte(ref))
	if err != nil {
		return "", ErrUnseal
	}
	key := string(plain)
	clear(plain)
	return key, nil
}
