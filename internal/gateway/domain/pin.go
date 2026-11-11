package domain

import (
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

// DefaultPinRetries is the retry budget of a pinned link whose account and
// model are not on the route. It is the budget a route link gets by default.
const DefaultPinRetries = 1

// ErrPinAccount is a pin to an account that belongs to another person. The
// text names no account.
var ErrPinAccount = errors.New("gateway: the pinned account is not available")

// ErrPinCaller is a pin on a call that names no person, or that spends a
// bring-your-own key: a pin is the person's own choice and nobody else's.
var ErrPinCaller = errors.New("gateway: a pin needs a person and no credential ref")

var accountID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Pin names one model, tried before the route's chain, either on one provider
// account (AccountID) or on a provider by name (Provider), which the gateway
// resolves to an account the person can use. The zero Pin is no pin.
type Pin struct {
	AccountID string
	Provider  string
	Model     string
}

// IsZero reports that nothing is pinned.
func (p Pin) IsZero() bool { return p.AccountID == "" && p.Provider == "" && p.Model == "" }

// Check refuses a half pin, a pin that names both an account and a provider,
// an account that is not a UUID, a provider that is empty, too long or holds
// a control character, and a model that is empty, too long or holds a
// control character.
func (p Pin) Check() error {
	if p.IsZero() {
		return nil
	}
	model := strings.TrimSpace(p.Model)
	switch {
	case p.AccountID != "" && p.Provider != "":
		return Invalid("The pin names an account or a provider, not both.")
	case p.Provider != "":
		provider := strings.TrimSpace(p.Provider)
		if provider != p.Provider || utf8.RuneCountInString(provider) > MaxProviderLabel ||
			hasControl(provider) || !utf8.ValidString(provider) {
			return Invalid("The pinned provider is not valid.")
		}
	case !accountID.MatchString(p.AccountID):
		return Invalid("The pinned account is not valid.")
	}
	if model == "" || model != p.Model || utf8.RuneCountInString(model) > MaxModelName ||
		hasControl(model) || !utf8.ValidString(model) {
		return Invalid("The pinned model is not valid.")
	}
	return nil
}

// PinPosition is the ledger position of a pinned attempt: before every link
// of the route, whose own positions start at 1.
const PinPosition = 0

// OwnLinks are the route's links on accounts the person owns, in order, and
// none when they have none. It never falls back to a house link.
func (r Route) OwnLinks(userID string) []Link {
	var own []Link
	for _, link := range r.Links {
		if userID != "" && link.Account.OwnerUserID == userID && !link.Account.House {
			own = append(own, link)
		}
	}
	return own
}

// PinLink is the link a pin puts first, at PinPosition. When the route has a
// link for the same account and model, the pin takes that link's retry budget
// and needs; otherwise it takes the default budget and no needs.
func (r Route) PinLink(account Account, model string) Link {
	link := Link{Position: PinPosition, Account: account, Model: model, Weight: 1, RetryBudget: DefaultPinRetries}
	for _, l := range r.Links {
		if l.Account.ID == account.ID && l.Model == model {
			link.Weight, link.RetryBudget, link.Need = l.Weight, l.RetryBudget, l.Need
			break
		}
	}
	return link
}

// Pinned puts the pinned link first in chain and drops any link of the chain
// for the same account and model, so the pin is not tried twice.
func Pinned(pin Link, chain []Link) []Link {
	out := make([]Link, 0, len(chain)+1)
	out = append(out, pin)
	for _, l := range chain {
		if l.Account.ID == pin.Account.ID && l.Model == pin.Model {
			continue
		}
		out = append(out, l)
	}
	return out
}

// PinUsable reports whether person, who is on teams, may pin account: their
// own account, one shared with a team of theirs, or one shared with the whole
// workspace. It is the set LinksForUser draws on. Another person's account,
// or one shared only with teams they are not on, is never usable.
func PinUsable(account Account, person string, teams []string) bool {
	reach, _ := account.ReachedBy(person, teams)
	return reach != ReachNone
}
