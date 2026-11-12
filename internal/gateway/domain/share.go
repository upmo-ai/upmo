package domain

import (
	"fmt"
	"sort"
	"strings"
)

// Who an account is for. Private is the owner's alone. Team is the teams in
// TeamIDs. Everyone is the whole workspace. A house account is everyone and
// is not written here.
const (
	VisibilityPrivate  = "private"
	VisibilityTeam     = "team"
	VisibilityEveryone = "everyone"
)

// SharePermission is the install permission that lets a person create, change
// and delete shared accounts. A private account needs no extra permission.
const SharePermission = "settings:manage"

// MaxShareTeams bounds the teams one account is shared with.
const MaxShareTeams = 50

// Share is how an account is shared, as a person sees it.
type Share struct {
	// Visibility is VisibilityPrivate, VisibilityTeam or VisibilityEveryone.
	Visibility string
	// TeamIDs are the teams a team account is shared with, and TeamNames their
	// names in the same order. Both are empty for any other visibility.
	TeamIDs   []string
	TeamNames []string
	// House marks an account read from the environment. It is for everyone
	// and read only: nobody changes it here.
	House bool
	// CanManage says whether the person may change or remove the account.
	CanManage bool
	// CanUse says whether the person may route a call through the account.
	// An admin can manage a team account they are not on, and still not use it.
	CanUse bool
	// IsOwner says the account is this person's and not a house account.
	// It is a display flag. Access and tiering follow Visibility and House.
	IsOwner bool
}

// CleanVisibility checks what a person asked for and returns the visibility
// the account will have, and the teams for VisibilityTeam. Private and
// everyone take no teams. A team account takes at least one, each a UUID,
// none twice. An empty visibility is private. The ids come back in a fixed
// order.
func CleanVisibility(visibility string, teamIDs []string) (string, []string, error) {
	if visibility == "" {
		visibility = VisibilityPrivate
	}
	switch visibility {
	case VisibilityPrivate:
		if len(teamIDs) > 0 {
			return "", nil, Invalid("A private provider takes no teams.")
		}
		return VisibilityPrivate, nil, nil
	case VisibilityEveryone:
		if len(teamIDs) > 0 {
			return "", nil, Invalid("A provider shared with everyone takes no teams.")
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
		if !accountID.MatchString(id) {
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
