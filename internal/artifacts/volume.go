package artifacts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrBlob is a store or key the volume cannot open.
var ErrBlob = errors.New("artifacts: blob")

// ErrNoBlob is a missing volume root or a missing file.
var ErrNoBlob = errors.New("artifacts: no blob")

// Blobs opens the bytes for an artifact store and key.
type Blobs interface {
	Open(ctx context.Context, store, key string) (io.ReadCloser, error)
}

// Volume reads artifact bytes from a directory on disk (the Compose path).
// Object store keys are refused while volume-only downloads hold.
type Volume struct {
	Root string
}

// Open returns a reader for a volume key under Root. The key is joined and
// cleaned so it cannot leave Root.
func (v Volume) Open(_ context.Context, store, key string) (io.ReadCloser, error) {
	if store != StoreVolume {
		return nil, ErrBlob
	}
	full, err := v.resolve(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(full) //nolint:gosec // full is confined under Root by the Rel check above
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoBlob
	}
	if err != nil {
		return nil, fmt.Errorf("artifacts: volume: %w", err)
	}
	return f, nil
}

// Put writes raw bytes at a relative key under Root. The write lands in a
// temporary file and is renamed into place, so a reader never sees a partial
// file.
func (v Volume) Put(_ context.Context, key string, raw []byte) error {
	full, err := v.resolve(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil { //nolint:gosec // full is confined under Root
		return fmt.Errorf("artifacts: volume: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(full), ".put-*") //nolint:gosec // full is confined under Root
	if err != nil {
		return fmt.Errorf("artifacts: volume: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("artifacts: volume: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("artifacts: volume: %w", err)
	}
	if err := os.Rename(tmpPath, full); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("artifacts: volume: %w", err)
	}
	return nil
}

// Remove deletes a volume key. An empty key and a missing file are not errors.
// A key that would leave Root is ErrBlob.
func (v Volume) Remove(_ context.Context, key string) error {
	if strings.TrimSpace(key) == "" {
		return nil
	}
	full, err := v.resolve(key)
	if err != nil {
		return err
	}
	err = os.Remove(full)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("artifacts: volume: %w", err)
	}
	return nil
}

// resolve joins key under Root and refuses a key that escapes it.
func (v Volume) resolve(key string) (string, error) {
	root := strings.TrimSpace(v.Root)
	if root == "" {
		return "", ErrNoBlob
	}
	key = strings.TrimSpace(key)
	if !validKey(key) {
		return "", ErrBlob
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("artifacts: volume: %w", err)
	}
	full := filepath.Join(rootAbs, filepath.FromSlash(key))
	full, err = filepath.Abs(full)
	if err != nil {
		return "", fmt.Errorf("artifacts: volume: %w", err)
	}
	rel, err := filepath.Rel(rootAbs, full)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrBlob
	}
	return full, nil
}
