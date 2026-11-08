package domain

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const (
	shareOwner  = "018f3b2c-7a10-7c4a-8e11-111111111111"
	sharePerson = "018f3b2c-7a10-7c4a-8e11-222222222222"
	shareTeam   = "018f3b2c-7a10-7c4a-8e11-333333333333"
)

func shareOwnerGrant() ShareGrant {
	return ShareGrant{SubjectType: SharePerson, SubjectID: shareOwner, Role: ShareOwner}
}

func TestValidateShareReturnsTheGrantsToStore(t *testing.T) {
	grants, err := ValidateShare(ShareUpdate{
		Visibility: VisibilityPrivate,
		Grants: []ShareGrant{
			shareOwnerGrant(),
			{SubjectType: SharePerson, SubjectID: sharePerson, Role: ShareViewer},
			{SubjectType: ShareTeam, SubjectID: shareTeam, Role: ShareEditor},
		},
	}, shareOwner)
	if err != nil {
		t.Fatal(err)
	}
	want := []ShareGrant{
		{SubjectType: SharePerson, SubjectID: sharePerson, Role: ShareViewer},
		{SubjectType: ShareTeam, SubjectID: shareTeam, Role: ShareEditor},
	}
	if !reflect.DeepEqual(grants, want) {
		t.Fatalf("grants = %v, want %v", grants, want)
	}
}

func TestValidateShareAcceptsEveryVisibility(t *testing.T) {
	for _, visibility := range []string{VisibilityPrivate, VisibilityTeam, VisibilityEveryone} {
		if _, err := ValidateShare(ShareUpdate{Visibility: visibility, Grants: []ShareGrant{shareOwnerGrant()}}, shareOwner); err != nil {
			t.Fatalf("%s: %v", visibility, err)
		}
	}
}

func TestValidateShareRefusals(t *testing.T) {
	editor := ShareGrant{SubjectType: SharePerson, SubjectID: sharePerson, Role: ShareEditor}
	tests := []struct {
		name   string
		update ShareUpdate
		text   string
	}{
		{
			name:   "unknown visibility",
			update: ShareUpdate{Visibility: "secret", Grants: []ShareGrant{shareOwnerGrant()}},
			text:   "unknown visibility",
		},
		{
			name: "unknown subject type",
			update: ShareUpdate{Visibility: VisibilityPrivate, Grants: []ShareGrant{
				shareOwnerGrant(),
				{SubjectType: "club", SubjectID: shareTeam, Role: ShareViewer},
			}},
			text: "share with a person or a team",
		},
		{
			name: "person id is not an id",
			update: ShareUpdate{Visibility: VisibilityPrivate, Grants: []ShareGrant{
				shareOwnerGrant(),
				{SubjectType: SharePerson, SubjectID: "bob", Role: ShareEditor},
			}},
			text: "that person is not a member",
		},
		{
			name: "team id is not an id",
			update: ShareUpdate{Visibility: VisibilityPrivate, Grants: []ShareGrant{
				shareOwnerGrant(),
				{SubjectType: ShareTeam, SubjectID: "design", Role: ShareViewer},
			}},
			text: "that team does not exist",
		},
		{
			name: "the same person twice",
			update: ShareUpdate{Visibility: VisibilityPrivate, Grants: []ShareGrant{
				shareOwnerGrant(), editor, editor,
			}},
			text: "that access is listed twice",
		},
		{
			name: "the owner twice",
			update: ShareUpdate{Visibility: VisibilityPrivate, Grants: []ShareGrant{
				shareOwnerGrant(), shareOwnerGrant(),
			}},
			text: "that access is listed twice",
		},
		{
			name: "the owner twice, once in capitals",
			update: ShareUpdate{Visibility: VisibilityPrivate, Grants: []ShareGrant{
				shareOwnerGrant(),
				{SubjectType: SharePerson, SubjectID: strings.ToUpper(shareOwner), Role: ShareEditor},
			}},
			text: "that access is listed twice",
		},
		{
			name: "the owner role changed",
			update: ShareUpdate{Visibility: VisibilityPrivate, Grants: []ShareGrant{
				{SubjectType: SharePerson, SubjectID: shareOwner, Role: ShareEditor},
			}},
			text: "the owner cannot be changed",
		},
		{
			name: "the owner missing",
			update: ShareUpdate{Visibility: VisibilityPrivate, Grants: []ShareGrant{
				editor,
			}},
			text: "the owner cannot be removed",
		},
		{
			name:   "no members",
			update: ShareUpdate{Visibility: VisibilityPrivate, Grants: []ShareGrant{}},
			text:   "the owner cannot be removed",
		},
		{
			name: "the owner role for another person",
			update: ShareUpdate{Visibility: VisibilityPrivate, Grants: []ShareGrant{
				shareOwnerGrant(),
				{SubjectType: SharePerson, SubjectID: sharePerson, Role: ShareOwner},
			}},
			text: "a role must be editor or viewer",
		},
		{
			name: "an unknown role",
			update: ShareUpdate{Visibility: VisibilityPrivate, Grants: []ShareGrant{
				shareOwnerGrant(),
				{SubjectType: SharePerson, SubjectID: sharePerson, Role: "chief"},
			}},
			text: "a role must be editor or viewer",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ValidateShare(test.update, shareOwner)
			var refusal ShareRefusal
			if !errors.As(err, &refusal) || refusal.Text != test.text {
				t.Fatalf("err = %v, want %q", err, test.text)
			}
		})
	}
}
