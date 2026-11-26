// Package sealer gives integration tests the test master key: the real
// email sealer over it, the real worker key service over it for passwords,
// and helpers that seed and inspect a stored password the way the worker
// does (five fields: key id, wrap nonce, wrapped key, payload nonce,
// payload). Tests that only plant or read a row use Seal and Open; the
// password use case always goes through Passwords.
package sealer

import (
	"encoding/base64"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/brainops"
	"github.com/upmo-ai/upmo/internal/platform/svcsig"
	"github.com/upmo-ai/upmo/internal/testutil/workerkeys"
)

// NewEmail returns the worker's in-process email sealer for the identity
// suites: the brainops sealer over the test master key, current id "k1". It
// opens a stored address itself and answers no email op, so a brain path
// that shows or mails an address uses WorkerEmail.
func NewEmail(t *testing.T) brainops.RingEmails {
	t.Helper()
	return brainops.NewEmailSealer(Ring(t))
}

// WorkerEmail starts the real worker key service over owner and the test
// master key, and returns brain's email sealer over it: every address brain
// shows or mails is opened by that worker.
func WorkerEmail(t *testing.T, owner *pgxpool.Pool) brainops.WorkerEmails {
	t.Helper()
	return WorkerEmailOn(t, workerkeys.Start(t, owner, Ring(t)))
}

// WorkerEmailOn is brain's email sealer over a running worker.
func WorkerEmailOn(t *testing.T, w *workerkeys.Worker) brainops.WorkerEmails {
	t.Helper()
	return brainops.WorkerEmails{Keys: w.Client(t, svcsig.AudienceWorkerBrain)}
}

// Hashes returns the index hashes NewEmail finds address under, current key
// first.
func Hashes(t *testing.T, address string) [][]byte {
	t.Helper()
	hashes, err := NewEmail(t).IndexHashes(t.Context(), address)
	if err != nil {
		t.Fatal(err)
	}
	return hashes
}

// Passwords starts the real worker key service over owner and the test
// master key, so it finds people by the index hashes NewEmail writes, and
// returns brain's password port over it.
func Passwords(t *testing.T, owner *pgxpool.Pool) identity.PasswordKeys {
	t.Helper()
	return PasswordsOn(t, workerkeys.Start(t, owner, Ring(t)))
}

// PasswordsOn is brain's password port over a running worker.
func PasswordsOn(t *testing.T, w *workerkeys.Worker) identity.PasswordKeys {
	t.Helper()
	return brainops.WorkerPasswords{Keys: w.Client(t, svcsig.AudienceWorkerBrain)}
}

// Ring builds the test master keyring over 32 bytes of key material,
// current id "k1".
func Ring(t *testing.T) gateway.Keyring {
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

// Seal returns hash stored for user as the worker stores one: a new data
// key wrapped for v1:password:<uuid> under the test master, and the payload
// sealed for identity:password:<uuid>.
func Seal(t *testing.T, user identity.UserID, hash string) string {
	t.Helper()
	ring := Ring(t)
	w, err := ring.WrapNew(gateway.KindPassword, user.String())
	if err != nil {
		t.Fatal(err)
	}
	defer clear(w.DataKey)
	sealed, err := gateway.SealWithDataKey(w, identity.PasswordRef(user), hash)
	if err != nil {
		t.Fatal(err)
	}
	return gateway.SealedText(sealed)
}

// Open returns the hash stored for user, or an error when it does not open
// under the test master.
func Open(t *testing.T, user identity.UserID, stored string) (string, error) {
	t.Helper()
	s, err := gateway.ParseSealedText(stored)
	if err != nil {
		return "", err
	}
	dataKey, err := Ring(t).Unwrap(gateway.KindPassword, user.String(), s.KeyID, s.WrapNonce, s.WrappedKey)
	if err != nil {
		return "", err
	}
	defer clear(dataKey)
	return gateway.OpenData(dataKey, identity.PasswordRef(user), s.Ciphertext, s.Nonce)
}

func bytes32(b byte) []byte {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = b
	}
	return raw
}
