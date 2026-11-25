package domain

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	TemplateGoalRunes         = 200
	TemplateTitleRunes        = 200
	TemplateTargetRunes       = 200
	TemplateOutlineCap        = 12
	TemplateDeliveryCap       = 8
	DefaultBudget       int64 = 2_000_000
	TemplateBudgetMax   int64 = 1_000_000_000
)

type Template struct {
	Goal       string
	AgentID    string
	Outline    []string
	Deliveries []string
	Budget     int64
}

var (
	ErrSlot     = errors.New("tasks: slot")
	ErrOutline  = errors.New("tasks: outline")
	ErrDelivery = errors.New("tasks: delivery")
	ErrBudget   = errors.New("tasks: budget")
	ErrAgent    = errors.New("tasks: agent")
)

// Normalize trims a template and returns the slot names in the order they appear in the goal.
func Normalize(in Template) (Template, []string, error) {
	in.Goal = strings.TrimSpace(in.Goal)
	if in.Goal == "" || utf8.RuneCountInString(in.Goal) > TemplateGoalRunes {
		return Template{}, nil, ErrGoal
	}

	in.AgentID = strings.TrimSpace(in.AgentID)
	if in.AgentID == "" {
		return Template{}, nil, ErrAgent
	}

	slots, err := templateSlots(in.Goal)
	if err != nil {
		return Template{}, nil, err
	}

	if len(in.Outline) > TemplateOutlineCap {
		return Template{}, nil, ErrOutline
	}
	outline := make([]string, len(in.Outline))
	for i, title := range in.Outline {
		title = strings.TrimSpace(title)
		if title == "" || utf8.RuneCountInString(title) > TemplateTitleRunes {
			return Template{}, nil, ErrOutline
		}
		outline[i] = title
	}
	in.Outline = outline

	if len(in.Deliveries) > TemplateDeliveryCap {
		return Template{}, nil, ErrDelivery
	}
	deliveries := make([]string, len(in.Deliveries))
	for i, target := range in.Deliveries {
		target = strings.TrimSpace(target)
		if target == "" || utf8.RuneCountInString(target) > TemplateTargetRunes {
			return Template{}, nil, ErrDelivery
		}
		deliveries[i] = target
	}
	in.Deliveries = deliveries

	if in.Budget < 1 || in.Budget > TemplateBudgetMax {
		return Template{}, nil, ErrBudget
	}

	return in, slots, nil
}

func templateSlots(goal string) ([]string, error) {
	slots := make([]string, 0)
	seen := make(map[string]struct{})

	for i := 0; i < len(goal); {
		switch {
		case strings.HasPrefix(goal[i:], "{{"):
			end := strings.Index(goal[i+2:], "}}")
			if end < 0 {
				return nil, ErrSlot
			}
			name := goal[i+2 : i+2+end]
			if !validSlotName(name) {
				return nil, ErrSlot
			}
			if _, exists := seen[name]; exists {
				return nil, ErrSlot
			}
			seen[name] = struct{}{}
			slots = append(slots, name)
			i += end + 4
		case strings.HasPrefix(goal[i:], "}}"):
			return nil, ErrSlot
		default:
			i++
		}
	}

	return slots, nil
}

func validSlotName(name string) bool {
	if len(name) == 0 || len(name) > 40 || !asciiLetter(name[0]) {
		return false
	}
	for i := 1; i < len(name); i++ {
		if !asciiLetter(name[i]) && (name[i] < '0' || name[i] > '9') && name[i] != '_' {
			return false
		}
	}
	return true
}

func asciiLetter(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}
