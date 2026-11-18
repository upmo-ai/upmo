package domain

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

var (
	// ErrRolesOnly refuses a person who cannot manage roles.
	ErrRolesOnly = errors.New("identity: you cannot manage roles")
	// ErrBeyondOwn refuses a permission the actor does not hold.
	ErrBeyondOwn = errors.New("identity: you cannot grant a permission you do not hold")
	// ErrSystemRole refuses a change to a system role.
	ErrSystemRole = errors.New("identity: a system role cannot be changed")
	// ErrRoleNameTaken refuses a role name already in use.
	ErrRoleNameTaken = errors.New("identity: a role with that name already exists")
	// ErrBadRole refuses a name or a permission set that cannot be stored.
	ErrBadRole = errors.New("identity: a role needs a name and at least one permission")
	// ErrNoSuchRole is an unknown role.
	ErrNoSuchRole = errors.New("identity: no such role")
	// ErrRoleInUse refuses deletion of a role a person still holds.
	ErrRoleInUse = errors.New("identity: role in use")
	// ErrRoleShared refuses deletion of a role a share still references.
	ErrRoleShared = errors.New("identity: a role used by a share cannot be deleted")
)

// RoleHolders is how many people still hold a role.
type RoleHolders struct {
	Count int
}

func (e *RoleHolders) Error() string {
	return fmt.Sprintf("identity: this role is held by %d members", e.Count)
}

func (e *RoleHolders) Unwrap() error { return ErrRoleInUse }

// ParseRoleSet checks a custom role's name and permission names.
func ParseRoleSet(name string, names []string) (string, []Permission, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 64 || len(names) == 0 {
		return "", nil, ErrBadRole
	}
	var permissions []Permission
	seen := map[Permission]bool{}
	for _, raw := range names {
		permission, err := ParsePermission(strings.TrimSpace(raw))
		if err != nil {
			return "", nil, ErrBadRole
		}
		if has(OwnerOnly(), permission) {
			return "", nil, ErrBadRole
		}
		if seen[permission] {
			continue
		}
		seen[permission] = true
		permissions = append(permissions, permission)
	}
	if len(permissions) == 0 {
		return "", nil, ErrBadRole
	}
	return name, permissions, nil
}

// WithoutOwnerOnly drops permissions a custom role cannot hold.
func WithoutOwnerOnly(permissions []Permission) []Permission {
	return without(known(permissions), OwnerOnly())
}

// Added is the permissions in next that are not already in current.
func Added(current, next []Permission) []Permission {
	var out []Permission
	for _, permission := range next {
		if !has(current, permission) {
			out = append(out, permission)
		}
	}
	return out
}

// MayGrant reports whether actor may put added onto a role.
func MayGrant(actor Subject, added []Permission) error {
	if !has(known(actor.Permissions), PermRolesManage) {
		return ErrRolesOnly
	}
	for _, permission := range added {
		if !has(known(actor.Permissions), permission) {
			return ErrBeyondOwn
		}
	}
	return nil
}

// MayAssign reports whether actor may give next to a person who currently holds currentSystem.
func MayAssign(actor Subject, currentSystem, nextSystem string, next []Permission) error {
	if !has(known(actor.Permissions), PermMembersManage) {
		return ErrNotPermitted
	}
	if currentSystem == string(RoleOwner) || nextSystem == string(RoleOwner) {
		if !has(known(actor.Permissions), PermOwnersManage) {
			return ErrOwnersOnly
		}
	}
	if nextSystem == string(RoleOwner) {
		return nil
	}
	for _, permission := range next {
		if !has(known(actor.Permissions), permission) {
			return ErrBeyondOwn
		}
	}
	return nil
}
