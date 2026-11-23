package blob

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	"github.com/upmo-ai/upmo/internal/identity/domain"
)

func TestVolumePutGetRemoveAndConfinement(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	vol := Volume{Root: root}

	key := uuid.Must(uuid.NewV7()).String()
	body := []byte("png-bytes")

	if err := vol.Put(ctx, key, body); err != nil {
		t.Fatalf("Put: %v", err)
	}
	info, err := os.Stat(filepath.Join(root, key))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 0600", info.Mode().Perm())
	}

	got, err := vol.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("got = %q, want %q", got, body)
	}

	if err := vol.Remove(ctx, key); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := vol.Remove(ctx, key); err != nil {
		t.Fatalf("second Remove should be idempotent: %v", err)
	}
	if _, err := vol.Get(ctx, key); !errors.Is(err, domain.ErrAvatarNotFound) {
		t.Fatalf("Get after remove = %v, want ErrAvatarNotFound", err)
	}

	for _, badKey := range []string{"", "../escape", "/etc/passwd", "sub/" + key, "not-a-uuid"} {
		if err := vol.Put(ctx, badKey, body); !errors.Is(err, ErrInvalidKey) {
			t.Fatalf("Put(%q) = %v, want ErrInvalidKey", badKey, err)
		}
		if _, err := vol.Get(ctx, badKey); !errors.Is(err, ErrInvalidKey) {
			t.Fatalf("Get(%q) = %v, want ErrInvalidKey", badKey, err)
		}
		if badKey != "" {
			if err := vol.Remove(ctx, badKey); !errors.Is(err, ErrInvalidKey) {
				t.Fatalf("Remove(%q) = %v, want ErrInvalidKey", badKey, err)
			}
		}
	}
}
