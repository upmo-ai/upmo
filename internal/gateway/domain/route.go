package domain

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// Limits on what a person may save on a route.
const (
	MaxRouteName = 64
	MaxRouteHops = 3
)

var routeNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// RouteHop is one hop to save on a route.
type RouteHop struct {
	Position             int
	AccountID            string
	Adapter              string
	Model                string
	Weight               int
	Retry                int
	RequiredCapabilities []string
}

// CleanRouteName trims name and checks it against the gateway/agent route
// naming rule (1 to 64 letters, numbers, dots, underscores or hyphens).
func CleanRouteName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", Invalid("Give the route a name.")
	}
	if !routeNamePattern.MatchString(name) {
		return "", Invalid(fmt.Sprintf("The route name must be 1 to %d letters, numbers, dots, underscores or hyphens.", MaxRouteName))
	}
	return name, nil
}

// CleanRouteCapability checks a route's capability and decision pairing.
func CleanRouteCapability(capability, decision string) (Capability, Decision, error) {
	c := Capability(strings.TrimSpace(capability))
	d := Decision(strings.TrimSpace(decision))
	if !c.known() {
		return "", "", Invalid("Choose chat, stream, embed or decide.")
	}
	if c == CapDecide {
		if d != DecisionYesNo && d != DecisionChoice {
			return "", "", Invalid("A decide route needs choice or yesno.")
		}
		return c, d, nil
	}
	if d != DecisionNone {
		return "", "", Invalid("Only a decide route takes a decision.")
	}
	return c, d, nil
}

// CleanRouteHops validates a route's hop chain: 1 to MaxRouteHops hops, unique
// positive positions, valid account UUIDs and models, non-negative retries,
// positive weights, known required capabilities, and no duplicate
// (account_id, model) cycles. Returned hops are sorted by Position.
func CleanRouteHops(hops []RouteHop) ([]RouteHop, error) {
	if len(hops) == 0 {
		return nil, Invalid("Add at least one hop.")
	}
	if len(hops) > MaxRouteHops {
		return nil, Invalid(fmt.Sprintf("Add up to %d hops.", MaxRouteHops))
	}
	type hopKey struct {
		account string
		model   string
	}
	seenPos := make(map[int]bool, len(hops))
	seenPair := make(map[hopKey]bool, len(hops))
	out := make([]RouteHop, 0, len(hops))
	for _, h := range hops {
		if h.Position < 1 {
			return nil, Invalid("Each hop position must be at least 1.")
		}
		if seenPos[h.Position] {
			return nil, Invalid("Each hop must have a unique position.")
		}
		seenPos[h.Position] = true

		acct := strings.ToLower(strings.TrimSpace(h.AccountID))
		if !accountID.MatchString(acct) {
			return nil, Invalid("Choose a provider account for every hop.")
		}

		model := strings.TrimSpace(h.Model)
		n := utf8.RuneCountInString(model)
		if n == 0 || n > MaxModelName || hasControl(model) || !utf8.ValidString(model) {
			return nil, Invalid(fmt.Sprintf("Each model must be 1 to %d characters.", MaxModelName))
		}

		if h.Weight < 1 {
			return nil, Invalid("Each hop weight must be at least 1.")
		}
		if h.Retry < 0 {
			return nil, Invalid("Each hop retry budget cannot be negative.")
		}

		pair := hopKey{account: acct, model: model}
		if seenPair[pair] {
			return nil, Invalid("A route cannot repeat the same provider account and model.")
		}
		seenPair[pair] = true

		var need []string
		if len(h.RequiredCapabilities) > 0 {
			seenCap := map[string]bool{}
			need = make([]string, 0, len(h.RequiredCapabilities))
			for _, c := range h.RequiredCapabilities {
				c = strings.ToLower(strings.TrimSpace(c))
				if !knownCap(c) {
					return nil, Invalid("Unknown required capability.")
				}
				if seenCap[c] {
					continue
				}
				seenCap[c] = true
				need = append(need, c)
			}
			sort.Strings(need)
		}

		out = append(out, RouteHop{
			Position:             h.Position,
			AccountID:            acct,
			Model:                model,
			Weight:               h.Weight,
			Retry:                h.Retry,
			RequiredCapabilities: need,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Position < out[j].Position })
	return out, nil
}

const (
	PlanMetered      = "metered"
	PlanSubscription = "subscription"
	PlanLocal        = "local"
)

// Account is one provider login from the routing tables. Adapter and model
// names live here so dispatch code never names a provider.
type Account struct {
	ID            string
	Provider      string
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
