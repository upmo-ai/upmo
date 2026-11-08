package app

import (
	"time"

	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

// TaskSummary is the part of the read model a list shows. Money is in cents.
type TaskSummary struct {
	ID    string
	Title string
	// State is the stored status. Server-side callers read it; a person sees Status.
	State  domain.Status
	Status domain.Shown
	// Phase is the stored status while it is a phase, else the last phase.
	Phase      domain.Status
	SpentCents int
	LimitCents int
	UpdatedAt  time.Time
}

// TaskView is the read model of one task for the person who can see it.
type TaskView struct {
	TaskSummary
	Result   string
	Detail   string
	Subtasks []SubtaskView
	// Events are the task events and chat lines in time order.
	Events []Event
}

// SubtaskView is a direct child of a task.
type SubtaskView struct {
	ID     string
	Title  string
	Status domain.Shown
}

// Event is one line of a task's history: a transition or a chat message.
type Event struct {
	ID      string
	Time    time.Time
	Phase   domain.Status
	Type    string
	Message string
}

// MessageType is the Type of an Event that holds a chat line.
const MessageType = "message"
