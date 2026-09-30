package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/upmo-ai/upmo/internal/runtime/domain"
)

const (
	// ScratchDir is the scratch volume every driver mounts in a sandbox.
	ScratchDir = "/scratch"
	// RepoDir is where the clone phase puts a task's repository.
	RepoDir = ScratchDir + "/repo"
	// MarkerPath is the file the clone phase writes and the probe reads
	// back, so the probe proves files move both ways.
	MarkerPath = ScratchDir + "/.upmo-provision"

	// cloneTimeout and stepTimeout bound each provisioning command. Both
	// sit well inside the runtime cap.
	cloneTimeout = 2 * time.Minute
	stepTimeout  = 30 * time.Second
	// stderrTail is how much of a failed command's stderr its error keeps.
	stderrTail = 512
)

// toolScript exits 1 naming the first of its arguments that is not on PATH.
// The tool names travel as arguments, never inside the script.
const toolScript = `for t in "$@"; do command -v "$t" >/dev/null 2>&1 || { echo "$t is missing" >&2; exit 1; }; done`

// probeScript checks its argument is a directory the sandbox can write.
const probeScript = `test -d "$1" && test -w "$1"`

var (
	// ErrProvisionFailed reports a sandbox that did not reach PhaseReady.
	// Every PhaseError matches it.
	ErrProvisionFailed = errors.New("runtime: provisioning failed")
	// ErrCommand reports a provisioning command that exited non-zero, or a
	// probe whose marker did not read back.
	ErrCommand = errors.New("runtime: provisioning command failed")
)

// PhaseError is a provisioning that stopped in Phase. It matches
// ErrProvisionFailed and wraps the cause.
type PhaseError struct {
	Phase domain.Phase
	Err   error
}

func (e *PhaseError) Error() string {
	return fmt.Sprintf("runtime: provisioning failed in the %s phase: %v", e.Phase, e.Err)
}

// Unwrap returns ErrProvisionFailed and the cause.
func (e *PhaseError) Unwrap() []error { return []error{ErrProvisionFailed, e.Err} }

// Provisioner prepares a new sandbox through the provider, phase by phase:
// clone, toolchain, probe, then ready. Every step is an Exec or
// a file call on the provider, so brain runs nothing itself.
//
//   - clone puts the repository in RepoDir with git, or notes that there is
//     none, and writes MarkerPath.
//   - toolchain checks that the programs the task relies on are on PATH:
//     timeout and sleep, which the drivers need, and git with a repository.
//   - probe checks the work directory is writable, that the clone has a
//     commit, and that MarkerPath reads back as written.
type Provisioner struct {
	Provider SandboxProvider
	// Track receives the record after every event. Nil tracks nothing.
	Track func(domain.Record)
}

// Run provisions sandbox id for repo, which may be empty, and returns the
// final record. A step that fails applies KindFail, so the record ends in
// PhaseFailed, and Run returns a PhaseError naming the phase it stopped in.
// Run does not destroy the sandbox; its caller releases the seat.
func (p Provisioner) Run(ctx context.Context, id, repo string) (domain.Record, error) {
	r := &provisioning{Provisioner: p, id: strings.TrimSpace(id), rec: domain.Begin()}
	r.track()
	url, err := domain.CloneURL(repo)
	switch {
	case err != nil:
		return r.fail(err)
	case p.Provider == nil:
		return r.fail(ErrSandboxdUnset)
	case r.id == "":
		return r.fail(ErrSpec)
	}
	r.url = url

	for _, step := range []func(context.Context) error{r.clone, r.toolchain, r.probe} {
		if err := step(ctx); err != nil {
			return r.fail(err)
		}
		if err := r.event(domain.KindDone); err != nil {
			return r.fail(err)
		}
	}
	return r.rec, nil
}

// provisioning is one Run in progress.
type provisioning struct {
	Provisioner
	id  string
	url string
	rec domain.Record
}

func (r *provisioning) track() {
	if r.Track != nil {
		r.Track(r.rec)
	}
}

// event applies kind to the record and tracks the result.
func (r *provisioning) event(kind domain.Kind) error {
	next, err := domain.Apply(r.rec, kind)
	if err != nil {
		return err
	}
	r.rec = next
	r.track()
	return nil
}

