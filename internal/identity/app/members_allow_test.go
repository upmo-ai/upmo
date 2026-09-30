package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/identity/domain"
)

func TestTaskAllowUsesTheMembersTaskPermissions(t *testing.T) {
	members := Members{Store: &allowFake{system: "member", joined: true}}
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
	members := Members{Store: &allowFake{system: "member"}}
	_, err := members.TaskAllow(context.Background(), domain.UserID{})
	if !errors.Is(err, domain.ErrNoSuchMember) {
		t.Fatalf("err = %v", err)
	}
}

const (
	teamA   = "11111111-1111-1111-1111-111111111111"
	teamB   = "22222222-2222-2222-2222-222222222222"
	viewed  = "33333333-3333-3333-3333-333333333333"
	editing = "44444444-4444-4444-4444-444444444444"
)

func TestShareAccessSplitsViewersFromEditors(t *testing.T) {
	fake := &allowFake{system: "member", joined: true, teams: []string{teamA, teamB}, shares: []ObjectShare{
		{ObjectID: viewed, Level: domain.LevelViewer},
		{ObjectID: editing, Level: domain.LevelEditor},
	}}
	got, err := Members{Store: fake}.ShareAccess(context.Background(), domain.UserID{}, domain.TypeTask)
	if err != nil {
		t.Fatal(err)
	}
	want := Shares{
		Teams:      "{" + teamA + "," + teamB + "}",
		SharedView: "{" + viewed + "," + editing + "}",
		SharedEdit: "{" + editing + "}",
	}
	if got != want {
		t.Fatalf("shares = %+v", got)
	}
	if fake.asked != domain.TypeTask || len(fake.askedTeams) != 2 || fake.askedTeams[0] != teamA {
		t.Fatalf("query = %q %v", fake.asked, fake.askedTeams)
	}
}

func TestShareAccessIsEmptyArraysWithNoTeamsOrShares(t *testing.T) {
	got, err := Members{Store: &allowFake{system: "member", joined: true}}.ShareAccess(context.Background(), domain.UserID{}, "decision")
	if err != nil {
		t.Fatal(err)
	}
	if got != (Shares{Teams: "{}", SharedView: "{}", SharedEdit: "{}"}) {
		t.Fatalf("shares = %+v", got)
	}
}

func TestShareAccessRefusesAPersonWhoHasNotJoined(t *testing.T) {
	_, err := Members{Store: &allowFake{system: "member"}}.ShareAccess(context.Background(), domain.UserID{}, domain.TypeTask)
	if !errors.Is(err, domain.ErrNoSuchMember) {
		t.Fatalf("err = %v", err)
	}
}

type allowFake struct {
	system     string
	joined     bool
	teams      []string
	shares     []ObjectShare
	asked      domain.ObjectType
	askedTeams []string
}

func (f allowFake) ListRoles(context.Context) ([]domain.MemberRole, error) { return nil, nil }
func (f allowFake) ListMembers(context.Context) ([]domain.Member, error)   { return nil, nil }
func (f allowFake) MemberActor(context.Context, domain.UserID) (domain.RoleID, string, bool, []string, error) {
	return domain.RoleID{}, f.system, f.joined, nil, nil
}
func (f allowFake) MemberTeams(context.Context, domain.UserID) ([]string, error) {
	return f.teams, nil
}
func (f *allowFake) SharedObjects(_ context.Context, objectType domain.ObjectType, _ domain.UserID, teams []string, _ domain.RoleID) ([]ObjectShare, error) {
	f.asked, f.askedTeams = objectType, teams
	return f.shares, nil
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
func (f allowFake) Profile(context.Context, domain.UserID) (string, string, error) {
	return "", "", nil
}

func TestArtifactAllowUsesTheMembersArtifactPermissions(t *testing.T) {
	members := Members{Store: &allowFake{system: "member", joined: true}}
	got, err := members.ArtifactAllow(context.Background(), domain.UserID{})
	if err != nil {
		t.Fatal(err)
	}
	const want = "artifact:create,artifact:view,artifact:edit,artifact:share,artifact:delete"
	if got != want {
		t.Fatalf("allow = %q", got)
	}
}

func TestArtifactAllowRefusesAPersonWhoHasNotJoined(t *testing.T) {
	members := Members{Store: &allowFake{system: "member"}}
	_, err := members.ArtifactAllow(context.Background(), domain.UserID{})
	if !errors.Is(err, domain.ErrNoSuchMember) {
		t.Fatalf("error = %v", err)
	}
}
