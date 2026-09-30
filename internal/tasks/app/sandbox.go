package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/upmo-ai/upmo/internal/runtime"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

// ErrSandbox is a task that needs a sandbox brain could not start: sandboxd
// is not configured, or its Create failed.
var ErrSandbox = errors.New("tasks: sandbox")

// ErrSandboxUnset is the ErrSandbox for a task that needs a sandbox while
// sandboxd is not configured (UPMO_SANDBOXD_URL unset).
var ErrSandboxUnset = fmt.Errorf("%w: sandboxd is not configured", ErrSandbox)

// Sandboxes holds the sandbox a task uses from Preparing until it ends.
// *runtime.Seats implements it.
type Sandboxes interface {
	// Ready reports whether a sandbox can be created at all.
	Ready() bool
	// Provision creates the sandbox when repo or classes need one.
	Provision(ctx context.Context, taskID, repo string, classes []runtime.Class) (bool, error)
	// Release destroys the sandbox the task holds, if any.
	Release(ctx context.Context, taskID string) error
}

// Opening is what a person names when opening a task. Repo and Classes are
// optional; either one can make the task need a sandbox.
type Opening struct {
	Goal    string
	Repo    string
	Classes []string
}

// OpenTask opens a task in Preparing and, when its repository or tool
// classes need a sandbox, creates one through boxes.
//
// An unknown class is refused before the task exists. A needed sandbox with
// no sandboxd configured is refused before the task exists too, with
// ErrSandbox. When Create fails after the task is open, the task is moved
// to Failed, any sandbox is released, and the Failed task is returned with
// ErrSandbox, so no container or Preparing task is left behind silently.
func OpenTask(ctx context.Context, book Book, boxes Sandboxes, access Access, open Opening) (Task, error) {
	classes := make([]runtime.Class, 0, len(open.Classes))
	for _, c := range open.Classes {
		classes = append(classes, runtime.Class(c))
	}
	needs, err := runtime.NeedsSandbox(open.Repo, classes)
	if err != nil {
		return Task{}, fmt.Errorf("%w: %w", domain.ErrInput, err)
	}
	if needs && (boxes == nil || !boxes.Ready()) {
		return Task{}, fmt.Errorf("%w: %w", ErrSandboxUnset, runtime.ErrSandboxdUnset)
	}

	task, err := ApplyTransition(ctx, book, access, "", domain.Input{Kind: domain.Open, Goal: open.Goal})
	if err != nil || !needs {
		return task, err
	}
	if _, err := boxes.Provision(ctx, task.ID, open.Repo, classes); err != nil {
		// The caller may have gone; cleanup still runs.
		cleanup := context.WithoutCancel(ctx)
		failed, ferr := book.Transition(cleanup, access, task.ID, domain.Input{Kind: domain.Fail})
		if ferr == nil {
			task = failed
		}
		return task, errors.Join(fmt.Errorf("%w: %w", ErrSandbox, err), boxes.Release(cleanup, task.ID), ferr)
	}
	return task, nil
}

// StepTask applies one transition to an open task. When the task ends, in
// Done or Failed, the sandbox it holds is released. A failed release does
// not undo the transition: runtime logs it and sandboxd reclaims the
// sandbox at its TTL.
func StepTask(ctx context.Context, book Book, boxes Sandboxes, access Access, taskID string, input domain.Input) (Task, error) {
	task, err := ApplyTransition(ctx, book, access, taskID, input)
	if err != nil {
		return task, err
	}
	if boxes != nil && (task.Status == domain.Done || task.Status == domain.Failed) {
		_ = boxes.Release(context.WithoutCancel(ctx), task.ID)
	}
	return task, nil
}
