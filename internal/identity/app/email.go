package app

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/upmo-ai/upmo/internal/identity/domain"
)

// ErrNoEmailSealer means no master key is wired, so no email address can be stored.
var ErrNoEmailSealer = errors.New("identity: email storage needs a master key")

// ErrStoredEmail is every reason a stored address does not open: not in the sealed
// format, an unknown key, a wrong key or altered bytes. The text never says which
// and never carries the address.
var ErrStoredEmail = errors.New("identity: stored email address")

// EmailSealer seals a person's email address for storage, computes the keyed
// index hashes used to find it, and asks for a stored address for one proof.
// Brain wires the worker's key service, which alone opens a stored address;
// the worker and the upmo commands wire the master keyring, which opens
// none through these ops.
type EmailSealer interface {
	Ready() bool
	// Seal returns the stored form of address (trimmed, casing kept), its index hash
	// under the current key and that key's id. The seal is bound to the hash.
	Seal(ctx context.Context, address string) (stored string, hash []byte, hashKeyID string, err error)
	// IndexHashes returns the index hash of address under every loaded key, the
	// current key first. Lookups match any of them, so rows sealed before a key
	// rotation are still found.
	IndexHashes(ctx context.Context, address string) ([][]byte, error)
	// OwnEmail is the address of the enabled person behind the unexpired
	// session tokenHash; any other hash is ErrNotSignedIn.
	OwnEmail(ctx context.Context, tokenHash []byte) (string, error)
	// LinkEmail is the address of the enabled person the unused, unexpired
	// sign-in link linkHash is for; any other hash is ErrInvalidLink.
	LinkEmail(ctx context.Context, linkHash []byte) (string, error)
	// MemberEmail is the address of the enabled person user, when the person
	// behind the session tokenHash may manage them; otherwise
	// domain.ErrNoSuchMember.
	MemberEmail(ctx context.Context, tokenHash []byte, user domain.UserID) (string, error)
	// EmailDirectory is every enabled person's address, for the joined person
	// behind the session tokenHash; any other hash is ErrNotSignedIn.
	EmailDirectory(ctx context.Context, tokenHash []byte) (map[domain.UserID]string, error)
}

// EmailRef is the seal ref (associated data) of an address with index hash h,
// so a sealed address cannot be moved to another row.
func EmailRef(hash []byte) string { return "identity:email:" + hex.EncodeToString(hash) }

// NormalEmail is the form the index hash is computed over: trimmed and lower case.
func NormalEmail(address string) string { return strings.ToLower(strings.TrimSpace(address)) }
