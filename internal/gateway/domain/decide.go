package domain

import "fmt"

// Decision is the kind of small decision a route may carry. Empty on chat,
// stream and embed routes. Decide routes are choice or yesno (UPM-24).
type Decision string

const (
	DecisionNone   Decision = ""
	DecisionYesNo  Decision = "yesno"
	DecisionChoice Decision = "choice"
)

// Capability is the adapter surface a route uses.
type Capability string

const (
	CapChat   Capability = "chat"
	CapStream Capability = "stream"
	CapEmbed  Capability = "embed"
	CapDecide Capability = "decide"
)

// DecideRequest is one decide call. Choice needs at least two options.
type DecideRequest struct {
	Kind    Decision
	Prompt  string
	Options []string
}

// DecideResult is the model's pick. YesNo is set for yesno; Choice is the
// matching option for choice.
type DecideResult struct {
	YesNo  bool
	Choice string
}

func (d Decision) known() bool {
	return d == DecisionNone || d == DecisionYesNo || d == DecisionChoice
}

func (c Capability) known() bool {
	return c == CapChat || c == CapStream || c == CapEmbed || c == CapDecide
}

// Check reports a caller fault when the decide payload does not match Kind.
func (r DecideRequest) Check() error {
	if r.Prompt == "" {
		return fmt.Errorf("gateway: decide prompt is empty")
	}
	switch r.Kind {
	case DecisionYesNo:
		if len(r.Options) != 0 {
			return fmt.Errorf("gateway: yesno takes no options")
		}
		return nil
	case DecisionChoice:
		if len(r.Options) < 2 {
			return fmt.Errorf("gateway: choice needs at least two options")
		}
		seen := map[string]bool{}
		for _, opt := range r.Options {
			if opt == "" {
				return fmt.Errorf("gateway: choice option is empty")
			}
			if seen[opt] {
				return fmt.Errorf("gateway: choice option %q is repeated", opt)
			}
			seen[opt] = true
		}
		return nil
	default:
		return fmt.Errorf("gateway: decide kind %q", r.Kind)
	}
}

// ParseYesNo reads a yesno answer. Anything else is a caller-visible miss
// that dispatch treats as a caller fault, not a retry.
func ParseYesNo(s string) (bool, error) {
	switch s {
	case "yes":
		return true, nil
	case "no":
		return false, nil
	default:
		return false, fmt.Errorf("gateway: yesno answer %q", s)
	}
}

// ParseChoice returns the option that equals s.
func ParseChoice(s string, options []string) (string, error) {
	for _, opt := range options {
		if opt == s {
			return opt, nil
		}
	}
	return "", fmt.Errorf("gateway: choice %q is not an option", s)
}
