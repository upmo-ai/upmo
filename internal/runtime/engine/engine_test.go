package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"syscall"
	"testing"
	"time"
)

// testCommand builds a command adapter for program so tests never run a real engine binary.
func testCommand(program string) Adapter {
	return command{name: "test", program: program}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func pollUntil(t *testing.T, s Session, n int) []Event {
	t.Helper()
	var got []Event
	deadline := time.Now().Add(5 * time.Second)
	for len(got) < n {
		if time.Now().After(deadline) {
			t.Fatalf("Poll() events = %v, want %d events", got, n)
		}
		events, err := s.Poll(t.Context())
		if err != nil {
			t.Fatalf("Poll() error = %v", err)
		}
		got = append(got, events...)
		time.Sleep(20 * time.Millisecond)
	}
	return got
}

// getsid returns the session id of pid. The syscall package exposes getsid only as a raw call on Linux.
func getsid(pid int) (int, error) {
	sid, _, errno := syscall.RawSyscall(syscall.SYS_GETSID, uintptr(pid), 0, 0)
	if errno != 0 {
		return 0, errno
	}
	return int(sid), nil
}

func stopOnCleanup(t *testing.T, s Session) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Stop(ctx)
	})
}

func TestOpenUnknownName(t *testing.T) {
	for _, name := range []string{"nope", "", " claude"} {
		a, err := Open(name)
		if !errors.Is(err, ErrEngine) {
			t.Errorf("Open(%q) error = %v, want ErrEngine", name, err)
		}
		if a != nil {
			t.Errorf("Open(%q) adapter = %v, want nil", name, a)
		}
	}
}

func TestOpenKnownNames(t *testing.T) {
	for _, name := range []string{Claude, Codex, OpenCode, Cursor, Pi, Builtin} {
		a, err := Open(name)
		if err != nil {
			t.Fatalf("Open(%q) error = %v", name, err)
		}
		if got := a.Name(); got != name {
			t.Errorf("Open(%q).Name() = %q", name, got)
		}
	}
}

func TestCommandRequiresPromptDirAndLog(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "run.ndjson")
	for name, spec := range map[string]Spec{
		"blank prompt": {Dir: dir, Prompt: " \t", Log: log},
		"blank dir":    {Dir: " ", Prompt: "hello", Log: log},
		"blank log":    {Dir: dir, Prompt: "hello", Log: " "},
	} {
		s, err := testCommand("/bin/sh").Start(t.Context(), spec)
		if !errors.Is(err, ErrEngine) {
			t.Errorf("%s: Start() error = %v, want ErrEngine", name, err)
		}
		if s != nil {
			t.Errorf("%s: Start() session = %v, want nil", name, s)
		}
		// The log is created just before the process starts, so its absence shows no process ran.
		if _, err := os.Stat(log); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: log stat error = %v, want not exist", name, err)
		}
	}
}

func TestCommandFailedStartReturnsErrEngine(t *testing.T) {
	dir := t.TempDir()
	s, err := testCommand(filepath.Join(dir, "missing")).Start(t.Context(), Spec{Dir: dir, Prompt: "hello", Log: filepath.Join(dir, "run.ndjson")})
	if !errors.Is(err, ErrEngine) {
		t.Fatalf("Start() error = %v, want ErrEngine", err)
	}
	if s != nil {
		t.Fatalf("Start() session = %v, want nil", s)
	}
}

