package domain

import (
	"fmt"
	"regexp"
	"strings"
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
// teams, or their role, with the share level. $1 is the object type, $2 the
// user, $3 the team ids, $4 the role id. One query yields both the view list
// (any level) and the editor list.
func SharedObjectsSQL() string {
	return `SELECT object_id::text, level FROM identity.shares WHERE object_type = $1 AND (user_id::text = $2 OR team_id::text = ANY($3::text[]) OR grantee_role_id::text = $4)`
}

// AccessSettings is one statement that writes the person's access into
// transaction-local settings. The adapter of the owning context runs it first
// in its transaction. Teams and shared ids are uuid array literals so
// identity.may can compare them to uuid columns. A disabled or invited
// person, or a lone agent, gets an empty allow list. objectType is accepted
// so the caller can pass the lists for that type; the allow list is the full
// effective set.
func AccessSettings(subject Subject, objectType ObjectType, sharedView, sharedEdit []string) (string, []any) {
	allow := ""
	if _, ok := screen(subject); ok && (objectType == "" || knownType(objectType)) {
		allow = joinPermissions(EffectivePermissions(subject))
	}
	teams := make([]string, len(subject.Teams))
	for i, team := range subject.Teams {
		teams[i] = team.String()
	}
	sql := `SELECT set_config('upmo.user_id', $1, true), set_config('upmo.allow', $2, true), set_config('upmo.teams', $3, true), set_config('upmo.shared_view', $4, true), set_config('upmo.shared_edit', $5, true)`
	args := []any{subject.UserID.String(), allow, uuidArray(teams), uuidArray(sharedView), uuidArray(sharedEdit)}
	return sql, args
}

// UUIDArray is the Postgres uuid array literal AccessSettings writes, "{}"
// for none.
func UUIDArray(ids []string) string { return uuidArray(ids) }

func uuidArray(ids []string) string {
	if len(ids) == 0 {
		return "{}"
	}
	return "{" + strings.Join(ids, ",") + "}"
}

func joinPermissions(permissions []Permission) string {
	names := make([]string, len(permissions))
	for i, permission := range permissions {
		names[i] = string(permission)
	}
	return strings.Join(names, ",")
}
