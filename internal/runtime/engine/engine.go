// Package engine detaches a coding engine and polls its log as NDJSON.
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
)

const (
	// Claude names the Claude Code engine.
	Claude = "claude"
	// Codex names the Codex engine.
	Codex = "codex"
	// OpenCode names the OpenCode engine.
	OpenCode = "opencode"
	// Cursor names the Cursor engine.
	Cursor = "cursor"
	// Pi names the Pi engine.
	Pi = "pi"
	// Builtin names the engine that writes supplied events without starting a process.
	Builtin = "builtin"
)

// ErrEngine reports an unknown engine, an invalid spec, a failed start, poll or stop, or a malformed log line.
var ErrEngine = errors.New("runtime: engine")

// Event is one NDJSON line in an engine log. Kind is "text", "done" or "error".
type Event struct {
	Kind string `json:"kind"`
	Text string `json:"text,omitempty"`
}

// Spec describes one engine run.
type Spec struct {
	// Dir is the working directory of the engine process.
	Dir string
	// Prompt is the only argument passed to the engine program.
	Prompt string
	// Log is the path of the NDJSON log the run writes.
	Log string
	// Events are the events the builtin engine writes to the log.
	Events []Event
}

// Session is a started engine run.
type Session interface {
	// Poll returns the complete events written to the log since the last poll.
	Poll(ctx context.Context) ([]Event, error)
	// Stop ends the run and waits for it to exit.
	Stop(ctx context.Context) error
}

// Adapter starts runs of one engine.
type Adapter interface {
	// Name returns the engine name.
	Name() string
	// Start begins a run described by spec.
	Start(ctx context.Context, spec Spec) (Session, error)
}

// Open returns the adapter for name, or ErrEngine for any other name.
func Open(name string) (Adapter, error) {
	switch name {
	case Claude, Codex, OpenCode, Cursor, Pi:
		return command{name: name, program: name}, nil
	case Builtin:
		return builtin{}, nil
	}
	return nil, fmt.Errorf("%w: unknown engine %q", ErrEngine, name)
}

type command struct {
	name    string
	program string
}

func (c command) Name() string { return c.name }

func (c command) Start(ctx context.Context, spec Spec) (Session, error) {
	if strings.TrimSpace(spec.Prompt) == "" || strings.TrimSpace(spec.Dir) == "" || strings.TrimSpace(spec.Log) == "" {
		return nil, fmt.Errorf("%w: %s needs a prompt, a dir and a log", ErrEngine, c.name)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: start %s: %w", ErrEngine, c.name, err)
	}
	out, err := createLog(spec.Log)
	if err != nil {
		return nil, err
	}
	// The program is a fixed engine name and the prompt is one argument, never a shell string.
	cmd := exec.Command(c.program, spec.Prompt) //nolint:gosec
	cmd.Dir = spec.Dir
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err = cmd.Start()
	// The child holds its own copy of the log descriptor.
	_ = out.Close()
	if err != nil {
		return nil, fmt.Errorf("%w: start %s: %w", ErrEngine, c.name, err)
	}
	s := &commandSession{poller: poller{path: spec.Log}, cmd: cmd, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(s.done)
	}()
	return s, nil
}

type commandSession struct {
	poller
	cmd     *exec.Cmd
	done    chan struct{}
	once    sync.Once
	killErr error
}

func (s *commandSession) Stop(ctx context.Context) error {
	s.once.Do(func() {
		select {
		case <-s.done:
			return
		default:
		}
		// Setsid makes the process the leader of its own group, so the negative pid reaches every child.
		if err := syscall.Kill(-s.cmd.Process.Pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			s.killErr = fmt.Errorf("%w: stop: %w", ErrEngine, err)
		}
	})
	if s.killErr != nil {
		return s.killErr
	}
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%w: stop: %w", ErrEngine, ctx.Err())
	}
}

type builtin struct{}

func (builtin) Name() string { return Builtin }

func (builtin) Start(ctx context.Context, spec Spec) (Session, error) {
	if len(spec.Events) == 0 || strings.TrimSpace(spec.Log) == "" {
		return nil, fmt.Errorf("%w: %s needs events and a log", ErrEngine, Builtin)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: start %s: %w", ErrEngine, Builtin, err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, e := range spec.Events {
		if !validKind(e.Kind) {
			return nil, fmt.Errorf("%w: event kind %q", ErrEngine, e.Kind)
		}
		if err := enc.Encode(e); err != nil {
			return nil, fmt.Errorf("%w: encode event: %w", ErrEngine, err)
		}
	}
	out, err := createLog(spec.Log)
	if err != nil {
		return nil, err
	}
	if _, err := out.Write(buf.Bytes()); err != nil {
		_ = out.Close()
		return nil, fmt.Errorf("%w: write log: %w", ErrEngine, err)
	}
	if err := out.Close(); err != nil {
		return nil, fmt.Errorf("%w: write log: %w", ErrEngine, err)
	}
	return &builtinSession{poller: poller{path: spec.Log}}, nil
}

type builtinSession struct {
	poller
}

func (*builtinSession) Stop(context.Context) error { return nil }

// poller reads complete NDJSON lines from a log, keeping a trailing partial line for the next poll.
type poller struct {
	mu      sync.Mutex
	path    string
	offset  int64
	partial []byte
}

func (p *poller) Poll(ctx context.Context) ([]Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: poll: %w", ErrEngine, err)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	fresh, err := readFrom(p.path, p.offset)
	if err != nil {
		return nil, err
	}
	data := append(bytes.Clone(p.partial), fresh...)
	end := bytes.LastIndexByte(data, '\n')
	var events []Event
	if end >= 0 {
		for _, line := range bytes.Split(data[:end], []byte("\n")) {
			e, err := decode(line)
			if err != nil {
				return nil, err
			}
			events = append(events, e)
		}
	}
	p.offset += int64(len(fresh))
	p.partial = bytes.Clone(data[end+1:])
	return events, nil
}

func readFrom(path string, offset int64) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // the path is the log this session created
	if err != nil {
		return nil, fmt.Errorf("%w: poll: %w", ErrEngine, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, fmt.Errorf("%w: poll: %w", ErrEngine, err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("%w: poll: %w", ErrEngine, err)
	}
	return data, nil
}

func decode(line []byte) (Event, error) {
	var e Event
	if err := json.Unmarshal(line, &e); err != nil {
		return Event{}, fmt.Errorf("%w: log line is not JSON: %w", ErrEngine, err)
	}
	if !validKind(e.Kind) {
		return Event{}, fmt.Errorf("%w: log line kind %q", ErrEngine, e.Kind)
	}
	return e, nil
}

func validKind(kind string) bool {
	return kind == "text" || kind == "done" || kind == "error"
}

func createLog(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // the caller owns the log path
	if err != nil {
		return nil, fmt.Errorf("%w: create log: %w", ErrEngine, err)
	}
	return f, nil
}
