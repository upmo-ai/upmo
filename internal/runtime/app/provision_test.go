package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/upmo-ai/upmo/internal/runtime/domain"
)

// tracked runs a Provisioner on p and returns its result and every record
// it tracked.
func tracked(p *fakeProvider, repo string) (domain.Record, []domain.Record, error) {
	var seen []domain.Record
	prep := Provisioner{Provider: p, Track: func(rec domain.Record) { seen = append(seen, rec) }}
	rec, err := prep.Run(context.Background(), " box-1 ", repo)
	return rec, seen, err
}

func phases(recs []domain.Record) []domain.Phase {
	out := make([]domain.Phase, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.Phase)
	}
	return out
}

func TestProvisionWithoutARepoNotesTheSkipAndReachesReady(t *testing.T) {
	p := &fakeProvider{}
	rec, seen, err := tracked(p, "  ")
	if err != nil || rec.Phase != domain.PhaseReady || rec.Events != 5 {
		t.Fatalf("run = %+v %v", rec, err)
	}
	// Begin, the note, then one done per phase.
	want := []domain.Phase{domain.PhaseClone, domain.PhaseClone, domain.PhaseToolchain, domain.PhaseProbe, domain.PhaseReady}
	if !slices.Equal(phases(seen), want) {
		t.Fatalf("phases = %v", phases(seen))
	}
	calls := p.execCalls()
	if len(calls) != 2 {
		t.Fatalf("execs = %+v", calls)
	}
	for _, c := range calls {
		if c.id != "box-1" || c.cmd.Timeout <= 0 || c.cmd.Timeout > domain.ResourceShape().Exec {
			t.Fatalf("exec = %+v", c)
		}
	}
	if tools := calls[0].cmd.Args; tools[0] != "sh" || slices.Contains(tools, "git") || !slices.Contains(tools, "timeout") || !slices.Contains(tools, "sleep") {
		t.Fatalf("toolchain = %q", tools)
	}
	if probe := calls[1].cmd; probe.Dir != ScratchDir || probe.Args[len(probe.Args)-1] != ScratchDir {
		t.Fatalf("probe = %+v", probe)
	}
	if marker := string(p.files[MarkerPath]); !strings.Contains(marker, "repo: none") {
		t.Fatalf("marker = %q", marker)
	}
}

func TestProvisionWithARepoClonesIntoScratch(t *testing.T) {
	p := &fakeProvider{}
	rec, seen, err := tracked(p, "org/name")
	if err != nil || rec.Phase != domain.PhaseReady || rec.Events != 4 {
		t.Fatalf("run = %+v %v", rec, err)
	}
	want := []domain.Phase{domain.PhaseClone, domain.PhaseToolchain, domain.PhaseProbe, domain.PhaseReady}
	if !slices.Equal(phases(seen), want) {
		t.Fatalf("phases = %v", phases(seen))
	}
	calls := p.execCalls()
	if len(calls) != 4 {
		t.Fatalf("execs = %+v", calls)
	}
	clone := calls[0].cmd.Args
	wantClone := []string{"env", "GIT_TERMINAL_PROMPT=0", "git", "clone", "--depth=1", "--quiet", "--", "https://github.com/org/name.git", RepoDir}
	if !slices.Equal(clone, wantClone) || calls[0].cmd.Dir != ScratchDir {
		t.Fatalf("clone = %q in %q", clone, calls[0].cmd.Dir)
	}
	if !slices.Contains(calls[1].cmd.Args, "git") {
		t.Fatalf("toolchain does not check git: %q", calls[1].cmd.Args)
	}
	if calls[2].cmd.Dir != RepoDir || calls[2].cmd.Args[len(calls[2].cmd.Args)-1] != RepoDir {
		t.Fatalf("probe = %+v", calls[2].cmd)
	}
	if head := calls[3].cmd.Args; head[0] != "git" || !slices.Contains(head, "rev-parse") {
		t.Fatalf("head check = %q", head)
	}
	if marker := string(p.files[MarkerPath]); !strings.Contains(marker, "https://github.com/org/name.git") || !strings.Contains(marker, RepoDir) {
		t.Fatalf("marker = %q", marker)
	}
}

