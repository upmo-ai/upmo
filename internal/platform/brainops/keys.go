package brainops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
)

// KeysHandler is the worker's key service: gateway's key routes on the
// owner pool, with password-verify opening stored passwords in process and
// provider-test resolving the person from their session or
// API token hash and applying brain's rules for who may manage an account
// and test a local plan, and whether the workspace lets members use their
// own accounts, so the worker refuses whoever brain would.
func KeysHandler(owner *pgxpool.Pool, ring gateway.Keyring, verify gateway.KeysVerify, publicURL string, log *slog.Logger) (http.Handler, error) {
	emails := NewEmailSealer(ring)
	members := identity.Members(owner, emails, publicURL, nil, log)
	providers := gateway.WorkerProviders(owner, log)
	providers.Allow = func(ctx context.Context, user string) (string, error) {
		person, err := identity.ParseUserID(user)
		if err != nil {
			return "", err
		}
		return members.SettingsAllow(ctx, person)
	}
	providers.Teams = func(ctx context.Context, user string) ([]string, error) {
		person, err := identity.ParseUserID(user)
		if err != nil {
			return nil, err
		}
		return members.TeamsOf(ctx, person)
	}
	providers.MemberProvidersAllowed = func(ctx context.Context, user string) (bool, error) {
		return MemberProvidersAllowed(ctx, owner, user)
	}
	providers.LocalPlan = func(ctx context.Context, user string) bool {
		allow, err := providers.Allow(ctx, user)
		return err == nil && allow != ""
	}
	actor := func(ctx context.Context, hash []byte) (string, bool, error) {
		return identity.ActorOfTokenHash(ctx, owner, hash)
	}
	passwords := passwordCheck{owner: owner, ring: ring, emails: emails}
	return gateway.KeysHandler(owner, ring, verify, providers, actor, passwords, emailLookup{owner: owner, members: members, emails: emails}, log)
}

// emailLookup is the worker's email route over the owner pool and the
// in-process ring: it checks each op's proof as brain's rules would and
// opens the address here. It satisfies gateway.KeysEmails.
type emailLookup struct {
	owner   *pgxpool.Pool
	members identity.MemberService
	emails  RingEmails
}

func (l emailLookup) Own(ctx context.Context, tokenHash []byte) (gateway.KeysEmailRow, bool, error) {
	row, ok, err := identity.StoredEmailOfSession(ctx, l.owner, tokenHash)
	return gateway.KeysEmailRow(row), ok, err
}

func (l emailLookup) Link(ctx context.Context, linkHash []byte) (gateway.KeysEmailRow, bool, error) {
	row, ok, err := identity.StoredEmailOfLink(ctx, l.owner, linkHash)
	return gateway.KeysEmailRow(row), ok, err
}

// Member applies identity.MayManage to the session's person, joined and
// enabled, and user's system role, and refuses a disabled or unknown user.
func (l emailLookup) Member(ctx context.Context, tokenHash []byte, user string) (gateway.KeysEmailRow, bool, error) {
	actor, ok, err := l.actor(ctx, tokenHash)
	if err != nil || !ok {
		return gateway.KeysEmailRow{}, false, err
	}
	target, err := identity.ParseUserID(user)
	if err != nil {
		return gateway.KeysEmailRow{}, false, nil
	}
	if err := l.members.MayManagePerson(ctx, actor, target); err != nil {
		return gateway.KeysEmailRow{}, false, refusal(err)
	}
	row, ok, err := identity.StoredEmailOfPerson(ctx, l.owner, target.String())
	return gateway.KeysEmailRow(row), ok, err
}

// Directory is every enabled person, for a joined, enabled actor.
func (l emailLookup) Directory(ctx context.Context, tokenHash []byte) ([]gateway.KeysEmailRow, bool, error) {
	actor, ok, err := l.actor(ctx, tokenHash)
	if err != nil || !ok {
		return nil, false, err
	}
	if err := l.members.Joined(ctx, actor); err != nil {
		return nil, false, refusal(err)
	}
	rows, err := identity.StoredEmailDirectory(ctx, l.owner)
	if err != nil {
		return nil, false, err
	}
	out := make([]gateway.KeysEmailRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, gateway.KeysEmailRow(row))
	}
	return out, true, nil
}

func (l emailLookup) Open(stored string, hash []byte) (string, error) {
	return l.emails.Open(stored, hash)
}

// actor is the enabled person behind an unexpired session, never an API
// token.
func (l emailLookup) actor(ctx context.Context, tokenHash []byte) (identity.UserID, bool, error) {
	user, ok, err := identity.SessionPerson(ctx, l.owner, tokenHash)
	if err != nil || !ok {
		return identity.UserID{}, false, err
	}
	id, err := identity.ParseUserID(user)
	if err != nil {
		return identity.UserID{}, false, err
	}
	return id, true, nil
}

// refusal is nil for an access rule's answer, which the route refuses, and
// err for a failure that could not tell.
func refusal(err error) error {
	if errors.Is(err, identity.ErrNoSuchMember) || errors.Is(err, identity.ErrNotPermitted) || errors.Is(err, identity.ErrOwnersOnly) {
		return nil
	}
	return err
}

// MemberProvidersAllowed reports identity.member_providers_allowed() for
// user: the workspace lets members use their own provider accounts, or user
// manages settings. Brain and the worker both apply it.
func MemberProvidersAllowed(ctx context.Context, pool *pgxpool.Pool, user string) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("brainops: member providers setting: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('upmo.user_id', $1, true)`, user); err != nil {
		return false, fmt.Errorf("brainops: member providers setting: %w", err)
	}
	var allowed bool
	if err := tx.QueryRow(ctx, `SELECT identity.member_providers_allowed()`).Scan(&allowed); err != nil {
		return false, fmt.Errorf("brainops: member providers setting: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("brainops: member providers setting: %w", err)
	}
	return allowed, nil
}
