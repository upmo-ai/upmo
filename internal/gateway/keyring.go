package gateway

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/gateway/domain"
)

// Keyring holds the master keys that seal the provider keys people add.
type Keyring = domain.Keyring

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
// it returns the zero ring and no error: the process runs, and saving a key
// is refused. A key that is set but wrong is an error naming the variable,
// never the value.
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

// DBSecrets reads the keys people add from gateway.credentials and opens them
// with the master keys. It is one link of the secret chain. A missing row, a
// ref it was not sealed for, a key it cannot open and a database error are all
// ErrMissing; none returns partial plaintext or says which it was.
type DBSecrets struct {
	Pool *pgxpool.Pool
	Keys Keyring
}

func (d DBSecrets) Get(ref string) (string, error) {
	if !credentialRef.MatchString(ref) {
		return "", fmt.Errorf("gateway: credential ref")
	}
	if d.Pool == nil || !d.Keys.Ready() {
		return "", ErrMissing
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sealed, err := postgres.Store{Pool: d.Pool}.SealedCredential(ctx, ref)
	if err != nil {
		return "", ErrMissing
	}
	plain, err := d.Keys.Open(ref, sealed)
	if err != nil || plain == "" {
		return "", ErrMissing
	}
	return plain, nil
}
