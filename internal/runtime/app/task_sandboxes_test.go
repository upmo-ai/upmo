package app

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/upmo-ai/upmo/internal/runtime/domain"
)

// boxExpires is when every sandbox fakeProvider creates lapses.
var boxExpires = time.Date(2026, 11, 1, 12, 30, 0, 0, time.UTC)

func newTaskSandboxes(p SandboxProvider) (*TaskSandboxes, *fakeStore) {
	book := &fakeStore{}
	return &TaskSandboxes{Provider: p, Store: book}, book
}

func held(t *testing.T, s *TaskSandboxes, taskID string) (string, bool) {
	t.Helper()
	id, ok, err := s.Held(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	return id, ok
}

// outcome records what Done receives, so a test can wait for it.
type outcome struct {
	got  chan error
	ctxs chan settledCtx
}

// settledCtx is the context done ran on, as it was when done ran.
type settledCtx struct {
	err      error
	deadline time.Time
	bounded  bool
}

func newOutcome() *outcome {
	return &outcome{got: make(chan error, 1), ctxs: make(chan settledCtx, 1)}
}

func (o *outcome) done(ctx context.Context, err error) error {
	deadline, bounded := ctx.Deadline()
	o.ctxs <- settledCtx{err: ctx.Err(), deadline: deadline, bounded: bounded}
	o.got <- err
	return nil
}

// result reports whether done was called and the error it got.
func (o *outcome) result() (bool, error) {
	select {
	case err := <-o.got:
		return true, err
	default:
		return false, nil
	}
}

// settle waits for every background provisioning on s to stop.
func settle(t *testing.T, s *TaskSandboxes) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Wait(ctx); err != nil {
		t.Fatalf("provisioning still running: %v", err)
	}
}

// provisioned calls Provision for task and waits for its background phases.
// It returns what done got, through the outcome, and what Provision returned.
func provisioned(t *testing.T, s *TaskSandboxes, repo string, classes []domain.Class) (*outcome, bool, error) {
	t.Helper()
	o := newOutcome()
	needed, err := s.Provision(context.Background(), task, repo, classes, o.done)
	settle(t, s)
	return o, needed, err
}

func TestTaskSandboxesProvisionCreatesForShellAndRepo(t *testing.T) {
	cases := map[string]struct {
		repo    string
		classes []domain.Class
	}{
		"repo":  {repo: "org/name"},
		"shell": {classes: []domain.Class{domain.ClassShell}},
		"file":  {classes: []domain.Class{domain.ClassWeb, domain.ClassFile}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p := &fakeProvider{}
			s, book := newTaskSandboxes(p)
			o, needed, err := provisioned(t, s, tc.repo, tc.classes)
			if err != nil || !needed {
				t.Fatalf("provision = %t %v", needed, err)
			}
			if called, got := o.result(); !called || got != nil {
				t.Fatalf("done = %t %v", called, got)
			}
			if len(p.created) != 1 {
				t.Fatalf("creates = %d", len(p.created))
			}
			spec := p.created[0]
			wantAllow := []string(nil)
			if tc.repo != "" {
				wantAllow = []string{domain.RepoHost}
			}
			if spec.Task != task || spec.Image != SandboxImage || spec.Tier != domain.TierContainer || spec.TTL != SandboxTTL || !slices.Equal(spec.Allow, wantAllow) {
				t.Fatalf("spec = %+v, want allow %v", spec, wantAllow)
			}
			if id, ok := held(t, s, task); !ok || id != "box-1" {
				t.Fatalf("held = %q %t", id, ok)
			}
			if book.ttl[task] != SandboxTTL || !book.expires[task].Equal(boxExpires) {
				t.Fatalf("book ttl = %v expires = %v", book.ttl[task], book.expires[task])
			}
			if rec, ok := s.Progress(task); !ok || rec.Phase != domain.PhaseReady {
				t.Fatalf("progress = %+v %t", rec, ok)
			}
			for _, call := range p.execCalls() {
				if call.id != "box-1" {
					t.Fatalf("exec on %q", call.id)
				}
			}
			if len(p.execCalls()) == 0 {
				t.Fatal("provisioned without an exec")
			}
		})
	}
}

