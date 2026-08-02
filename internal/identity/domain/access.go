package domain

import "fmt"

// Reason is why a decision was allowed, or why it was refused.
type Reason string

const (
	ReasonOwner      Reason = "owner"
	ReasonShare      Reason = "share"
	ReasonVisibility Reason = "visibility"
	ReasonPermission Reason = "permission"
	ReasonDisabled   Reason = "disabled"
	ReasonInvited    Reason = "invited"
	ReasonUnknown    Reason = "unknown"
	ReasonAgent      Reason = "agent"
	ReasonPolicy     Reason = "policy"
)

// Decision is one access answer.
type Decision struct {
	Allow  bool
	Reason Reason
}

func refuse(reason Reason) Decision { return Decision{Reason: reason} }

func allow(reason Reason) Decision { return Decision{Allow: true, Reason: reason} }

// Subject is who is asking. Permissions is the role set, already resolved
// from code for a system role or from the row for a custom role. Policy is
// read only for a run.
type Subject struct {
	Kind        Kind
	UserID      UserID
	RoleID      RoleID
	Disabled    bool
	Joined      bool
	Permissions []Permission
	Teams       []TeamID
	Policy      []Permission
}

// Grant is one share on an object.
type Grant struct {
	UserID UserID
	TeamID TeamID
	RoleID RoleID
	Level  Level
}

// Object is a work object the decision is about.
type Object struct {
	Type           ObjectType
	ID             string
	OwnerID        UserID
	Visibility     Visibility
	VisibilityTeam TeamID
	Shares         []Grant
}

// Can decides an action on one work object. Create is an install permission
// and is decided by Permitted.
func Can(subject Subject, object Object, action Action) Decision {
	if !knownAction(action) || action == ActionCreate || !knownType(object.Type) {
		return refuse(ReasonUnknown)
	}
	if decision, ok := screen(subject); !ok {
		return decision
	}
	permission := ObjectPermission(object.Type, action)
	if decision, ok := permit(subject, permission); !ok {
		return decision
	}
	return relationship(subject, object, action)
}

// Permitted decides a permission that is not about one existing object:
// an install permission, or creating a work object.
func Permitted(subject Subject, permission Permission) Decision {
	if _, err := ParsePermission(string(permission)); err != nil {
		return refuse(ReasonUnknown)
	}
	if decision, ok := screen(subject); !ok {
		return decision
	}
	if decision, ok := permit(subject, permission); !ok {
		return decision
	}
	return allow(ReasonPermission)
}

// screen refuses an agent, a disabled person, and a person who has not joined.
// ok is false when the decision is a refusal.
func screen(subject Subject) (Decision, bool) {
	if subject.Kind == KindAgent {
		return refuse(ReasonAgent), false
	}
	if subject.Disabled {
		return refuse(ReasonDisabled), false
	}
	if !subject.Joined {
		return refuse(ReasonInvited), false
	}
	return Decision{}, true
}

// permit refuses when the effective set does not hold permission. A run that
// lost it to its policy, or to the rule that a run never deletes or manages
// the install, is refused as policy.
func permit(subject Subject, permission Permission) (Decision, bool) {
	roleHolds := has(known(subject.Permissions), permission)
	if has(EffectivePermissions(subject), permission) {
		return Decision{}, true
	}
	if subject.Kind == KindRun && roleHolds {
		return refuse(ReasonPolicy), false
	}
	return refuse(ReasonPermission), false
}

func relationship(subject Subject, object Object, action Action) Decision {
	if object.OwnerID == subject.UserID {
		return allow(ReasonOwner)
	}
	level, shared := bestShare(subject, object)
	if shared && levelAllows(level, action) {
		return allow(ReasonShare)
	}
	if action == ActionView && visibilityAllows(subject, object) {
		return allow(ReasonVisibility)
	}
	if shared {
		return refuse(ReasonShare)
	}
	return refuse(ReasonVisibility)
}

func bestShare(subject Subject, object Object) (Level, bool) {
	var (
		level Level
		found bool
	)
	for _, grant := range object.Shares {
		if !grantMatches(subject, grant) {
			continue
		}
		if grant.Level == LevelEditor {
			return LevelEditor, true
		}
		if grant.Level == LevelViewer {
			level = LevelViewer
			found = true
		}
	}
	return level, found
}

func grantMatches(subject Subject, grant Grant) bool {
	if grant.UserID == subject.UserID && subject.UserID != (UserID{}) {
		return true
	}
	for _, team := range subject.Teams {
		if grant.TeamID == team && team != (TeamID{}) {
			return true
		}
	}
	return grant.RoleID == subject.RoleID && subject.RoleID != (RoleID{})
}

func levelAllows(level Level, action Action) bool {
	switch action {
	case ActionView:
		return level == LevelViewer || level == LevelEditor
	case ActionEdit, ActionShare, ActionDelete:
		return level == LevelEditor
	default:
		return false
	}
}

func visibilityAllows(subject Subject, object Object) bool {
	switch object.Visibility {
	case VisibilityEveryone:
		return true
	case VisibilityTeam:
		for _, team := range subject.Teams {
			if team == object.VisibilityTeam && team != (TeamID{}) {
				return true
			}
		}
	}
	return false
}

func knownAction(action Action) bool {
	for _, candidate := range Actions() {
		if candidate == action {
			return true
		}
	}
	return false
}

func knownType(objectType ObjectType) bool {
	for _, candidate := range ObjectTypes() {
		if candidate == objectType {
			return true
		}
	}
	return false
}

// ParseAction rejects an unknown object action.
func ParseAction(s string) (Action, error) {
	for _, action := range Actions() {
		if s == string(action) {
			return action, nil
		}
	}
	return "", fmt.Errorf("identity: unknown action %q", s)
}
