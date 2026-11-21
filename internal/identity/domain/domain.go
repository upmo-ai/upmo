// Package domain holds the identity values the database checks enforce.
// It does no I/O.
package domain

import (
	"fmt"

	"github.com/upmo-ai/upmo/internal/kernel"
)

type userID struct{}
type teamID struct{}
type shareID struct{}
type roleID struct{}

// UserID identifies a person.
type UserID = kernel.ID[userID]

// TeamID identifies a team.
type TeamID = kernel.ID[teamID]

// ShareID identifies a grant.
type ShareID = kernel.ID[shareID]

// RoleID identifies a role row. System roles and custom roles share it.
type RoleID = kernel.ID[roleID]

// ParseUserID parses a person id.
func ParseUserID(s string) (UserID, error) { return kernel.ParseID[userID](s) }

// ParseTeamID parses a team id.
func ParseTeamID(s string) (TeamID, error) { return kernel.ParseID[teamID](s) }

// ParseRoleID parses a role id.
func ParseRoleID(s string) (RoleID, error) { return kernel.ParseID[roleID](s) }

// Role is a person's role in the install.
type Role string

const (
	RoleOwner  Role = "owner"
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
	RoleViewer Role = "viewer"
)

// Roles are the membership roles the database accepts.
func Roles() []Role {
	return []Role{RoleOwner, RoleAdmin, RoleMember, RoleViewer}
}

// ParseRole rejects an unknown role.
func ParseRole(s string) (Role, error) {
	for _, role := range Roles() {
		if s == string(role) {
			return role, nil
		}
	}
	return "", fmt.Errorf("identity: unknown role %q", s)
}

// Visibility is who can see a work object before any share.
type Visibility string

const (
	VisibilityPrivate  Visibility = "private"
	VisibilityTeam     Visibility = "team"
	VisibilityEveryone Visibility = "everyone"
)

// Visibilities are the visibility values a work object may store.
func Visibilities() []Visibility {
	return []Visibility{VisibilityPrivate, VisibilityTeam, VisibilityEveryone}
}

// ParseVisibility rejects an unknown visibility.
func ParseVisibility(s string) (Visibility, error) {
	for _, visibility := range Visibilities() {
		if s == string(visibility) {
			return visibility, nil
		}
	}
	return "", fmt.Errorf("identity: unknown visibility %q", s)
}

// Level is a share grant. The owner is a column on the object, never a level.
type Level string

const (
	LevelEditor Level = "editor"
	LevelViewer Level = "viewer"
)

// Levels are the share levels the database accepts.
func Levels() []Level {
	return []Level{LevelEditor, LevelViewer}
}

// ParseLevel rejects an unknown level, including owner.
func ParseLevel(s string) (Level, error) {
	for _, level := range Levels() {
		if s == string(level) {
			return level, nil
		}
	}
	return "", fmt.Errorf("identity: unknown level %q", s)
}

// Kind is who a principal is.
type Kind string

const (
	KindUser  Kind = "user"
	KindAgent Kind = "agent"
	KindRun   Kind = "run"
	// KindToken is a personal API token.
	KindToken Kind = "token"
)
