package domain

import (
	"github.com/google/uuid"
)

// The visibilities a task share stores.
const (
	VisibilityPrivate  = "private"
	VisibilityTeam     = "team"
	VisibilityEveryone = "everyone"
)

// The subjects and roles a share names.
const (
	SharePerson = "person"
	ShareTeam   = "team"

	ShareOwner  = "owner"
	ShareEditor = "editor"
	ShareViewer = "viewer"
)

// ShareGrant is one member entry of a share: a person or a team with a role.
type ShareGrant struct {
	SubjectType string
	SubjectID   string
	Role        string
}

// ShareUpdate is one PUT body of a task share. TeamID is optional and read
// only for team visibility.
type ShareUpdate struct {
	Visibility string
	TeamID     string
	Grants     []ShareGrant
}

// ShareRefusal is one refused share update. Text is the whole message.
type ShareRefusal struct {
	Text string
}

func (r ShareRefusal) Error() string { return "tasks: share: " + r.Text }

// ValidateShare checks one share update against the task's owner and returns
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
		// The canonical text, so the same id in capitals is still the same subject.
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
