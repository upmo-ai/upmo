// Package blob stores re-encoded user avatar PNG bytes on a confined volume.
package blob

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/identity/domain"
)

// ErrInvalidKey reports a key or root the avatar volume cannot use.
var ErrInvalidKey = errors.New("identity: invalid avatar key")

// Volume reads and writes avatar PNG bytes under Root.
type Volume struct {
	Root string
}

// Put writes body atomically under a canonical UUID key inside Root.
func (v Volume) Put(_ context.Context, key string, body []byte) error {
	full, err := v.resolve(key)
	if err != nil {
		return err
	}
	dir := filepath.Dir(full)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("identity: avatar blob: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".put-*")
	if err != nil {
		return fmt.Errorf("identity: avatar blob: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		_ = tmp.Close()
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("identity: avatar blob: %w", err)
	}
	if _, err := tmp.Write(body); err != nil {
		return fmt.Errorf("identity: avatar blob: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("identity: avatar blob: %w", err)
	}
	if err := os.Rename(tmpPath, full); err != nil {
		return fmt.Errorf("identity: avatar blob: %w", err)
	}
	cleanup = false
	return nil
}

// Get reads the avatar PNG bytes for key under Root.
func (v Volume) Get(_ context.Context, key string) ([]byte, error) {
	full, err := v.resolve(key)
	if err != nil {
		return nil, err
	}
	body, err := os.ReadFile(full) //nolint:gosec // resolve confines full under Root
	if errors.Is(err, os.ErrNotExist) {
		return nil, domain.ErrAvatarNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("identity: avatar blob: %w", err)
	}
	return body, nil
}

// Remove deletes the avatar file for key. Missing files are ignored.
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
		return fmt.Errorf("identity: avatar blob: %w", err)
	}
	return nil
}

func (v Volume) resolve(key string) (string, error) {
	root := strings.TrimSpace(v.Root)
	if root == "" {
		return "", domain.ErrAvatarNotFound
	}
	key = strings.TrimSpace(key)
	if !validKey(key) {
		return "", ErrInvalidKey
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("identity: avatar blob: %w", err)
	}
	full := filepath.Join(rootAbs, key)
	full, err = filepath.Abs(full)
	if err != nil {
		return "", fmt.Errorf("identity: avatar blob: %w", err)
	}
	rel, err := filepath.Rel(rootAbs, full)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || strings.ContainsRune(rel, filepath.Separator) {
		return "", ErrInvalidKey
	}
	return full, nil
}

func validKey(key string) bool {
	parsed, err := uuid.Parse(key)
	if err != nil {
		return false
	}
	return parsed.String() == key
}
