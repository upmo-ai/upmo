package domain

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

const (
	team1 = "018f3b2c-7a10-7c4a-8e11-0000000000a1"
	team2 = "018f3b2c-7a10-7c4a-8e11-0000000000a2"
	team3 = "018f3b2c-7a10-7c4a-8e11-0000000000a3"
)

func models(links []Link) string {
	var out []string
	for _, l := range links {
		out = append(out, l.Model)
	}
	return strings.Join(out, ",")
}

func TestReachedByTellsHowAPersonComesToAnAccount(t *testing.T) {
	teams := []string{team2, team3}
	for _, tc := range []struct {
		name    string
		account Account
		user    string
		reach   Reach
		via     string
	}{
		{"own", Account{OwnerUserID: "u1"}, "u1", ReachOwn, ""},
		{"another person's", Account{OwnerUserID: "u2"}, "u1", ReachNone, ""},
		{"nobody's own", Account{OwnerUserID: "u1"}, "", ReachNone, ""},
		{"workspace", Account{}, "u1", ReachWorkspace, ""},
		{"house", Account{House: true}, "u1", ReachWorkspace, ""},
		{"a team of theirs", Account{TeamIDs: []string{team1, team2}}, "u1", ReachTeam, team2},
		{"only other teams", Account{TeamIDs: []string{team1}}, "u1", ReachNone, ""},
		// Two of their teams share it: the lowest team id is the one reported.
		{"two teams of theirs", Account{TeamIDs: []string{team3, team2}}, "u1", ReachTeam, team2},
	} {
		reach, via := tc.account.ReachedBy(tc.user, teams)
		if reach != tc.reach || via != tc.via {
			t.Errorf("%s: reach = %v via %q, want %v via %q", tc.name, reach, via, tc.reach, tc.via)
		}
	}
	// A person on no team reaches no team account.
	if reach, _ := (Account{TeamIDs: []string{team1}}).ReachedBy("u1", nil); reach != ReachNone {
		t.Errorf("a person on no team reached a team account: %v", reach)
	}
}

func TestLinksForUserIsTheUnionOwnThenTeamThenWorkspace(t *testing.T) {
	own := Account{ID: "own", OwnerUserID: "u1"}
	other := Account{ID: "other", OwnerUserID: "u2"}
	team := Account{ID: "team", TeamIDs: []string{team1}}
	foreign := Account{ID: "foreign", TeamIDs: []string{team3}}
	// The workspace link has the lowest position and the own link the highest,
	// so the order below is the tiers' and not the route's.
	r := Route{Name: "chat", Capability: CapChat, Links: []Link{
		{Position: 1, Account: house(), Model: "workspace"},
		{Position: 2, Account: other, Model: "other"},
		{Position: 3, Account: foreign, Model: "foreign"},
		{Position: 4, Account: team, Model: "team"},
		{Position: 5, Account: own, Model: "own"},
	}}
	got, err := r.LinksForUser("u1", []string{team1, team2})
	if err != nil {
		t.Fatal(err)
	}
	if models(got) != "own,team,workspace" {
		t.Fatalf("chain = %s, want own,team,workspace", models(got))
	}
	if got[1].ViaTeam != team1 || got[0].ViaTeam != "" || got[2].ViaTeam != "" {
		t.Fatalf("via teams = %q %q %q", got[0].ViaTeam, got[1].ViaTeam, got[2].ViaTeam)
	}
	// Someone on no team gets the workspace link only.
	got, err = r.LinksForUser("u9", nil)
	if err != nil || models(got) != "workspace" {
		t.Fatalf("outsider chain = %s, %v", models(got), err)
	}
	// Inside a tier the route's own order holds.
	more := Route{Name: "chat", Capability: CapChat, Links: []Link{
		{Position: 1, Account: team, Model: "t-first"},
		{Position: 2, Account: own, Model: "o-second"},
		{Position: 3, Account: Account{ID: "team-b", TeamIDs: []string{team2}}, Model: "t-third"},
		{Position: 4, Account: Account{ID: "own-b", OwnerUserID: "u1"}, Model: "o-fourth"},
	}}
	got, err = more.LinksForUser("u1", []string{team1, team2})
	if err != nil || models(got) != "o-second,o-fourth,t-first,t-third" {
		t.Fatalf("chain = %s, %v", models(got), err)
	}
}

func TestAnAccountReachedThroughTwoTeamsAppearsOnce(t *testing.T) {
	both := Account{ID: "both", TeamIDs: []string{team1, team2}}
	r := Route{Name: "chat", Capability: CapChat, Links: []Link{
		{Position: 1, Account: both, Model: "m"},
		// The same link read once more, as a join over the two teams would.
		{Position: 1, Account: both, Model: "m"},
		{Position: 2, Account: both, Model: "m2"},
	}}
	got, err := r.LinksForUser("u1", []string{team2, team1})
	if err != nil {
		t.Fatal(err)
	}
	if models(got) != "m,m2" {
		t.Fatalf("chain = %s, want each link once", models(got))
	}
	// And the team it is reached through is the lowest of the two.
	if got[0].ViaTeam != team1 || got[1].ViaTeam != team1 {
		t.Fatalf("via = %q, %q, want %q", got[0].ViaTeam, got[1].ViaTeam, team1)
	}
}

func TestLinksForUserWithNothingUsableIsAnError(t *testing.T) {
	r := Route{Name: "chat", Capability: CapChat, Links: []Link{
		{Position: 1, Account: Account{ID: "x", OwnerUserID: "u2"}, Model: "x"},
		{Position: 2, Account: Account{ID: "y", TeamIDs: []string{team3}}, Model: "y"},
	}}
	if _, err := r.LinksForUser("u1", []string{team1}); err == nil {
		t.Fatal("a person with no usable link was given a chain")
	}
}

func TestCleanShare(t *testing.T) {
	scope, ids, err := CleanShare(ShareWorkspace, nil)
	if err != nil || scope != ScopeWorkspace || len(ids) != 0 {
		t.Fatalf("workspace = %q %v %v", scope, ids, err)
	}
	scope, ids, err = CleanShare(ShareTeams, []string{" " + strings.ToUpper(team2) + " ", team1, team2})
	if err != nil || scope != ScopeTeam || strings.Join(ids, ",") != team1+","+team2 {
		t.Fatalf("teams = %q %v %v", scope, ids, err)
	}
	for name, tc := range map[string]struct {
		ask   string
		teams []string
	}{
		"workspace with teams": {ShareWorkspace, []string{team1}},
		"teams with none":      {ShareTeams, nil},
		"teams not a uuid":     {ShareTeams, []string{"finance"}},
		"no scope":             {"", []string{team1}},
		"unknown scope":        {"everyone", nil},
		"too many teams":       {ShareTeams, manyTeams(MaxShareTeams + 1)},
	} {
		_, _, err := CleanShare(tc.ask, tc.teams)
		var invalid Invalid
		if !errors.As(err, &invalid) || string(invalid) == "" {
			t.Errorf("%s: err = %v, want a plain refusal", name, err)
		}
	}
}

func manyTeams(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("018f3b2c-7a10-7c4a-8e11-%012d", i)
	}
	return out
}
