package domain

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
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

// Sealed is a value as stored: AES-256-GCM ciphertext, its nonce and the id
// of the master key behind it. A credential's payload is sealed under its own
// data key, and WrappedKey and WrapNonce hold that data key wrapped by the
// master key KeyID names; a password or email value leaves them empty.
type Sealed struct {
	Ciphertext []byte
	Nonce      []byte
	KeyID      string
	WrappedKey []byte
	WrapNonce  []byte
}

// KindCredential is the kind a credential's data key is wrapped for.
const KindCredential = "credential"

// DataKeyLen is the length of a data key in bytes (AES-256).
const DataKeyLen = 32

// Wrapped is a new data key and that key wrapped under the current master.
// The caller seals the payload with DataKey and then wipes it.
type Wrapped struct {
	KeyID      string
	DataKey    []byte
	WrapNonce  []byte
	WrappedKey []byte
}

// Keyring holds the master keys by id. New credentials are sealed with the
// current key; any key in the ring can open the credentials it sealed, so a
// key can be rotated by adding the new one as current and keeping the old.
// The zero Keyring holds no keys.
type Keyring struct {
	current string
	keys    map[string]cipher.AEAD
	macs    map[string][]byte
	digests map[string][]byte
}

// NewKeyring builds a ring from raw 32-byte keys. current must be one of them.
// The returned error names an id or a length, never key bytes. The raw key
// bytes are wiped before NewKeyring returns: each cipher keeps its own copy.
func NewKeyring(current string, keys map[string][]byte) (Keyring, error) {
	defer wipeKeys(keys)
	if !ValidKeyID(current) {
		return Keyring{}, fmt.Errorf("gateway: master key id %q", current)
	}
	ring := Keyring{current: current, keys: map[string]cipher.AEAD{}, macs: map[string][]byte{}, digests: map[string][]byte{}}
	for id, raw := range keys {
		if !ValidKeyID(id) {
			return Keyring{}, fmt.Errorf("gateway: master key id %q", id)
		}
		if len(raw) != MasterKeyLen {
			return Keyring{}, fmt.Errorf("gateway: master key %q is not %d bytes", id, MasterKeyLen)
		}
		mac, err := hkdf.Key(sha256.New, raw, []byte("upmo-email-index-v1"), "identity:email_hash", 32)
		if err != nil {
			return Keyring{}, fmt.Errorf("gateway: master key %q", id)
		}
		digest, err := hkdf.Key(sha256.New, raw, []byte("upmo-audit-ref-v1"), "keys:ref", 32)
		if err != nil {
			return Keyring{}, fmt.Errorf("gateway: master key %q", id)
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
		ring.macs[id] = mac
		ring.digests[id] = digest
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

// CurrentID returns the id of the key that seals new values; the zero ring
// returns "".
func (k Keyring) CurrentID() string { return k.current }

// Has reports whether a key with that id is loaded, so values it sealed can
// be opened for a reseal.
func (k Keyring) Has(id string) bool { return k.keys[id] != nil }

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

// IndexMAC returns HMAC-SHA256 of data under the current key's index key, and
// that key's id. The zero ring returns ErrNoMasterKey.
func (k Keyring) IndexMAC(data []byte) (keyID string, mac []byte, err error) {
	key := k.macs[k.current]
	if key == nil {
		return "", nil, ErrNoMasterKey
	}
	return k.current, indexMAC(key, data), nil
}

// IndexMACs returns HMAC-SHA256 of data under every loaded key's index key:
// the current key first, then the others in key-id order. The zero ring
// returns nil.
func (k Keyring) IndexMACs(data []byte) [][]byte {
	current, mac, err := k.IndexMAC(data)
	if err != nil {
		return nil
	}
	others := make([]string, 0, len(k.macs)-1)
	for id := range k.macs {
		if id != current {
			others = append(others, id)
		}
	}
	sort.Strings(others)
	macs := [][]byte{mac}
	for _, id := range others {
		macs = append(macs, indexMAC(k.macs[id], data))
	}
	return macs
}

// indexMAC returns HMAC-SHA256 of data under an index key.
func indexMAC(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

// wrapData is the additional data a data key is wrapped under, so a wrapped
// key only opens for the kind and ref it was made for.
func wrapData(kind, ref string) []byte { return []byte("v1:" + kind + ":" + ref) }

// WrapNew makes a 32-byte data key and wraps it under the current master
// for kind and ref.
func (k Keyring) WrapNew(kind, ref string) (Wrapped, error) {
	aead := k.keys[k.current]
	if aead == nil {
		return Wrapped{}, ErrNoMasterKey
	}
	dataKey := make([]byte, DataKeyLen)
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(dataKey); err != nil {
		return Wrapped{}, fmt.Errorf("gateway: seal")
	}
	if _, err := rand.Read(nonce); err != nil {
		clear(dataKey)
		return Wrapped{}, fmt.Errorf("gateway: seal")
	}
	return Wrapped{
		KeyID:      k.current,
		DataKey:    dataKey,
		WrapNonce:  nonce,
		WrappedKey: aead.Seal(nil, nonce, dataKey, wrapData(kind, ref)),
	}, nil
}

// Unwrap opens a wrapped data key for kind and ref under the master keyID
// names. Any failure is ErrUnseal and returns no bytes.
func (k Keyring) Unwrap(kind, ref, keyID string, wrapNonce, wrappedKey []byte) ([]byte, error) {
	aead := k.keys[keyID]
	if aead == nil || len(wrapNonce) != aead.NonceSize() {
		return nil, ErrUnseal
	}
	dataKey, err := aead.Open(nil, wrapNonce, wrappedKey, wrapData(kind, ref))
	if err != nil || len(dataKey) != DataKeyLen {
		clear(dataKey)
		return nil, ErrUnseal
	}
	return dataKey, nil
}

// SealCredential seals a credential for ref under a new data key wrapped by
// the current master, in process. The worker uses it; brain wraps through
// the worker instead. The context is unused.
func (k Keyring) SealCredential(_ context.Context, ref, plaintext string) (Sealed, error) {
	w, err := k.WrapNew(KindCredential, ref)
	if err != nil {
		return Sealed{}, err
	}
	defer clear(w.DataKey)
	return SealWithDataKey(w, ref, plaintext)
}

// OpenCredential opens a credential sealed by SealCredential, in process.
func (k Keyring) OpenCredential(ref string, s Sealed) (string, error) {
	dataKey, err := k.Unwrap(KindCredential, ref, s.KeyID, s.WrapNonce, s.WrappedKey)
	if err != nil {
		return "", err
	}
	defer clear(dataKey)
	return OpenData(dataKey, ref, s.Ciphertext, s.Nonce)
}

// AuditDigest is HMAC-SHA256 of raw under the current master's audit key, as
// 64 lowercase hex characters. It stands in for caller bytes an audit row
// must not hold. The zero ring returns "".
func (k Keyring) AuditDigest(raw []byte) string {
	key := k.digests[k.current]
	if key == nil {
		return ""
	}
	return hex.EncodeToString(indexMAC(key, raw))
}

// SealWithDataKey seals plaintext for ref under w's data key and returns the
// row to store: the payload and w's wrap. It does not wipe the data key.
func SealWithDataKey(w Wrapped, ref, plaintext string) (Sealed, error) {
	aead, err := dataCipher(w.DataKey)
	if err != nil {
		return Sealed{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Sealed{}, fmt.Errorf("gateway: seal")
	}
	return Sealed{
		Ciphertext: aead.Seal(nil, nonce, []byte(plaintext), []byte(ref)),
		Nonce:      nonce,
		KeyID:      w.KeyID,
		WrappedKey: w.WrappedKey,
		WrapNonce:  w.WrapNonce,
	}, nil
}

// OpenData opens a payload sealed for ref under dataKey. Any failure is
// ErrUnseal and returns no bytes; the decrypted buffer is wiped.
func OpenData(dataKey []byte, ref string, ciphertext, nonce []byte) (string, error) {
	aead, err := dataCipher(dataKey)
	if err != nil || len(nonce) != aead.NonceSize() {
		return "", ErrUnseal
	}
	plain, err := aead.Open(nil, nonce, ciphertext, []byte(ref))
	if err != nil {
		return "", ErrUnseal
	}
	out := string(plain)
	clear(plain)
	return out, nil
}

func dataCipher(dataKey []byte) (cipher.AEAD, error) {
	if len(dataKey) != DataKeyLen {
		return nil, ErrUnseal
	}
	block, err := aes.NewCipher(dataKey)
	if err != nil {
		return nil, ErrUnseal
	}
	return cipher.NewGCM(block)
}
