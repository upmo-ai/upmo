package domain

import (
	"fmt"
	"sort"
	"strings"
)

// What a person may ask for when they share an account: the whole workspace,
// or specific teams.
const (
	ShareWorkspace = "workspace"
	ShareTeams     = "teams"
)

// Who an account is for, as a person is told. A personal account is "own".
const (
	ScopeOwn       = "own"
	ScopeWorkspace = "workspace"
	ScopeTeam      = "team"
)

// SharePermission is the install permission that lets a person create, change
// and delete shared accounts.
const SharePermission = "teams:manage"

// MaxShareTeams bounds the teams one account is shared with.
const MaxShareTeams = 50

// Share is how an account is shared, as a person sees it.
type Share struct {
	// Scope is ScopeOwn, ScopeWorkspace or ScopeTeam.
	Scope string
	// TeamIDs are the teams a team account is shared with, and TeamNames their
	// names in the same order. Both are empty for any other scope.
	TeamIDs   []string
	TeamNames []string
	// House marks an account read from the environment. It is workspace-wide
	// and read only: nobody changes it here.
	House bool
	// CanManage says whether the person may change or remove the account.
	CanManage bool
}

// CleanShare checks what a person asked for and returns the scope the account
// will have, and the teams for ScopeTeam. The whole workspace takes no teams;
// specific teams take at least one, each a UUID, none twice. The ids come
// back in a fixed order.
func CleanShare(ask string, teamIDs []string) (string, []string, error) {
	switch ask {
	case ShareWorkspace:
		if len(teamIDs) > 0 {
			return "", nil, Invalid("Sharing with the whole workspace takes no teams.")
		}
		return ScopeWorkspace, nil, nil
	case ShareTeams:
		ids, err := cleanTeamIDs(teamIDs)
		if err != nil {
			return "", nil, err
		}
		return ScopeTeam, ids, nil
	}
	return "", nil, Invalid("Share with the whole workspace or with specific teams.")
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
