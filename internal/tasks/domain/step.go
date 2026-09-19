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
	Checking  Status = "Checking"
	Done      Status = "Done"
	Failed    Status = "Failed"
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
)

const (
	// GoalRunes is the longest goal Step accepts.
	GoalRunes = 200
	// ChatRunes is the longest chat line Step accepts.
	ChatRunes = 4000
)

// Input is one thing that happened. Goal is read for Open. Body is read for Chat.
type Input struct {
	Kind Kind
	Goal string
	Body string
	Turn string
}

// Effect is the event the writer appends. It is a value, not a write.
type Effect struct {
	Kind Kind
	From Status
	To   Status
	Goal string
	Body string
}

var (
	// ErrStatus is a status Step does not know.
	ErrStatus = errors.New("tasks: status")
	// ErrInput is an input that does not apply to this status.
	ErrInput = errors.New("tasks: input")
	// ErrTerminal is a task that has already finished.
	ErrTerminal = errors.New("tasks: terminal")
	// ErrEvidence is a pass, or an advance out of Checking. Done needs core evidence.
	ErrEvidence = errors.New("tasks: evidence")
	// ErrGoal is a goal that is empty or too long.
	ErrGoal = errors.New("tasks: goal")
	// ErrChat is a chat line that is empty or too long.
	ErrChat = errors.New("tasks: chat")
)

var advance = map[Status]Status{
	Preparing: Working,
	Planning:  Working,
	Working:   Checking,
}

// Step returns the next status and the event to append. It does not write.
// Pass is the only edge into Done. The writer persists Pass only when core
// checks have passed; Step itself still knows the edge.
func Step(status Status, input Input) (Status, Effect, error) {
	switch input.Kind {
	case Open:
		return openTask(status, strings.TrimSpace(input.Goal))
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
	default:
		return status, Effect{}, ErrInput
	}
}

func openTask(status Status, goal string) (Status, Effect, error) {
	if status != "" {
		return status, Effect{}, ErrInput
	}
	if goal == "" || utf8.RuneCountInString(goal) > GoalRunes {
		return status, Effect{}, ErrGoal
	}
	return Preparing, Effect{Kind: Open, To: Preparing, Goal: goal}, nil
}

func move(status Status) (Status, Effect, error) {
	if err := underway(status); err != nil {
		return status, Effect{}, err
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
	case Checking:
		return Done, Effect{Kind: Pass, From: Checking, To: Done}, nil
	case Preparing, Planning, Working:
		return status, Effect{}, ErrInput
	default:
		return status, Effect{}, ErrStatus
	}
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
	case Preparing, Planning, Working, Checking:
		return nil
	case Done, Failed:
		return ErrTerminal
	default:
		return ErrStatus
	}
}
