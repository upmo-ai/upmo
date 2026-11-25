package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestStepWalksToCheckingAndStops(t *testing.T) {
	status, effect, err := Step("", Input{Kind: Open, Goal: "  File the receipts  "})
	if err != nil || status != Preparing || effect.Goal != "File the receipts" || effect.From != "" {
		t.Fatalf("open = %s %+v %v", status, effect, err)
	}
	want := []Status{Working, Verifying}
	for _, next := range want {
		status, effect, err = Step(status, Input{Kind: Advance})
		if err != nil || status != next || effect.To != next {
			t.Fatalf("advance to %s = %s %+v %v", next, status, effect, err)
		}
	}
	if _, _, err := Step(status, Input{Kind: Advance}); !errors.Is(err, ErrEvidence) {
		t.Fatalf("advance from Verifying = %v", err)
	}
}

func TestStepPlansOnlyFromPreparing(t *testing.T) {
	status, effect, err := Step(Preparing, Input{Kind: Plan, Goal: "  leave this alone  "})
	if err != nil || status != Planning || effect.Kind != Plan || effect.From != Preparing || effect.To != Planning || effect.Goal != "" {
		t.Fatalf("plan = %s %+v %v", status, effect, err)
	}
	status, effect, err = Step(status, Input{Kind: Advance})
	if err != nil || status != Working || effect.Kind != Advance || effect.From != Planning || effect.To != Working {
		t.Fatalf("advance from Planning = %s %+v %v", status, effect, err)
	}
	if _, _, err := Step(Working, Input{Kind: Plan}); !errors.Is(err, ErrInput) {
		t.Fatalf("plan from Working = %v", err)
	}
}

func TestStepPassIsTheOnlyEdgeIntoDone(t *testing.T) {
	status, effect, err := Step(Verifying, Input{Kind: Pass})
	if err != nil || status != Done || effect.Kind != Pass || effect.From != Verifying {
		t.Fatalf("pass = %s %+v %v", status, effect, err)
	}
	if _, _, err := Step(Working, Input{Kind: Pass}); !errors.Is(err, ErrInput) {
		t.Fatalf("pass from Working = %v", err)
	}
	if _, _, err := Step(Done, Input{Kind: Pass}); !errors.Is(err, ErrTerminal) {
		t.Fatalf("pass from Done = %v", err)
	}
}

func TestStepFailFromEachOpenStatus(t *testing.T) {
	for _, status := range []Status{Preparing, Planning, Working, Verifying} {
		next, effect, err := Step(status, Input{Kind: Fail})
		if err != nil || next != Failed || effect.From != status {
			t.Fatalf("fail from %s = %s %+v %v", status, next, effect, err)
		}
	}
	if _, _, err := Step(Failed, Input{Kind: Fail}); !errors.Is(err, ErrTerminal) {
		t.Fatalf("fail from Failed = %v", err)
	}
	if _, _, err := Step(Done, Input{Kind: Chat, Body: "hello"}); !errors.Is(err, ErrTerminal) {
		t.Fatalf("chat on Done = %v", err)
	}
}

func TestStepPauseAndResumeWorking(t *testing.T) {
	status, effect, err := Step(Working, Input{Kind: Pause})
	if err != nil || status != NeedsApproval || effect != (Effect{Kind: Pause, From: Working, To: NeedsApproval}) {
		t.Fatalf("pause = %s %+v %v", status, effect, err)
	}
	if status != "Needs approval" {
		t.Fatalf("status text = %q", status)
	}
	status, effect, err = Step(status, Input{Kind: Resume})
	if err != nil || status != Working || effect != (Effect{Kind: Resume, From: NeedsApproval, To: Working}) {
		t.Fatalf("resume = %s %+v %v", status, effect, err)
	}
}

func TestStepPauseOnlyFromWorking(t *testing.T) {
	for _, status := range []Status{Preparing, Planning, Verifying, NeedsApproval} {
		if _, _, err := Step(status, Input{Kind: Pause}); !errors.Is(err, ErrInput) {
			t.Fatalf("pause from %s = %v", status, err)
		}
	}
	for _, status := range []Status{Done, Failed} {
		if _, _, err := Step(status, Input{Kind: Pause}); !errors.Is(err, ErrTerminal) {
			t.Fatalf("pause from %s = %v", status, err)
		}
	}
	if _, _, err := Step("", Input{Kind: Pause}); !errors.Is(err, ErrStatus) {
		t.Fatalf("pause from nothing = %v", err)
	}
}