func TestTaskSandboxesProvisionPhaseFailureKeepsTheBindingForRelease(t *testing.T) {
	ctx := context.Background()
	p := &fakeProvider{run: func(cmd Command) (Exit, string, error) {
		if slices.Contains(cmd.Args, "clone") {
			return Exit{Code: 128}, "fatal: unable to access the repository", nil
		}
		return Exit{}, "", nil
	}}
	s, book := newTaskSandboxes(p)
	o, needed, err := provisioned(t, s, "org/name", nil)
	if !needed || err != nil {
		t.Fatalf("provision = %t %v", needed, err)
	}
	// The phase fails after Provision has returned, so done reports it.
	called, err := o.result()
	var phase *PhaseError
	if !called || !errors.As(err, &phase) || phase.Phase != domain.PhaseClone || !errors.Is(err, ErrProvisionFailed) || !errors.Is(err, ErrCommand) {
		t.Fatalf("done = %t %v", called, err)
	}
	if rec, ok := s.Progress(task); !ok || rec.Phase != domain.PhaseFailed {
		t.Fatalf("progress = %+v %t", rec, ok)
	}
	// The caller fails the task and releases; until then the task sandbox is held.
	if id, ok := held(t, s, task); !ok || id != "box-1" || len(p.destroyedIDs()) != 0 {
		t.Fatalf("held = %q %t destroyed = %v", id, ok, p.destroyedIDs())
	}
	if err := s.Release(ctx, task); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.destroyedIDs(), []string{"box-1"}) || book.size() != 0 {
		t.Fatalf("destroyed = %v rows = %d", p.destroyedIDs(), book.size())
	}
	if _, ok := s.Progress(task); ok {
		t.Fatal("progress kept after release")
	}
}

func TestTaskSandboxesProvisionRefusesABadRepoBeforeCreate(t *testing.T) {
	p := &fakeProvider{}
	s, book := newTaskSandboxes(p)
	needed, err := s.Provision(context.Background(), task, "https://ada@example.com/org/name", nil, nil)
	if !needed || !errors.Is(err, domain.ErrRepo) || len(p.created) != 0 || book.size() != 0 {
		t.Fatalf("provision = %t %v creates=%d", needed, err, len(p.created))
	}
	if _, ok := s.Progress(task); ok {
		t.Fatal("progress for a refused repo")
	}
}

func TestTaskSandboxesProvisionCreateFailureLeavesNoProgress(t *testing.T) {
	p := &fakeProvider{createErr: ErrIsolation}
	s, _ := newTaskSandboxes(p)
	o := newOutcome()
	if _, err := s.Provision(context.Background(), task, "", []domain.Class{domain.ClassShell}, o.done); !errors.Is(err, ErrIsolation) {
		t.Fatalf("provision = %v", err)
	}
	settle(t, s)
	if _, ok := s.Progress(task); ok || len(p.execCalls()) != 0 {
		t.Fatalf("progress kept or exec ran: %v", p.execCalls())
	}
	if called, _ := o.result(); called {
		t.Fatal("done called for a Create that failed")
	}
}

// gated makes p hold its first Exec until gate is closed, after closing
// entered, so a test can look at a provisioning in the middle of a phase.
func gated(p *fakeProvider) (entered, gate chan struct{}) {
	entered, gate = make(chan struct{}), make(chan struct{})
	var once sync.Once
	p.run = func(Command) (Exit, string, error) {
		once.Do(func() { close(entered); <-gate })
		return Exit{}, "", nil
	}
	return entered, gate
}

