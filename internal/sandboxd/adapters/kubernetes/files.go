package kubernetes

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/upmo-ai/upmo/internal/runtime"
)

// MaxFile is the largest file ReadFile returns or WriteFile copies.
const MaxFile = 16 << 20

// ErrTooLarge reports a file larger than MaxFile.
var ErrTooLarge = errors.New("kubernetes: file is too large")

// ErrNotFile reports a path that names a directory, link or device.
var ErrNotFile = errors.New("kubernetes: not a regular file")

// ReadFile copies one regular file out of the sandbox through exec. It works
// on a paused sandbox too. A relative path starts at ScratchDir.
func (d *Driver) ReadFile(ctx context.Context, id, name string) ([]byte, error) {
	id, _, ok := d.live(id)
	if !ok {
		return nil, runtime.ErrNotFound
	}
	p, err := sandboxPath(name)
	if err != nil {
		return nil, err
	}
	var out strings.Builder
	err = d.api.Exec(ctx, podName(id), []string{"sh", "-c", "test -f " + shellQuote(p) + " && base64 -w0 " + shellQuote(p)}, nil, &out, nil)
	if err != nil {
		var exit remotecommandExit
		if errors.As(err, &exit) && exit.ExitStatus() != 0 {
			return nil, fmt.Errorf("kubernetes: %s: %w", p, fs.ErrNotExist)
		}
		return nil, fmt.Errorf("kubernetes: read %s: %w", p, err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(out.String()))
	if err != nil {
		return nil, fmt.Errorf("kubernetes: read %s: %w", p, err)
	}
	if len(raw) > MaxFile {
		return nil, ErrTooLarge
	}
	return raw, nil
}

// WriteFile copies data into the sandbox as a regular file, mode 0644. The
// parent directory must exist. A relative path starts at ScratchDir.
func (d *Driver) WriteFile(ctx context.Context, id, name string, data []byte) error {
	id, _, ok := d.live(id)
	if !ok {
		return runtime.ErrNotFound
	}
	if len(data) > MaxFile {
		return ErrTooLarge
	}
	p, err := sandboxPath(name)
	if err != nil {
		return err
	}
	dir, base := path.Split(p)
	if base == "" || strings.HasSuffix(strings.TrimSpace(name), "/") {
		return runtime.ErrSpec
	}
	enc := base64.StdEncoding.EncodeToString(data)
	script := "test -d " + shellQuote(strings.TrimSuffix(dir, "/")) + " && base64 -d > " + shellQuote(p)
	var stderr strings.Builder
	err = d.api.Exec(ctx, podName(id), []string{"sh", "-c", script}, bytes.NewBufferString(enc), nil, &stderr)
	if err != nil {
		var exit remotecommandExit
		if errors.As(err, &exit) && exit.ExitStatus() != 0 {
			return fmt.Errorf("kubernetes: %s: %w", dir, fs.ErrNotExist)
		}
		return fmt.Errorf("kubernetes: write %s: %w", p, err)
	}
	return nil
}

func sandboxPath(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsRune(name, 0) {
		return "", runtime.ErrSpec
	}
	if !path.IsAbs(name) {
		name = path.Join(ScratchDir, name)
	}
	p := path.Clean(name)
	if p == "/" {
		return "", runtime.ErrSpec
	}
	return p, nil
}

// remotecommandExit matches exec exit errors without importing remotecommand in tests.
type remotecommandExit interface {
	error
	ExitStatus() int
}
