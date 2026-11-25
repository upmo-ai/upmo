package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestAcceptPlanEnforcesUnitAndCriteriaLimitsAndSourceHierarchy(t *testing.T) {
	one := Criterion{Text: "Invoices match", Kind: CheckCommand, Target: "verify"}
	two := Criterion{Text: "Ledger matches", Kind: CheckRule}
	if _, _, err := AcceptPlan(FlowStandard, nil, nil); !errors.Is(err, ErrPlan) {
		t.Fatalf("0 units = %v", err)
	}
	tooMany := make([]Unit, 13)
	for i := range tooMany {
		tooMany[i] = Unit{Title: "Unit", Criteria: []Criterion{one, two}}
	}
	if _, _, err := AcceptPlan(FlowStandard, tooMany, nil); !errors.Is(err, ErrPlan) {
		t.Fatalf("13 units = %v", err)
	}
	_, digest, err := AcceptPlan(FlowStandard, []Unit{{Title: "Only", Criteria: []Criterion{one, two}}}, nil)
	if err != nil || len(digest) != 64 {
		t.Fatalf("1 unit digest = %q %v", digest, err)
	}
	if _, again, err := AcceptPlan(FlowStandard, []Unit{{Title: "Only", Criteria: []Criterion{one, two}}}, nil); err != nil || again != digest {
		t.Fatalf("digest changed %q %q %v", digest, again, err)
	}
	dozen := make([]Unit, 12)
	for i := range dozen {
		dozen[i] = Unit{Title: "Unit", Criteria: []Criterion{one, two}}
	}
	if _, digest, err := AcceptPlan(FlowStandard, dozen, nil); err != nil || len(digest) != 64 {
		t.Fatalf("12 units = %q %v", digest, err)
	}
	if _, _, err := AcceptPlan(FlowStandard, []Unit{{Title: "Thin", Criteria: []Criterion{one}}}, nil); !errors.Is(err, ErrPlan) {
		t.Fatalf("standard one criterion = %v", err)
	}
	if _, digest, err := AcceptPlan(FlowLight, []Unit{{Title: "Thin", Criteria: []Criterion{two}}}, nil); err != nil || len(digest) != 64 {
		t.Fatalf("light one criterion = %q %v", digest, err)
	}
	seven := make([]Criterion, 7)
	for i := range seven {
		seven[i] = two
	}
	if _, _, err := AcceptPlan(FlowLight, []Unit{{Title: "Wide", Criteria: seven}}, nil); !errors.Is(err, ErrPlan) {
		t.Fatalf("7 criteria = %v", err)
	}
	if _, _, err := AcceptPlan(FlowStandard, []Unit{{Title: "Bad", Criteria: []Criterion{
		{Text: "Run a shell", Kind: "shell", Target: "sh"}, two,
	}}}, nil); !errors.Is(err, ErrPlan) {
		t.Fatalf("shell = %v", err)
	}
	for _, kind := range []CheckKind{CheckExists, CheckCommand} {
		if _, _, err := AcceptPlan(FlowStandard, []Unit{{Title: "Bare", Criteria: []Criterion{
			{Text: "Look", Kind: kind}, two,
		}}}, nil); !errors.Is(err, ErrPlan) {
			t.Fatalf("%s empty target = %v", kind, err)
		}
	}

	locked := Criterion{Text: "All invoices match", Kind: CheckCommand, Source: SourceTemplate, Target: "verify-invoices"}
	kept := []Unit{{Title: "Books", Criteria: []Criterion{locked, {Text: "Extra", Kind: CheckRule, Source: SourceModel}}}}
	if _, _, err := AcceptPlan(FlowStandard, kept, []Criterion{locked}); err != nil {
		t.Fatalf("kept locked = %v", err)
	}
	dropped := []Unit{{Title: "Books", Criteria: []Criterion{one, two}}}
	if _, _, err := AcceptPlan(FlowStandard, dropped, []Criterion{locked}); !errors.Is(err, ErrPlan) {
		t.Fatalf("dropped locked = %v", err)
	}
	changedKind := []Unit{{Title: "Books", Criteria: []Criterion{
		{Text: locked.Text, Kind: CheckRule, Source: SourceTemplate, Target: locked.Target}, two,
	}}}
	if _, _, err := AcceptPlan(FlowStandard, changedKind, []Criterion{locked}); !errors.Is(err, ErrPlan) {
		t.Fatalf("changed kind = %v", err)
	}
	changedTarget := []Unit{{Title: "Books", Criteria: []Criterion{
		{Text: locked.Text, Kind: CheckCommand, Source: SourceTemplate, Target: "other"}, two,
	}}}
	if _, _, err := AcceptPlan(FlowStandard, changedTarget, []Criterion{locked}); !errors.Is(err, ErrPlan) {
		t.Fatalf("changed target = %v", err)
	}
	lowered := []Unit{{Title: "Books", Criteria: []Criterion{
		{Text: locked.Text, Kind: CheckCommand, Source: SourceModel, Target: locked.Target}, two,
	}}}
	if _, _, err := AcceptPlan(FlowStandard, lowered, []Criterion{locked}); !errors.Is(err, ErrPlan) {
		t.Fatalf("lowered source = %v", err)
	}
}