func TestTaskSandboxesProvisionReturnsBeforeASlowPhaseFinishes(t *testing.T) {
	p := &fakeProvider{}
	s, _ := newTaskSandboxes(p)
	entered, gate := gated(p)
	o := newOutcome()
	needed, err := s.Provision(context.Background(), task, "", []domain.Class{domain.ClassShell}, o.done)
	if err != nil || !needed {
		t.Fatalf("provision = %t %v", needed, err)
	}
	<-entered
	// Provision has returned while the toolchain phase is still running.
	if rec, ok := s.Progress(task); !ok || rec.Phase != domain.PhaseToolchain {
		t.Fatalf("progress while running = %+v %t", rec, ok)
	}
	if id, ok := held(t, s, task); !ok || id != "box-1" {
		t.Fatalf("held while running = %q %t", id, ok)
	}
	if called, _ := o.result(); called {
		t.Fatal("done called before the phases finished")
	}
	busy, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := s.Wait(busy); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait while running = %v", err)
	}

	close(gate)
	settle(t, s)
	if called, err := o.result(); !called || err != nil {
		t.Fatalf("done = %t %v", called, err)
	}
	if rec, ok := s.Progress(task); !ok || rec.Phase != domain.PhaseReady {
		t.Fatalf("progress = %+v %t", rec, ok)
	}
}

func TestTaskSandboxesProvisionOutlivesTheCallerWithinTheTTL(t *testing.T) {
	p := &fakeProvider{}
	s, _ := newTaskSandboxes(p)
	s.TTL = time.Minute
	entered, gate := gated(p)
	o := newOutcome()
	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()
	if _, err := s.Provision(ctx, task, "", []domain.Class{domain.ClassShell}, o.done); err != nil {
		t.Fatal(err)
	}
	<-entered
	// The request that opened the task has gone; the phases carry on.
	cancel()
	close(gate)
	settle(t, s)
	if called, err := o.result(); !called || err != nil {
		t.Fatalf("done = %t %v", called, err)
	}
	execs := p.execCalls()
	if len(execs) == 0 {
		t.Fatal("no exec ran")
	}
	settled := <-o.ctxs
	if settled.err != nil {
		t.Fatalf("done got a context that had ended: %v", settled.err)
	}
	if !settled.bounded || settled.deadline.After(time.Now().Add(settleTimeout)) {
		t.Fatalf("done context deadline = %v %t", settled.deadline, settled.bounded)
	}
	if deadline, ok := p.lastDeadline(); !ok || deadline.Before(start) || deadline.After(start.Add(time.Minute+time.Second)) {
		t.Fatalf("phase deadline = %v %t, want within the TTL", deadline, ok)
	}
}

func TestTaskSandboxesReleaseDuringProvisionStopsThePhases(t *testing.T) {
	ctx := context.Background()
	p := &fakeProvider{}
	s, _ := newTaskSandboxes(p)
	entered, gate := gated(p)
	o := newOutcome()
	if _, err := s.Provision(ctx, task, "", []domain.Class{domain.ClassShell}, o.done); err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := s.Release(ctx, task); err != nil {
		t.Fatal(err)
	}
	if !p.lastCanceled() {
		t.Fatal("release left the phases running")
	}
	close(gate)
	settle(t, s)
	if _, ok := s.Progress(task); ok {
		t.Fatal("progress came back after release")
	}
	// Whoever released the task sandbox has moved the task on already.
	if called, err := o.result(); called {
		t.Fatalf("done called after release: %v", err)
	}
}

func TestTaskSandboxesProvisionSurvivesAPanic(t *testing.T) {
	cases := map[string]struct {
		run  func(Command) (Exit, string, error)
		done func(context.Context, error) error
	}{
		"in a phase": {run: func(Command) (Exit, string, error) { panic("provider bug") }},
		"in done": {done: func(context.Context, error) error {
			panic("settle bug")
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p := &fakeProvider{run: tc.run}
			s, _ := newTaskSandboxes(p)
			got := make(chan error, 1)
			done := Done(func(ctx context.Context, err error) error {
				got <- err
				if tc.done != nil {
					return tc.done(ctx, err)
				}
				return nil
			})
			if _, err := s.Provision(context.Background(), task, "", []domain.Class{domain.ClassShell}, done); err != nil {
				t.Fatal(err)
			}
			settle(t, s)
			err := <-got
			if tc.run != nil {
				if !errors.Is(err, ErrProvisionFailed) {
					t.Fatalf("done = %v", err)
				}
				if rec, ok := s.Progress(task); !ok || rec.Phase != domain.PhaseFailed {
					t.Fatalf("progress = %+v %t", rec, ok)
				}
			}
		})
	}
}

