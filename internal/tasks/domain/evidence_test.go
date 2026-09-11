package domain

import (
	"strings"
	"testing"
)

func TestPasses(t *testing.T) {
	tests := []struct {
		name   string
		checks []Check
		want   bool
	}{
		{name: "nil", checks: nil},
		{name: "empty", checks: []Check{}},
		{name: "one successful check", checks: []Check{{Name: "unit", Code: 0}}, want: true},
		{name: "failed check", checks: []Check{{Name: "unit", Code: 1}}},
		{
			name: "mixed",
			checks: []Check{
				{Name: "unit", Code: 0},
				{Name: "integration", Code: 1},
			},
		},
		{name: "blank name", checks: []Check{{Name: "  ", Code: 0}}},
		{
			name:   "name at rune limit",
			checks: []Check{{Name: strings.Repeat("é", 80), Code: 0}},
			want:   true,
		},
		{
			name:   "name over rune limit",
			checks: []Check{{Name: strings.Repeat("é", 81), Code: 0}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Passes(test.checks); got != test.want {
				t.Errorf("Passes() = %t, want %t", got, test.want)
			}
		})
	}
}
