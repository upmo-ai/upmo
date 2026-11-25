package httpapi

import (
	"time"

	"github.com/upmo-ai/upmo/internal/tasks/app"
)

// summaryJSON is a task in a list, and the head of a task in full.
type summaryJSON struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Status         string `json:"status"`
	Phase          string `json:"phase"`
	SpentMicroUSD  int64  `json:"spent_micro_usd"`
	BudgetMicroUSD int64  `json:"budget_micro_usd"`
	UpdatedAt      string `json:"updated_at"`
}

// taskViewJSON is one task in full.
type taskViewJSON struct {
	summaryJSON
	Result   string        `json:"result"`
	Detail   string        `json:"detail"`
	Subtasks []subtaskJSON `json:"subtasks"`
	Events   []eventJSON   `json:"events"`
}

type subtaskJSON struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

type eventJSON struct {
	ID      string `json:"id"`
	Time    string `json:"time"`
	Phase   string `json:"phase"`
	Type    string `json:"type"`
	Message string `json:"message"`
}

func summaryBody(task app.TaskSummary) summaryJSON {
	return summaryJSON{
		ID:             task.ID,
		Title:          task.Title,
		Status:         string(task.Status),
		Phase:          string(task.Phase),
		SpentMicroUSD:  task.SpentMicroUSD,
		BudgetMicroUSD: task.BudgetMicroUSD,
		UpdatedAt:      task.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func taskBody(task app.TaskView) taskViewJSON {
	body := taskViewJSON{
		summaryJSON: summaryBody(task.TaskSummary),
		Result:      task.Result,
		Detail:      task.Detail,
		Subtasks:    make([]subtaskJSON, 0, len(task.Subtasks)),
		Events:      make([]eventJSON, 0, len(task.Events)),
	}
	for _, sub := range task.Subtasks {
		body.Subtasks = append(body.Subtasks, subtaskJSON{ID: sub.ID, Title: sub.Title, Status: string(sub.Status)})
	}
	for _, event := range task.Events {
		body.Events = append(body.Events, eventBody(event))
	}
	return body
}

func eventBody(event app.Event) eventJSON {
	return eventJSON{
		ID:      event.ID,
		Time:    event.Time.UTC().Format(time.RFC3339Nano),
		Phase:   string(event.Phase),
		Type:    event.Type,
		Message: event.Message,
	}
}
