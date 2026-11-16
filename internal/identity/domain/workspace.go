package domain

import (
	"errors"
	"net/url"
	"strings"
	"time"
	_ "time/tzdata" // Embed IANA zone database so LoadLocation works regardless of host zoneinfo.
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

var (
	// ErrSSORequired refuses non-SSO sign-in for non-owners when the workspace requires SSO.
	ErrSSORequired = errors.New("identity: sso required")
	// ErrSettingsOnly refuses a person who cannot manage workspace settings.
	ErrSettingsOnly = errors.New("identity: you cannot manage workspace settings")
	// ErrInvalidWorkspace refuses workspace settings that fail domain validation.
	ErrInvalidWorkspace = errors.New("identity: invalid workspace settings")
)

// WorkspaceError explains why a workspace update was refused.
type WorkspaceError struct {
	Message string
}

func (e WorkspaceError) Error() string { return e.Message }

func (e WorkspaceError) Unwrap() error { return ErrInvalidWorkspace }

// Workspace is the single-row install workspace configuration.
type Workspace struct {
	Name                  string
	LogoURL               string
	TimeZone              string
	Locale                string
	DefaultRoute          string
	DefaultAgentID        *uuid.UUID
	AllowMemberProviders  bool
	AllowMemberRoutes     bool
	AllowMemberConnectors bool
	AllowMemberAgents     bool
	SSORequired           bool
	UpdatedAt             time.Time
}

// WorkspaceUpdate is the validated write payload for workspace settings.
type WorkspaceUpdate struct {
	Name                  string
	LogoURL               string
	TimeZone              string
	Locale                string
	DefaultRoute          string
	DefaultAgentID        *uuid.UUID
	AllowMemberProviders  bool
	AllowMemberRoutes     bool
	AllowMemberConnectors bool
	AllowMemberAgents     bool
	SSORequired           bool
}

// ParseWorkspaceUpdate trims and validates every field of a workspace update.
func ParseWorkspaceUpdate(in WorkspaceUpdate) (WorkspaceUpdate, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" || utf8.RuneCountInString(name) > 120 || hasControl(name) {
		return WorkspaceUpdate{}, WorkspaceError{Message: "workspace name must be 1 to 120 characters"}
	}

	logoURL := strings.TrimSpace(in.LogoURL)
	if logoURL != "" {
		if len(logoURL) > 512 || hasControl(logoURL) {
			return WorkspaceUpdate{}, WorkspaceError{Message: "invalid logo_url"}
		}
		parsed, err := url.Parse(logoURL)
		if err != nil {
			return WorkspaceUpdate{}, WorkspaceError{Message: "invalid logo_url"}
		}
		scheme := strings.ToLower(parsed.Scheme)
		if (scheme != "http" && scheme != "https") || parsed.Host == "" || parsed.User != nil {
			return WorkspaceUpdate{}, WorkspaceError{Message: "logo_url must be an http or https URL"}
		}
	}

	tz := strings.TrimSpace(in.TimeZone)
	if tz == "" || strings.EqualFold(tz, "Local") {
		return WorkspaceUpdate{}, WorkspaceError{Message: "invalid time_zone"}
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return WorkspaceUpdate{}, WorkspaceError{Message: "invalid time_zone"}
	}

	locale := strings.TrimSpace(in.Locale)
	if locale == "" || len(locale) > 35 || hasSpaceOrControl(locale) {
		return WorkspaceUpdate{}, WorkspaceError{Message: "invalid locale"}
	}

	defaultRoute := strings.TrimSpace(in.DefaultRoute)
	if utf8.RuneCountInString(defaultRoute) > 128 || hasControl(defaultRoute) {
		return WorkspaceUpdate{}, WorkspaceError{Message: "invalid default_route"}
	}

	var agentID *uuid.UUID
	if in.DefaultAgentID != nil {
		copied := *in.DefaultAgentID
		agentID = &copied
	}

	return WorkspaceUpdate{
		Name:                  name,
		LogoURL:               logoURL,
		TimeZone:              tz,
		Locale:                locale,
		DefaultRoute:          defaultRoute,
		DefaultAgentID:        agentID,
		AllowMemberProviders:  in.AllowMemberProviders,
		AllowMemberRoutes:     in.AllowMemberRoutes,
		AllowMemberConnectors: in.AllowMemberConnectors,
		AllowMemberAgents:     in.AllowMemberAgents,
		SSORequired:           in.SSORequired,
	}, nil
}

// MayManageWorkspace requires settings:manage.
func MayManageWorkspace(actor Subject) error {
	if !has(known(actor.Permissions), PermSettingsManage) {
		return ErrSettingsOnly
	}
	return nil
}

// CheckSSOSignIn decides whether a sign-in is allowed under the workspace SSO
// requirement. When sso_required is true, non-SSO flows are refused for
// non-owners and allowed as break-glass for owners.
func CheckSSOSignIn(ssoRequired bool, roleKey string, isSSO bool) (breakGlass bool, err error) {
	if !ssoRequired || isSSO {
		return false, nil
	}
	if roleKey == string(RoleOwner) {
		return true, nil
	}
	return false, ErrSSORequired
}

func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func hasSpaceOrControl(s string) bool {
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return true
		}
	}
	return false
}
