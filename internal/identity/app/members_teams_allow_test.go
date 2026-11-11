package app

import (
	"context"
	"testing"

	"github.com/upmo-ai/upmo/internal/identity/domain"
)

func TestTeamsAllowIsTeamsManageForAnAdminAndNothingForAMember(t *testing.T) {
	for _, tc := range []struct {
		system string
		joined bool
		want   string
	}{
		{"owner", true, "teams:manage"},
		{"admin", true, "teams:manage"},
		{"member", true, ""},
		{"viewer", true, ""},
		// Not joined is no member at all: no permission, and no error.
		{"admin", false, ""},
	} {
		members := Members{Store: &allowFake{system: tc.system, joined: tc.joined}}
		got, err := members.TeamsAllow(context.Background(), domain.UserID{})
		if err != nil || got != tc.want {
			t.Errorf("%s joined=%v: allow = %q, err = %v, want %q", tc.system, tc.joined, got, err, tc.want)
		}
	}
}
