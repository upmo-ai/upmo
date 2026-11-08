package app

import (
	"context"
	"errors"

	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

// ErrNoEvent is a stored chat line that the task read model does not hold.
var ErrNoEvent = errors.New("tasks: no such event")

// Stepped is the answer to one step. Posted is the event a chat step added
// and is nil for any other step.
type Stepped struct {
	Task   TaskView
	Posted *Event
}

// StepView applies one transition as StepTask does, then reads the task back
// as the person sees it. For a chat step it also returns the event that line
// became.
func StepView(ctx context.Context, book Book, view Viewer, boxes Sandboxes, access Access, taskID string, input domain.Input) (Stepped, error) {
	task, err := StepTask(ctx, book, boxes, access, taskID, input)
	if err != nil {
		return Stepped{}, err
	}
	read, err := ViewTask(ctx, view, access, task.ID)
	if err != nil {
		return Stepped{}, err
	}
	out := Stepped{Task: read}
	if task.ChatID == "" {
		return out, nil
	}
	for i := range read.Events {
		if read.Events[i].ID == task.ChatID {
			out.Posted = &read.Events[i]
			return out, nil
		}
	}
	return Stepped{}, ErrNoEvent
}
