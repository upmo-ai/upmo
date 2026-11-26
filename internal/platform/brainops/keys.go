package brainops

import (
	"context"
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
	return gateway.KeysHandler(owner, ring, verify, providers, actor, passwords, log)
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
