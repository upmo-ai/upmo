package brainops

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/gateway"
	"github.com/upmo-ai/upmo/internal/identity"
)

// KeysHandler is the worker's key service: gateway's key routes on the
// owner pool, with provider-test resolving the person from their session or
// API token hash and applying brain's rules for who may manage an account
// and test a local plan, so the worker refuses whoever brain would.
func KeysHandler(owner *pgxpool.Pool, ring gateway.Keyring, verify gateway.KeysVerify, publicURL string, log *slog.Logger) (http.Handler, error) {
	members := identity.Members(owner, NewEmailSealer(ring), publicURL, nil, log)
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
	providers.LocalPlan = func(ctx context.Context, user string) bool {
		allow, err := providers.Allow(ctx, user)
		return err == nil && allow != ""
	}
	actor := func(ctx context.Context, hash []byte) (string, bool, error) {
		return identity.ActorOfTokenHash(ctx, owner, hash)
	}
	return gateway.KeysHandler(owner, ring, verify, providers, actor, log)
}
