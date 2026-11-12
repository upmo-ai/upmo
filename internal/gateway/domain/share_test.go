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
		{"own", Account{OwnerUserID: "u1", Visibility: VisibilityPrivate}, "u1", ReachOwn, ""},
		{"another person's", Account{OwnerUserID: "u2", Visibility: VisibilityPrivate}, "u1", ReachNone, ""},
		{"nobody's own", Account{OwnerUserID: "u1", Visibility: VisibilityPrivate}, "", ReachNone, ""},
		{"everyone", Account{OwnerUserID: "u2", Visibility: VisibilityEveryone}, "u1", ReachEveryone, ""},
		{"house", Account{House: true, Visibility: VisibilityEveryone}, "u1", ReachHouse, ""},
		{"a team of theirs", Account{OwnerUserID: "u9", Visibility: VisibilityTeam, TeamIDs: []string{team1, team2}}, "u1", ReachTeam, team2},
		{"only other teams", Account{OwnerUserID: "u9", Visibility: VisibilityTeam, TeamIDs: []string{team1}}, "u1", ReachNone, ""},
		// Two of their teams share it: the lowest team id is the one reported.
		{"two teams of theirs", Account{OwnerUserID: "u9", Visibility: VisibilityTeam, TeamIDs: []string{team3, team2}}, "u1", ReachTeam, team2},
		// The owner of a team account they are not on does not reach it as their own.
		{"owner off the team", Account{OwnerUserID: "u1", Visibility: VisibilityTeam, TeamIDs: []string{team1}}, "u1", ReachNone, ""},
	} {
		reach, via := tc.account.ReachedBy(tc.user, teams)
		if reach != tc.reach || via != tc.via {
			t.Errorf("%s: reach = %v via %q, want %v via %q", tc.name, reach, via, tc.reach, tc.via)
		}
	}
	// A person on no team reaches no team account.
	if reach, _ := (Account{Visibility: VisibilityTeam, TeamIDs: []string{team1}}).ReachedBy("u1", nil); reach != ReachNone {
		t.Errorf("a person on no team reached a team account: %v", reach)
	}
}

func TestLinksForUserIsTheUnionOwnThenTeamThenWorkspace(t *testing.T) {
	own := Account{ID: "own", OwnerUserID: "u1", Visibility: VisibilityPrivate}
	other := Account{ID: "other", OwnerUserID: "u2", Visibility: VisibilityPrivate}
	team := Account{ID: "team", OwnerUserID: "u9", Visibility: VisibilityTeam, TeamIDs: []string{team1}}
	foreign := Account{ID: "foreign", OwnerUserID: "u9", Visibility: VisibilityTeam, TeamIDs: []string{team3}}
	everyone := Account{ID: "all", OwnerUserID: "u9", Visibility: VisibilityEveryone}
	// The house link has the lowest position and the own link the highest,
	// so the order below is the tiers' and not the route's.
	r := Route{Name: "chat", Capability: CapChat, Links: []Link{
		{Position: 1, Account: house(), Model: "house"},
		{Position: 2, Account: other, Model: "other"},
		{Position: 3, Account: foreign, Model: "foreign"},
		{Position: 4, Account: team, Model: "team"},
		{Position: 5, Account: own, Model: "own"},
		{Position: 6, Account: everyone, Model: "everyone"},
	}}
	got, err := r.LinksForUser("u1", []string{team1, team2})
	if err != nil {
		t.Fatal(err)
	}
	if models(got) != "own,team,everyone,house" {
		t.Fatalf("chain = %s, want own,team,everyone,house", models(got))
	}
	if got[1].ViaTeam != team1 || got[0].ViaTeam != "" || got[2].ViaTeam != "" || got[3].ViaTeam != "" {
		t.Fatalf("via teams = %q %q %q %q", got[0].ViaTeam, got[1].ViaTeam, got[2].ViaTeam, got[3].ViaTeam)
	}
	// Someone on no team gets everyone, then the house account.
	got, err = r.LinksForUser("u8", nil)
	if err != nil || models(got) != "everyone,house" {
		t.Fatalf("outsider chain = %s, %v", models(got), err)
	}
	// Inside a tier the route's own order holds.
	more := Route{Name: "chat", Capability: CapChat, Links: []Link{
		{Position: 1, Account: team, Model: "t-first"},
		{Position: 2, Account: own, Model: "o-second"},
		{Position: 3, Account: Account{ID: "team-b", Visibility: VisibilityTeam, TeamIDs: []string{team2}}, Model: "t-third"},
		{Position: 4, Account: Account{ID: "own-b", OwnerUserID: "u1", Visibility: VisibilityPrivate}, Model: "o-fourth"},
	}}
	got, err = more.LinksForUser("u1", []string{team1, team2})
	if err != nil || models(got) != "o-second,o-fourth,t-first,t-third" {
		t.Fatalf("chain = %s, %v", models(got), err)
	}
}

func TestAnAccountReachedThroughTwoTeamsAppearsOnce(t *testing.T) {
	both := Account{ID: "both", Visibility: VisibilityTeam, TeamIDs: []string{team1, team2}}
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
		{Position: 1, Account: Account{ID: "x", OwnerUserID: "u2", Visibility: VisibilityPrivate}, Model: "x"},
		{Position: 2, Account: Account{ID: "y", Visibility: VisibilityTeam, TeamIDs: []string{team3}}, Model: "y"},
	}}
	if _, err := r.LinksForUser("u1", []string{team1}); err == nil {
		t.Fatal("a person with no usable link was given a chain")
	}
}

func TestFeature_UPM277_CleanVisibility(t *testing.T) {
	visibility, ids, err := CleanVisibility(VisibilityEveryone, nil)
	if err != nil || visibility != VisibilityEveryone || len(ids) != 0 {
		t.Fatalf("everyone = %q %v %v", visibility, ids, err)
	}
	visibility, ids, err = CleanVisibility("", nil)
	if err != nil || visibility != VisibilityPrivate || len(ids) != 0 {
		t.Fatalf("private = %q %v %v", visibility, ids, err)
	}
	visibility, ids, err = CleanVisibility(VisibilityTeam, []string{" " + strings.ToUpper(team2) + " ", team1, team2})
	if err != nil || visibility != VisibilityTeam || strings.Join(ids, ",") != team1+","+team2 {
		t.Fatalf("teams = %q %v %v", visibility, ids, err)
	}
	for name, tc := range map[string]struct {
		ask   string
		teams []string
	}{
		"everyone with teams": {VisibilityEveryone, []string{team1}},
		"private with teams":  {VisibilityPrivate, []string{team1}},
		"team with none":      {VisibilityTeam, nil},
		"team not a uuid":     {VisibilityTeam, []string{"finance"}},
		"unknown visibility":  {"workspace", nil},
		"too many teams":      {VisibilityTeam, manyTeams(MaxShareTeams + 1)},
	} {
		_, _, err := CleanVisibility(tc.ask, tc.teams)
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
