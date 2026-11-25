package domain

import (
	"bytes"
	"encoding/json"
)

// Shown is the status a person sees. Working stands for every phase.
type Shown string

const (
	ShownWorking       Shown = "Working"
	ShownNeedsApproval Shown = "Needs approval"
	ShownDone          Shown = "Done"
	ShownFailed        Shown = "Failed"
)

// Show collapses a stored status to the status a person sees. Preparing,
// Planning, Working and Verifying are all Working.
func Show(status Status) (Shown, error) {
	switch status {
	case Preparing, Planning, Working, Verifying:
		return ShownWorking, nil
	case NeedsApproval:
		return ShownNeedsApproval, nil
	case Done:
		return ShownDone, nil
	case Failed:
		return ShownFailed, nil
	default:
		return "", ErrStatus
	}
}

// PhaseOf returns the phase a status shows: the status itself while it is
// Preparing, Planning, Working or Verifying. Any other status shows last, the
// phase the task was in before, and Working when there was none.
func PhaseOf(status, last Status) Status {
	if isPhase(status) {
		return status
	}
	if isPhase(last) {
		return last
	}
	return Working
}

func isPhase(status Status) bool {
	switch status {
	case Preparing, Planning, Working, Verifying:
		return true
	default:
		return false
	}
}

// EventMessage is the fixed line shown for a transition event. It reports
// false for an input that is not a transition.
func EventMessage(kind Kind, to Status) (string, bool) {
	switch kind {
	case Open:
		return "Task opened", true
	case Advance:
		return "Moved on to " + string(to), true
	case Plan:
		return "Planning", true
	case Pause, AskPlan, Hold:
		return "Waiting for approval", true
	case Rework, Replan:
		return "Moved on to " + string(to), true
	case Resume, Approve:
		return "Approval given", true
	case Deny:
		return "Approval denied", true
	case Pass:
		return "Checks passed", true
	case Fail:
		return "Task failed", true
	default:
		return "", false
	}
}

// ApprovalType is the Type of a task event that records a person's decision.
const ApprovalType = "approval"

// EventType is the type a person sees for a stored transition: the kind
// itself, except that an approval and a denial are both "approval".
func EventType(kind Kind) string {
	if kind == Approve || kind == Deny {
		return ApprovalType
	}
	return string(kind)
}

// ResultText renders a stored result as text: a JSON string as itself, any
// other JSON as compact JSON, and nothing for no result.
func ResultText(raw []byte) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var out bytes.Buffer
	if json.Compact(&out, raw) != nil {
		return ""
	}
	return out.String()
}
