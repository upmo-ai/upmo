package domain_test

import (
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/identity/domain"
)

func TestParseRoleSet(t *testing.T) {
	name, permissions, err := domain.ParseRoleSet(" Steward ", []string{"task:view", "task:view"})
	if err != nil || name != "Steward" || len(permissions) != 1 {
		t.Fatalf("parse = %q %#v %v", name, permissions, err)
	}
	if _, _, err := domain.ParseRoleSet("", []string{"task:view"}); !errors.Is(err, domain.ErrBadRole) {
		t.Fatalf("empty name %v", err)
	}
	if _, _, err := domain.ParseRoleSet("Steward", nil); !errors.Is(err, domain.ErrBadRole) {
		t.Fatalf("empty set %v", err)
	}
	if _, _, err := domain.ParseRoleSet("Steward", []string{"nope"}); !errors.Is(err, domain.ErrBadRole) {
		t.Fatalf("unknown %v", err)
	}
	if _, _, err := domain.ParseRoleSet("Steward", []string{"owners:manage"}); !errors.Is(err, domain.ErrBadRole) {
		t.Fatalf("owner-only %v", err)
	}
	long := ""
	for range 65 {
		long += "a"
	}
	if _, _, err := domain.ParseRoleSet(long, []string{"task:view"}); !errors.Is(err, domain.ErrBadRole) {
		t.Fatalf("long name %v", err)
	}
}

func TestMayGrantAndAssign(t *testing.T) {
	owner := roleSubject(t, domain.RoleOwner)
	admin := roleSubject(t, domain.RoleAdmin)
	member := roleSubject(t, domain.RoleMember)

	if err := domain.MayGrant(admin, []domain.Permission{domain.PermRolesManage}); err != nil {
		t.Fatal(err)
	}
	if err := domain.MayGrant(admin, []domain.Permission{domain.PermOwnersManage}); !errors.Is(err, domain.ErrBeyondOwn) {
		t.Fatalf("admin grant owner-only %v", err)
	}
	if err := domain.MayGrant(member, []domain.Permission{domain.PermMembersManage}); !errors.Is(err, domain.ErrRolesOnly) {
		t.Fatalf("member grant %v", err)
	}
	if err := domain.MayGrant(admin, nil); err != nil {
		t.Fatalf("rename %v", err)
	}

	view := []domain.Permission{domain.ObjectPermission(domain.TypeTask, domain.ActionView)}
	if err := domain.MayAssign(admin, "member", "member", view); err != nil {
		t.Fatal(err)
	}
	if err := domain.MayAssign(admin, "member", "owner", nil); !errors.Is(err, domain.ErrOwnersOnly) {
		t.Fatalf("admin gives owner %v", err)
	}
	if err := domain.MayAssign(admin, "owner", "admin", nil); !errors.Is(err, domain.ErrOwnersOnly) {
		t.Fatalf("admin takes owner %v", err)
	}
	if err := domain.MayAssign(owner, "member", "owner", nil); err != nil {
		t.Fatal(err)
	}
	if err := domain.MayAssign(member, "member", "viewer", view); !errors.Is(err, domain.ErrNotPermitted) {
		t.Fatalf("member assigns %v", err)
	}
	if err := domain.MayAssign(admin, "member", "", []domain.Permission{domain.PermOwnersManage}); !errors.Is(err, domain.ErrBeyondOwn) {
		t.Fatalf("admin assigns beyond %v", err)
	}
}

func TestDuplicateDropsOwnerOnly(t *testing.T) {
	owner, err := domain.SystemPermissions(domain.RoleOwner)
	if err != nil {
		t.Fatal(err)
	}
	copied := domain.WithoutOwnerOnly(owner)
	if domain.Added(copied, []domain.Permission{domain.PermOwnersManage}) == nil {
		t.Fatal("owner-only stayed on the copy")
	}
	for _, permission := range copied {
		if permission == domain.PermOwnersManage {
			t.Fatal("copy held owners:manage")
		}
	}
	if len(copied) != len(owner)-1 {
		t.Fatalf("copy len %d, owner %d", len(copied), len(owner))
	}
}

func roleSubject(t *testing.T, role domain.Role) domain.Subject {
	t.Helper()
	permissions, err := domain.SystemPermissions(role)
	if err != nil {
		t.Fatal(err)
	}
	return domain.Subject{Kind: domain.KindUser, Joined: true, Permissions: permissions}
}
