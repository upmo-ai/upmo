package domain

import (
	"fmt"
	"regexp"
)

// aliasName is the only text ListFilter interpolates. Values travel as
// placeholders, and the predicate never joins the identity schema.
var aliasName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ListFilter is the view predicate for one object type. It agrees with
// Can(view): the owner, a share, everyone visibility, or team visibility.
// shared is the object ids from SharedObjectsSQL. No view permission yields
// FALSE.
func ListFilter(alias string, subject Subject, objectType ObjectType, shared []string) (string, []any, error) {
	if !aliasName.MatchString(alias) {
		return "", nil, fmt.Errorf("identity: alias %q", alias)
	}
	if !knownType(objectType) {
		return "", nil, fmt.Errorf("identity: unknown object type %q", objectType)
	}
	if shared == nil {
		shared = []string{}
	}
	view := Can(subject, Object{
		Type:       objectType,
		Visibility: VisibilityEveryone,
	}, ActionView)
	// An everyone-visible object with no owner match still needs the view
	// permission. Refuse the whole list when that sample is refused for a
	// reason other than visibility, which a real row can still satisfy.
	if !view.Allow && view.Reason != ReasonVisibility && view.Reason != ReasonOwner && view.Reason != ReasonShare {
		return "FALSE", nil, nil
	}
	if !has(EffectivePermissions(subject), ObjectPermission(objectType, ActionView)) {
		return "FALSE", nil, nil
	}
	teams := make([]string, len(subject.Teams))
	for i, team := range subject.Teams {
		teams[i] = team.String()
	}
	clause := fmt.Sprintf(
		`(%s.owner_user_id = $1 OR %s.id::text = ANY($2::text[]) OR %s.visibility = 'everyone' OR (%s.visibility = 'team' AND %s.visibility_team_id::text = ANY($3::text[])))`,
		alias, alias, alias, alias, alias,
	)
	args := []any{subject.UserID.String(), shared, teams}
	return clause, args, nil
}

// SharedObjectsSQL returns the object ids shared with a person, one of their
// teams, or their role. $1 is the object type, $2 the user, $3 the team ids,
// $4 the role id.
func SharedObjectsSQL() string {
	return `SELECT object_id::text FROM identity.shares WHERE object_type = $1 AND (user_id::text = $2 OR team_id::text = ANY($3::text[]) OR grantee_role_id::text = $4)`
}