func TestCommandPollsEventsFromDetachedProcess(t *testing.T) {
	dir := t.TempDir()
	gate := filepath.Join(dir, "gate")
	script := filepath.Join(dir, "run.sh")
	writeFile(t, script, `printf '%s\n' '{"kind":"text","text":"hello"}' '{"kind":"done"}'
while [ ! -e '`+gate+`' ]; do sleep 0.1; done
`)

	s, err := testCommand("/bin/sh").Start(t.Context(), Spec{Dir: dir, Prompt: script, Log: filepath.Join(dir, "run.ndjson")})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	stopOnCleanup(t, s)

	got := pollUntil(t, s, 2)
	want := []Event{{Kind: "text", Text: "hello"}, {Kind: "done"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Poll() = %v, want %v", got, want)
	}

	cs := s.(*commandSession)
	childSid, err := getsid(cs.cmd.Process.Pid)
	if err != nil {
		t.Fatalf("getsid(child) error = %v", err)
	}
	ourSid, err := getsid(0)
	if err != nil {
		t.Fatalf("getsid(0) error = %v", err)
	}
	if childSid == ourSid {
		t.Fatalf("child session id = %d, want different from test session id %d", childSid, ourSid)
	}

	writeFile(t, gate, "")
	select {
	case <-cs.done:
	case <-time.After(5 * time.Second):
		t.Fatal("process did not exit")
	}
	events, err := s.Poll(t.Context())
	if err != nil || events != nil {
		t.Fatalf("Poll() after exit = %v, %v, want nil, nil", events, err)
	}
}

func TestPollKeepsPartialLine(t *testing.T) {
	log := filepath.Join(t.TempDir(), "run.ndjson")
	writeFile(t, log, `{"kind":"text","text":"a"`)
	p := &poller{path: log}

	events, err := p.Poll(t.Context())
	if err != nil || events != nil {
		t.Fatalf("Poll() partial = %v, %v, want nil, nil", events, err)
	}

	f, err := os.OpenFile(log, os.O_WRONLY|os.O_APPEND, 0) //nolint:gosec // the path is under t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("}\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	events, err = p.Poll(t.Context())
	if err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	if want := []Event{{Kind: "text", Text: "a"}}; !reflect.DeepEqual(events, want) {
		t.Fatalf("Poll() = %v, want %v", events, want)
	}
}

func TestPollRejectsMalformedLine(t *testing.T) {
	for name, body := range map[string]string{
		"not json":     "{\"kind\":\"text\"}\nnope\n",
		"unknown kind": "{\"kind\":\"thinking\"}\n",
	} {
		log := filepath.Join(t.TempDir(), "run.ndjson")
		writeFile(t, log, body)
		events, err := (&poller{path: log}).Poll(t.Context())
		if !errors.Is(err, ErrEngine) {
			t.Errorf("%s: Poll() error = %v, want ErrEngine", name, err)
		}
		if events != nil {
			t.Errorf("%s: Poll() events = %v, want nil", name, events)
		}
	}
}

func TestBuiltinWritesAndPollsEvents(t *testing.T) {
	events := []Event{{Kind: "text", Text: "one"}, {Kind: "error", Text: "two"}, {Kind: "done"}}
	before := slices.Clone(events)
	a, err := Open(Builtin)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	s, err := a.Start(t.Context(), Spec{Log: filepath.Join(t.TempDir(), "run.ndjson"), Events: events})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	got, err := s.Poll(t.Context())
	if err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	if !reflect.DeepEqual(got, before) {
		t.Fatalf("Poll() = %v, want %v", got, before)
	}
	if !reflect.DeepEqual(events, before) {
		t.Fatalf("caller events = %v, want %v", events, before)
	}
	if err := s.Stop(t.Context()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}

func TestBuiltinRequiresEventsAndLog(t *testing.T) {
	log := filepath.Join(t.TempDir(), "run.ndjson")
	for name, spec := range map[string]Spec{
		"no events": {Log: log},
		"blank log": {Log: " ", Events: []Event{{Kind: "done"}}},
	} {
		if _, err := (builtin{}).Start(t.Context(), spec); !errors.Is(err, ErrEngine) {
			t.Errorf("%s: Start() error = %v, want ErrEngine", name, err)
		}
	}
}

func TestStopEndsRunningCommand(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "run.sh")
	writeFile(t, script, "sleep 30\n")

	s, err := testCommand("/bin/sh").Start(t.Context(), Spec{Dir: dir, Prompt: script, Log: filepath.Join(dir, "run.ndjson")})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	pid := s.(*commandSession).cmd.Process.Pid

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := s.Stop(ctx); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("Kill(pid, 0) after Stop error = %v, want ESRCH", err)
	}
	if err := s.Stop(ctx); err != nil {
		t.Fatalf("second Stop() error = %v", err)
	}
}
