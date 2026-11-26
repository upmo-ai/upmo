package app

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
)

// ErrNoEmailSealer means no master key is wired, so no email address can be stored.
var ErrNoEmailSealer = errors.New("identity: email storage needs a master key")

// ErrStoredEmail is every reason a stored address does not open: not in the sealed
// format, an unknown key, a wrong key or altered bytes. The text never says which
// and never carries the address.
var ErrStoredEmail = errors.New("identity: stored email address")

// EmailSealer seals a person's email address for storage and computes the keyed
// index hashes used to find it. Brain wires the worker's key service; the
// worker and the upmo commands wire the master keyring.
type EmailSealer interface {
	Ready() bool
	// Seal returns the stored form of address (trimmed, casing kept), its index hash
	// under the current key and that key's id. The seal is bound to the hash.
	Seal(ctx context.Context, address string) (stored string, hash []byte, hashKeyID string, err error)
	// Open returns the address of stored, which was sealed for hash.
	Open(stored string, hash []byte) (string, error)
	// IndexHashes returns the index hash of address under every loaded key, the
	// current key first. Lookups match any of them, so rows sealed before a key
	// rotation are still found.
	IndexHashes(ctx context.Context, address string) ([][]byte, error)
}

// EmailRef is the seal ref (associated data) of an address with index hash h,
// so a sealed address cannot be moved to another row.
func EmailRef(hash []byte) string { return "identity:email:" + hex.EncodeToString(hash) }

// NormalEmail is the form the index hash is computed over: trimmed and lower case.
func NormalEmail(address string) string { return strings.ToLower(strings.TrimSpace(address)) }
