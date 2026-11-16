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
)

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
		return VisibilityPrivate, []string{}, nil
	case VisibilityEveryone:
		if len(teamIDs) > 0 {
			return "", nil, Invalid("An agent shared with everyone takes no teams.")
		}
		return VisibilityEveryone, []string{}, nil
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
