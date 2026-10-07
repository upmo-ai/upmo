package app

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/upmo-ai/upmo/internal/runtime"
	"github.com/upmo-ai/upmo/internal/tasks/domain"
)

var openAccess = Access{UserID: userID, Allow: "task:create,task:edit"}

func TestOpenTaskWithoutASandboxSkipsProvision(t *testing.T) {
	book := &fake{}
	boxes := &fakeBoxes{ready: true}
	task, err := OpenTask(context.Background(), book, boxes, openAccess, Opening{Goal: "File the receipts", Classes: []string{"connector", "web"}})
	if err != nil || task.Status != domain.Preparing {
		t.Fatalf("open = %+v %v", task, err)
	}
	if len(boxes.provisioned) != 0 {
		t.Fatalf("provisioned = %v", boxes.provisioned)
	}
}

func TestOpenTaskWithoutSandboxesNeedsNone(t *testing.T) {
	book := &fake{}
	task, err := OpenTask(context.Background(), book, nil, openAccess, Opening{Goal: "File the receipts"})
	if err != nil || task.Status != domain.Preparing {
		t.Fatalf("open = %+v %v", task, err)
	}
}

func TestOpenTaskProvisionsForARepoOrShell(t *testing.T) {
	cases := map[string]Opening{
		"repo":  {Goal: "Fix the build", Repo: "org/name"},
		"shell": {Goal: "Fix the build", Classes: []string{"shell"}},
	}
	for name, open := range cases {
		t.Run(name, func(t *testing.T) {
			book := &fake{}
			boxes := &fakeBoxes{ready: true}
			task, err := OpenTask(context.Background(), book, boxes, openAccess, open)
			if err != nil || task.Status != domain.Preparing {
				t.Fatalf("open = %+v %v", task, err)
			}
			if !slices.Equal(boxes.provisioned, []string{task.ID}) || len(boxes.released) != 0 {
				t.Fatalf("provisioned = %v released = %v", boxes.provisioned, boxes.released)
			}
		})
	}
}

func TestOpenTaskRefusesAnUnknownClassBeforeOpening(t *testing.T) {
	book := &fake{}
	boxes := &fakeBoxes{ready: true}
	_, err := OpenTask(context.Background(), book, boxes, openAccess, Opening{Goal: "x", Classes: []string{"sideways"}})
	if !errors.Is(err, domain.ErrInput) || !errors.Is(err, runtime.ErrClass) || book.calls != 0 {
		t.Fatalf("err = %v calls = %d", err, book.calls)
	}
}

func TestOpenTaskRefusesWhenSandboxdIsUnset(t *testing.T) {
	for name, boxes := range map[string]Sandboxes{
		"nil":       nil,
		"not ready": &fakeBoxes{},
		"task sandboxes":     &runtime.TaskSandboxes{},
	} {
		t.Run(name, func(t *testing.T) {
			book := &fake{}
			_, err := OpenTask(context.Background(), book, boxes, openAccess, Opening{Goal: "Fix the build", Repo: "org/name"})
			if !errors.Is(err, ErrSandboxUnset) || !errors.Is(err, ErrSandbox) || !errors.Is(err, runtime.ErrSandboxdUnset) {
				t.Fatalf("err = %v", err)
			}
			if book.calls != 0 {
				t.Fatalf("opened a task with no sandboxd: calls = %d", book.calls)
			}
		})
	}
}

func TestOpenTaskFailsTheTaskWhenCreateFails(t *testing.T) {
	book := &fake{}
	boxes := &fakeBoxes{ready: true, err: runtime.ErrIsolation}
	task, err := OpenTask(context.Background(), book, boxes, openAccess, Opening{Goal: "Fix the build", Repo: "org/name"})
	if !errors.Is(err, ErrSandbox) || !errors.Is(err, runtime.ErrIsolation) {
		t.Fatalf("err = %v", err)
	}
	if task.ID == "" || task.Status != domain.Failed || book.status != domain.Failed {
		t.Fatalf("task = %+v book = %s", task, book.status)
	}
	if !slices.Equal(boxes.released, []string{task.ID}) {
		t.Fatalf("released = %v", boxes.released)
	}
}

func TestOpenTaskFailsTheTaskWhenAPhaseFailsAfterOpen(t *testing.T) {
	book := &fake{}
	boxes := &fakeBoxes{ready: true}
	task, err := OpenTask(context.Background(), book, boxes, openAccess, Opening{Goal: "Fix the build", Classes: []string{"shell"}})
	// Open returns Preparing before the phases have run.
	if err != nil || task.Status != domain.Preparing || len(boxes.released) != 0 {
		t.Fatalf("open = %+v %v released = %v", task, err, boxes.released)
	}
	cause := &runtime.PhaseError{Phase: runtime.PhaseToolchain, Err: runtime.ErrCommand}
	if err := boxes.finish(cause); err != nil {
		t.Fatalf("settle = %v", err)
	}
	if book.status != domain.Failed {
		t.Fatalf("book = %s", book.status)
	}
	if !slices.Equal(boxes.released, []string{task.ID}) {
		t.Fatalf("released = %v", boxes.released)
	}
}

