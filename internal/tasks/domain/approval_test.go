package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestDecideApprovesOrDeniesPendingOnly(t *testing.T) {
	tests := []struct {
		state ApprovalState
		kind  Kind
		want  ApprovalState
		err   error
	}{
		{ApprovalPending, Approve, ApprovalApproved, nil},
		{ApprovalPending, Deny, ApprovalDenied, nil},
		{ApprovalApproved, Approve, ApprovalApproved, ErrDecided},
		{ApprovalApproved, Deny, ApprovalApproved, ErrDecided},
		{ApprovalDenied, Approve, ApprovalDenied, ErrDecided},
		{ApprovalDenied, Deny, ApprovalDenied, ErrDecided},
		{ApprovalClosed, Approve, ApprovalClosed, ErrClosed},
		{ApprovalClosed, Deny, ApprovalClosed, ErrClosed},
		{ApprovalPending, Resume, ApprovalPending, ErrInput},
		{ApprovalPending, Pause, ApprovalPending, ErrInput},
		{ApprovalPending, "", ApprovalPending, ErrInput},
		{ApprovalClosed, Resume, ApprovalClosed, ErrInput},
	}
	for _, test := range tests {
		got, err := Decide(test.state, test.kind)
		if got != test.want || !errors.Is(err, test.err) {
			t.Errorf("Decide(%q, %q) = %q, %v; want %q, %v", test.state, test.kind, got, err, test.want, test.err)
		}
	}
}

func TestApproveAndDenyTakeTheResumeEdgeOnly(t *testing.T) {
	for _, kind := range []Kind{Approve, Deny} {
		next, effect, err := Step(NeedsApproval, Input{Kind: kind})
		if err != nil || next != Working || effect.Kind != kind || effect.From != NeedsApproval || effect.To != Working {
			t.Fatalf("%s = %s %+v %v", kind, next, effect, err)
		}
		if _, _, err := Step(Working, Input{Kind: kind}); !errors.Is(err, ErrInput) {
			t.Errorf("%s from Working = %v", kind, err)
		}
		if _, _, err := Step(Done, Input{Kind: kind}); !errors.Is(err, ErrTerminal) {
			t.Errorf("%s from Done = %v", kind, err)
		}
	}
}

func TestDecisionEventsAreFixedLinesOfTypeApproval(t *testing.T) {
	tests := []struct {
		kind Kind
		want string
	}{
		{Approve, "Approval given"},
		{Deny, "Approval denied"},
	}
	for _, test := range tests {
		got, ok := EventMessage(test.kind, Working)
		if !ok || got != test.want {
			t.Errorf("EventMessage(%q) = %q, %t; want %q", test.kind, got, ok, test.want)
		}
		if EventType(test.kind) != "approval" {
			t.Errorf("EventType(%q) = %q", test.kind, EventType(test.kind))
		}
	}
	if EventType(Pause) != "pause" || EventType(Advance) != "advance" {
		t.Errorf("a transition keeps its own type: %q %q", EventType(Pause), EventType(Advance))
	}
	if DeniedLine != "The action was refused. Find another way." {
		t.Errorf("denied line = %q", DeniedLine)
	}
}

func TestNewAskChecksToolReasonAndAction(t *testing.T) {
	good, err := NewAsk("  mail.send ", " ask ", "  Send the March invoice to Sam  ")
	if err != nil || good != (Ask{Tool: "mail.send", Reason: "ask", Action: "Send the March invoice to Sam"}) {
		t.Fatalf("good = %+v %v", good, err)
	}
	if _, err := NewAsk("mail.send", "expired", "Send it"); err != nil {
		t.Errorf("expired reason = %v", err)
	}
	tests := []struct {
		name                 string
		tool, reason, action string
	}{
		{"no tool", " ", "ask", "Send it"},
		{"long tool", strings.Repeat("t", ToolBytes+1), "ask", "Send it"},
		{"credential in tool", "send-Bearer-abc", "ask", "Send it"},
		{"key prefix in tool", "sk-live", "ask", "Send it"},
		{"unknown reason", "mail.send", "always", "Send it"},
		{"no reason", "mail.send", "", "Send it"},
		{"no action", "mail.send", "ask", "  "},
		{"long action", "mail.send", "ask", strings.Repeat("a", ActionRunes+1)},
	}
	for _, test := range tests {
		if _, err := NewAsk(test.tool, test.reason, test.action); !errors.Is(err, ErrApproval) {
			t.Errorf("%s: err = %v", test.name, err)
		} else if strings.Contains(err.Error(), test.tool) && test.tool != " " {
			t.Errorf("%s: the error carries the tool text: %v", test.name, err)
		}
	}
	if _, err := NewAsk("mail.send", "ask", strings.Repeat("é", ActionRunes)); err != nil {
		t.Errorf("an action of exactly %d runes = %v", ActionRunes, err)
	}
	if _, err := NewAsk(strings.Repeat("t", ToolBytes), "ask", "Send it"); err != nil {
		t.Errorf("a tool of exactly %d bytes = %v", ToolBytes, err)
	}
}
