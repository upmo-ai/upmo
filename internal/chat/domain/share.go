package domain

import (
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
)

const (
	VisibilityPrivate  = "private"
	VisibilityTeam     = "team"
	VisibilityEveryone = "everyone"

	SharePerson = "person"
	ShareTeam   = "team"

	ShareOwner  = "owner"
	ShareEditor = "editor"
	ShareViewer = "viewer"
)

// ShareGrant is one member entry of a chat share: a person or a team with a role.
type ShareGrant struct {
	SubjectType string
	SubjectID   string
	Role        string
}

// ShareUpdate is one PUT body of a chat share. TeamID is optional and read
// only for team visibility.
type ShareUpdate struct {
	Visibility string
	TeamID     string
	Grants     []ShareGrant
}

// ShareRefusal is one refused chat share update. Text is the whole message.
type ShareRefusal struct {
	Text string
}

func (r ShareRefusal) Error() string { return "chat: share: " + r.Text }

// ValidateShare checks one share update against the chat's owner and returns
// the grants to store: every entry but the owner's. The owner entry must
// appear exactly once with the owner role, every other role is editor or
// viewer, and a subject appears once.
func ValidateShare(update ShareUpdate, ownerID string) ([]ShareGrant, error) {
	switch update.Visibility {
	case VisibilityPrivate, VisibilityTeam, VisibilityEveryone:
	default:
		return nil, ShareRefusal{Text: "unknown visibility"}
	}
	seen := map[string]bool{}
	var stored []ShareGrant
	ownerListed := false
	for _, grant := range update.Grants {
		switch grant.SubjectType {
		case SharePerson, ShareTeam:
		default:
			return nil, ShareRefusal{Text: "share with a person or a team"}
		}
		id, err := uuid.Parse(grant.SubjectID)
		if err != nil {
			if grant.SubjectType == SharePerson {
				return nil, ShareRefusal{Text: "that person is not a member"}
			}
			return nil, ShareRefusal{Text: "that team does not exist"}
		}
		grant.SubjectID = id.String()
		key := grant.SubjectType + ":" + grant.SubjectID
		if seen[key] {
			return nil, ShareRefusal{Text: "that access is listed twice"}
		}
		seen[key] = true
		if grant.SubjectType == SharePerson && grant.SubjectID == ownerID {
			if grant.Role != ShareOwner {
				return nil, ShareRefusal{Text: "the owner cannot be changed"}
			}
			ownerListed = true
			continue
		}
		switch grant.Role {
		case ShareEditor, ShareViewer:
		default:
			return nil, ShareRefusal{Text: "a role must be editor or viewer"}
		}
		stored = append(stored, grant)
	}
	if !ownerListed {
		return nil, ShareRefusal{Text: "the owner cannot be removed"}
	}
	return stored, nil
}

const MaxShareTeams = 50

// CleanVisibility checks what a person asked for and returns the visibility an
// agent will have, and the teams for VisibilityTeam.
func CleanVisibility(visibility string, teamIDs []string) (string, []string, error) {
	if visibility == "" {
		visibility = VisibilityPrivate
	}
	switch visibility {
	case VisibilityPrivate:
		if len(teamIDs) > 0 {
			return "", nil, Invalid("A private agent takes no teams.")
		}
		return VisibilityPrivate, nil, nil
	case VisibilityEveryone:
		if len(teamIDs) > 0 {
			return "", nil, Invalid("An agent shared with everyone takes no teams.")
		}
		return VisibilityEveryone, nil, nil
	case VisibilityTeam:
		ids, err := cleanTeamIDs(teamIDs)
		if err != nil {
			return "", nil, err
		}
		return VisibilityTeam, ids, nil
	}
	return "", nil, Invalid("Choose private, team or everyone.")
}

func cleanTeamIDs(in []string) ([]string, error) {
	if len(in) == 0 {
		return nil, Invalid("Choose at least one team.")
	}
	if len(in) > MaxShareTeams {
		return nil, Invalid(fmt.Sprintf("Share with up to %d teams.", MaxShareTeams))
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, id := range in {
		id = strings.ToLower(strings.TrimSpace(id))
		if _, err := uuid.Parse(id); err != nil {
			return nil, Invalid("Choose teams from the list.")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}