func TestDueSkipsStandingPassesAndOrdersCheapestFirst(t *testing.T) {
	plan, _, err := AcceptPlan(FlowStandard, []Unit{
		{Title: "One", Criteria: []Criterion{
			{Text: "File exists", Kind: CheckExists, Target: "ledger.csv"},
			{Text: "Command exits", Kind: CheckCommand, Target: "go test"},
		}},
		{Title: "Two", Criteria: []Criterion{
			{Text: "Cite the source", Kind: CheckCitation, Target: "notes"},
			{Text: "Rule holds", Kind: CheckRule},
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	due := Due(plan, nil, nil, nil)
	got := make([]CheckKind, len(due))
	for i, item := range due {
		got[i] = item.Kind
	}
	want := []CheckKind{CheckExists, CheckCommand, CheckCitation, CheckRule}
	if len(got) != len(want) {
		t.Fatalf("due = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("due = %v, want %v", got, want)
		}
	}

	exists := plan.Units[0].Criteria[0]
	passed := Check{
		Name: "file", Code: 0, Unit: 1, Criterion: 1,
		Verdict: VerdictPass, Digest: CheckDigest(exists, "v1"),
	}
	due = Due(plan, []Check{passed}, nil, map[int]string{1: "v1"})
	for _, item := range due {
		if item.Kind == CheckExists {
			t.Fatalf("standing pass was due: %+v", item)
		}
	}
	due = Due(plan, []Check{passed}, nil, map[int]string{1: "v2"})
	var prior bool
	for _, item := range due {
		if item.Kind == CheckExists {
			prior = item.PriorPassed
		}
	}
	if !prior {
		t.Fatalf("changed input did not return the exists check as a prior pass: %+v", due)
	}

	rule := plan.Units[1].Criteria[1]
	due = Due(plan, nil, []ReviewFinding{{
		Unit: 2, Criterion: rule.Criterion, State: FindingAccepted,
	}}, nil)
	for _, item := range due {
		if item.Kind == CheckRule {
			t.Fatalf("accepted rule was due: %+v", item)
		}
	}
}

func TestBrakeStallReplanFailureAndReviewLimits(t *testing.T) {
	first := Brake(BrakeState{}, "u1:c1:exit 1")
	second := Brake(BrakeState{Stalls: first.Stalls, Failures: first.Failures, LastGap: "u1:c1:exit 1"}, "u1:c1:exit 1")
	if second.Action != BrakeReplan || !second.Replanned || second.Stalls != 0 || second.Failures != 0 {
		t.Fatalf("replan = %+v", second)
	}
	held := Brake(BrakeState{}, "u1:c1:exit 1")
	again := Brake(BrakeState{
		Stalls: held.Stalls, Failures: held.Failures, LastGap: "u1:c1:exit 1", Replanned: true,
	}, "u1:c1:exit 1")
	if again.Action != BrakeHold || again.Reason != ReasonStalled {
		t.Fatalf("stalled = %+v", again)
	}

	state := BrakeState{}
	var last BrakeDecision
	for _, gap := range []string{"g1", "g2", "g3"} {
		last = Brake(state, gap)
		state = BrakeState{Stalls: last.Stalls, Failures: last.Failures, LastGap: gap, Replanned: last.Replanned}
	}
	if last.Action != BrakeHold || last.Reason != ReasonFailing || last.Failures != 3 {
		t.Fatalf("failing = %+v", last)
	}

	state = BrakeState{}
	for i := 0; i < 4; i++ {
		last = Brake(state, "")
		if last.Action != BrakeContinue || last.Failures != 0 {
			t.Fatalf("empty gap %d = %+v", i, last)
		}
		state = BrakeState{Stalls: last.Stalls, Failures: last.Failures, Replanned: last.Replanned}
	}
	kept := Brake(BrakeState{Failures: 2}, "")
	if kept.Action != BrakeContinue || kept.Failures != 2 {
		t.Fatalf("empty gap keeps failures = %+v", kept)
	}
	review := Brake(BrakeState{ReviewRound: 3, Failures: 4}, "g1")
	if review.Action != BrakeHold || review.Reason != ReasonReviewLimit || review.Failures != 4 {
		t.Fatalf("review = %+v", review)
	}
	unknown := Brake(BrakeState{HasUnknown: true, Failures: 2}, "g1")
	if unknown.Action != BrakeHold || unknown.Reason != ReasonUnknown || unknown.Failures != 2 {
		t.Fatalf("unknown = %+v", unknown)
	}
}

func TestTightenAndPlanNeedsPersonFloors(t *testing.T) {
	got, err := Tighten(PlanApprovalRisky, PlanApprovalAlways)
	if err != nil || got != PlanApprovalAlways {
		t.Fatalf("risky, always = %s %v", got, err)
	}
	got, err = Tighten(PlanApprovalAlways, PlanApprovalNever)
	if err != nil || got != PlanApprovalAlways {
		t.Fatalf("always, never = %s %v", got, err)
	}
	got, err = Tighten(PlanApprovalRisky, PlanApprovalNever)
	if err != nil || got != PlanApprovalRisky {
		t.Fatalf("risky, never = %s %v", got, err)
	}
	if !PlanNeedsPerson(PlanApprovalNever, Facts{Tainted: true}) ||
		!PlanNeedsPerson(PlanApprovalNever, Facts{OverBudget: true}) ||
		!PlanNeedsPerson(PlanApprovalNever, Facts{DigestChanged: true}) ||
		!PlanNeedsPerson(PlanApprovalNever, Facts{HasImpact: true}) ||
		PlanNeedsPerson(PlanApprovalNever, Facts{}) {
		t.Fatal("never floors")
	}
	if !PlanNeedsPerson(PlanApprovalRisky, Facts{DeciderAsk: true}) || PlanNeedsPerson(PlanApprovalRisky, Facts{}) {
		t.Fatal("risky ask")
	}
}

func TestPlanPassesRejectsAStaleDigest(t *testing.T) {
	plan, _, err := AcceptPlan(FlowLight, []Unit{{
		Title: "One", Criteria: []Criterion{{Text: "Ledger matches", Kind: CheckRule}},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	crit := plan.Units[0].Criteria[0]
	fresh := Check{
		Name: "go test", Code: 0, Unit: 1, Criterion: 1,
		Verdict: VerdictPass, Digest: CheckDigest(crit, "v1"),
	}
	if !PlanPasses(&plan, []Check{fresh}, nil, map[int]string{1: "v1"}) {
		t.Fatal("matching digest did not pass")
	}
	if PlanPasses(&plan, []Check{fresh}, nil, map[int]string{1: "v2"}) {
		t.Fatal("stale input passed")
	}
	stale := fresh
	stale.Digest = strings.Repeat("0", 64)
	if PlanPasses(&plan, []Check{stale}, nil, map[int]string{1: "v1"}) {
		t.Fatal("zero digest passed")
	}
	empty := plan
	empty.Units = nil
	if PlanPasses(&empty, []Check{fresh}, nil, map[int]string{1: "v1"}) {
		t.Fatal("empty units passed")
	}
	if !PlanPasses(nil, []Check{{Name: "go test", Code: 0}}, nil, nil) {
		t.Fatal("nil plan did not use Passes")
	}
}
