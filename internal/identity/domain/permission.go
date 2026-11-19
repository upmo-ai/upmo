package domain

import (
	"fmt"
	"strings"
)

// ObjectType is a kind of work object.
type ObjectType string

const (
	TypeTask       ObjectType = "task"
	TypeChat       ObjectType = "chat"
	TypeFolder     ObjectType = "folder"
	TypeAutomation ObjectType = "automation"
	TypeAgent      ObjectType = "agent"
	TypeArtifact   ObjectType = "artifact"
)

// ObjectTypes are the work objects the catalog covers.
func ObjectTypes() []ObjectType {
	return []ObjectType{TypeTask, TypeChat, TypeFolder, TypeAutomation, TypeAgent, TypeArtifact}
}

// Action is something a principal may do to a work object.
type Action string

const (
	ActionCreate Action = "create"
	ActionView   Action = "view"
	ActionEdit   Action = "edit"
	ActionShare  Action = "share"
	ActionDelete Action = "delete"
)

// Actions are the object verbs.
func Actions() []Action {
	return []Action{ActionCreate, ActionView, ActionEdit, ActionShare, ActionDelete}
}

// Permission is one catalog entry, resource:verb. The catalog is closed.
type Permission string

// ObjectPermission is the catalog name of an object action.
func ObjectPermission(objectType ObjectType, action Action) Permission {
	return Permission(string(objectType) + ":" + string(action))
}

// Install permissions. An install combines these; it never invents names.
const (
	PermMembersManage    Permission = "members:manage"
	PermTeamsManage      Permission = "teams:manage"
	PermRolesManage      Permission = "roles:manage"
	PermSettingsManage   Permission = "settings:manage"
	PermConnectorsManage Permission = "connectors:manage"
	PermOwnersManage     Permission = "owners:manage"
	PermAuditView        Permission = "audit:view"
	PermUsageView        Permission = "usage:view"
)

// Permissions is the whole catalog.
func Permissions() []Permission {
	out := objectPermissions()
	out = append(out, installPermissions()...)
	return out
}

// OwnerOnly are the permissions only the system owner role holds.
func OwnerOnly() []Permission {
	return []Permission{PermOwnersManage}
}

// ParsePermission rejects a name that is not in the catalog.
func ParsePermission(s string) (Permission, error) {
	for _, permission := range Permissions() {
		if s == string(permission) {
			return permission, nil
		}
	}
	return "", fmt.Errorf("identity: unknown permission %q", s)
}

// SystemPermissions is the set a system role holds. The database stores the
// key; the set lives here. Unknown names in a custom set grant nothing.
func SystemPermissions(role Role) ([]Permission, error) {
	switch role {
	case RoleOwner:
		return Permissions(), nil
	case RoleAdmin:
		return without(Permissions(), OwnerOnly()), nil
	case RoleMember:
		return objectPermissions(), nil
	case RoleViewer:
		var out []Permission
		for _, objectType := range ObjectTypes() {
			out = append(out, ObjectPermission(objectType, ActionView))
		}
		return out, nil
	default:
		return nil, fmt.Errorf("identity: unknown role %q", role)
	}
}

// EffectivePermissions is the set a subject actually holds. A run's policy
// and a token's scope only narrow, and neither holds a delete. A run never
// holds an install permission; a token holds audit:view alone. An agent
// holds nothing. Names outside the catalog are dropped.
func EffectivePermissions(subject Subject) []Permission {
	if subject.Kind == KindAgent {
		return nil
	}
	held := known(subject.Permissions)
	if subject.Kind != KindRun && subject.Kind != KindToken {
		return held
	}
	held = intersect(held, known(subject.Policy))
	forbidden := runForbidden
	if subject.Kind == KindToken {
		forbidden = tokenForbidden
	}
	var out []Permission
	for _, permission := range held {
		if forbidden(permission) {
			continue
		}
		out = append(out, permission)
	}
	return out
}

func objectPermissions() []Permission {
	var out []Permission
	for _, objectType := range ObjectTypes() {
		for _, action := range Actions() {
			out = append(out, ObjectPermission(objectType, action))
		}
	}
	return out
}

func installPermissions() []Permission {
	return []Permission{
		PermMembersManage,
		PermTeamsManage,
		PermRolesManage,
		PermSettingsManage,
		PermConnectorsManage,
		PermOwnersManage,
		PermAuditView,
		PermUsageView,
	}
}

func known(permissions []Permission) []Permission {
	var out []Permission
	for _, permission := range permissions {
		if _, err := ParsePermission(string(permission)); err == nil {
			out = append(out, permission)
		}
	}
	return out
}

func without(permissions, drop []Permission) []Permission {
	var out []Permission
	for _, permission := range permissions {
		if !has(drop, permission) {
			out = append(out, permission)
		}
	}
	return out
}

func intersect(a, b []Permission) []Permission {
	var out []Permission
	for _, permission := range a {
		if has(b, permission) {
			out = append(out, permission)
		}
	}
	return out
}

func has(permissions []Permission, want Permission) bool {
	for _, permission := range permissions {
		if permission == want {
			return true
		}
	}
	return false
}

func runForbidden(permission Permission) bool {
	if has(installPermissions(), permission) {
		return true
	}
	return strings.HasSuffix(string(permission), ":delete")
}

// tokenForbidden is runForbidden with audit:view let through: the one
// install permission a token may hold. Every other install permission and
// every delete stays forbidden.
func tokenForbidden(permission Permission) bool {
	if permission == PermAuditView {
		return false
	}
	return runForbidden(permission)
}
