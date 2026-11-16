package domain

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestParseWorkspaceUpdate(t *testing.T) {
	agentID := uuid.MustParse("0199a2f5-7a01-7c1e-9b3a-5c2d8e4f6a01")
	valid := WorkspaceUpdate{
		Name:                  "  Acme Corp  ",
		LogoURL:               " https://example.com/logo.png ",
		TimeZone:              "America/New_York",
		Locale:                "en-US",
		DefaultRoute:          " default-chat ",
		DefaultAgentID:        &agentID,
		AllowMemberProviders:  true,
		AllowMemberRoutes:     false,
		AllowMemberConnectors: true,
		AllowMemberAgents:     false,
		SSORequired:           true,
	}
	got, err := ParseWorkspaceUpdate(valid)
	if err != nil {
		t.Fatalf("valid workspace update failed: %v", err)
	}
	if got.Name != "Acme Corp" || got.LogoURL != "https://example.com/logo.png" || got.DefaultRoute != "default-chat" {
		t.Fatalf("trimmed = %+v", got)
	}
	if got.DefaultAgentID == nil || *got.DefaultAgentID != agentID {
		t.Fatalf("agent id = %v", got.DefaultAgentID)
	}

	utc := valid
	utc.TimeZone = "UTC"
	utc.LogoURL = ""
	utc.DefaultAgentID = nil
	if _, err := ParseWorkspaceUpdate(utc); err != nil {
		t.Fatalf("UTC update failed: %v", err)
	}

	invalidCases := []struct {
		name string
		edit func(*WorkspaceUpdate)
	}{
		{"empty name", func(w *WorkspaceUpdate) { w.Name = "   " }},
		{"overlong name", func(w *WorkspaceUpdate) { w.Name = strings.Repeat("a", 121) }},
		{"control name", func(w *WorkspaceUpdate) { w.Name = "a\nb" }},
		{"javascript logo", func(w *WorkspaceUpdate) { w.LogoURL = "javascript:alert(1)" }},
		{"relative logo", func(w *WorkspaceUpdate) { w.LogoURL = "/logo.png" }},
		{"userinfo logo", func(w *WorkspaceUpdate) { w.LogoURL = "https://user:pass@example.com/logo.png" }},
		{"mars timezone", func(w *WorkspaceUpdate) { w.TimeZone = "Mars/Olympus" }},
		{"empty timezone", func(w *WorkspaceUpdate) { w.TimeZone = "" }},
		{"local timezone", func(w *WorkspaceUpdate) { w.TimeZone = "Local" }},
		{"empty locale", func(w *WorkspaceUpdate) { w.Locale = "  " }},
		{"space locale", func(w *WorkspaceUpdate) { w.Locale = "en US" }},
		{"control route", func(w *WorkspaceUpdate) { w.DefaultRoute = "a\x00b" }},
	}
	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			in := valid
			tc.edit(&in)
			_, err := ParseWorkspaceUpdate(in)
			if !errors.Is(err, ErrInvalidWorkspace) {
				t.Fatalf("err = %v, want ErrInvalidWorkspace", err)
			}
		})
	}
}

func TestCheckSSOSignIn(t *testing.T) {
	if bg, err := CheckSSOSignIn(false, "member", false); err != nil || bg {
		t.Fatalf("sso off member = (%t, %v)", bg, err)
	}
	if bg, err := CheckSSOSignIn(true, "member", true); err != nil || bg {
		t.Fatalf("sso flow member = (%t, %v)", bg, err)
	}
	if bg, err := CheckSSOSignIn(true, "owner", true); err != nil || bg {
		t.Fatalf("sso flow owner = (%t, %v)", bg, err)
	}
	if bg, err := CheckSSOSignIn(true, "owner", false); err != nil || !bg {
		t.Fatalf("owner break-glass = (%t, %v), want (true, nil)", bg, err)
	}
	for _, role := range []string{"admin", "member", "viewer", ""} {
		if bg, err := CheckSSOSignIn(true, role, false); !errors.Is(err, ErrSSORequired) || bg {
			t.Fatalf("role %q = (%t, %v), want (false, ErrSSORequired)", role, bg, err)
		}
	}
}

func TestMayManageWorkspace(t *testing.T) {
	if err := MayManageWorkspace(Subject{Permissions: []Permission{ObjectPermission(TypeTask, ActionView)}}); !errors.Is(err, ErrSettingsOnly) {
		t.Fatalf("err = %v, want ErrSettingsOnly", err)
	}
	if err := MayManageWorkspace(Subject{Permissions: []Permission{PermSettingsManage}}); err != nil {
		t.Fatalf("settings:manage refused: %v", err)
	}
}
