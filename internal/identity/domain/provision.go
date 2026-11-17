package domain

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	// ErrExternalIDConflict reports a duplicate external_id on creation.
	ErrExternalIDConflict = errors.New("identity: external_id already exists")
	// ErrEmailConflict reports an email already belonging to a different user.
	ErrEmailConflict = errors.New("identity: email already belongs to another user")
	// ErrUnknownTeamExternalID reports an unknown team external_id in a member payload.
	ErrUnknownTeamExternalID = errors.New("identity: unknown team external_id")
	// ErrUnknownMemberExternalID reports an unknown or disabled member external_id in a team payload.
	ErrUnknownMemberExternalID = errors.New("identity: unknown member external_id")
	// ErrMemberDisabled reports an operation targeting a disabled member.
	ErrMemberDisabled = errors.New("identity: member is disabled")
	// ErrInvalidProvisionInput reports invalid provisioning input fields.
	ErrInvalidProvisionInput = errors.New("identity: invalid provisioning input")
	// ErrInvalidQuota reports an invalid workspace spend ceiling.
	ErrInvalidQuota = errors.New("identity: spend_ceiling_micro_usd must be >= 0")
	// ErrNonceReplay reports a provision bearer nonce that was already used.
	ErrNonceReplay = errors.New("identity: provision nonce already used")
)

// CleanExternalID trims and validates an external_id string.
func CleanExternalID(raw string) (string, error) {
	id := strings.TrimSpace(raw)
	if id == "" || utf8.RuneCountInString(id) > 255 || strings.ContainsFunc(id, unicode.IsControl) || !utf8.ValidString(id) {
		return "", fmt.Errorf("%w: external_id must be 1 to 255 non-control characters", ErrInvalidProvisionInput)
	}
	return id, nil
}

// MapProvisionRole maps a control-plane role name (owner, admin, operator,
// member, viewer) onto a system role key in identity.roles.
func MapProvisionRole(raw string) (Role, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "owner":
		return RoleOwner, nil
	case "admin":
		return RoleAdmin, nil
	case "operator", "member":
		return RoleMember, nil
	case "viewer":
		return RoleViewer, nil
	default:
		return "", fmt.Errorf("%w: unknown role %q", ErrInvalidProvisionInput, raw)
	}
}
