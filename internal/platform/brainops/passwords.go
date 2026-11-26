package brainops

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
	"github.com/upmo-ai/upmo/internal/platform/keys"
)

// errStoredHash is every reason a stored password hash does not open. The
// text never says which part failed.
var errStoredHash = errors.New("brain: stored password hash")

// WorkerPasswords is brain's side of a password over the worker's key
// service: a new hash is sealed here under a data key the worker wraps for
// the person, and a password is checked by the worker, which alone opens
// the stored hash. It satisfies identity.PasswordKeys by structure.
type WorkerPasswords struct {
	Keys *keys.Client
}

// Seal wraps a new data key for user and seals hash under it, as the
// five-field text. A worker that cannot wrap is identity.ErrBusy.
func (w WorkerPasswords) Seal(ctx context.Context, user identity.UserID, hash string) (string, error) {
	if w.Keys == nil {
		return "", identity.ErrNoSealer
	}
	made, err := w.Keys.Wrap(ctx, keys.KindPassword, user.String())
	if err != nil {
		return "", identity.ErrBusy
	}
	defer clear(made.DataKey)
	if !gateway.ValidKeyID(made.KeyID) || len(made.WrapNonce) != 12 || len(made.WrappedKey) == 0 {
		return "", identity.ErrBusy
	}
	return sealPasswordWith(gateway.Wrapped{
		KeyID: made.KeyID, DataKey: made.DataKey, WrapNonce: made.WrapNonce, WrappedKey: made.WrappedKey,
	}, user, hash)
}

// Verify asks the worker to check password for the person behind email.
func (w WorkerPasswords) Verify(ctx context.Context, email, password string) (bool, bool, error) {
	if w.Keys == nil {
		return false, false, identity.ErrNoSealer
	}
	return passwordAnswer(w.Keys.VerifyPassword(ctx, email, password))
}

// VerifySession asks the worker to check password for the signed-in person
// behind tokenHash.
func (w WorkerPasswords) VerifySession(ctx context.Context, tokenHash []byte, password string) (bool, bool, error) {
	if w.Keys == nil {
		return false, false, identity.ErrNoSealer
	}
	return passwordAnswer(w.Keys.VerifyPasswordForSession(ctx, tokenHash, password))
}

// passwordAnswer maps the worker's refusals: its limit is identity's, and
// anything else that is not an answer means try again, never a match.
func passwordAnswer(match, rehash bool, err error) (bool, bool, error) {
	switch {
	case err == nil:
		return match, rehash, nil
	case errors.Is(err, keys.ErrLimited):
		return false, false, identity.ErrLimited
	default:
		return false, false, identity.ErrBusy
	}
}

// sealPasswordWith seals hash for user under w's data key as the stored
// five-field text: the wrap is for v1:password:<uuid>, the payload for
// identity:password:<uuid>.
func sealPasswordWith(w gateway.Wrapped, user identity.UserID, hash string) (string, error) {
	sealed, err := gateway.SealWithDataKey(w, identity.PasswordRef(user), hash)
	if err != nil {
		return "", err
	}
	return gateway.SealedText(sealed), nil
}

// sealPassword seals hash for user under a new data key wrapped by ring's
// current master, in process. Only the worker and upmo keys hold a ring.
func sealPassword(ring gateway.Keyring, user identity.UserID, hash string) (string, error) {
	w, err := ring.WrapNew(gateway.KindPassword, user.String())
	if err != nil {
		return "", err
	}
	defer clear(w.DataKey)
	return sealPasswordWith(w, user, hash)
}

// openPassword opens user's five-field stored password with ring. Any other
// shape, an unknown key or altered bytes is errStoredHash.
func openPassword(ring gateway.Keyring, user identity.UserID, stored string) (string, error) {
	s, err := gateway.ParseSealedText(stored)
	if err != nil {
		return "", errStoredHash
	}
	dataKey, err := ring.Unwrap(gateway.KindPassword, user.String(), s.KeyID, s.WrapNonce, s.WrappedKey)
	if err != nil {
		return "", errStoredHash
	}
	defer clear(dataKey)
	hash, err := gateway.OpenData(dataKey, identity.PasswordRef(user), s.Ciphertext, s.Nonce)
	if err != nil {
		return "", errStoredHash
	}
	return hash, nil
}

// passwordCheck is the worker's password-verify over the owner pool and
// the in-process ring. It satisfies gateway.KeysPasswords.
type passwordCheck struct {
	owner  *pgxpool.Pool
	ring   gateway.Keyring
	emails RingEmails
}