// fail ends the record in PhaseFailed and reports the phase it stopped in.
func (r *provisioning) fail(cause error) (domain.Record, error) {
	phase := r.rec.Phase
	// The phases use at most six of the eight events, so the fail always fits.
	_ = r.event(domain.KindFail)
	return r.rec, &PhaseError{Phase: phase, Err: cause}
}

// clone puts the repository in RepoDir, or notes that there is none, then
// writes the marker the probe reads back. GIT_TERMINAL_PROMPT=0 makes a
// repository that asks for a login fail at once instead of waiting.
func (r *provisioning) clone(ctx context.Context) error {
	if r.url == "" {
		if err := r.event(domain.KindNote); err != nil {
			return err
		}
	} else {
		cmd := Command{
			Args:    []string{"env", "GIT_TERMINAL_PROMPT=0", "git", "clone", "--depth=1", "--quiet", "--", r.url, RepoDir},
			Dir:     ScratchDir,
			Timeout: cloneTimeout,
		}
		if err := r.exec(ctx, "git clone", "git", cmd); err != nil {
			return err
		}
	}
	return r.Provider.WriteFile(ctx, r.id, MarkerPath, r.marker())
}

// toolchain checks the programs the task relies on are on PATH.
func (r *provisioning) toolchain(ctx context.Context) error {
	args := []string{"sh", "-c", toolScript, "toolchain", "timeout", "sleep"}
	if r.url != "" {
		args = append(args, "git")
	}
	return r.exec(ctx, "toolchain check", "sh", Command{Args: args, Dir: ScratchDir, Timeout: stepTimeout})
}

// probe checks the sandbox is ready for work: the work directory is
// writable, a clone has a commit, and the marker reads back as written.
func (r *provisioning) probe(ctx context.Context) error {
	work := r.work()
	cmd := Command{Args: []string{"sh", "-c", probeScript, "probe", work}, Dir: work, Timeout: stepTimeout}
	if err := r.exec(ctx, "probe", "sh", cmd); err != nil {
		return err
	}
	if r.url != "" {
		head := Command{Args: []string{"git", "-C", RepoDir, "rev-parse", "--verify", "--quiet", "HEAD"}, Dir: work, Timeout: stepTimeout}
		if err := r.exec(ctx, "git rev-parse", "git", head); err != nil {
			return err
		}
	}
	got, err := r.Provider.ReadFile(ctx, r.id, MarkerPath)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, r.marker()) {
		return fmt.Errorf("%w: %s does not read back as written", ErrCommand, MarkerPath)
	}
	return nil
}

// work is where a task's work starts: the clone, or scratch without one.
func (r *provisioning) work() string {
	if r.url != "" {
		return RepoDir
	}
	return ScratchDir
}

// marker is the content of MarkerPath for this sandbox.
func (r *provisioning) marker() []byte {
	repo := r.url
	if repo == "" {
		repo = "none"
	}
	return []byte("upmo provision\nrepo: " + repo + "\nwork: " + r.work() + "\n")
}

// exec runs cmd and turns a non-zero exit into ErrCommand, with the tail of
// its stderr. program is what a 126 or 127 exit means is missing.
func (r *provisioning) exec(ctx context.Context, what, program string, cmd Command) error {
	var stderr tail
	exit, err := r.Provider.Exec(ctx, r.id, cmd, func(o Output) error {
		if o.Stream == "stderr" {
			stderr.write(o.Data)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	switch exit.Code {
	case 0:
		return nil
	case 126, 127:
		return fmt.Errorf("%w: %s: %s is not in the sandbox image", ErrCommand, what, program)
	}
	if msg := stderr.String(); msg != "" {
		return fmt.Errorf("%w: %s exited %d: %s", ErrCommand, what, exit.Code, msg)
	}
	return fmt.Errorf("%w: %s exited %d", ErrCommand, what, exit.Code)
}

// tail keeps the last stderrTail bytes written to it.
type tail struct{ b []byte }

func (t *tail) write(p []byte) {
	t.b = append(t.b, p...)
	if len(t.b) > stderrTail {
		t.b = append(t.b[:0], t.b[len(t.b)-stderrTail:]...)
	}
}

// String returns the tail on one line, with control characters and
// invalid bytes turned into spaces.
func (t *tail) String() string {
	s := strings.Map(func(r rune) rune {
		if r == unicode.ReplacementChar || unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(string(t.b), " "))
	return strings.Join(strings.Fields(s), " ")
}