func TestStepResumeOnlyFromNeedsApproval(t *testing.T) {
	for _, status := range []Status{Preparing, Planning, Working, Verifying} {
		if _, _, err := Step(status, Input{Kind: Resume}); !errors.Is(err, ErrInput) {
			t.Fatalf("resume from %s = %v", status, err)
		}
	}
	for _, status := range []Status{Done, Failed} {
		if _, _, err := Step(status, Input{Kind: Resume}); !errors.Is(err, ErrTerminal) {
			t.Fatalf("resume from %s = %v", status, err)
		}
	}
}

func TestStepNeedsApprovalRefusesOtherMoves(t *testing.T) {
	for _, kind := range []Kind{Advance, Plan, Pass, Open} {
		status, _, err := Step(NeedsApproval, Input{Kind: kind, Goal: "again"})
		if !errors.Is(err, ErrInput) || status != NeedsApproval {
			t.Fatalf("%s from Needs approval = %s %v", kind, status, err)
		}
	}
	status, effect, err := Step(NeedsApproval, Input{Kind: Fail})
	if err != nil || status != Failed || effect.From != NeedsApproval {
		t.Fatalf("fail from Needs approval = %s %+v %v", status, effect, err)
	}
	status, effect, err = Step(NeedsApproval, Input{Kind: Chat, Body: "still there?"})
	if err != nil || status != NeedsApproval || effect.To != NeedsApproval {
		t.Fatalf("chat on Needs approval = %s %+v %v", status, effect, err)
	}
}

func TestStepChatKeepsTheStatus(t *testing.T) {
	status, effect, err := Step(Planning, Input{Kind: Chat, Body: "  the figures  "})
	if err != nil || status != Planning || effect.Body != "the figures" || effect.To != Planning {
		t.Fatalf("chat = %s %+v %v", status, effect, err)
	}
	if _, _, err := Step(Planning, Input{Kind: Chat}); !errors.Is(err, ErrChat) {
		t.Fatalf("blank chat = %v", err)
	}
	long := strings.Repeat("é", ChatRunes)
	if _, effect, err := Step(Planning, Input{Kind: Chat, Body: long}); err != nil || utfLen(effect.Body) != ChatRunes {
		t.Fatalf("chat at the rune cap = %+v %v", effect, err)
	}
	if _, _, err := Step(Planning, Input{Kind: Chat, Body: long + "é"}); !errors.Is(err, ErrChat) {
		t.Fatalf("chat over the rune cap = %v", err)
	}
}

func TestStepRejectsABadGoal(t *testing.T) {
	if _, _, err := Step("", Input{Kind: Open}); !errors.Is(err, ErrGoal) {
		t.Fatalf("blank goal = %v", err)
	}
	goal := strings.Repeat("é", GoalRunes)
	status, _, err := Step("", Input{Kind: Open, Goal: goal})
	if err != nil || status != Preparing {
		t.Fatalf("goal at the rune cap = %s %v", status, err)
	}
	if _, _, err := Step("", Input{Kind: Open, Goal: goal + "é"}); !errors.Is(err, ErrGoal) {
		t.Fatalf("goal over the rune cap = %v", err)
	}
	if _, _, err := Step(Preparing, Input{Kind: Open, Goal: "again"}); !errors.Is(err, ErrInput) {
		t.Fatalf("second open = %v", err)
	}
	if _, _, err := Step("", Input{Kind: Advance}); !errors.Is(err, ErrStatus) {
		t.Fatalf("advance from nothing = %v", err)
	}
	if _, _, err := Step(Preparing, Input{Kind: "sideways"}); !errors.Is(err, ErrInput) {
		t.Fatalf("unknown input = %v", err)
	}
}