func TestTaskSandboxesProvisionSkipsCreateWhenNotNeeded(t *testing.T) {
	p := &fakeProvider{}
	s, book := newTaskSandboxes(p)
	needed, err := s.Provision(context.Background(), task, " ", []domain.Class{domain.ClassConnector, domain.ClassKnowledge}, nil)
	if err != nil || needed || len(p.created) != 0 {
		t.Fatalf("provision = %t %v creates=%d", needed, err, len(p.created))
	}
	if _, err := s.Provision(context.Background(), task, "", []domain.Class{"sideways"}, nil); !errors.Is(err, domain.ErrClass) || len(p.created) != 0 {
		t.Fatalf("unknown class = %v creates=%d", err, len(p.created))
	}
	if len(book.rows) != 0 {
		t.Fatalf("rows = %v", book.rows)
	}
}

func TestTaskSandboxesUsesImageAndTTL(t *testing.T) {
	p := &fakeProvider{}
	s, book := newTaskSandboxes(p)
	s.Image, s.TTL = "upmo/sandbox:base", time.Minute
	if _, _, err := provisioned(t, s, "org/name", nil); err != nil {
		t.Fatal(err)
	}
	if p.created[0].Image != "upmo/sandbox:base" || p.created[0].TTL != time.Minute {
		t.Fatalf("spec = %+v", p.created[0])
	}
	if book.ttl[task] != time.Minute {
		t.Fatalf("claim ttl = %v", book.ttl[task])
	}
}

func TestTaskSandboxesWithoutProviderIsClear(t *testing.T) {
	ctx := context.Background()
	book := &fakeStore{rows: map[string]string{task: "box-0"}}
	for name, s := range map[string]*TaskSandboxes{"zero": {}, "nil": nil, "book only": {Store: book}} {
		t.Run(name, func(t *testing.T) {
			if s.Ready() {
				t.Fatal("ready without a provider")
			}
			if _, err := s.Start(ctx, task, validSpec()); !errors.Is(err, ErrSandboxdUnset) {
				t.Fatalf("start = %v", err)
			}
			if needed, err := s.Provision(ctx, task, "org/name", nil, nil); !needed || !errors.Is(err, ErrSandboxdUnset) {
				t.Fatalf("provision = %t %v", needed, err)
			}
			if err := s.Release(ctx, task); err != nil {
				t.Fatalf("release = %v", err)
			}
		})
	}
	// The task sandbox waits for a brain that can reach sandboxd.
	if book.rows[task] != "box-0" {
		t.Fatalf("rows = %v", book.rows)
	}
}

func TestTaskSandboxesStartEditorNeedsHeldSandbox(t *testing.T) {
	p := &fakeProvider{editorEp: Endpoint{URL: "http://127.0.0.1:30001"}}
	s, _ := newTaskSandboxes(p)
	task := "11111111-1111-1111-1111-111111111111"
	if _, err := s.StartEditor(context.Background(), task, "Working"); !errors.Is(err, ErrNoHeldSandbox) {
		t.Fatalf("err = %v", err)
	}
}

func TestTaskSandboxesStartEditorCallsProvider(t *testing.T) {
	p := &fakeProvider{editorEp: Endpoint{URL: "http://127.0.0.1:30001"}}
	s, book := newTaskSandboxes(p)
	task := "11111111-1111-1111-1111-111111111111"
	if err := book.Claim(context.Background(), task, SandboxTTL, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	if ok, err := book.Fill(context.Background(), task, "box-1", time.Now().Add(SandboxTTL)); err != nil || !ok {
		t.Fatalf("fill = %t %v", ok, err)
	}
	ep, err := s.StartEditor(context.Background(), task, "Working")
	if err != nil || ep.URL != "http://127.0.0.1:30001" {
		t.Fatalf("start editor = %+v %v", ep, err)
	}
}

func TestTaskSandboxesWithoutBookRefusesToCreate(t *testing.T) {
	ctx := context.Background()
	p := &fakeProvider{}
	s := &TaskSandboxes{Provider: p}
	if s.Ready() {
		t.Fatal("ready without a task sandbox store")
	}
	if _, err := s.Start(ctx, task, validSpec()); !errors.Is(err, ErrNoTaskSandboxStore) || len(p.created) != 0 {
		t.Fatalf("start = %v creates=%d", err, len(p.created))
	}
	if err := s.Release(ctx, task); err != nil {
		t.Fatalf("release = %v", err)
	}
	if _, ok := held(t, s, task); ok {
		t.Fatal("held without a task sandbox store")
	}
}

func TestTaskSandboxesReleaseDestroysAndForgets(t *testing.T) {
	ctx := context.Background()
	p := &fakeProvider{}
	s, book := newTaskSandboxes(p)
	if _, err := s.Start(ctx, task, validSpec()); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(ctx, " "+task+" "); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.destroyed, []string{"box-1"}) {
		t.Fatalf("destroyed = %v", p.destroyed)
	}
	if _, ok := held(t, s, task); ok || len(book.rows) != 0 {
		t.Fatalf("still held after release: %v", book.rows)
	}
	if err := s.Release(ctx, task); err != nil || len(p.destroyed) != 1 {
		t.Fatalf("second release = %v destroyed=%v", err, p.destroyed)
	}
	if err := s.Release(ctx, "nope"); !errors.Is(err, ErrSpec) {
		t.Fatalf("bad id = %v", err)
	}
}

