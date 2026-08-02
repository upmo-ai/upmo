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

// SharedObjectsSQL lists object ids shared with a person, their teams, or their role.
func SharedObjectsSQL() string { return domain.SharedObjectsSQL() }