// ByAddress keys the window by the address's current index MAC, and finds
// the person under any loaded index key, disabled or not.
func (c passwordCheck) ByAddress(ctx context.Context, email string) (gateway.KeysPasswordAccount, error) {
	macs := c.ring.IndexMACs([]byte(identity.NormalEmail(email)))
	if len(macs) == 0 {
		return gateway.KeysPasswordAccount{}, gateway.ErrNoMasterKey
	}
	account := gateway.KeysPasswordAccount{Subject: macs[0]}
	stored, found, err := identity.StoredPasswordByAddress(ctx, c.owner, macs)
	if err != nil {
		return gateway.KeysPasswordAccount{}, err
	}
	if found {
		account.User, account.Stored = stored.User, stored.Stored
		account.Usable = !stored.Disabled && stored.Stored != ""
	}
	return account, nil
}

// BySession finds the enabled person behind an unexpired session and keys
// the window by their address's current index MAC, as a sign-in would.
func (c passwordCheck) BySession(ctx context.Context, tokenHash []byte) (gateway.KeysPasswordAccount, bool, error) {
	stored, email, emailHash, ok, err := identity.StoredPasswordOfSession(ctx, c.owner, tokenHash)
	if err != nil || !ok {
		return gateway.KeysPasswordAccount{}, false, err
	}
	address, err := c.emails.Open(email, emailHash)
	if err != nil {
		return gateway.KeysPasswordAccount{}, false, err
	}
	_, subject, err := c.ring.IndexMAC([]byte(identity.NormalEmail(address)))
	if err != nil {
		return gateway.KeysPasswordAccount{}, false, err
	}
	return gateway.KeysPasswordAccount{
		Subject: subject, User: stored.User, Stored: stored.Stored, Usable: stored.Stored != "",
	}, true, nil
}

func (c passwordCheck) Take(ctx context.Context, subject []byte) (int, error) {
	return identity.TakePasswordVerify(ctx, c.owner, subject)
}

// Check spends exactly one hash: the stored one for a usable account, a
// dummy one otherwise, which answers no match.
func (c passwordCheck) Check(_ context.Context, account gateway.KeysPasswordAccount, password string) (bool, bool, error) {
	if !account.Usable {
		if err := identity.DummyPasswordHash(); err != nil {
			return false, false, gateway.ErrKeysHashBusy
		}
		return false, false, nil
	}
	user, err := identity.ParseUserID(account.User)
	if err != nil {
		return false, false, gateway.ErrKeysStoredPassword
	}
	hash, err := openPassword(c.ring, user, account.Stored)
	if err != nil {
		return false, false, gateway.ErrKeysStoredPassword
	}
	match, rehash, err := identity.VerifyPassword(password, hash)
	if errors.Is(err, identity.ErrBusy) {
		return false, false, gateway.ErrKeysHashBusy
	}
	if err != nil {
		return false, false, gateway.ErrKeysStoredPassword
	}
	return match, rehash, nil
}

// PasswordWindowSweepArgs is the River payload that deletes old password
// check windows.
type PasswordWindowSweepArgs struct{}

func (PasswordWindowSweepArgs) Kind() string { return "password_verify_window_sweep" }

type passwordWindowSweep struct {
	river.WorkerDefaults[PasswordWindowSweepArgs]
	owner *pgxpool.Pool
}

func (w *passwordWindowSweep) Work(ctx context.Context, _ *river.Job[PasswordWindowSweepArgs]) error {
	_, err := identity.SweepPasswordVerify(ctx, w.owner)
	return err
}

// AddPasswordWindowSweep registers the worker's sweep of the password check
// windows older than 10 minutes, on the owner pool, which bounds the rows
// unknown addresses leave.
func AddPasswordWindowSweep(workers *river.Workers, owner *pgxpool.Pool) {
	river.AddWorker(workers, &passwordWindowSweep{owner: owner})
}

// PasswordWindowSweepPeriodic runs the sweep at startup and then every 10
// minutes.
func PasswordWindowSweepPeriodic() *river.PeriodicJob {
	return river.NewPeriodicJob(
		river.PeriodicInterval(10*time.Minute),
		func() (river.JobArgs, *river.InsertOpts) { return PasswordWindowSweepArgs{}, nil },
		&river.PeriodicJobOpts{RunOnStart: true},
	)
}
