package domain

import (
	"strings"
	"unicode/utf8"
)

const CheckRunes = 80

type Check struct {
	Name      string
	Code      int
	Unit      int
	Criterion int
	Kind      CheckKind
	Verdict   Verdict
	Digest    string
	Excerpt   string
	Regressed bool
	Batch     int
	Seq       int
}

// Passes is true only when the batch recorded at least one check and every check exited 0.
func Passes(checks []Check) bool {
	if len(checks) == 0 {
		return false
	}
	for _, check := range checks {
		if !checkOK(check) {
			return false
		}
	}
	return true
}

func checkOK(check Check) bool {
	name := strings.TrimSpace(check.Name)
	if name == "" || utf8.RuneCountInString(name) > CheckRunes || check.Code != 0 {
		return false
	}
	if utf8.RuneCountInString(check.Excerpt) > CheckExcerptRunes {
		return false
	}
	return check.Verdict == "" || check.Verdict == VerdictPass
}

// PlanPasses reports whether every planned criterion is accepted or has a
// latest check for the current input. A nil plan uses Passes.
func PlanPasses(plan *TaskPlan, checks []Check, findings []ReviewFinding, inputs map[int]string) bool {
	if plan == nil {
		return Passes(checks)
	}
	if len(plan.Units) == 0 {
		return false
	}
	latest := latestChecks(checks)
	openBlock, accepted := findingIndex(findings)
	for _, unit := range plan.Units {
		if len(unit.Criteria) == 0 {
			return false
		}
		for _, crit := range unit.Criteria {
			key := pairKey{unit.Unit, crit.Criterion}
			if openBlock[key] {
				return false
			}
			if accepted[key] {
				continue
			}
			check, ok := latest[key]
			want := CheckDigest(crit, inputs[unit.Unit])
			if !ok || !checkOK(check) || check.Digest != want || !hexDigest(want) {
				return false
			}
		}
	}
	return true
}