func TestTaskSandboxesReleaseAfterRestart(t *testing.T) {
	ctx := context.Background()
	p := &fakeProvider{}
	before, book := newTaskSandboxes(p)
	if _, err := before.Start(ctx, task, validSpec()); err != nil {
		t.Fatal(err)
	}
	// A new process shares only the book.
	after := &TaskSandboxes{Provider: p, Store: book}
	if id, ok := held(t, after, task); !ok || id != "box-1" {
		t.Fatalf("held after restart = %q %t", id, ok)
	}
	if err := after.Release(ctx, task); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.destroyed, []string{"box-1"}) || len(book.rows) != 0 {
		t.Fatalf("destroyed = %v rows = %v", p.destroyed, book.rows)
	}
}

func TestTaskSandboxesReleaseDropsAPendingBindingLeftByAGoneProcess(t *testing.T) {
	p := &fakeProvider{}
	book := &fakeStore{rows: map[string]string{task: ""}}
	s := &TaskSandboxes{Provider: p, Store: book}
	if err := s.Release(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if len(p.destroyed) != 0 || len(book.rows) != 0 {
		t.Fatalf("destroyed = %v rows = %v", p.destroyed, book.rows)
	}
}

func TestTaskSandboxesRefusesASecondBinding(t *testing.T) {
	ctx := context.Background()
	p := &fakeProvider{}
	s, _ := newTaskSandboxes(p)
	if _, err := s.Start(ctx, task, validSpec()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start(ctx, task, validSpec()); !errors.Is(err, ErrAlreadyHeld) || len(p.created) != 1 {
		t.Fatalf("second start = %v creates=%d", err, len(p.created))
	}
}

func TestTaskSandboxesStartRefusesASpecForAnotherTask(t *testing.T) {
	p := &fakeProvider{}
	s, book := newTaskSandboxes(p)
	if _, err := s.Start(context.Background(), "018f3b2c-7a10-7c4a-8e11-222222222222", validSpec()); !errors.Is(err, ErrSpec) || len(p.created) != 0 {
		t.Fatalf("start = %v creates=%d", err, len(p.created))
	}
	spec := validSpec()
	spec.Tier = "microvm"
	if _, err := s.Start(context.Background(), task, spec); !errors.Is(err, ErrSpec) || len(p.created) != 0 {
		t.Fatalf("bad spec = %v creates=%d", err, len(p.created))
	}
	if len(book.rows) != 0 {
		t.Fatalf("rows = %v", book.rows)
	}
}

func TestTaskSandboxesCreateFailureHoldsNothing(t *testing.T) {
	ctx := context.Background()
	p := &fakeProvider{createErr: ErrIsolation}
	s, book := newTaskSandboxes(p)
	if _, err := s.Start(ctx, task, validSpec()); !errors.Is(err, ErrIsolation) {
		t.Fatalf("start = %v", err)
	}
	if len(book.rows) != 0 {
		t.Fatalf("rows after failed create = %v", book.rows)
	}
	p.createErr = nil
	if _, err := s.Start(ctx, task, validSpec()); err != nil {
		t.Fatalf("retry = %v", err)
	}
}

func TestTaskSandboxesClaimFailureCreatesNothing(t *testing.T) {
	p := &fakeProvider{}
	s, book := newTaskSandboxes(p)
	book.claimErr = errors.New("database down")
	if _, err := s.Start(context.Background(), task, validSpec()); !errors.Is(err, book.claimErr) || len(p.created) != 0 {
		t.Fatalf("start = %v creates=%d", err, len(p.created))
	}
}

func TestTaskSandboxesFillFailureDestroysTheNewSandbox(t *testing.T) {
	p := &fakeProvider{}
	s, book := newTaskSandboxes(p)
	book.fillErr = errors.New("database down")
	if _, err := s.Start(context.Background(), task, validSpec()); !errors.Is(err, book.fillErr) {
		t.Fatalf("start = %v", err)
	}
	if !slices.Equal(p.destroyedIDs(), []string{"box-1"}) {
		t.Fatalf("destroyed = %v", p.destroyedIDs())
	}
	if len(book.rows) != 0 {
		t.Fatalf("rows = %v", book.rows)
	}
}

func TestTaskSandboxesReleaseDuringCreateDestroysTheNewSandbox(t *testing.T) {
	ctx := context.Background()
	p := &fakeProvider{gate: make(chan struct{}), entered: make(chan struct{})}
	s, book := newTaskSandboxes(p)
	done := make(chan error, 1)
	go func() {
		_, err := s.Start(ctx, task, validSpec())
		done <- err
	}()
	<-p.entered
	if err := s.Release(ctx, task); err != nil {
		t.Fatal(err)
	}
	close(p.gate)
	if err := <-done; !errors.Is(err, ErrReleased) {
		t.Fatalf("start = %v", err)
	}
	if !slices.Equal(p.destroyedIDs(), []string{"box-1"}) {
		t.Fatalf("destroyed = %v", p.destroyedIDs())
	}
	if _, ok := held(t, s, task); ok || book.size() != 0 {
		t.Fatal("held after release during create")
	}
}

func TestTaskSandboxesDestroyFailureKeepsTheBinding(t *testing.T) {
	ctx := context.Background()
	p := &fakeProvider{}
	s, _ := newTaskSandboxes(p)
	if _, err := s.Start(ctx, task, validSpec()); err != nil {
		t.Fatal(err)
	}
	p.destroyErr = errors.New("sandboxd down")
	if err := s.Release(ctx, task); err == nil {
		t.Fatal("release hid the destroy failure")
	}
	if _, ok := held(t, s, task); !ok {
		t.Fatal("task sandbox forgotten after a failed destroy")
	}
	p.destroyErr = ErrNotFound
	if err := s.Release(ctx, task); err != nil {
		t.Fatalf("gone sandbox = %v", err)
	}
	if _, ok := held(t, s, task); ok {
		t.Fatal("task sandbox kept after sandboxd lost the sandbox")
	}
}

// fakeStore is a TaskSandboxStore in memory. An empty sandbox id is a pending task sandbox.
type fakeStore struct {
	mu       sync.Mutex
	rows     map[string]string
	ttl      map[string]time.Duration
	expires  map[string]time.Time
	claimErr error
	fillErr  error
}

var _ TaskSandboxStore = (*fakeStore)(nil)

func (b *fakeStore) Claim(_ context.Context, taskID string, ttl time.Duration, _ []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.claimErr != nil {
		return b.claimErr
	}
	if _, taken := b.rows[taskID]; taken {
		return ErrAlreadyHeld
	}
	if b.rows == nil {
		b.rows, b.ttl, b.expires = map[string]string{}, map[string]time.Duration{}, map[string]time.Time{}
	}
	b.rows[taskID] = ""
	b.ttl[taskID] = ttl
	return nil
}

func (b *fakeStore) Fill(_ context.Context, taskID, sandboxID string, expires time.Time) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fillErr != nil {
		return false, b.fillErr
	}
	id, ok := b.rows[taskID]
	if !ok || id != "" {
		return false, nil
	}
	b.rows[taskID] = sandboxID
	if !expires.IsZero() {
		b.expires[taskID] = expires
	}
	return true, nil
}

func (b *fakeStore) Lookup(_ context.Context, taskID string) (string, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id, ok := b.rows[taskID]
	return id, ok, nil
}

func (b *fakeStore) Drop(_ context.Context, taskID, sandboxID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if id, ok := b.rows[taskID]; ok && id == sandboxID {
		delete(b.rows, taskID)
	}
	return nil
}

func (b *fakeStore) size() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.rows)
}

