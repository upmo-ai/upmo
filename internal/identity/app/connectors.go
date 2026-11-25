package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/platform/runjwt"
)

// ConnectorGrantStore is the per-user connector grant table.
type ConnectorGrantStore interface {
	ListConnectorGrants(ctx context.Context, user domain.UserID) ([]domain.ConnectorGrant, error)
	SaveConnectorGrant(ctx context.Context, user domain.UserID, grant domain.ConnectorGrant) error
	DeleteConnectorGrant(ctx context.Context, user domain.UserID, connector string) error
	LoadConnectorGrant(ctx context.Context, user domain.UserID, connector string) (domain.ConnectorGrant, bool, error)
}

// ConnectorGrants is listing, saving and deciding a person's connector grants.
type ConnectorGrants struct {
	Store ConnectorGrantStore
	Log   *slog.Logger
}

// List returns saved grants. Missing connectors stay at the default.
func (g ConnectorGrants) List(ctx context.Context, user domain.UserID) ([]domain.ConnectorGrant, error) {
	return g.Store.ListConnectorGrants(ctx, user)
}

// Put upserts one grant for the signed-in person.
func (g ConnectorGrants) Put(ctx context.Context, user domain.UserID, connector, scope string, resources []string, changes string) (domain.ConnectorGrant, error) {
	grant, err := domain.ParseConnectorGrant(connector, scope, resources, changes)
	if err != nil {
		return domain.ConnectorGrant{}, err
	}
	if err := g.Store.SaveConnectorGrant(ctx, user, grant); err != nil {
		return domain.ConnectorGrant{}, err
	}
	return grant, nil
}

// Remove deletes a saved grant so the default applies again.
func (g ConnectorGrants) Remove(ctx context.Context, user domain.UserID, connector string) error {
	name, err := domain.ParseConnector(connector)
	if err != nil {
		return err
	}
	return g.Store.DeleteConnectorGrant(ctx, user, name)
}

// Decide loads the saved grant, or the default, and returns allow, ask or block.
func (g ConnectorGrants) Decide(ctx context.Context, user domain.UserID, connector string, action domain.ConnectorAction, resource string) (domain.ConnectorVerdict, error) {
	name, err := domain.ParseConnector(connector)
	if err != nil {
		return "", err
	}
	grant, ok, err := g.Store.LoadConnectorGrant(ctx, user, name)
	if err != nil {
		return "", err
	}
	if !ok {
		grant = domain.DefaultConnectorGrant(name)
	}
	return domain.ConnectorDecide(grant, action, resource), nil
}

// RunTokens mints and verifies the per-run JWT.
type RunTokens struct {
	keys runjwt.Keys
}

// NewRunTokens parses the current and optional previous run key
// (UPMO_RUN_KEY, UPMO_RUN_KEY_PREVIOUS).
func NewRunTokens(current, previous string) (RunTokens, error) {
	keys, err := runjwt.ParseKeys(current, previous)
	if err != nil {
		return RunTokens{}, err
	}
	return RunTokens{keys: keys}, nil
}

// Mint returns a 15-minute JWT for this audience, user and run.
func (t RunTokens) Mint(audience string, user domain.UserID, run string, now time.Time) (string, error) {
	return t.keys.Sign(audience, user.String(), run, now)
}

// Verify checks a run JWT for this audience.
func (t RunTokens) Verify(audience, token string, now time.Time) (runjwt.Claims, error) {
	return t.keys.Verify(audience, token, now)
}
