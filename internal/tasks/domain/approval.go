package domain

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// ApprovalState is where a stored approval is. It starts Pending and a person
// decides it once, or the task ends while it waits and it closes.
type ApprovalState string

const (
	ApprovalPending  ApprovalState = "pending"
	ApprovalApproved ApprovalState = "approved"
	ApprovalDenied   ApprovalState = "denied"
	ApprovalClosed   ApprovalState = "closed"
)

// Reasons an approval is asked for.
const (
	ReasonAsk         = "ask"
	ReasonExpired     = "expired"
	ReasonPlan        = "plan"
	ReasonQuestion    = "question"
	ReasonTrifecta    = "trifecta"
	ReasonWriteguard  = "writeguard"
	ReasonStalled     = "stalled"
	ReasonFailing     = "failing"
	ReasonReviewLimit = "review_limit"
	ReasonUnknown     = "unknown"
)

const DeniedToolResult = "denied by a person"

const (
	// ToolBytes is the longest tool name an approval accepts, in bytes.
	ToolBytes = 64
	// ActionRunes is the longest action sentence an approval accepts.
	ActionRunes = 200
)

// DeniedLine is the chat line a denial leaves so the agent sees it.
const DeniedLine = "The action was refused. Find another way."

var (
	// ErrApproval is an approval request with a bad tool, reason or action.
	// It never carries the tool text.
	ErrApproval = errors.New("tasks: approval")
	// ErrDecided is a decision on an approval that was already decided.
	ErrDecided = errors.New("tasks: approval decided")
	// ErrClosed is a decision on an approval whose task ended while it waited.
	ErrClosed = errors.New("tasks: approval closed")
	// ErrPending is a request while the task still has a pending approval.
	ErrPending = errors.New("tasks: approval pending")
)

// credentialMarks are substrings that mark a credential. A tool name that
// holds one is refused.
var credentialMarks = []string{"upmo_pat_", "upmo_s2s_", "upmo_svc_", "bearer", "sk-"}

// Ask is a checked request for one approval.
type Ask struct {
	Tool   string
	Reason string
	Action string
}

// NewAsk trims and checks a request. Tool is a non-empty name of at most
// ToolBytes bytes with no credential mark. Reason is one of the ten stored
// reasons. Action is the one sentence the card shows, 1 to ActionRunes runes.
func NewAsk(tool, reason, action string) (Ask, error) {
	tool = strings.TrimSpace(tool)
	reason = strings.TrimSpace(reason)
	action = strings.TrimSpace(action)
	if tool == "" || len(tool) > ToolBytes || hasCredentialMark(tool) {
		return Ask{}, ErrApproval
	}
	if !ValidReason(reason) {
		return Ask{}, ErrApproval
	}
	if action == "" || utf8.RuneCountInString(action) > ActionRunes {
		return Ask{}, ErrApproval
	}
	return Ask{Tool: tool, Reason: reason, Action: action}, nil
}

// Decide returns the state a decision leaves. Only a pending approval can be
// decided, and only to approved (Approve) or denied (Deny). A closed one was
// settled by the task ending, and a decided one stays as it is.
func Decide(state ApprovalState, kind Kind) (ApprovalState, error) {
	if kind != Approve && kind != Deny {
		return state, ErrInput
	}
	if state == ApprovalClosed {
		return state, ErrClosed
	}
	if state != ApprovalPending {
		return state, ErrDecided
	}
	if kind == Approve {
		return ApprovalApproved, nil
	}
	return ApprovalDenied, nil
}

// ValidReason reports whether reason is one of the ten stored approval reasons.
func ValidReason(reason string) bool {
	switch reason {
	case ReasonAsk, ReasonExpired, ReasonPlan, ReasonQuestion, ReasonTrifecta, ReasonWriteguard, ReasonStalled, ReasonFailing, ReasonReviewLimit, ReasonUnknown:
		return true
	default:
		return false
	}
}

func hasCredentialMark(text string) bool {
	low := strings.ToLower(text)
	for _, mark := range credentialMarks {
		if strings.Contains(low, mark) {
			return true
		}
	}
	return false
}