func TestOpenTaskLeavesAReadySandboxPreparing(t *testing.T) {
	book := &fake{}
	boxes := &fakeBoxes{ready: true}
	task, err := OpenTask(context.Background(), book, boxes, openAccess, Opening{Goal: "Fix the build", Repo: "org/name"})
	if err != nil {
		t.Fatal(err)
	}
	calls := book.calls
	if err := boxes.finish(nil); err != nil {
		t.Fatalf("settle = %v", err)
	}
	if book.status != domain.Preparing || book.calls != calls || len(boxes.released) != 0 {
		t.Fatalf("book = %s calls = %d released = %v", book.status, book.calls, boxes.released)
	}
	if next, err := StepTask(context.Background(), book, boxes, openAccess, task.ID, domain.Input{Kind: domain.Advance}); err != nil || next.Status != domain.Working {
		t.Fatalf("advance = %+v %v", next, err)
	}
}

func TestOpenTaskReportsASettleThatFails(t *testing.T) {
	book := &fake{}
	boxes := &fakeBoxes{ready: true, releaseErr: errors.New("sandboxd down")}
	if _, err := OpenTask(context.Background(), book, boxes, openAccess, Opening{Goal: "Fix the build", Classes: []string{"shell"}}); err != nil {
		t.Fatal(err)
	}
	cause := &runtime.PhaseError{Phase: runtime.PhaseProbe, Err: runtime.ErrCommand}
	if err := boxes.finish(cause); !errors.Is(err, boxes.releaseErr) || book.status != domain.Failed {
		t.Fatalf("settle = %v book = %s", err, book.status)
	}
}

func TestOpenTaskRefusesARepoItCannotCloneBeforeOpening(t *testing.T) {
	book := &fake{}
	boxes := &fakeBoxes{ready: true}
	_, err := OpenTask(context.Background(), book, boxes, openAccess, Opening{Goal: "Fix the build", Repo: "https://ada@example.com/org/name"})
	if !errors.Is(err, domain.ErrInput) || !errors.Is(err, runtime.ErrRepo) || book.calls != 0 || len(boxes.provisioned) != 0 {
		t.Fatalf("err = %v calls = %d provisioned = %v", err, book.calls, boxes.provisioned)
	}
}

func TestStepTaskReleasesWhenTheTaskEnds(t *testing.T) {
	ctx := context.Background()
	book := &fake{}
	boxes := &fakeBoxes{ready: true}
	task, err := OpenTask(ctx, book, boxes, openAccess, Opening{Goal: "Fix the build", Repo: "org/name"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := StepTask(ctx, book, boxes, openAccess, task.ID, domain.Input{Kind: domain.Advance}); err != nil {
		t.Fatal(err)
	}
	if len(boxes.released) != 0 {
		t.Fatalf("released on advance: %v", boxes.released)
	}
	failed, err := StepTask(ctx, book, boxes, openAccess, task.ID, domain.Input{Kind: domain.Fail})
	if err != nil || failed.Status != domain.Failed {
		t.Fatalf("fail = %+v %v", failed, err)
	}
	if !slices.Equal(boxes.released, []string{task.ID}) {
		t.Fatalf("released = %v", boxes.released)
	}
}

func TestStepTaskKeepsTheTransitionWhenReleaseFails(t *testing.T) {
	ctx := context.Background()
	book := &fake{status: domain.Working}
	boxes := &fakeBoxes{ready: true, releaseErr: errors.New("sandboxd down")}
	got, err := StepTask(ctx, book, boxes, openAccess, taskOf(book), domain.Input{Kind: domain.Fail})
	if err != nil || got.Status != domain.Failed || len(boxes.released) != 1 {
		t.Fatalf("fail = %+v %v released = %v", got, err, boxes.released)
	}
}

// fakeBoxes answers Provision at once, as TaskSandboxes does, and keeps done so a
// test can finish the background phases with finish. err is a Create that
// fails, so done is not kept.
type fakeBoxes struct {
	ready       bool
	err         error
	releaseErr  error
	provisioned []string
	released    []string
	done        runtime.Done
}

func (f *fakeBoxes) Ready() bool { return f.ready }

func (f *fakeBoxes) Provision(_ context.Context, taskID, repo string, classes []runtime.Class, done runtime.Done) (bool, error) {
	needs, err := runtime.NeedsSandbox(repo, classes)
	if err != nil || !needs {
		return false, err
	}
	f.provisioned = append(f.provisioned, taskID)
	if f.err == nil {
		f.done = done
	}
	return true, f.err
}

// finish ends the phases Provision started with outcome, as TaskSandboxes would.
func (f *fakeBoxes) finish(outcome error) error {
	if f.done == nil {
		return errors.New("no provisioning to finish")
	}
	return f.done(context.Background(), outcome)
}

func (f *fakeBoxes) Release(_ context.Context, taskID string) error {
	f.released = append(f.released, taskID)
	return f.releaseErr
}
