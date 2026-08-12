package identity

import "github.com/upmo-ai/upmo/internal/identity/domain"

// Types other contexts need. They import this package and not the domain.
type (
	UserID     = domain.UserID
	TeamID     = domain.TeamID
	RoleID     = domain.RoleID
	Permission = domain.Permission
	Action     = domain.Action
	ObjectType = domain.ObjectType
	Subject    = domain.Subject
	Object     = domain.Object
	Grant      = domain.Grant
	Decision   = domain.Decision
	Reason     = domain.Reason
	User       = domain.User
)

const (
	IdleTimeout = domain.IdleTimeout
	MaxAge      = domain.MaxAge
)

var (
	ParseUserID     = domain.ParseUserID
	ParseTeamID     = domain.ParseTeamID
	ParseRoleID     = domain.ParseRoleID
	ErrNoSuchPerson = domain.ErrNoSuchPerson
	ErrNoSuchMember = domain.ErrNoSuchMember
	ErrLastPasskey  = domain.ErrLastPasskey
	NewUser         = domain.NewUser
)

// Can decides an action on one work object.
func Can(subject Subject, object Object, action Action) Decision {
	return domain.Can(subject, object, action)
}

// Permitted decides a permission that is not about one existing object.
func Permitted(subject Subject, permission Permission) Decision {
	return domain.Permitted(subject, permission)
}

// ListFilter is the view predicate for one object type.
func ListFilter(alias string, subject Subject, objectType ObjectType, shared []string) (string, []any, error) {
	return domain.ListFilter(alias, subject, objectType, shared)
}

// SharedObjectsSQL lists object ids shared with a person, their teams, or their role, with the share level.
func SharedObjectsSQL() string { return domain.SharedObjectsSQL() }

// AccessSettings writes the person's access into transaction-local settings.
func AccessSettings(subject Subject, objectType ObjectType, sharedView, sharedEdit []string) (string, []any) {
	return domain.AccessSettings(subject, objectType, sharedView, sharedEdit)
}
