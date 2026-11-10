package domain

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits on what a person may save in an agent.
const (
	AgentNameRunes        = 60
	AgentDescriptionRunes = 300
	AgentOverlayRunes     = 4000
	AgentProviderRunes    = 60
)

// ClassAgent is the failure class of a reply whose agent could not be used.
const ClassAgent = "agent"

// Invalid is a plain-language reason an agent was refused.
type Invalid string

func (e Invalid) Error() string { return string(e) }

func hasControl(s string, keep ...rune) bool {
	return strings.ContainsFunc(s, func(r rune) bool {
		if !unicode.IsControl(r) {
			return false
		}
		for _, k := range keep {
			if r == k {
				return false
			}
		}
		return true
	})
}

// AgentName trims the name of an agent: 1 to 60 characters, no control
// characters.
func AgentName(name string) (string, error) {
	name = strings.TrimSpace(name)
	n := utf8.RuneCountInString(name)
	switch {
	case n == 0:
		return "", Invalid("Give the agent a name.")
	case n > AgentNameRunes || hasControl(name) || !utf8.ValidString(name):
		return "", Invalid(fmt.Sprintf("The name must be 1 to %d characters.", AgentNameRunes))
	}
	return name, nil
}

// AgentDescription trims the description of an agent. Empty is allowed.
func AgentDescription(text string) (string, error) {
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) > AgentDescriptionRunes || hasControl(text, '\n') || !utf8.ValidString(text) {
		return "", Invalid(fmt.Sprintf("The description must be at most %d characters.", AgentDescriptionRunes))
	}
	return text, nil
}

// AgentOverlay trims the system text an agent adds to a reply. Empty is
// allowed. Line breaks and tabs are kept.
func AgentOverlay(text string) (string, error) {
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) > AgentOverlayRunes || hasControl(text, '\n', '\r', '\t') || !utf8.ValidString(text) {
		return "", Invalid(fmt.Sprintf("The instructions must be at most %d characters.", AgentOverlayRunes))
	}
	return text, nil
}

// AgentRoute trims the route an agent defaults to. Empty means none.
func AgentRoute(name string) (string, error) {
	route, err := Route(name)
	if err != nil {
		return "", Invalid("The route is not a valid route name.")
	}
	return route, nil
}

// AgentModel checks the model an agent defaults to, by provider name and
// model id, both or neither.
func AgentModel(provider, model string) (string, string, error) {
	provider, model = strings.TrimSpace(provider), strings.TrimSpace(model)
	if provider == "" && model == "" {
		return "", "", nil
	}
	if provider == "" || model == "" {
		return "", "", Invalid("Give both a provider and a model, or neither.")
	}
	if utf8.RuneCountInString(provider) > AgentProviderRunes || hasControl(provider) || !utf8.ValidString(provider) {
		return "", "", Invalid(fmt.Sprintf("The provider must be 1 to %d characters.", AgentProviderRunes))
	}
	if utf8.RuneCountInString(model) > PinModelRunes || hasControl(model) || !utf8.ValidString(model) {
		return "", "", Invalid(fmt.Sprintf("The model must be 1 to %d characters.", PinModelRunes))
	}
	return provider, model, nil
}

// AgentSystem is the system text an agent adds to a reply: its name and its
// instructions. An agent with no instructions adds nothing.
func AgentSystem(name, overlay string) string {
	overlay = strings.TrimSpace(overlay)
	if overlay == "" {
		return ""
	}
	return "# Agent: " + name + "\n\n" + overlay
}

// Picked is what the composer gave a reply: the route, and the model pinned as
// a provider account and a model id. Empty means no pick.
type Picked struct {
	Route      string
	PinAccount string
	PinModel   string
}

// Defaults are an agent's route, and its model by provider name and model id.
// Each is empty when the agent has none.
type Defaults struct {
	Route         string
	ModelProvider string
	ModelID       string
}

// Resolved is the route and the pin a reply is sent with. The pin is a
// provider account and a model, or a provider name and a model, never both.
type Resolved struct {
	Route       string
	PinAccount  string
	PinProvider string
	PinModel    string
}

// Resolve settles each axis on its own. The route is the composer's, else the
// agent's, else the install's default. The model is the composer's pin, else
// the agent's model, else none, which leaves the route's chain alone. agent
// is nil for a reply with no agent.
func Resolve(picked Picked, agent *Defaults, install string) Resolved {
	var out Resolved
	switch {
	case picked.Route != "":
		out.Route = picked.Route
	case agent != nil && agent.Route != "":
		out.Route = agent.Route
	default:
		out.Route = install
	}
	switch {
	case picked.PinModel != "":
		out.PinAccount, out.PinModel = picked.PinAccount, picked.PinModel
	case agent != nil && agent.ModelProvider != "" && agent.ModelID != "":
		out.PinProvider, out.PinModel = agent.ModelProvider, agent.ModelID
	}
	return out
}
