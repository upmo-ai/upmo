package gateway

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway/adapters/keyservice"
	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/gateway/app"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
	workerkeys "github.com/upmo-ai/upmo/internal/platform/keys"
	"github.com/upmo-ai/upmo/internal/platform/secretchain"
)

// Keyring holds the master keys that seal the provider keys people add.
type Keyring = domain.Keyring

// Sealed is a credential as stored: ciphertext, its nonce and the key id.
type Sealed = domain.Sealed

// The configuration names. The current key is UPMO_MASTER_KEY, base64 of
// exactly 32 bytes, named by UPMO_MASTER_KEY_ID ("k1" when unset). An older
// key that still opens credentials sealed with it is
// UPMO_MASTER_KEY_PREVIOUS_<id>, one variable per key. To rotate, set the
// new key as current under a new id and keep the old one as previous.
const (
	EnvMasterKey         = "UPMO_MASTER_KEY"
	EnvMasterKeyID       = "UPMO_MASTER_KEY_ID"
	EnvMasterKeyPrevious = "UPMO_MASTER_KEY_PREVIOUS_"
	defaultMasterKeyID   = "k1"
)

// KeyringFromEnv builds the ring from configuration. With no UPMO_MASTER_KEY
// it still returns the zero ring and no error; process startup guards require
// the key before brain or gateway starts. A set but invalid key is an error
// naming the variable, never the value.
func KeyringFromEnv(lookup func(string) (string, bool), environ []string) (Keyring, error) {
	current, _ := lookup(EnvMasterKey)
	if strings.TrimSpace(current) == "" {
		return Keyring{}, nil
	}
	id := defaultMasterKeyID
	if v, ok := lookup(EnvMasterKeyID); ok && strings.TrimSpace(v) != "" {
		id = strings.TrimSpace(v)
	}
	if !domain.ValidKeyID(id) {
		return Keyring{}, fmt.Errorf("gateway: %s must be 1 to 32 letters, digits or underscores", EnvMasterKeyID)
	}
	keys := map[string][]byte{}
	raw, err := decodeMasterKey(current)
	if err != nil {
		return Keyring{}, fmt.Errorf("gateway: %s must be base64 of exactly %d bytes", EnvMasterKey, domain.MasterKeyLen)
	}
	keys[id] = raw
	for _, entry := range environ {
		name, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		prevID, ok := strings.CutPrefix(name, EnvMasterKeyPrevious)
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		if !domain.ValidKeyID(prevID) {
			return Keyring{}, fmt.Errorf("gateway: %s%s: the key id must be 1 to 32 letters, digits or underscores", EnvMasterKeyPrevious, "<id>")
		}
		if prevID == id {
			return Keyring{}, fmt.Errorf("gateway: %s%s repeats the current key id", EnvMasterKeyPrevious, prevID)
		}
		prev, err := decodeMasterKey(value)
		if err != nil {
			return Keyring{}, fmt.Errorf("gateway: %s%s must be base64 of exactly %d bytes", EnvMasterKeyPrevious, prevID, domain.MasterKeyLen)
		}
		keys[prevID] = prev
	}
	ring, err := domain.NewKeyring(id, keys)
	if err != nil {
		return Keyring{}, fmt.Errorf("gateway: master keys")
	}
	return ring, nil
}

func decodeMasterKey(v string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v))
	if err != nil {
		return nil, err
	}
	if len(raw) != domain.MasterKeyLen {
		return nil, fmt.Errorf("length")
	}
	return raw, nil
}

// DataKeys gets the data key of one stored credential from the worker's
// key service, as gateway. keys.Client is the one implementation.
type DataKeys interface {
	UnwrapCredential(ctx context.Context, ref string) ([]byte, error)
}

// DBSecrets reads the keys people add from gateway.credentials. It is one
// link of the secret chain. With Unwrap set (gateway) it reads the payload,
// asks the worker for its data key and opens it here; with only Keys set
// (the worker) it opens the row in process and Pool must be the owner,
// since only the owner reads the wrap. A missing row, a ref it was not
// sealed for, a key it cannot open, a worker that does not answer and a
// database error are all ErrMissing; none returns partial plaintext or says
// which it was. No data key outlives one Get.
type DBSecrets struct {
	Pool   *pgxpool.Pool
	Keys   Keyring
	Unwrap DataKeys
}

func (d DBSecrets) Get(ref string) (string, error) {
	if !secretchain.ValidRef(ref) {
		return "", fmt.Errorf("gateway: credential ref")
	}
	if d.Pool == nil || (d.Unwrap == nil && !d.Keys.Ready()) {
		return "", ErrMissing
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store := postgres.Store{Pool: d.Pool}
	var plain string
	if d.Unwrap != nil {
		sealed, err := store.SealedCredential(ctx, ref)
		if err != nil {
			return "", ErrMissing
		}
		dataKey, err := d.Unwrap.UnwrapCredential(ctx, ref)
		if err != nil {
			return "", ErrMissing
		}
		plain, err = domain.OpenData(dataKey, ref, sealed.Ciphertext, sealed.Nonce)
		clear(dataKey)
		if err != nil {
			return "", ErrMissing
		}
	} else {
		sealed, err := store.WrappedCredential(ctx, ref)
		if err != nil {
			return "", ErrMissing
		}
		if plain, err = d.Keys.OpenCredential(ref, sealed); err != nil {
			return "", ErrMissing
		}
	}
	if plain == "" {
		return "", ErrMissing
	}
	return plain, nil
}

// WorkerSealer seals the keys a person adds for brain: the worker wraps a
// new data key, brain seals the payload with it here and wipes it. Brain
// holds no master key for credentials.
type WorkerSealer struct {
	Keys *workerkeys.Client
}

// Ready reports whether a key service is set up.
func (w WorkerSealer) Ready() bool { return w.Keys != nil }

func (w WorkerSealer) SealCredential(ctx context.Context, ref, plaintext string) (Sealed, error) {
	if w.Keys == nil {
		return Sealed{}, domain.ErrNoMasterKey
	}
	made, err := w.Keys.Wrap(ctx, domain.KindCredential, ref)
	if err != nil {
		return Sealed{}, app.ErrKeysUnavailable
	}
	defer clear(made.DataKey)
	if !domain.ValidKeyID(made.KeyID) || len(made.WrapNonce) != 12 || len(made.WrappedKey) == 0 {
		return Sealed{}, app.ErrKeysUnavailable
	}
	return domain.SealWithDataKey(domain.Wrapped{
		KeyID: made.KeyID, DataKey: made.DataKey, WrapNonce: made.WrapNonce, WrappedKey: made.WrappedKey,
	}, ref, plaintext)
}

// KeysVerify holds the worker audience keys the key service verifies.
type KeysVerify = keyservice.Verify

// KeysHandler serves the worker's /internal/keys/* routes with the
// in-process ring, reading wrap columns and writing audit rows as owner.
func KeysHandler(owner *pgxpool.Pool, ring Keyring, verify KeysVerify, log *slog.Logger) (http.Handler, error) {
	return keyservice.New(owner, ring, verify, log)
}
