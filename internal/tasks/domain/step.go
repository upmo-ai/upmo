// Package domain is the task state machine. Step does no I/O.
package domain

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// Status is where a task is. The words are the glossary.
type Status string

const (
	Preparing Status = "Preparing"
	Planning  Status = "Planning"
	Working   Status = "Working"
	Verifying Status = "Verifying"
	Done      Status = "Done"
	Failed    Status = "Failed"
	// NeedsApproval is a Working task whose connector call is paused.
	NeedsApproval Status = "Needs approval"
)

// Kind is the input that Step reads.
type Kind string

const (
	Open    Kind = "open"
	Advance Kind = "advance"
	// Plan enters Planning from Preparing.
	Plan Kind = "plan"
	Fail Kind = "fail"
	Pass Kind = "pass"
	Chat Kind = "chat"
	// Pause enters Needs approval from Working once a connector pause is stored.
	Pause Kind = "pause"
	// Resume returns to Working once that same stored pause is taken.
	Resume Kind = "resume"
	// Approve and Deny are Resume once a person has decided a stored approval.
	// The reason and the stored from-status choose the edge.
	Approve Kind = "approve"
	Deny    Kind = "deny"
	// AskPlan, Rework, Replan, and Hold are the verification edges.
	AskPlan Kind = "ask_plan"
	Rework  Kind = "rework"
	Replan  Kind = "replan"
	Hold    Kind = "hold"
	// Fund raises a non-terminal root budget. It is not a status transition.
	Fund Kind = "fund"
)

const (
	// GoalRunes is the longest goal Step accepts.
	GoalRunes = 200
	// ChatRunes is the longest chat line Step accepts.
	ChatRunes = 4000
)

// Input is one thing that happened. Goal is read for Open. Body is read for Chat.
type Input struct {
	Kind   Kind
	Goal   string
	Body   string
	Turn   string
	Reason string
	From   Status
	To     Status
}

// Effect is the event the writer appends. It is a value, not a write.
type Effect struct {
	Kind        Kind
	From        Status
	To          Status
	Goal        string
	Body        string
	Reason      string
	Cause       string
	ResetBrakes bool
}

var (
	// ErrStatus is a status Step does not know.
	ErrStatus = errors.New("tasks: status")
	// ErrInput is an input that does not apply to this status.
	ErrInput = errors.New("tasks: input")
	// ErrTerminal is a task that has already finished.
	ErrTerminal = errors.New("tasks: terminal")
	// ErrEvidence is a pass, or an advance out of Verifying. Done needs core evidence.
	ErrEvidence = errors.New("tasks: evidence")
	// ErrGoal is a goal that is empty or too long.
	ErrGoal = errors.New("tasks: goal")
	// ErrChat is a chat line that is empty or too long.
	ErrChat = errors.New("tasks: chat")
)

var advance = map[Status]Status{
	Preparing: Working,
	Planning:  Working,
	Working:   Verifying,
}

// Step returns the next status and the event to append. It does not write.
// Pass is the only edge into Done. The writer persists Pass only when core
// checks have passed; Step itself still knows the edge.
func Step(status Status, input Input) (Status, Effect, error) {
	switch input.Kind {
	case Open:
		return openTask(status, strings.TrimSpace(input.Goal), input.To)
	case Advance:
		return move(status)
	case Plan:
		return planTask(status)
	case Fail:
		return fail(status)
	case Pass:
		return pass(status)
	case Chat:
		return chat(status, strings.TrimSpace(input.Body))
	case Pause:
		return edge(status, Pause, Working, NeedsApproval)
	case Resume:
		return edge(status, Resume, NeedsApproval, Working)
	case AskPlan:
		return edge(status, AskPlan, Planning, NeedsApproval)
	case Rework:
		return edge(status, Rework, Verifying, Working)
	case Replan:
		return edge(status, Replan, Working, Planning)
	case Hold:
		return holdTask(status, input)
	case Approve:
		return approveTask(status, input)
	case Deny:
		return denyTask(status, input)
	default:
		return status, Effect{}, ErrInput
	}
}

func openTask(status Status, goal string, to Status) (Status, Effect, error) {
	if status != "" {
		return status, Effect{}, ErrInput
	}
	if goal == "" || utf8.RuneCountInString(goal) > GoalRunes {
		return status, Effect{}, ErrGoal
	}
	if to == "" {
		to = Preparing
	}
	switch to {
	case Preparing, Planning, Working:
	default:
		return status, Effect{}, ErrInput
	}
	return to, Effect{Kind: Open, To: to, Goal: goal}, nil
}

func move(status Status) (Status, Effect, error) {
	if err := underway(status); err != nil {
		return status, Effect{}, err
	}
	if status == NeedsApproval {
		return status, Effect{}, ErrInput
	}
	next, ok := advance[status]
	if !ok {
		return status, Effect{}, ErrEvidence
	}
	return next, Effect{Kind: Advance, From: status, To: next}, nil
}

func planTask(status Status) (Status, Effect, error) {
	if err := underway(status); err != nil {
		return status, Effect{}, err
	}
	if status != Preparing {
		return status, Effect{}, ErrInput
	}
	return Planning, Effect{Kind: Plan, From: Preparing, To: Planning}, nil
}

