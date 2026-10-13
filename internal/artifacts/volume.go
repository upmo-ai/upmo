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
	root := strings.TrimSpace(v.Root)
	if root == "" {
		return nil, ErrNoBlob
	}
	key = strings.TrimSpace(key)
	if !validKey(key) {
		return nil, ErrBlob
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("artifacts: volume: %w", err)
	}
	full := filepath.Join(rootAbs, filepath.FromSlash(key))
	full, err = filepath.Abs(full)
	if err != nil {
		return nil, fmt.Errorf("artifacts: volume: %w", err)
	}
	rel, err := filepath.Rel(rootAbs, full)
	if err != nil || strings.HasPrefix(rel, "..") {
		return nil, ErrBlob
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
