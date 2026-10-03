package app

import (
	"context"
	"strings"

	"github.com/google/uuid"
)

// SourceDeleter removes one source under folder edit.
type SourceDeleter interface {
	DeleteSource(ctx context.Context, access Access, folderID, sourceID string) error
}

// DeleteSource removes a source and, by cascade, its chunks and embeddings.
func DeleteSource(ctx context.Context, book SourceDeleter, access Access, folderID, sourceID string) error {
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
	return book.DeleteSource(ctx, access, folderID, sourceID)
}
