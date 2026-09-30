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
		"seats":     &runtime.Seats{},
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

type fakeBoxes struct {
	ready       bool
	err         error
	releaseErr  error
	provisioned []string
	released    []string
}

func (f *fakeBoxes) Ready() bool { return f.ready }

func (f *fakeBoxes) Provision(_ context.Context, taskID, repo string, classes []runtime.Class) (bool, error) {
	needs, err := runtime.NeedsSandbox(repo, classes)
	if err != nil || !needs {
		return false, err
	}
	f.provisioned = append(f.provisioned, taskID)
	return true, f.err
}

func (f *fakeBoxes) Release(_ context.Context, taskID string) error {
	f.released = append(f.released, taskID)
	return f.releaseErr
}
