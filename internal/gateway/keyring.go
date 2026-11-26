package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
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

// ProviderTest asks the worker to test a saved account with its saved key
// for the person whose session hashes to actorTokenHash. Brain never sees
// the key. Any failure, a refusal included, is app.ErrKeysUnavailable.
func (w WorkerSealer) ProviderTest(ctx context.Context, accountID string, actorTokenHash []byte) (app.TestResult, error) {
	if w.Keys == nil {
		return app.TestResult{}, app.ErrKeysUnavailable
	}
	out, err := w.Keys.ProviderTest(ctx, accountID, actorTokenHash)
	if err != nil {
		return app.TestResult{}, app.ErrKeysUnavailable
	}
	result := app.TestResult{OK: out.OK, Error: out.Error}
	// An empty list stays empty and null stays nil: brain clears the seen
	// models for the first only.
	if out.Models != nil {
		result.Models = make([]domain.SeenModel, 0, len(out.Models))
		for _, raw := range out.Models {
			var m domain.SeenModel
			if err := json.Unmarshal(raw, &m); err != nil {
				return app.TestResult{}, app.ErrKeysUnavailable
			}
			result.Models = append(result.Models, m)
		}
	}
	return result, nil
}

// KeysVerify holds the worker audience keys the key service verifies.
type KeysVerify = keyservice.Verify

// KeysPasswords is what the key service's password-verify needs from
// identity; KeysPasswordAccount is the person one check is for.
type (
	KeysPasswords       = keyservice.Passwords
	KeysPasswordAccount = keyservice.PasswordAccount
)

// KeysEmails is what the key service's email route needs from identity;
// KeysEmailRow is one stored address it reads.
type (
	KeysEmails   = keyservice.Emails
	KeysEmailRow = keyservice.EmailRow
)

var (
	// ErrKeysHashBusy is a password check with every hash slot in use.
	ErrKeysHashBusy = keyservice.ErrHashBusy
	// ErrKeysStoredPassword is a stored password that does not open.
	ErrKeysStoredPassword = keyservice.ErrStoredPassword
)

// Wrapped is a new data key and its wrap under the current master.
type Wrapped = domain.Wrapped

// KindPassword is the kind a stored password's data key is wrapped for.
const KindPassword = domain.KindPassword

// KindEmail is the kind a stored email address's data key is wrapped for.
const KindEmail = domain.KindEmail

// ValidKeyID reports whether id can name a master key.
func ValidKeyID(id string) bool { return domain.ValidKeyID(id) }

// SealWithDataKey seals plaintext for ref under w's data key.
func SealWithDataKey(w Wrapped, ref, plaintext string) (Sealed, error) {
	return domain.SealWithDataKey(w, ref, plaintext)
}

// OpenData opens a payload sealed for ref under dataKey.
func OpenData(dataKey []byte, ref string, ciphertext, nonce []byte) (string, error) {
	return domain.OpenData(dataKey, ref, ciphertext, nonce)
}

// SealedText is s as one five-field text value, the stored form of a
// password: key id, wrap nonce, wrapped key, payload nonce and payload.
func SealedText(s Sealed) string { return domain.SealedText(s) }

// ParseSealedText reads a five-field value; any other shape is an error.
func ParseSealedText(stored string) (Sealed, error) { return domain.ParseSealedText(stored) }

// ActorOfTokenHash names the person whose unexpired session or API token
// hashes to hash. The worker wires identity's owner-pool lookup.
type ActorOfTokenHash func(ctx context.Context, hash []byte) (user string, ok bool, err error)

// KeysHandler serves the worker's /internal/keys/* routes with the
// in-process ring, reading wrap columns and writing audit rows as owner.
// providers is the worker's provider use case on the owner pool, wired with
// the same Allow, Teams and LocalPlan rules brain uses; actor resolves the
// person behind a provider-test; passwords serves password-verify and
// emails the email route.
func KeysHandler(owner *pgxpool.Pool, ring Keyring, verify KeysVerify, providers app.Providers, actor ActorOfTokenHash, passwords KeysPasswords, emails KeysEmails, log *slog.Logger) (http.Handler, error) {
	if actor == nil {
		return nil, fmt.Errorf("gateway: provider test needs an actor lookup")
	}
	secrets := DBSecrets{Pool: owner, Keys: ring}
	test := func(ctx context.Context, accountID string, hash []byte, allowed func() error) (workerkeys.ProviderTestResponse, error) {
		user, ok, err := actor(ctx, hash)
		if err != nil || !ok {
			return workerkeys.ProviderTestResponse{}, app.ErrNotFound
		}
		result, err := providers.ProbeSaved(ctx, user, accountID, allowed, secrets.Get)
		if err != nil {
			return workerkeys.ProviderTestResponse{}, err
		}
		out := workerkeys.ProviderTestResponse{OK: result.OK, Error: result.Error}
		if result.Models != nil {
			out.Models = make([]json.RawMessage, 0, len(result.Models))
		}
		for _, m := range result.Models {
			raw, err := json.Marshal(m)
			if err != nil {
				return workerkeys.ProviderTestResponse{}, err
			}
			out.Models = append(out.Models, raw)
		}
		return out, nil
	}
	return keyservice.New(owner, ring, verify, test, passwords, emails, log)
}

// WorkerProviders is the worker's provider use case for provider-test: the
// owner pool and the adapters' probes, with no sealer, since the worker
// only opens saved keys. The caller sets Allow, Teams,
// MemberProvidersAllowed and LocalPlan as brain does.
func WorkerProviders(owner *pgxpool.Pool, log *slog.Logger) app.Providers {
	p := NewProviders(owner, nil, log)
	p.Limiter = nil
	p.SealTest = nil
	return p
}
