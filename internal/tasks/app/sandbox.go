package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/upmo-ai/upmo/internal/runtime"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

// ErrSandbox is a task that needs a sandbox brain could not start: sandboxd
// is not configured or its Create failed. A provisioning phase fails after
// Open has returned, so it fails the task without reaching the caller.
var ErrSandbox = errors.New("tasks: sandbox")

// ErrSandboxUnset is the ErrSandbox for a task that needs a sandbox while
// sandboxd is not configured (UPMO_SANDBOXD_URL unset).
var ErrSandboxUnset = fmt.Errorf("%w: sandboxd is not configured", ErrSandbox)

// Sandboxes holds the sandbox a task uses from Preparing until it ends.
// *runtime.Seats implements it.
type Sandboxes interface {
	// Ready reports whether a sandbox can be created at all.
	Ready() bool
	// Provision creates the sandbox when repo or classes need one and
	// returns once the task holds it. Clone, toolchain and probe then run
	// in the background, and done gets the outcome once: nil at
	// ready, or a runtime.PhaseError. done is not called after Release.
	Provision(ctx context.Context, taskID, repo string, classes []runtime.Class, done runtime.Done) (bool, error)
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
// classes need a sandbox, creates one through boxes. It returns the
// Preparing task once the sandbox is held, without waiting for clone,
// toolchain and probe, which run in the background. The
// provisioning record is runtime's, not a task event. A sandbox that
// reaches ready leaves the task in Preparing until an Advance moves it on,
// as for a task that needs no sandbox; Advance does not wait for ready.
//
// An unknown class or a repository that cannot be cloned is refused before
// the task exists. A needed sandbox with no sandboxd configured is refused
// before the task exists too, with ErrSandbox. When Create fails after the
// task is open, the task is moved to Failed, any sandbox is released, and
// the Failed task is returned with ErrSandbox. When a provisioning phase
// fails later, the task is moved to Failed and the sandbox released in the
// background with access, so no container or Preparing task is left behind
// silently.
func OpenTask(ctx context.Context, book Book, boxes Sandboxes, access Access, open Opening) (Task, error) {
	classes := make([]runtime.Class, 0, len(open.Classes))
	for _, c := range open.Classes {
		classes = append(classes, runtime.Class(c))
	}
	needs, err := runtime.NeedsSandbox(open.Repo, classes)
	if err != nil {
		return Task{}, fmt.Errorf("%w: %w", domain.ErrInput, err)
	}
	if _, err := runtime.CloneURL(open.Repo); err != nil {
		return Task{}, fmt.Errorf("%w: %w", domain.ErrInput, err)
	}
	if needs && (boxes == nil || !boxes.Ready()) {
		return Task{}, fmt.Errorf("%w: %w", ErrSandboxUnset, runtime.ErrSandboxdUnset)
	}

	task, err := ApplyTransition(ctx, book, access, "", domain.Input{Kind: domain.Open, Goal: open.Goal})
	if err != nil || !needs {
		return task, err
	}
	// done runs after this call has returned, on a context runtime detaches
	// from ctx. At ready there is nothing to do.
	id := task.ID
	done := func(ctx context.Context, err error) error {
		if err == nil {
			return nil
		}
		_, ferr := book.Transition(ctx, access, id, domain.Input{Kind: domain.Fail})
		return errors.Join(ferr, boxes.Release(ctx, id))
	}
	if _, err := boxes.Provision(ctx, task.ID, open.Repo, classes, done); err != nil {
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