func fail(status Status) (Status, Effect, error) {
	if err := underway(status); err != nil {
		return status, Effect{}, err
	}
	return Failed, Effect{Kind: Fail, From: status, To: Failed}, nil
}

func pass(status Status) (Status, Effect, error) {
	switch status {
	case Done, Failed:
		return status, Effect{}, ErrTerminal
	case Verifying:
		return Done, Effect{Kind: Pass, From: Verifying, To: Done}, nil
	case Preparing, Planning, Working, NeedsApproval:
		return status, Effect{}, ErrInput
	default:
		return status, Effect{}, ErrStatus
	}
}

func holdTask(status Status, input Input) (Status, Effect, error) {
	if err := underway(status); err != nil {
		return status, Effect{}, err
	}
	if status != Working && status != Verifying {
		return status, Effect{}, ErrInput
	}
	reason := strings.TrimSpace(input.Reason)
	if reason != "" && !holdReason(status, reason) {
		return status, Effect{}, ErrInput
	}
	return NeedsApproval, Effect{Kind: Hold, From: status, To: NeedsApproval, Reason: reason}, nil
}

func holdReason(status Status, reason string) bool {
	switch reason {
	case ReasonStalled, ReasonFailing, ReasonReviewLimit, ReasonUnknown:
		return status == Working || status == Verifying
	case ReasonAsk, ReasonExpired, ReasonQuestion, ReasonTrifecta, ReasonWriteguard:
		// A batch hold leaves Verifying with reason ask.
		return status == Working || status == Verifying
	default:
		return false
	}
}

func approveTask(status Status, input Input) (Status, Effect, error) {
	if err := underway(status); err != nil {
		return status, Effect{}, err
	}
	if status != NeedsApproval {
		return status, Effect{}, ErrInput
	}
	reason := strings.TrimSpace(input.Reason)
	if reason == "" {
		reason = ReasonAsk
	}
	switch reason {
	case ReasonPlan:
		return Working, Effect{Kind: Approve, From: NeedsApproval, To: Working, Reason: ReasonPlan}, nil
	case ReasonQuestion:
		answer := strings.TrimSpace(input.Body)
		if answer == "" || utf8.RuneCountInString(answer) > ChatRunes {
			return status, Effect{}, ErrChat
		}
		return Working, Effect{Kind: Approve, From: NeedsApproval, To: Working, Reason: ReasonQuestion, Body: answer}, nil
	case ReasonStalled, ReasonFailing, ReasonReviewLimit, ReasonUnknown:
		target := input.From
		if target == "" {
			target = Working
		}
		if target != Working && target != Verifying {
			return status, Effect{}, ErrInput
		}
		return target, Effect{Kind: Approve, From: NeedsApproval, To: target, Reason: reason, ResetBrakes: true}, nil
	case ReasonAsk, ReasonExpired, ReasonTrifecta, ReasonWriteguard:
		return Working, Effect{Kind: Approve, From: NeedsApproval, To: Working, Reason: reason}, nil
	default:
		return status, Effect{}, ErrInput
	}
}

func denyTask(status Status, input Input) (Status, Effect, error) {
	if err := underway(status); err != nil {
		return status, Effect{}, err
	}
	if status != NeedsApproval {
		return status, Effect{}, ErrInput
	}
	reason := strings.TrimSpace(input.Reason)
	if reason == "" {
		reason = ReasonAsk
	}
	switch reason {
	case ReasonPlan:
		note := strings.TrimSpace(input.Body)
		if utf8.RuneCountInString(note) > ChatRunes {
			return status, Effect{}, ErrChat
		}
		return Planning, Effect{Kind: Deny, From: NeedsApproval, To: Planning, Reason: ReasonPlan, Body: note}, nil
	case ReasonQuestion:
		return Failed, Effect{Kind: Deny, From: NeedsApproval, To: Failed, Reason: ReasonQuestion, Cause: CauseCanceled}, nil
	case ReasonStalled, ReasonFailing, ReasonReviewLimit, ReasonUnknown:
		return Failed, Effect{Kind: Deny, From: NeedsApproval, To: Failed, Reason: reason, Cause: reason}, nil
	case ReasonAsk, ReasonExpired, ReasonTrifecta, ReasonWriteguard:
		return Working, Effect{Kind: Deny, From: NeedsApproval, To: Working, Reason: reason, Body: DeniedToolResult}, nil
	default:
		return status, Effect{}, ErrInput
	}
}

// edge moves from one status to another for kind, and refuses any other start.
func edge(status Status, kind Kind, from, to Status) (Status, Effect, error) {
	if err := underway(status); err != nil {
		return status, Effect{}, err
	}
	if status != from {
		return status, Effect{}, ErrInput
	}
	return to, Effect{Kind: kind, From: from, To: to}, nil
}

func chat(status Status, body string) (Status, Effect, error) {
	if err := underway(status); err != nil {
		return status, Effect{}, err
	}
	if body == "" || utf8.RuneCountInString(body) > ChatRunes {
		return status, Effect{}, ErrChat
	}
	return status, Effect{Kind: Chat, From: status, To: status, Body: body}, nil
}

func underway(status Status) error {
	switch status {
	case Preparing, Planning, Working, Verifying, NeedsApproval:
		return nil
	case Done, Failed:
		return ErrTerminal
	default:
		return ErrStatus
	}
}