func TestProvisionFailsInThePhaseThatFailed(t *testing.T) {
	cases := map[string]struct {
		repo   string
		fail   string // an argument that picks the failing command
		code   int
		stderr string
		phase  domain.Phase
		says   string
	}{
		"clone":          {repo: "org/name", fail: "clone", code: 128, stderr: "fatal: could not resolve host\n", phase: domain.PhaseClone, says: "git clone exited 128: fatal: could not resolve host"},
		"no git":         {repo: "org/name", fail: "clone", code: 127, phase: domain.PhaseClone, says: "git is not in the sandbox image"},
		"toolchain":      {fail: "toolchain", code: 1, stderr: "timeout is missing", phase: domain.PhaseToolchain, says: "toolchain check exited 1: timeout is missing"},
		"probe":          {fail: "probe", code: 1, phase: domain.PhaseProbe, says: "probe exited 1"},
		"empty clone":    {repo: "org/name", fail: "rev-parse", code: 1, phase: domain.PhaseProbe, says: "git rev-parse exited 1"},
		"no sh in image": {fail: "toolchain", code: 127, phase: domain.PhaseToolchain, says: "sh is not in the sandbox image"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p := &fakeProvider{run: func(cmd Command) (Exit, string, error) {
				if slices.Contains(cmd.Args, tc.fail) {
					return Exit{Code: tc.code}, tc.stderr, nil
				}
				return Exit{}, "", nil
			}}
			rec, seen, err := tracked(p, tc.repo)
			var phase *PhaseError
			if !errors.As(err, &phase) || phase.Phase != tc.phase {
				t.Fatalf("err = %v", err)
			}
			if !errors.Is(err, ErrProvisionFailed) || !errors.Is(err, ErrCommand) || !strings.Contains(err.Error(), tc.says) {
				t.Fatalf("err = %v", err)
			}
			if rec.Phase != domain.PhaseFailed || seen[len(seen)-1].Phase != domain.PhaseFailed {
				t.Fatalf("record = %+v", rec)
			}
			if _, err := domain.Apply(rec, domain.KindNote); !errors.Is(err, domain.ErrProvision) {
				t.Fatalf("failed record took another event: %v", err)
			}
		})
	}
}

func TestProvisionStopsAtTheFirstFailure(t *testing.T) {
	p := &fakeProvider{run: func(cmd Command) (Exit, string, error) {
		if slices.Contains(cmd.Args, "clone") {
			return Exit{Code: 128}, "", nil
		}
		return Exit{}, "", nil
	}}
	if _, _, err := tracked(p, "org/name"); err == nil {
		t.Fatal("clone failure passed")
	}
	if calls := p.execCalls(); len(calls) != 1 {
		t.Fatalf("ran past the failed clone: %+v", calls)
	}
	if _, ok := p.files[MarkerPath]; ok {
		t.Fatal("marker written after a failed clone")
	}
}

func TestProvisionReportsProviderErrors(t *testing.T) {
	cases := map[string]struct {
		p     *fakeProvider
		phase domain.Phase
		want  error
	}{
		"exec":  {p: &fakeProvider{run: func(Command) (Exit, string, error) { return Exit{}, "", ErrPaused }}, phase: domain.PhaseToolchain, want: ErrPaused},
		"write": {p: &fakeProvider{writeErr: ErrNotFound}, phase: domain.PhaseClone, want: ErrNotFound},
		"read":  {p: &fakeProvider{readErr: context.DeadlineExceeded}, phase: domain.PhaseProbe, want: context.DeadlineExceeded},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec, _, err := tracked(tc.p, "")
			var phase *PhaseError
			if !errors.As(err, &phase) || phase.Phase != tc.phase || !errors.Is(err, tc.want) || rec.Phase != domain.PhaseFailed {
				t.Fatalf("run = %+v %v", rec, err)
			}
		})
	}
}

func TestProvisionRefusesAMarkerThatDoesNotReadBack(t *testing.T) {
	p := &fakeProvider{}
	p.run = func(cmd Command) (Exit, string, error) {
		// Something in the sandbox rewrites the marker before the probe reads it.
		p.files[MarkerPath] = []byte("changed\n")
		return Exit{}, "", nil
	}
	_, _, err := tracked(p, "")
	var phase *PhaseError
	if !errors.As(err, &phase) || phase.Phase != domain.PhaseProbe || !errors.Is(err, ErrCommand) {
		t.Fatalf("err = %v", err)
	}
}

func TestProvisionRefusesBeforeAnyCall(t *testing.T) {
	p := &fakeProvider{}
	for name, run := range map[string]func() (domain.Record, error){
		"bad repo": func() (domain.Record, error) {
			return Provisioner{Provider: p}.Run(context.Background(), "box-1", "--upload-pack=sh")
		},
		"no provider": func() (domain.Record, error) {
			return Provisioner{}.Run(context.Background(), "box-1", "")
		},
		"no sandbox": func() (domain.Record, error) {
			return Provisioner{Provider: p}.Run(context.Background(), " ", "")
		},
	} {
		t.Run(name, func(t *testing.T) {
			rec, err := run()
			var phase *PhaseError
			if !errors.As(err, &phase) || phase.Phase != domain.PhaseClone || rec.Phase != domain.PhaseFailed {
				t.Fatalf("run = %+v %v", rec, err)
			}
		})
	}
	if len(p.execCalls()) != 0 || len(p.files) != 0 {
		t.Fatalf("calls = %+v files = %v", p.execCalls(), p.files)
	}
}

func TestProvisionKeepsTheTailOfStderrOnOneLine(t *testing.T) {
	long := strings.Repeat("x", 2*stderrTail) + "\x1b[31mlast\tline\r\n\xff"
	p := &fakeProvider{run: func(Command) (Exit, string, error) { return Exit{Code: 2}, long, nil }}
	_, _, err := tracked(p, "")
	if err == nil {
		t.Fatal("toolchain failure passed")
	}
	msg := err.Error()
	if strings.ContainsAny(msg, "\x1b\r\n\t") || !strings.HasSuffix(msg, "[31mlast line") {
		t.Fatalf("err = %q", msg)
	}
	if len(msg) > stderrTail+200 {
		t.Fatalf("err keeps %d bytes", len(msg))
	}
}
