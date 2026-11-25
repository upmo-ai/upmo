package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeTrimsValuesAndReturnsSlotsInOrder(t *testing.T) {
	in := Template{
		Goal:       "  Send {{first}} before {{second_2}}  ",
		AgentID:    "  agent-1  ",
		Outline:    []string{"  Gather files  ", "Check totals"},
		Deliveries: []string{"  Finance team  "},
		Budget:     10,
	}

	got, slots, err := Normalize(in)
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if got.Goal != "Send {{first}} before {{second_2}}" {
		t.Errorf("goal = %q", got.Goal)
	}
	if got.AgentID != "agent-1" {
		t.Errorf("agent id = %q", got.AgentID)
	}
	if len(got.Outline) != 2 || got.Outline[0] != "Gather files" || got.Outline[1] != "Check totals" {
		t.Errorf("outline = %#v", got.Outline)
	}
	if len(got.Deliveries) != 1 || got.Deliveries[0] != "Finance team" {
		t.Errorf("deliveries = %#v", got.Deliveries)
	}
	if len(slots) != 2 || slots[0] != "first" || slots[1] != "second_2" {
		t.Errorf("slots = %#v", slots)
	}
}

func TestNormalizeReturnsNonNilEmptySlices(t *testing.T) {
	got, slots, err := Normalize(validTemplate())
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if got.Outline == nil {
		t.Error("outline is nil")
	}
	if got.Deliveries == nil {
		t.Error("deliveries are nil")
	}
	if slots == nil {
		t.Error("slots are nil")
	}
	if len(slots) != 0 {
		t.Errorf("slots = %#v", slots)
	}
}

func TestNormalizeRejectsRepeatedSlot(t *testing.T) {
	in := validTemplate()
	in.Goal = "{{name}} and {{name}}"

	if _, _, err := Normalize(in); !errors.Is(err, ErrSlot) {
		t.Fatalf("Normalize() error = %v, want %v", err, ErrSlot)
	}
}

func TestNormalizeRejectsMalformedSlots(t *testing.T) {
	tests := []struct {
		name string
		goal string
	}{
		{name: "unclosed", goal: "Use {{name"},
		{name: "unopened", goal: "Use name}}"},
		{name: "empty", goal: "Use {{}}"},
		{name: "space", goal: "Use {{first name}}"},
		{name: "invalid first character", goal: "Use {{1name}}"},
		{name: "invalid punctuation", goal: "Use {{first-name}}"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			in := validTemplate()
			in.Goal = test.goal
			if _, _, err := Normalize(in); !errors.Is(err, ErrSlot) {
				t.Fatalf("Normalize() error = %v, want %v", err, ErrSlot)
			}
		})
	}
}

func TestNormalizeAcceptsSingleBraces(t *testing.T) {
	in := validTemplate()
	in.Goal = "Use {name} and one } or {"

	got, slots, err := Normalize(in)
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if got.Goal != in.Goal || len(slots) != 0 {
		t.Fatalf("Normalize() = %#v, %#v", got, slots)
	}
}

func TestNormalizeChecksSlotNameLength(t *testing.T) {
	name40 := "a" + strings.Repeat("b", 39)
	in := validTemplate()
	in.Goal = "{{" + name40 + "}}"

	_, slots, err := Normalize(in)
	if err != nil {
		t.Fatalf("40-character name error = %v", err)
	}
	if len(slots) != 1 || slots[0] != name40 {
		t.Fatalf("slots = %#v", slots)
	}

	in.Goal = "{{" + name40 + "b}}"
	if _, _, err := Normalize(in); !errors.Is(err, ErrSlot) {
		t.Fatalf("41-character name error = %v, want %v", err, ErrSlot)
	}
}

func TestNormalizeChecksGoal(t *testing.T) {
	in := validTemplate()
	in.Goal = strings.Repeat("é", TemplateGoalRunes)
	got, _, err := Normalize(in)
	if err != nil {
		t.Fatalf("goal at cap error = %v", err)
	}
	if got.Goal != in.Goal {
		t.Fatalf("goal = %q", got.Goal)
	}

	in.Goal += "é"
	if _, _, err := Normalize(in); !errors.Is(err, ErrGoal) {
		t.Fatalf("goal over cap error = %v, want %v", err, ErrGoal)
	}

	in.Goal = "   "
	if _, _, err := Normalize(in); !errors.Is(err, ErrGoal) {
		t.Fatalf("blank goal error = %v, want %v", err, ErrGoal)
	}
}

func TestNormalizeChecksOutline(t *testing.T) {
	in := validTemplate()
	in.Outline = repeatedStrings("Step", TemplateOutlineCap)
	got, _, err := Normalize(in)
	if err != nil {
		t.Fatalf("outline at cap error = %v", err)
	}
	if len(got.Outline) != TemplateOutlineCap {
		t.Fatalf("outline length = %d", len(got.Outline))
	}

	in.Outline = repeatedStrings("Step", TemplateOutlineCap+1)
	if _, _, err := Normalize(in); !errors.Is(err, ErrOutline) {
		t.Fatalf("outline over cap error = %v, want %v", err, ErrOutline)
	}

	in.Outline = []string{"  "}
	if _, _, err := Normalize(in); !errors.Is(err, ErrOutline) {
		t.Fatalf("blank outline title error = %v, want %v", err, ErrOutline)
	}
}

func TestNormalizeChecksDeliveries(t *testing.T) {
	in := validTemplate()
	in.Deliveries = repeatedStrings("Records", TemplateDeliveryCap)
	got, _, err := Normalize(in)
	if err != nil {
		t.Fatalf("deliveries at cap error = %v", err)
	}
	if len(got.Deliveries) != TemplateDeliveryCap {
		t.Fatalf("deliveries length = %d", len(got.Deliveries))
	}

	in.Deliveries = repeatedStrings("Records", TemplateDeliveryCap+1)
	if _, _, err := Normalize(in); !errors.Is(err, ErrDelivery) {
		t.Fatalf("deliveries over cap error = %v, want %v", err, ErrDelivery)
	}

	in.Deliveries = []string{"  "}
	if _, _, err := Normalize(in); !errors.Is(err, ErrDelivery) {
		t.Fatalf("blank delivery error = %v, want %v", err, ErrDelivery)
	}
}

func TestNormalizeChecksBudget(t *testing.T) {
	tests := []struct {
		name    string
		budget  int64
		wantErr bool
	}{
		{name: "zero", budget: 0, wantErr: true},
		{name: "one", budget: 1},
		{name: "maximum", budget: TemplateBudgetMax},
		{name: "over maximum", budget: TemplateBudgetMax + 1, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			in := validTemplate()
			in.Budget = test.budget
			got, _, err := Normalize(in)
			if test.wantErr {
				if !errors.Is(err, ErrBudget) {
					t.Fatalf("Normalize() error = %v, want %v", err, ErrBudget)
				}
				return
			}
			if err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}
			if got.Budget != test.budget {
				t.Fatalf("budget = %d, want %d", got.Budget, test.budget)
			}
		})
	}
}

func TestNormalizeRejectsEmptyAgentID(t *testing.T) {
	in := validTemplate()
	in.AgentID = "  "

	if _, _, err := Normalize(in); !errors.Is(err, ErrAgent) {
		t.Fatalf("Normalize() error = %v, want %v", err, ErrAgent)
	}
}

func validTemplate() Template {
	return Template{
		Goal:    "Prepare report",
		AgentID: "agent-1",
		Budget:  1,
	}
}

func repeatedStrings(value string, count int) []string {
	values := make([]string, count)
	for i := range values {
		values[i] = value
	}
	return values
}
