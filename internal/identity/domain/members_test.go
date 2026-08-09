package domain_test

import (
	"errors"
	"testing"

	"github.com/upmo-ai/upmo/internal/identity/domain"
)

func TestMayManage(t *testing.T) {
	owner := subject(t, domain.RoleOwner)
	admin := subject(t, domain.RoleAdmin)
	member := subject(t, domain.RoleMember)

	tests := []struct {
		name   string
		actor  domain.Subject
		target string
		want   error
	}{
		{name: "owner manages an owner", actor: owner, target: "owner"},
		{name: "admin manages a member", actor: admin, target: "member"},
		{name: "admin manages a custom role", actor: admin, target: ""},
		{name: "admin cannot manage an owner", actor: admin, target: "owner", want: domain.ErrOwnersOnly},
		{name: "member cannot manage a member", actor: member, target: "member", want: domain.ErrNotPermitted},
		{name: "viewer cannot manage a viewer", actor: subject(t, domain.RoleViewer), target: "viewer", want: domain.ErrNotPermitted},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := domain.MayManage(tc.actor, tc.target)
			if !errors.Is(err, tc.want) {
				t.Fatalf("MayManage() = %v, want %v", err, tc.want)
			}
		})
	}
}

func subject(t *testing.T, role domain.Role) domain.Subject {
	t.Helper()
	permissions, err := domain.SystemPermissions(role)
	if err != nil {
		t.Fatal(err)
	}
	return domain.Subject{Kind: domain.KindUser, Joined: true, Permissions: permissions}
}
