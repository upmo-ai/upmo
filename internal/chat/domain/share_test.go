package domain

import "testing"

func TestCleanVisibilityForAgents(t *testing.T) {
	visibility, ids, err := CleanVisibility(VisibilityEveryone, nil)
	if err != nil || visibility != VisibilityEveryone || len(ids) != 0 {
		t.Fatalf("everyone = %q %v %v", visibility, ids, err)
	}
	visibility, ids, err = CleanVisibility("", nil)
	if err != nil || visibility != VisibilityPrivate || len(ids) != 0 {
		t.Fatalf("default = %q", visibility)
	}
	team := "10000000-0000-7000-8000-000000000007"
	visibility, ids, err = CleanVisibility(VisibilityTeam, []string{team})
	if err != nil || visibility != VisibilityTeam || len(ids) != 1 || ids[0] != team {
		t.Fatalf("team = %q %v %v", visibility, ids, err)
	}
	if _, _, err := CleanVisibility(VisibilityPrivate, []string{team}); err == nil {
		t.Fatal("private with teams was accepted")
	}
}

func TestValidateShareForChats(t *testing.T) {
	ownerID := "018f3b2c-7a10-7c4a-8e11-111111111111"
	bobID := "018f3b2c-7a10-7c4a-8e11-222222222222"
	teamID := "018f3b2c-7a10-7c4a-8e11-333333333333"

	stored, err := ValidateShare(ShareUpdate{
		Visibility: VisibilityPrivate,
		Grants: []ShareGrant{
			{SubjectType: SharePerson, SubjectID: ownerID, Role: ShareOwner},
			{SubjectType: SharePerson, SubjectID: "018F3B2C-7A10-7C4A-8E11-222222222222", Role: ShareEditor},
			{SubjectType: ShareTeam, SubjectID: teamID, Role: ShareViewer},
		},
	}, ownerID)
	if err != nil {
		t.Fatalf("valid share = %v", err)
	}
	if len(stored) != 2 || stored[0].SubjectID != bobID || stored[0].Role != ShareEditor || stored[1].SubjectID != teamID {
		t.Fatalf("stored = %+v", stored)
	}

	cases := []struct {
		name   string
		update ShareUpdate
		want   string
	}{
		{"unknown visibility", ShareUpdate{Visibility: "secret", Grants: []ShareGrant{{SubjectType: SharePerson, SubjectID: ownerID, Role: ShareOwner}}}, "unknown visibility"},
		{"bad subject type", ShareUpdate{Visibility: VisibilityPrivate, Grants: []ShareGrant{{SubjectType: "role", SubjectID: ownerID, Role: ShareOwner}}}, "share with a person or a team"},
		{"bad person id", ShareUpdate{Visibility: VisibilityPrivate, Grants: []ShareGrant{{SubjectType: SharePerson, SubjectID: "bad", Role: ShareEditor}}}, "that person is not a member"},
		{"bad team id", ShareUpdate{Visibility: VisibilityPrivate, Grants: []ShareGrant{{SubjectType: ShareTeam, SubjectID: "bad", Role: ShareViewer}}}, "that team does not exist"},
		{"duplicate grant", ShareUpdate{Visibility: VisibilityPrivate, Grants: []ShareGrant{
			{SubjectType: SharePerson, SubjectID: ownerID, Role: ShareOwner},
			{SubjectType: SharePerson, SubjectID: bobID, Role: ShareEditor},
			{SubjectType: SharePerson, SubjectID: bobID, Role: ShareViewer},
		}}, "that access is listed twice"},
		{"owner role changed", ShareUpdate{Visibility: VisibilityPrivate, Grants: []ShareGrant{
			{SubjectType: SharePerson, SubjectID: ownerID, Role: ShareEditor},
		}}, "the owner cannot be changed"},
		{"owner removed", ShareUpdate{Visibility: VisibilityPrivate, Grants: []ShareGrant{
			{SubjectType: SharePerson, SubjectID: bobID, Role: ShareEditor},
		}}, "the owner cannot be removed"},
		{"other person owner", ShareUpdate{Visibility: VisibilityPrivate, Grants: []ShareGrant{
			{SubjectType: SharePerson, SubjectID: ownerID, Role: ShareOwner},
			{SubjectType: SharePerson, SubjectID: bobID, Role: ShareOwner},
		}}, "a role must be editor or viewer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateShare(tc.update, ownerID)
			refusal, ok := err.(ShareRefusal)
			if !ok || refusal.Text != tc.want {
				t.Fatalf("err = %v, want ShareRefusal(%q)", err, tc.want)
			}
		})
	}
}
