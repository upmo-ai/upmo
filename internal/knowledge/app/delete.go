package app

import (
	"context"
	"strings"

	"github.com/google/uuid"
)

// SourceDeleter removes one source under folder edit and returns its blob key.
type SourceDeleter interface {
	DeleteSource(ctx context.Context, access Access, folderID, sourceID string) (blobKey string, err error)
}

// Originals removes stored upload bytes for a relative blob key.
type Originals interface {
	Remove(ctx context.Context, key string) error
}

// DeleteSource removes a source and, by cascade, its chunks and embeddings.
// When originals is set, any stored upload bytes for that source are removed too.
func DeleteSource(ctx context.Context, book SourceDeleter, originals Originals, access Access, folderID, sourceID string) error {
	if _, err := uuid.Parse(access.UserID); err != nil || strings.TrimSpace(access.Allow) == "" {
		return ErrAccess
	}
	if _, err := uuid.Parse(folderID); err != nil {
		return ErrFolder
	}
	if _, err := uuid.Parse(sourceID); err != nil {
		return ErrFolder
	}
	if book == nil {
		return ErrGone
	}
	key, err := book.DeleteSource(ctx, access, folderID, sourceID)
	if err != nil {
		return err
	}
	if originals != nil && strings.TrimSpace(key) != "" {
		_ = originals.Remove(ctx, key)
	}
	return nil
}
