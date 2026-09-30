package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/identity/domain"
)

func TestTaskAllowUsesTheMembersTaskPermissions(t *testing.T) {
	members := Members{Store: allowFake{system: "member", joined: true}}
	got, err := members.TaskAllow(context.Background(), domain.UserID{})
	if err != nil {
		t.Fatal(err)
	}
	const want = "task:create,task:view,task:edit,task:share,task:delete"
	if got != want {
		t.Fatalf("allow = %q", got)
	}
}

func TestTaskAllowRefusesAPersonWhoHasNotJoined(t *testing.T) {
	members := Members{Store: allowFake{system: "member"}}
	_, err := members.TaskAllow(context.Background(), domain.UserID{})
	if !errors.Is(err, domain.ErrNoSuchMember) {
		t.Fatalf("err = %v", err)
	}
}

type allowFake struct {
	system string
	joined bool
}

func (f allowFake) ListRoles(context.Context) ([]domain.MemberRole, error) { return nil, nil }
func (f allowFake) ListMembers(context.Context) ([]domain.Member, error)   { return nil, nil }
func (f allowFake) MemberActor(context.Context, domain.UserID) (domain.RoleID, string, bool, []string, error) {
	return domain.RoleID{}, f.system, f.joined, nil, nil
}
func (f allowFake) RoleSystem(context.Context, domain.RoleID) (string, error) {
	return "", nil
}
func (f allowFake) UserByEmail(context.Context, string) (domain.UserID, bool, error) {
	return domain.UserID{}, false, nil
}
func (f allowFake) InviteMember(context.Context, string, string, domain.RoleID, []byte, []byte, time.Time, time.Time) (domain.Member, error) {
	return domain.Member{}, nil
}
func (f allowFake) RemoveMember(context.Context, domain.UserID, time.Time, func(string) error) error {
	return nil
}
func (f allowFake) ReplaceMemberLink(context.Context, domain.UserID, string, []byte, time.Time, func(string, bool) error) error {
	return nil
}
