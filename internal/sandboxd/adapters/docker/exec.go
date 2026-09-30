package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"

	"github.com/upmo-ai/upmo/internal/runtime"
)

// exitPoll bounds how long Exec waits for the engine to report an exit code
// after the output stream has ended.
const (
	exitPoll  = 2 * time.Second
	exitEvery = 20 * time.Millisecond
)

// Exec runs cmd in the sandbox and streams its output to out as it arrives.
// The command runs under timeout, so the engine kills it when its time is
// up; Exec itself returns at the same deadline with an error that wraps
// context.DeadlineExceeded. A zero Timeout means the runtime cap.
func (d *Driver) Exec(ctx context.Context, id string, cmd runtime.Command, out func(runtime.Output) error) (runtime.Exit, error) {
	id, b, ok := d.live(id)
	if !ok {
		return runtime.Exit{}, runtime.ErrNotFound
	}
	if b.paused {
		return runtime.Exit{}, ErrPaused
	}
	limit, err := execLimit(cmd.Timeout)
	if err != nil {
		return runtime.Exit{}, err
	}
	if len(cmd.Args) == 0 || cmd.Args[0] == "" {
		return runtime.Exit{}, runtime.ErrSpec
	}
	for _, arg := range cmd.Args {
		if strings.ContainsRune(arg, 0) {
			return runtime.Exit{}, runtime.ErrSpec
		}
	}
	dir := strings.TrimSpace(cmd.Dir)
	if dir != "" {
		if !path.IsAbs(dir) || strings.ContainsRune(dir, 0) {
			return runtime.Exit{}, runtime.ErrSpec
		}
		dir = path.Clean(dir)
	}
	if out == nil {
		out = func(runtime.Output) error { return nil }
	}

	runCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	created, err := d.api.ExecCreate(runCtx, containerName(id), client.ExecCreateOptions{
		AttachStdout: true,
		AttachStderr: true,
		WorkingDir:   dir,
		Cmd:          wrap(cmd.Args, limit),
	})
	if err != nil {
		return runtime.Exit{}, timedOut(ctx, runCtx, fmt.Errorf("docker: exec create: %w", err))
	}
	attached, err := d.api.ExecAttach(runCtx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return runtime.Exit{}, timedOut(ctx, runCtx, fmt.Errorf("docker: exec attach: %w", err))
	}
	defer attached.Close()
	// Closing the connection is what unblocks the copy below at the deadline.
	stop := context.AfterFunc(runCtx, attached.Close)
	defer stop()

	_, copyErr := stdcopy.StdCopy(
		streamWriter{stream: "stdout", out: out},
		streamWriter{stream: "stderr", out: out},
		attached.Reader,
	)
	if runCtx.Err() != nil {
		return runtime.Exit{}, timedOut(ctx, runCtx, runCtx.Err())
	}
	if copyErr != nil {
		return runtime.Exit{}, copyErr
	}
	return d.exitCode(ctx, created.ID)
}

// exitCode waits briefly for the engine to mark the exec finished.
func (d *Driver) exitCode(ctx context.Context, execID string) (runtime.Exit, error) {
	deadline := time.Now().Add(exitPoll)
	for {
		res, err := d.api.ExecInspect(ctx, execID, client.ExecInspectOptions{})
		if err != nil {
			return runtime.Exit{}, fmt.Errorf("docker: exec inspect: %w", err)
		}
		if !res.Running {
			return runtime.Exit{Code: res.ExitCode}, nil
		}
		if time.Now().After(deadline) {
			return runtime.Exit{}, errors.New("docker: exec still running after its output ended")
		}
		select {
		case <-ctx.Done():
			return runtime.Exit{}, ctx.Err()
		case <-time.After(exitEvery):
		}
	}
}

// execLimit refuses a timeout outside the runtime cap and turns zero into the cap.
func execLimit(timeout time.Duration) (time.Duration, error) {
	limit := runtime.ResourceShape().Exec
	if timeout < 0 || timeout > limit {
		return 0, runtime.ErrSpec
	}
	if timeout == 0 {
		return limit, nil
	}
	return timeout, nil
}

// wrap runs args under timeout with SIGKILL, rounded up to whole seconds,
// so the command and its process group stop inside the sandbox too.
func wrap(args []string, limit time.Duration) []string {
	secs := int64(math.Ceil(limit.Seconds()))
	return append([]string{"timeout", "-s", "KILL", strconv.FormatInt(secs, 10)}, args...)
}

// timedOut reports the command deadline as context.DeadlineExceeded, and
// leaves the caller's own cancellation and other errors as they are.
func timedOut(parent, run context.Context, err error) error {
	if parent.Err() == nil && errors.Is(run.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("docker: command timed out: %w", context.DeadlineExceeded)
	}
	if parent.Err() != nil {
		return parent.Err()
	}
	return err
}

// streamWriter hands each chunk to out with a copy of the bytes, since the
// demultiplexer reuses its buffer.
type streamWriter struct {
	stream string
	out    func(runtime.Output) error
}

func (w streamWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := w.out(runtime.Output{Stream: w.stream, Data: append([]byte(nil), p...)}); err != nil {
		return 0, err
	}
	return len(p), nil
}

var _ io.Writer = streamWriter{}