func TestStepVerificationEdgesAndDeterministicApproveDenyRouting(t *testing.T) {
	status, _, err := Step("", Input{Kind: Open, Goal: "File the receipts", To: Planning})
	if err != nil || status != Planning {
		t.Fatalf("open planning = %s %v", status, err)
	}
	status, _, err = Step("", Input{Kind: Open, Goal: "File the receipts", To: Working})
	if err != nil || status != Working {
		t.Fatalf("open working = %s %v", status, err)
	}
	if _, _, err := Step("", Input{Kind: Open, Goal: "File the receipts", To: Verifying}); !errors.Is(err, ErrInput) {
		t.Fatalf("open verifying = %v", err)
	}

	status, effect, err := Step(Planning, Input{Kind: AskPlan})
	if err != nil || status != NeedsApproval || effect.Kind != AskPlan || effect.From != Planning || effect.To != NeedsApproval {
		t.Fatalf("ask plan = %s %+v %v", status, effect, err)
	}
	status, effect, err = Step(Verifying, Input{Kind: Rework})
	if err != nil || status != Working || effect.Kind != Rework || effect.To != Working {
		t.Fatalf("rework = %s %+v %v", status, effect, err)
	}
	status, effect, err = Step(Working, Input{Kind: Replan})
	if err != nil || status != Planning || effect.Kind != Replan || effect.To != Planning {
		t.Fatalf("replan = %s %+v %v", status, effect, err)
	}
	for _, from := range []Status{Working, Verifying} {
		status, effect, err = Step(from, Input{Kind: Hold, Reason: ReasonAsk})
		if from == Verifying {
			status, effect, err = Step(from, Input{Kind: Hold, Reason: ReasonStalled})
		}
		if err != nil || status != NeedsApproval || effect.Kind != Hold || effect.From != from {
			t.Fatalf("hold from %s = %s %+v %v", from, status, effect, err)
		}
	}

	status, effect, err = Step(NeedsApproval, Input{Kind: Approve, Reason: ReasonPlan})
	if err != nil || status != Working || effect.To != Working {
		t.Fatalf("approve plan = %s %+v %v", status, effect, err)
	}
	status, effect, err = Step(NeedsApproval, Input{Kind: Deny, Reason: ReasonPlan, Body: "Add unit 2"})
	if err != nil || status != Planning || effect.Body != "Add unit 2" {
		t.Fatalf("deny plan = %s %+v %v", status, effect, err)
	}
	if _, _, err := Step(NeedsApproval, Input{Kind: Approve, Reason: ReasonQuestion}); !errors.Is(err, ErrChat) {
		t.Fatalf("empty answer = %v", err)
	}
	status, effect, err = Step(NeedsApproval, Input{Kind: Approve, Reason: ReasonQuestion, Body: "Use EUR"})
	if err != nil || status != Working || effect.Body != "Use EUR" {
		t.Fatalf("approve question = %s %+v %v", status, effect, err)
	}
	status, effect, err = Step(NeedsApproval, Input{Kind: Deny, Reason: ReasonQuestion})
	if err != nil || status != Failed || effect.Cause != CauseCanceled {
		t.Fatalf("deny question = %s %+v %v", status, effect, err)
	}
	for _, reason := range []string{ReasonStalled, ReasonFailing, ReasonReviewLimit, ReasonUnknown} {
		status, effect, err = Step(NeedsApproval, Input{Kind: Approve, Reason: reason, From: Verifying})
		if err != nil || status != Verifying || !effect.ResetBrakes {
			t.Fatalf("approve %s = %s %+v %v", reason, status, effect, err)
		}
		status, effect, err = Step(NeedsApproval, Input{Kind: Deny, Reason: reason})
		if err != nil || status != Failed || effect.Cause != reason {
			t.Fatalf("deny %s = %s %+v %v", reason, status, effect, err)
		}
	}
	for _, reason := range []string{ReasonAsk, ReasonExpired, ReasonTrifecta, ReasonWriteguard} {
		status, effect, err = Step(NeedsApproval, Input{Kind: Approve, Reason: reason})
		if err != nil || status != Working {
			t.Fatalf("approve %s = %s %+v %v", reason, status, effect, err)
		}
		status, effect, err = Step(NeedsApproval, Input{Kind: Deny, Reason: reason})
		if err != nil || status != Working || effect.Body != DeniedToolResult {
			t.Fatalf("deny %s = %s %+v %v", reason, status, effect, err)
		}
	}
}

func utfLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}
