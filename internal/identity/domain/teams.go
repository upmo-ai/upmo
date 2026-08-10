package domain

import (
	"errors"
	"strings"
	"unicode/utf8"
)

var (
	// ErrTeamsOnly refuses a person who cannot manage teams.
	ErrTeamsOnly = errors.New("identity: you cannot manage teams")
	// ErrBadTeam refuses a team name that cannot be stored.
	ErrBadTeam = errors.New("identity: a team needs a name")
	// ErrTeamNameTaken refuses a team name already in use.
	ErrTeamNameTaken = errors.New("identity: a team with that name already exists")
	// ErrNoSuchTeam is an unknown team.
	ErrNoSuchTeam = errors.New("identity: no such team")
)

// Team is a named group of joined people.
type Team struct {
	ID   TeamID
	Name string
}

// ParseTeamName trims a team name and refuses an empty or overlong one.
func ParseTeamName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 64 {
		return "", ErrBadTeam
	}
	return name, nil
}

// MayManageTeams requires teams:manage.
func MayManageTeams(actor Subject) error {
	if !has(actor.Permissions, PermTeamsManage) {
		return ErrTeamsOnly
	}
	return nil
}