// fakeProvider records Create, Destroy and Exec, and keeps files in memory.
// gate, when set, holds Create until it is closed, after signalling entered.
type fakeProvider struct {
	mu         sync.Mutex
	created    []Spec
	destroyed  []string
	createErr  error
	destroyErr error
	gate       chan struct{}
	entered    chan struct{}

	execs    []execCall
	lastCtx  context.Context
	run      func(Command) (Exit, string, error)
	files    map[string][]byte
	readErr  error
	writeErr error

	editorEp  Endpoint
	editorErr error
}

type execCall struct {
	id  string
	cmd Command
}

var _ SandboxProvider = (*fakeProvider)(nil)

func (f *fakeProvider) Create(_ context.Context, spec Spec) (Sandbox, error) {
	if f.gate != nil {
		close(f.entered)
		<-f.gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return Sandbox{}, f.createErr
	}
	f.created = append(f.created, spec)
	return Sandbox{ID: "box-1", Tier: spec.Tier, Expires: boxExpires}, nil
}

func (f *fakeProvider) Destroy(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.destroyErr != nil {
		return f.destroyErr
	}
	f.destroyed = append(f.destroyed, id)
	return nil
}

func (f *fakeProvider) destroyedIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.destroyed)
}

// Exec records cmd and answers through run, or exits 0 when run is nil.
// stderr from run is streamed before the exit.
func (f *fakeProvider) Exec(ctx context.Context, id string, cmd Command, out func(Output) error) (Exit, error) {
	f.mu.Lock()
	f.execs = append(f.execs, execCall{id: id, cmd: cmd})
	f.lastCtx = ctx
	run := f.run
	f.mu.Unlock()
	if run == nil {
		return Exit{}, nil
	}
	exit, stderr, err := run(cmd)
	if stderr != "" && out != nil {
		if oerr := out(Output{Stream: "stderr", Data: []byte(stderr)}); oerr != nil {
			return Exit{}, oerr
		}
	}
	return exit, err
}

