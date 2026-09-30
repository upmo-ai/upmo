package kubernetes

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/upmo-ai/upmo/internal/runtime"
)

// Exec runs cmd in the sandbox and streams its output to out as it arrives.
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
	argv := wrap(cmd.Args, limit)
	if dir != "" {
		script := "cd " + shellQuote(dir) + " && exec " + shellJoin(argv)
		argv = []string{"sh", "-c", script}
	}

	stdout := &streamWriter{stream: "stdout", out: out}
	stderr := &streamWriter{stream: "stderr", out: out}
	err = d.api.Exec(runCtx, podName(id), argv, nil, stdout, stderr)
	if runCtx.Err() != nil {
		return runtime.Exit{}, timedOut(ctx, runCtx, runCtx.Err())
	}
	if err != nil {
		var exitErr remotecommandExit
		if errors.As(err, &exitErr) {
			return runtime.Exit{Code: exitErr.ExitStatus()}, nil
		}
		return runtime.Exit{}, timedOut(ctx, runCtx, fmt.Errorf("kubernetes: exec: %w", err))
	}
	return runtime.Exit{Code: 0}, nil
}

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

func wrap(args []string, limit time.Duration) []string {
	secs := int64(math.Ceil(limit.Seconds()))
	return append([]string{"timeout", "-s", "KILL", strconv.FormatInt(secs, 10)}, args...)
}

func shellJoin(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
		parts[i] = shellQuote(a)
	}
	return strings.Join(parts, " ")
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, " \t\r\n'\"\\$`!") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func timedOut(parent, run context.Context, err error) error {
	if parent.Err() == nil && errors.Is(run.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("kubernetes: command timed out: %w", context.DeadlineExceeded)
	}
	if parent.Err() != nil {
		return parent.Err()
	}
	return err
}
