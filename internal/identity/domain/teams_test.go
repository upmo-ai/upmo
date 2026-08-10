package domain

import "testing"

func TestParseTeamName(t *testing.T) {
	name, err := ParseTeamName("  Finance  ")
	if err != nil || name != "Finance" {
		t.Fatalf("name %q %v", name, err)
	}
	if _, err := ParseTeamName("  "); err != ErrBadTeam {
		t.Fatalf("blank %v", err)
	}
	long := ""
	for range 65 {
		long += "a"
	}
	if _, err := ParseTeamName(long); err != ErrBadTeam {
		t.Fatalf("long %v", err)
	}
}

func TestMayManageTeams(t *testing.T) {
	if err := MayManageTeams(Subject{Permissions: []Permission{PermTeamsManage}}); err != nil {
		t.Fatal(err)
	}
	if err := MayManageTeams(Subject{Permissions: []Permission{PermMembersManage}}); err != ErrTeamsOnly {
		t.Fatalf("member %v", err)
	}
}
