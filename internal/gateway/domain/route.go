package domain

import "fmt"

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
