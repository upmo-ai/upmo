package identity

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	httpapi "github.com/upmo-ai/upmo/internal/identity/adapters/http"
	"github.com/upmo-ai/upmo/internal/identity/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/identity/app"
	"github.com/upmo-ai/upmo/internal/identity/domain"
	"github.com/upmo-ai/upmo/internal/platform/runjwt"
)

// Connector grants and the run JWT, re-exported so other contexts import this
// package and not domain or app.
type (
	ConnectorGrant   = domain.ConnectorGrant
	ConnectorAction  = domain.ConnectorAction
	ConnectorVerdict = domain.ConnectorVerdict
	RunTokens        = app.RunTokens
	RunClaims        = runjwt.Claims
)

const (
	ScopeAll           = domain.ScopeAll
	ScopeThese         = domain.ScopeThese
	ChangesAllow       = domain.ChangesAllow
	ChangesAsk         = domain.ChangesAsk
	ChangesBlock       = domain.ChangesBlock
	ConnectorRead      = domain.ConnectorRead
	ConnectorChange    = domain.ConnectorChange
	ConnectorDelete    = domain.ConnectorDelete
	AudienceConnectors = runjwt.AudienceConnectors
)

// ConnectorGrants builds the per-user connector grant use cases.
func ConnectorGrants(pool *pgxpool.Pool) app.ConnectorGrants {
	return app.ConnectorGrants{Store: postgres.Passkeys{Pool: pool}}
}

// MountConnectorGrants registers the grant routes.
func MountConnectorGrants(mux *http.ServeMux, grants app.ConnectorGrants, sessions app.Service) {
	(httpapi.ConnectorGrants{Grants: grants, Sessions: sessions}).Mount(mux)
}

// NewRunTokens derives signing keys from the current and optional previous master.
func NewRunTokens(current, previous string) (app.RunTokens, error) {
	return app.NewRunTokens(current, previous)
}

// ConnectorDecide is the safety rule for one action on one resource.
func ConnectorDecide(grant domain.ConnectorGrant, action domain.ConnectorAction, resource string) domain.ConnectorVerdict {
	return domain.ConnectorDecide(grant, action, resource)
}