func (f *fakeProvider) PTY(context.Context, string, Size) (io.ReadWriteCloser, error) {
	return nil, ErrUnsupported
}

func (f *fakeProvider) ReadFile(_ context.Context, _ string, path string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readErr != nil {
		return nil, f.readErr
	}
	data, ok := f.files[path]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return slices.Clone(data), nil
}

func (f *fakeProvider) WriteFile(_ context.Context, _ string, path string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeErr != nil {
		return f.writeErr
	}
	if f.files == nil {
		f.files = map[string][]byte{}
	}
	f.files[path] = slices.Clone(data)
	return nil
}

// lastDeadline is the deadline of the context the last Exec ran on.
func (f *fakeProvider) lastDeadline() (time.Time, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lastCtx == nil {
		return time.Time{}, false
	}
	return f.lastCtx.Deadline()
}

// lastCanceled reports whether the context the last Exec ran on has ended.
func (f *fakeProvider) lastCanceled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastCtx != nil && f.lastCtx.Err() != nil
}

func (f *fakeProvider) execCalls() []execCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.execs)
}

func (f *fakeProvider) Expose(context.Context, string, int) (Endpoint, error) {
	return Endpoint{}, ErrUnsupported
}

func (f *fakeProvider) Pause(context.Context, string) error { return ErrUnsupported }

func (f *fakeProvider) StartEditor(_ context.Context, id, status string) (Endpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.editorErr != nil {
		return Endpoint{}, f.editorErr
	}
	if f.editorEp.URL == "" {
		return Endpoint{}, ErrUnsupported
	}
	return f.editorEp, nil
}

func (f *fakeProvider) StopEditor(context.Context, string) error { return ErrUnsupported }
