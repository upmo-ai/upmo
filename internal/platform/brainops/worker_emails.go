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

// WorkerEmails is brain's EmailSealer: the worker computes every index MAC,
// wraps every data key and opens every stored address, and brain seals the
// address here under the wrapped key and wipes it. Brain opens no stored
// address. A worker that refuses to seal or index is keys.ErrLimited (429)
// or keys.ErrUnavailable, each also identity.ErrBusy, so a sign-in says try
// again.
type WorkerEmails struct {
	Keys *keys.Client
}

func (w WorkerEmails) Ready() bool { return w.Keys != nil }

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

// OwnEmail asks the worker for the address of the person behind the session
// tokenHash. A refusal is identity.ErrNotSignedIn.
func (w WorkerEmails) OwnEmail(ctx context.Context, tokenHash []byte) (string, error) {
	return w.one(ctx, keys.EmailRequest{Op: keys.EmailOwn, ActorTokenHash: tokenHash}, identity.ErrNotSignedIn)
}

// LinkEmail asks the worker for the address of the person the sign-in link
// linkHash is for. A refusal is identity.ErrInvalidLink.
func (w WorkerEmails) LinkEmail(ctx context.Context, linkHash []byte) (string, error) {
	return w.one(ctx, keys.EmailRequest{Op: keys.EmailLink, LinkHash: linkHash}, identity.ErrInvalidLink)
}

// MemberEmail asks the worker for user's address, for the person behind the
// session tokenHash, who must manage user. A refusal is
// identity.ErrNoSuchMember.
func (w WorkerEmails) MemberEmail(ctx context.Context, tokenHash []byte, user identity.UserID) (string, error) {
	return w.one(ctx, keys.EmailRequest{Op: keys.EmailMember, ActorTokenHash: tokenHash, UserID: user.String()}, identity.ErrNoSuchMember)
}

// EmailDirectory asks the worker for every enabled person's address, for
// the joined person behind the session tokenHash. A refusal is
// identity.ErrNotSignedIn.
func (w WorkerEmails) EmailDirectory(ctx context.Context, tokenHash []byte) (map[identity.UserID]string, error) {
	if w.Keys == nil {
		return nil, identity.ErrNoEmailSealer
	}
	out, err := w.Keys.Email(ctx, keys.EmailRequest{Op: keys.EmailDirectory, ActorTokenHash: tokenHash})
	if err != nil {
		return nil, emailOpErr(err, identity.ErrNotSignedIn)
	}
	addresses := make(map[identity.UserID]string, len(out.Addresses))
	for _, a := range out.Addresses {
		id, err := identity.ParseUserID(a.ID)
		if err != nil || a.Address == "" {
			return nil, workerEmailErr(keys.ErrUnavailable)
		}
		addresses[id] = a.Address
	}
	if len(addresses) == 0 {
		return nil, workerEmailErr(keys.ErrUnavailable)
	}
	return addresses, nil
}

// one asks for a single address; refused is the caller's answer to a 403.
func (w WorkerEmails) one(ctx context.Context, in keys.EmailRequest, refused error) (string, error) {
	if w.Keys == nil {
		return "", identity.ErrNoEmailSealer
	}
	out, err := w.Keys.Email(ctx, in)
	if err != nil {
		return "", emailOpErr(err, refused)
	}
	if out.Address == "" {
		return "", workerEmailErr(keys.ErrUnavailable)
	}
	return out.Address, nil
}

// emailOpErr maps an email op's failure: a refusal is the caller's own
// error, a limit is also identity.ErrLimited so a page says slow down, and
// anything else is identity.ErrBusy. None is ever an empty address.
func emailOpErr(err, refused error) error {
	switch {
	case errors.Is(err, keys.ErrRefused):
		return refused
	case errors.Is(err, keys.ErrLimited):
		return fmt.Errorf("%w: %w: %w", keys.ErrLimited, identity.ErrLimited, identity.ErrBusy)
	default:
		return workerEmailErr(err)
	}
}
