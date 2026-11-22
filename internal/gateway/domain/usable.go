package domain

import "strings"

// What the pickers may offer a person. An account's model is usable when the
// account is Connected and lists the model; a route is usable when a hop
// reaches a usable account the person can use; an agent is usable when it is
// enabled and its model, or its route, resolves to something usable. These
// rules are pure: the HTTP layers read the rows and call them.

// accountHasModel reports whether p is Connected and lists model as non-retired.
func accountHasModel(p Provider, model string) bool {
	if p.Disabled || p.Status != StatusConnected {
		return false
	}
	for _, entry := range p.EnabledModels {
		if entry.ID == model {
			return !entry.Retired
		}
	}
	for _, have := range p.Models {
		if have == model {
			return true
		}
	}
	return false
}

// kindOf returns the model's catalog kind on p. A model with no catalog row
// counts as chat with text input.
func (p Provider) kindOf(model string) ModelKind {
	if p.ModelKinds != nil {
		if k, ok := p.ModelKinds[model]; ok && k != "" {
			return k
		}
	}
	return KindChat
}

// UsableModel reports whether one model of an account can be used in a chat,
// agent, or route picker now: the account is Connected, lists the model, and
// the model's kind is chat. A model with no catalog row counts as chat with
// text input.
func UsableModel(p Provider, model string) bool {
	return accountHasModel(p, model) && p.kindOf(model) == KindChat
}

// UsableModels lists the account's chat-usable models in stored order. The
// result is never nil, so a JSON answer carries an empty list rather than null.
func UsableModels(p Provider) []string {
	out := []string{}
	for _, model := range p.Models {
		if UsableModel(p, model) {
			out = append(out, model)
		}
	}
	return out
}

// UsableHop is one link of a route as the usability rules read it: which
// account it calls and with which model.
type UsableHop struct {
	AccountID string
	Model     string
}

// UsableRouteView is one route as the usability rules read it.
type UsableRouteView struct {
	Name       string
	Capability Capability
	Hops       []UsableHop
}

// UsableRoute reports whether the route can carry a call: at least one hop
// names an account the person can use — one of the accounts given, their own
// and the workspace's — whose status is Connected and which lists the hop's
// model. Embed routes accept embedding models (and uncataloged models); all
// other routes require a chat model (where no catalog row counts as chat). A
// hop naming an account outside the list is one of another person's, and never
// counts.
func UsableRoute(r UsableRouteView, accounts []Provider) bool {
	for _, hop := range r.Hops {
		for _, a := range accounts {
			if a.ID != hop.AccountID {
				continue
			}
			if r.Capability == CapEmbed {
				if accountHasModel(a, hop.Model) {
					kind, hasRow := a.ModelKinds[hop.Model]
					if !hasRow || kind == "" || kind == KindEmbedding {
						return true
					}
				}
				continue
			}
			if UsableModel(a, hop.Model) {
				return true
			}
		}
	}
	return false
}

// AgentView is one agent as the usability rules read it.
type AgentView struct {
	Enabled       bool
	Route         string
	ModelProvider string
	ModelID       string
}

// UsableAgentModel reports whether the agent's model can be used now: an
// account the person can use names the provider — its label, or its adapter
// when the label is empty, without regard to case — and lists the model while
// Connected. The person's own account comes before a workspace one, as a pin
// does.
func UsableAgentModel(provider, model string, accounts []Provider) bool {
	if provider == "" || model == "" {
		return false
	}
	want := strings.ToLower(provider)
	global := false
	for _, a := range accounts {
		label := a.Provider
		if label == "" {
			label = a.Adapter
		}
		if strings.ToLower(label) != want {
			continue
		}
		if UsableModel(a, model) {
			if a.Own {
				return true
			}
			global = true
		}
	}
	return global
}

// UsableAgent reports whether the agent may be offered in the chat: it is
// enabled, and its model or its route resolves to something usable. The
// install default route decides only for an agent that names neither — one
// that names a model or a route stands or falls with it.
func UsableAgent(a AgentView, routes []UsableRouteView, accounts []Provider, defaultRoute string) bool {
	if !a.Enabled {
		return false
	}
	modelNamed := a.ModelProvider != "" || a.ModelID != ""
	if modelNamed && UsableAgentModel(a.ModelProvider, a.ModelID, accounts) {
		return true
	}
	name := a.Route
	if name == "" {
		// A model that resolves nothing leaves no route and no default: the
		// agent stands or falls with the model it names.
		if modelNamed {
			return false
		}
		name = defaultRoute
	}
	for _, r := range routes {
		if r.Name == name && (r.Capability == CapChat || r.Capability == CapStream) && UsableRoute(r, accounts) {
			return true
		}
	}
	return false
}
