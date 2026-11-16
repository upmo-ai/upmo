package domain

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Limits on what a person may save on an agent.
const (
	NameRunes         = 60
	SlugRunes         = 80
	RoleRunes         = 120
	DescriptionRunes  = 300
	InstructionsRunes = 4000
	RouteRunes        = 64
	ProviderRunes     = 60
	ModelRunes        = 120
	ToolClassRunes    = 64
	ConnectorIDRunes  = 120
	MaxArrayItems     = 64
)

// PlanApproval modes.
const (
	PlanApprovalAlways = "always"
	PlanApprovalRisky  = "risky"
	PlanApprovalNever  = "never"
)

var (
	slugPattern  = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	routePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	nonSlugRun   = regexp.MustCompile(`[^a-z0-9]+`)
)

// Invalid is a plain-language reason an agent or lesson was refused.
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

// CleanName trims the name of an agent: 1 to 60 characters, no control characters.
func CleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	n := utf8.RuneCountInString(name)
	switch {
	case n == 0:
		return "", Invalid("Give the agent a name.")
	case n > NameRunes || hasControl(name) || !utf8.ValidString(name):
		return "", Invalid(fmt.Sprintf("The name must be 1 to %d characters.", NameRunes))
	}
	return name, nil
}

// DeriveSlug converts an agent name into a lowercase kebab-case slug.
func DeriveSlug(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = nonSlugRun.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "agent"
	}
	if len(s) > SlugRunes {
		s = strings.TrimRight(s[:SlugRunes], "-")
		if s == "" {
			return "agent"
		}
	}
	return s
}

// CleanSlug validates an explicit slug, or derives one from name if slug is empty.
func CleanSlug(slug, name string) (string, error) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return DeriveSlug(name), nil
	}
	if len(slug) > SlugRunes || !slugPattern.MatchString(slug) {
		return "", Invalid("The slug must use lowercase letters, numbers, and hyphens.")
	}
	return slug, nil
}

// CleanRole validates the optional agent role label.
func CleanRole(role string) (string, error) {
	role = strings.TrimSpace(role)
	if utf8.RuneCountInString(role) > RoleRunes || hasControl(role) || !utf8.ValidString(role) {
		return "", Invalid(fmt.Sprintf("The role must be at most %d characters.", RoleRunes))
	}
	return role, nil
}

// CleanDescription trims the description of an agent. Empty is allowed.
func CleanDescription(text string) (string, error) {
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) > DescriptionRunes || hasControl(text, '\n') || !utf8.ValidString(text) {
		return "", Invalid(fmt.Sprintf("The description must be at most %d characters.", DescriptionRunes))
	}
	return text, nil
}

// CleanInstructions trims the system instructions an agent adds to a turn.
func CleanInstructions(text string) (string, error) {
	text = strings.TrimSpace(text)
	if utf8.RuneCountInString(text) > InstructionsRunes || hasControl(text, '\n', '\r', '\t') || !utf8.ValidString(text) {
		return "", Invalid(fmt.Sprintf("The instructions must be at most %d characters.", InstructionsRunes))
	}
	return text, nil
}

// CleanRoute validates the route an agent defaults to. Empty means none.
func CleanRoute(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name != "" && !routePattern.MatchString(name) {
		return "", Invalid("The route is not a valid route name.")
	}
	return name, nil
}

// CleanModel checks the model an agent defaults to, by provider name and
// model id, both or neither.
func CleanModel(provider, model string) (string, string, error) {
	provider, model = strings.TrimSpace(provider), strings.TrimSpace(model)
	if provider == "" && model == "" {
		return "", "", nil
	}
	if provider == "" || model == "" {
		return "", "", Invalid("Give both a provider and a model, or neither.")
	}
	if utf8.RuneCountInString(provider) > ProviderRunes || hasControl(provider) || !utf8.ValidString(provider) {
		return "", "", Invalid(fmt.Sprintf("The provider must be 1 to %d characters.", ProviderRunes))
	}
	if utf8.RuneCountInString(model) > ModelRunes || hasControl(model) || !utf8.ValidString(model) {
		return "", "", Invalid(fmt.Sprintf("The model must be 1 to %d characters.", ModelRunes))
	}
	return provider, model, nil
}

// CleanPlanApproval validates plan_approval, defaulting empty to risky.
func CleanPlanApproval(mode string) (string, error) {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return PlanApprovalRisky, nil
	}
	switch mode {
	case PlanApprovalAlways, PlanApprovalRisky, PlanApprovalNever:
		return mode, nil
	default:
		return "", Invalid("Choose always, risky, or never for plan approval.")
	}
}

func cleanStringList(in []string, maxLen int, label string) ([]string, error) {
	if len(in) > MaxArrayItems {
		return nil, Invalid(fmt.Sprintf("Too many %s (at most %d).", label, MaxArrayItems))
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		v := strings.TrimSpace(raw)
		if v == "" || utf8.RuneCountInString(v) > maxLen || hasControl(v) || !utf8.ValidString(v) {
			return nil, Invalid(fmt.Sprintf("Invalid entry in %s.", label))
		}
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out, nil
}

// CleanToolClasses trims and deduplicates tool class names.
func CleanToolClasses(in []string) ([]string, error) {
	return cleanStringList(in, ToolClassRunes, "tool_classes")
}

// CleanConnectorIDs trims and deduplicates connector IDs.
func CleanConnectorIDs(in []string) ([]string, error) {
	return cleanStringList(in, ConnectorIDRunes, "connector_ids")
}

// CleanKnowledgeFolderIDs validates and deduplicates knowledge folder UUIDs.
func CleanKnowledgeFolderIDs(in []string) ([]string, error) {
	if len(in) > MaxArrayItems {
		return nil, Invalid(fmt.Sprintf("Too many knowledge_folder_ids (at most %d).", MaxArrayItems))
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		parsed, err := uuid.Parse(strings.TrimSpace(raw))
		if err != nil {
			return nil, Invalid("Knowledge folder ids must be valid UUIDs.")
		}
		s := parsed.String()
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out, nil
}

// CleanCheckCriteria validates that each criterion is valid JSON and returns a
// non-nil slice.
func CleanCheckCriteria(in []json.RawMessage) ([]json.RawMessage, error) {
	if len(in) > MaxArrayItems {
		return nil, Invalid(fmt.Sprintf("Too many check_criteria (at most %d).", MaxArrayItems))
	}
	out := make([]json.RawMessage, 0, len(in))
	for _, item := range in {
		trimmed := strings.TrimSpace(string(item))
		if trimmed == "" || !json.Valid([]byte(trimmed)) {
			return nil, Invalid("Each check criterion must be valid JSON.")
		}
		out = append(out, json.RawMessage(trimmed))
	}
	return out, nil
}
