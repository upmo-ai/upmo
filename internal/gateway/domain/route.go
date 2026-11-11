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
	// OwnerUserID is the person a personal account belongs to. It is empty
	// for a house account and for a shared one.
	OwnerUserID string
	// TeamIDs are the teams a shared account is shared with. It is empty for
	// a personal account and for a workspace-wide one.
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
	// ViaTeam is the team through which the person reached this hop's shared
	// account, for the ledger. It is empty for their own account, a
	// workspace-wide one, and any hop not chosen for a person.
	ViaTeam string
}

// Reach is how a person comes to use an account.
type Reach int

// The ways to reach an account, best first. ReachNone is an account the
// person may not use.
const (
	ReachNone Reach = iota
	ReachOwn
	ReachTeam
	ReachWorkspace
)

// ReachedBy says how user, who is on teams, reaches the account, and for a
// team account the team they reach it through. A personal account is reached
// only by its owner. A shared account is reached by anyone on one of its
// teams; a workspace-wide one, which has no teams, by everyone.
//
// When more than one of the person's teams shares the account, the lowest
// team id is the one reported. Any fixed choice would do; this one makes the
// ledger say the same thing every time for the same person and account.
func (a Account) ReachedBy(user string, teams []string) (Reach, string) {
	if a.OwnerUserID != "" {
		if user != "" && a.OwnerUserID == user {
			return ReachOwn, ""
		}
		return ReachNone, ""
	}
	if len(a.TeamIDs) == 0 {
		return ReachWorkspace, ""
	}
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
// own accounts, on accounts shared with a team of theirs, and on accounts
// shared with the whole workspace. There are no fallback rules and no single
// team is chosen. Own, then team, then workspace is only the order when more
// than one could serve the route; inside each, the route's own order holds.
//
// A link appears once, however many of the person's teams share its account
// (or however many times the store read it, say once per team).
func (r Route) LinksForUser(userID string, teamIDs []string) ([]Link, error) {
	if err := r.Check(); err != nil {
		return nil, err
	}
	var own, team, workspace []Link
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
		default:
			workspace = append(workspace, link)
		}
	}
	out := append(append(own, team...), workspace...)
	if len(out) == 0 {
		return nil, fmt.Errorf("gateway: route %q has no link for this person", r.Name)
	}
	return out, nil
}
