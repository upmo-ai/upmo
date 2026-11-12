package domain

import (
	"fmt"
	"slices"
)

const (
	PlanMetered      = "metered"
	PlanSubscription = "subscription"
	PlanLocal        = "local"
)

// Account is one provider login from the routing tables. Adapter and model
// names live here so dispatch code never names a provider.
type Account struct {
	ID            string
	Adapter       string
	CredentialRef string
	PlanKind      string
	House         bool
	BaseURL       string
	// OwnerUserID is the account's owner. It is empty only for a house
	// account: every other account has an owner, shared ones included.
	// Reaching an account follows Visibility, not whether an owner is set.
	OwnerUserID string
	// Visibility is private, team or everyone.
	Visibility string
	// TeamIDs are the teams a team account is shared with. Empty otherwise.
	TeamIDs []string
}

// Link is one hop on a route, in position order.
type Link struct {
	Position    int
	Account     Account
	Model       string
	Weight      int
	RetryBudget int
	// Need is the capabilities this hop requires. Empty means no extra demand.
	Need []string
	// ViaTeam is the team through which the person reached this hop's team
	// account, for the ledger. It is empty for their own account, one shared
	// with everyone, a house account, and any hop not chosen for a person.
	ViaTeam string
}

// Reach is how a person comes to use an account.
type Reach int

// The ways to reach an account, best first. ReachNone is an account the
// person may not use. Own, then team, then everyone, then house.
const (
	ReachNone Reach = iota
	ReachOwn
	ReachTeam
	ReachEveryone
	ReachHouse
)

// ReachedBy says how user, who is on teams, reaches the account, and for a
// team account the team they reach it through. A private account is reached
// only by its owner. A team account is reached by anyone on one of its teams.
// An everyone account, and a house account, are reached by everyone. The
// owner of a team or everyone account does not reach it as their own: every
// non-house account has an owner.
//
// When more than one of the person's teams shares the account, the lowest
// team id is the one reported, and that id is what the ledger stores as
// owner_team_id. Any fixed choice would do; this one makes the ledger say
// the same thing every time for the same person and account.
func (a Account) ReachedBy(user string, teams []string) (Reach, string) {
	if a.House {
		return ReachHouse, ""
	}
	switch a.Visibility {
	case VisibilityPrivate:
		if user != "" && a.OwnerUserID == user {
			return ReachOwn, ""
		}
		return ReachNone, ""
	case VisibilityEveryone:
		return ReachEveryone, ""
	case VisibilityTeam:
		via := ""
		for _, id := range a.TeamIDs {
			if !slices.Contains(teams, id) {
				continue
			}
			if via == "" || id < via {
				via = id
			}
		}
		if via == "" {
			return ReachNone, ""
		}
		return ReachTeam, via
	default:
		return ReachNone, ""
	}
}

// Route is one named path through the catalog. Decision is empty except on
// decide routes, which are choice or yesno.
type Route struct {
	Name       string
	Capability Capability
	Decision   Decision
	Links      []Link
}

// Check reports a malformed row so a bad migration or override fails closed.
func (r Route) Check() error {
	if r.Name == "" {
		return fmt.Errorf("gateway: route name is empty")
	}
	if !r.Capability.known() {
		return fmt.Errorf("gateway: capability %q", r.Capability)
	}
	if !r.Decision.known() {
		return fmt.Errorf("gateway: decision %q", r.Decision)
	}
	if r.Capability == CapDecide {
		if r.Decision != DecisionYesNo && r.Decision != DecisionChoice {
			return fmt.Errorf("gateway: decide route %q needs choice or yesno", r.Name)
		}
	} else if r.Decision != DecisionNone {
		return fmt.Errorf("gateway: route %q carries a decision on %s", r.Name, r.Capability)
	}
	if len(r.Links) == 0 {
		return fmt.Errorf("gateway: route %q has no links", r.Name)
	}
	return nil
}

// LinksFor returns the chain for this call. An empty credential ref uses
// house keys. A bring-your-own ref never includes a house key.
func (r Route) LinksFor(credentialRef string) ([]Link, error) {
	if err := r.Check(); err != nil {
		return nil, err
	}
	var out []Link
	for _, link := range r.Links {
		if credentialRef == "" {
			if link.Account.House {
				out = append(out, link)
			}
			continue
		}
		if link.Account.House {
			continue
		}
		if link.Account.CredentialRef == credentialRef {
			out = append(out, link)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("gateway: route %q has no link for this key", r.Name)
	}
	return out, nil
}

// linkKey names a hop of a route, whichever way it was read.
type linkKey struct {
	position int
	account  string
	model    string
}

// LinksForUser returns the chain for a call that names a person and no
// credential ref. Every link the person may use is in it: the links on their
// private accounts, on accounts shared with a team of theirs, on accounts
// shared with everyone, and on house accounts. There are no fallback rules
// and no single team is chosen. Own, then team, then everyone, then house is
// only the order when more than one could serve the route; inside each, the
// route's own order holds. A person's own key never falls back to a house key:
// that chain is OwnLinks.
//
// A link appears once, however many of the person's teams share its account
// (or however many times the store read it, say once per team).
func (r Route) LinksForUser(userID string, teamIDs []string) ([]Link, error) {
	if err := r.Check(); err != nil {
		return nil, err
	}
	var own, team, everyone, house []Link
	seen := map[linkKey]bool{}
	for _, link := range r.Links {
		reach, via := link.Account.ReachedBy(userID, teamIDs)
		if reach == ReachNone {
			continue
		}
		key := linkKey{link.Position, link.Account.ID, link.Model}
		if seen[key] {
			continue
		}
		seen[key] = true
		link.ViaTeam = via
		switch reach {
		case ReachOwn:
			own = append(own, link)
		case ReachTeam:
			team = append(team, link)
		case ReachHouse:
			house = append(house, link)
		default:
			everyone = append(everyone, link)
		}
	}
	out := append(append(append(own, team...), everyone...), house...)
	if len(out) == 0 {
		return nil, fmt.Errorf("gateway: route %q has no link for this person", r.Name)
	}
	return out, nil
}
