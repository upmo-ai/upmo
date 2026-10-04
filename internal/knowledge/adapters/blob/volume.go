// Package blob stores original knowledge file bytes on a Compose volume.
package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

var (
	// ErrBlob is a key or root the volume cannot use.
	ErrBlob = errors.New("knowledge: blob")
	// ErrNoBlob is a missing volume root or missing file.
	ErrNoBlob = errors.New("knowledge: no blob")
)

// Volume reads and writes original upload bytes under Root.
type Volume struct {
	Root string
}

// Put writes raw bytes at a relative key under Root.
func (v Volume) Put(_ context.Context, key string, raw []byte) error {
	full, err := v.resolve(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("knowledge: blob: %w", err)
	}
	tmp := full + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("knowledge: blob: %w", err)
	}
	if err := os.Rename(tmp, full); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("knowledge: blob: %w", err)
	}
	return nil
}

// Open returns a reader for a stored original.
func (v Volume) Open(_ context.Context, key string) (io.ReadCloser, error) {
	full, err := v.resolve(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(full)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoBlob
	}
	if err != nil {
		return nil, fmt.Errorf("knowledge: blob: %w", err)
	}
	return f, nil
}

// Remove deletes a stored original. Missing files are ignored.
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
		return fmt.Errorf("knowledge: blob: %w", err)
	}
	return nil
}

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
		return "", fmt.Errorf("knowledge: blob: %w", err)
	}
	full := filepath.Join(rootAbs, filepath.FromSlash(key))
	full, err = filepath.Abs(full)
	if err != nil {
		return "", fmt.Errorf("knowledge: blob: %w", err)
	}
	rel, err := filepath.Rel(rootAbs, full)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", ErrBlob
	}
	return full, nil
}

func validKey(key string) bool {
	if key == "" || strings.Contains(key, `\`) || strings.Contains(key, "..") || strings.HasPrefix(key, "/") {
		return false
	}
	_, err := uuid.Parse(key)
	return err == nil
}
