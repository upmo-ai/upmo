package identity

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/upmo-ai/upmo/internal/identity/adapters/postgres"
	"github.com/upmo-ai/upmo/internal/identity/domain"
)

// Provisioning types and error sentinels exposed on the identity facade.
type (
	ProvisionStore        = postgres.ProvisionStore
	ProvisionMemberInput  = postgres.ProvisionMemberInput
	ProvisionMemberUpdate = postgres.ProvisionMemberUpdate
	ProvisionMember       = postgres.ProvisionMember
	ProvisionTeamInput    = postgres.ProvisionTeamInput
	ProvisionTeamUpdate   = postgres.ProvisionTeamUpdate
	ProvisionTeam         = postgres.ProvisionTeam
	ProvisionLink         = postgres.ProvisionLink
	ProvisionQuota        = postgres.ProvisionQuota
)

var (
	ErrExternalIDConflict      = domain.ErrExternalIDConflict
	ErrEmailConflict           = domain.ErrEmailConflict
	ErrUnknownTeamExternalID   = domain.ErrUnknownTeamExternalID
	ErrUnknownMemberExternalID = domain.ErrUnknownMemberExternalID
	ErrMemberDisabled          = domain.ErrMemberDisabled
	ErrInvalidProvisionInput   = domain.ErrInvalidProvisionInput
	ErrInvalidQuota            = domain.ErrInvalidQuota
	ErrLastOwner               = domain.ErrLastOwner
	ErrNoSuchTeam              = domain.ErrNoSuchTeam
	ErrBadTeam                 = domain.ErrBadTeam
	ErrTeamNameTaken           = domain.ErrTeamNameTaken
	ErrNonceReplay             = domain.ErrNonceReplay
)

// Provision builds the control-plane ProvisionStore on the schema owner pool.
// The emails sealer stores addresses; without it member writes fail.
func Provision(ownerPool *pgxpool.Pool, emails EmailSealer, publicURL string) ProvisionStore {
	return postgres.ProvisionStore{Pool: ownerPool, Emails: emails, PublicURL: publicURL}
}
