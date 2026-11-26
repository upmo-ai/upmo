package brainops

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/keys"
)

// maxEmailBytes is the longest address the worker computes an index MAC for.
const maxEmailBytes = 320

// WorkerEmails is brain's EmailSealer: the worker computes every index MAC
// and wraps every data key, and brain seals the address here under that key
// and wipes it. Ring only opens stored addresses, until the worker does.
// A worker that refuses is keys.ErrLimited (429) or keys.ErrUnavailable,
// each also identity.ErrBusy, so a sign-in says try again.
type WorkerEmails struct {
	Keys *keys.Client
	Ring gateway.Keyring
}

func (w WorkerEmails) Ready() bool { return w.Keys != nil && w.Ring.Ready() }

func (w WorkerEmails) Seal(ctx context.Context, address string) (string, []byte, string, error) {
	if w.Keys == nil {
		return "", nil, "", identity.ErrNoEmailSealer
	}
	a := strings.TrimSpace(address)
	if a == "" || len(a) > maxEmailBytes {
		return "", nil, "", errEmptyEmail
	}
	keyID, macs, err := w.Keys.IndexMAC(ctx, a, false)
	if err != nil {
		return "", nil, "", workerEmailErr(err)
	}
	if len(macs) != 1 || len(macs[0]) != 32 || !gateway.ValidKeyID(keyID) {
		return "", nil, "", workerEmailErr(keys.ErrUnavailable)
	}
	hash := macs[0]
	made, err := w.Keys.Wrap(ctx, keys.KindEmail, hex.EncodeToString(hash))
	if err != nil {
		return "", nil, "", workerEmailErr(err)
	}
	defer clear(made.DataKey)
	if !gateway.ValidKeyID(made.KeyID) || len(made.WrapNonce) != 12 || len(made.WrappedKey) == 0 {
		return "", nil, "", workerEmailErr(keys.ErrUnavailable)
	}
	stored, err := sealEmailWith(gateway.Wrapped{
		KeyID: made.KeyID, DataKey: made.DataKey, WrapNonce: made.WrapNonce, WrappedKey: made.WrappedKey,
	}, hash, a)
	if err != nil {
		return "", nil, "", err
	}
	return stored, hash, keyID, nil
}

func (w WorkerEmails) Open(stored string, hash []byte) (string, error) {
	return openEmail(w.Ring, stored, hash)
}

func (w WorkerEmails) IndexHashes(ctx context.Context, address string) ([][]byte, error) {
	if w.Keys == nil {
		return nil, identity.ErrNoEmailSealer
	}
	// shortcut: an address the worker refuses by length matches no row brain
	// could have sealed; rows the worker sealed over 320 bytes are not found
	// by address, as a sign-in never sent one.
	if len(address) == 0 || len(address) > maxEmailBytes {
		return [][]byte{}, nil
	}
	_, macs, err := w.Keys.IndexMAC(ctx, address, true)
	if err != nil {
		return nil, workerEmailErr(err)
	}
	if len(macs) == 0 {
		return nil, workerEmailErr(keys.ErrUnavailable)
	}
	for _, mac := range macs {
		if len(mac) != 32 {
			return nil, workerEmailErr(keys.ErrUnavailable)
		}
	}
	return macs, nil
}

// workerEmailErr keeps the worker's refusal for errors.Is and adds
// identity.ErrBusy, which a sign-in answers with try again.
func workerEmailErr(err error) error {
	if !errors.Is(err, keys.ErrLimited) {
		err = keys.ErrUnavailable
	}
	return fmt.Errorf("%w: %w", err, identity.ErrBusy)
}
